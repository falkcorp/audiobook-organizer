// file: internal/books/store/snapshot_pins.go
// version: 1.2.0
// guid: 4a7d1c93-e5b2-4f08-9c61-2d8f0b3a6e75
// last-edited: 2026-11-17

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Snapshot pins: book_ver: snapshots a live merge journal row still points at.
//
// WHY THIS EXISTS. An automatic merge is undone by writing both books back to
// the book_ver: snapshots its journal row names (Engine.UnmergeAuto reads
// AutoMergeJournalEntry.WinnerPreMergeTS / LoserPreMergeTS). The snapshot prune
// kept the newest N per book and exempted nothing, so N later edits of a book and
// one prune deleted the very row the undo needed, and the undo failed half way
// (the loser put back, the survivor not). A pin is the exemption: PruneBookSnapshots
// and PlanBookSnapshotPrune both decide through planSnapshotPrune, which asks
// this set, so there is exactly one place that says what may be deleted.
//
// WHICH JOURNALS POINT AT SNAPSHOTS. Checked by reading every reader of a
// book_ver: key (GetBookAtVersion / RevertBookToVersion / GetBookSnapshots and
// their callers) on 2026-11-17:
//
//   - dedup:automerge: names a winner and a loser snapshot by timestamp. Pinned.
//   - merge:combine-journal: records file-row ownership, external IDs and user
//     progress. It holds no book_ver timestamp (combine_journal.go says why a
//     row snapshot cannot reverse a combine), so there is nothing to pin.
//   - merge:sibling-journal: records group membership and primary flags as
//     before-values in the journal itself. No book_ver timestamp.
//   - The metadata history UI picks a timestamp from the live snapshot list at
//     click time, and the tags page falls back to the activity log when a
//     snapshot is gone. Neither stores a pointer.
//
// A journal kind added later that points at snapshots registers ONE entry in
// snapshotPinSources. The prune loop never changes.

// snapshotPinSource reads one journal kind's snapshot references.
type snapshotPinSource struct {
	// name is the journal's key prefix, for error messages.
	name string
	// generation counts this journal's writes in this process. The pinned set
	// is rebuilt when the sum over all sources changes, so an entry written
	// after the set was built is honoured by the next prune decision.
	generation func() uint64
	// collect records, on pins, every snapshot a LIVE (not undone) row points at
	// (pins.add), and the floor of every provisional row (pins.addFloor). It
	// returns an error, rather than skipping, for a row it cannot decode: a
	// row that cannot be read might be the one that names the snapshot.
	collect func(db *GatedDB, pins *SnapshotPins) error
}

var snapshotPinSources = []snapshotPinSource{
	{name: dedupAutoMergeJournalPfx, generation: AutoMergeJournalGeneration, collect: collectAutoMergeSnapshotPins},
}

// SnapshotPins is what the live journal rows point at: exact (book, snapshot
// timestamp) pairs, and per book the FLOORS of the provisional rows, one per row.
// The zero value and nil pin nothing.
//
// A provisional auto-merge entry names no timestamp yet. What its post-merge
// patch will name is "the earliest snapshot newer than the book's baseline"
// (EarliestSnapshotAbove, the one lookup the patch and this set share), so the
// row pins exactly that one snapshot per book and nothing more. Which snapshot it
// is depends on which snapshots exist when the prune decides, not when this set
// was built: the merge's own writes land after the set was built. So the set
// holds only the floors, and Pinned resolves them against the book's snapshot
// list at the moment of the decision. The cached set therefore never goes stale
// for a floor.
//
// Floors are kept per row, never merged: two rows on one book with different
// floors each need the first snapshot above THEIR floor, and the lower floor's
// first snapshot is not the newer row's.
type SnapshotPins struct {
	gen    uint64
	byBook map[string]map[int64]struct{}
	// floors maps a book to the distinct floors of its live provisional rows.
	floors map[string][]int64
	count  int
}

// Pinned returns which of existing (every snapshot timestamp the book has) are
// pinned: the exact pins, plus for each floor of a provisional row the earliest
// snapshot above it.
func (s *SnapshotPins) Pinned(bookID string, existing []int64) map[int64]struct{} {
	if s == nil {
		return nil
	}
	exact, floors := s.byBook[bookID], s.floors[bookID]
	if len(exact) == 0 && len(floors) == 0 {
		return nil
	}
	out := make(map[int64]struct{}, len(exact)+len(floors))
	for _, ts := range existing {
		if _, ok := exact[ts]; ok {
			out[ts] = struct{}{}
		}
	}
	for _, floor := range floors {
		if ts := EarliestSnapshotAbove(floor, existing); ts != 0 {
			out[ts] = struct{}{}
		}
	}
	return out
}

// EarliestSnapshotAbove returns the smallest timestamp in snapshots that is
// strictly greater than floor, or 0 when there is none. It is the lookup that
// names a merge's pre-merge snapshot: MergeBooksJournaled's patch uses it to
// record the snapshot (Engine.preMergeSnapshotNanos) and the snapshot prune uses
// it to keep that same snapshot while the entry is still provisional. They share
// this function so the snapshot kept and the snapshot the undo restores cannot
// drift apart. The order of snapshots does not matter.
func EarliestSnapshotAbove(floor int64, snapshots []int64) int64 {
	var best int64
	for _, ts := range snapshots {
		if ts > floor && (best == 0 || ts < best) {
			best = ts
		}
	}
	return best
}

// Len is the number of distinct exact pins.
func (s *SnapshotPins) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}

// FloorRows is how many (book, floor) pairs of provisional rows are held.
func (s *SnapshotPins) FloorRows() int {
	if s == nil {
		return 0
	}
	n := 0
	for _, f := range s.floors {
		n += len(f)
	}
	return n
}

// Floors lists a book's provisional-row floors.
func (s *SnapshotPins) Floors(bookID string) []int64 {
	if s == nil {
		return nil
	}
	return append([]int64(nil), s.floors[bookID]...)
}

func (s *SnapshotPins) add(bookID string, tsNanos int64) {
	if bookID == "" || tsNanos == 0 {
		return // an entry that recorded no snapshot pins nothing
	}
	m := s.byBook[bookID]
	if m == nil {
		m = map[int64]struct{}{}
		s.byBook[bookID] = m
	}
	if _, dup := m[tsNanos]; !dup {
		m[tsNanos] = struct{}{}
		s.count++
	}
}

// addFloor records one provisional row's floor on bookID. Identical floors
// collapse (they pin the same snapshot); different ones are all kept.
func (s *SnapshotPins) addFloor(bookID string, floor int64) {
	if bookID == "" {
		return
	}
	if slices.Contains(s.floors[bookID], floor) {
		return
	}
	s.floors[bookID] = append(s.floors[bookID], floor)
}

// snapshotPinGeneration is the sum of every source's write counter. Each
// counter only increases, so the sum changes whenever any of them does.
func snapshotPinGeneration() uint64 {
	var g uint64
	for _, src := range snapshotPinSources {
		g += src.generation()
	}
	return g
}

// snapshotPinCache holds the last built set. Building scans every journal row,
// so a whole-library prune (one PruneBookSnapshots call per book, from a pool
// of workers) must pay for it once, not once per book.
type snapshotPinCache struct {
	mu  sync.Mutex
	cur *SnapshotPins
	// built distinguishes "never built" from "built at generation 0": the
	// counters start at zero with journal rows already on disk.
	built bool
	// failErr is the last build failure, kept for pinFailureTTL at the journal
	// generation it failed at. An unreadable row does not mend itself, and
	// without this every book of a whole-library prune would scan the journal up
	// to the bad row again (59k scans on one lock).
	failErr error
	failGen uint64
	failAt  time.Time
}

// pinFailureTTL is how long a failed pin build is answered from memory. A write
// to the journal (a new generation) ends it early; so does the TTL, which is what
// lets an operator's repair of the bad row take effect without a restart.
const pinFailureTTL = time.Minute

// pinClock is the clock the failure memo reads; a test replaces it.
var pinClock = time.Now

// buildSnapshotPins scans every journal source once.
func buildSnapshotPins(db *GatedDB) (*SnapshotPins, error) {
	// The generation is read BEFORE the scan. A write that lands during the scan
	// is committed before its counter moves, so either the scan saw it or the
	// counter is already ahead of this value and the set reads as stale.
	pins := &SnapshotPins{gen: snapshotPinGeneration(), byBook: map[string]map[int64]struct{}{}, floors: map[string][]int64{}}
	for _, src := range snapshotPinSources {
		if err := src.collect(db, pins); err != nil {
			return nil, fmt.Errorf("snapshot pins from %s: %w", src.name, err)
		}
	}
	return pins, nil
}

// snapshotPins returns the pinned set, rebuilding it only when a journal was
// written since it was built. An error means the pins cannot be known, and every
// caller must then delete nothing.
func (p *PebbleStore) snapshotPins() (*SnapshotPins, error) {
	c := &p.snapPins
	c.mu.Lock()
	defer c.mu.Unlock()
	gen := snapshotPinGeneration()
	if c.built && c.cur.gen == gen {
		return c.cur, nil
	}
	if c.failErr != nil && c.failGen == gen && pinClock().Sub(c.failAt) < pinFailureTTL {
		return nil, c.failErr
	}
	pins, err := buildSnapshotPins(p.db)
	if err != nil {
		c.built, c.cur = false, nil
		c.failErr, c.failGen, c.failAt = err, gen, pinClock()
		return nil, err
	}
	c.built, c.cur = true, pins
	c.failErr = nil
	return pins, nil
}

// CheckSnapshotPins reports whether every journal row that pins a snapshot can be
// read. A whole-library prune calls it once before it starts: with an unreadable
// row no book can be pruned, so the run fails at once naming the row (a
// *MergeJournalRowUnreadableError) instead of failing once per book.
func (p *PebbleStore) CheckSnapshotPins() error {
	_, err := p.snapshotPins()
	return err
}

// MergeJournalRowUnreadableError is a merge journal row that cannot be decoded.
// The prune refuses to run while one exists: the row may be the one that names
// the snapshot an undo needs.
type MergeJournalRowUnreadableError struct {
	// Key is the row's journal key, enough to find and repair it.
	Key string
	Err error
}

func (e *MergeJournalRowUnreadableError) Error() string {
	return fmt.Sprintf("a merge journal row is unreadable: %s; prune is blocked until it is repaired (%v)", e.Key, e.Err)
}
func (e *MergeJournalRowUnreadableError) Unwrap() error { return e.Err }

// pinsAreCurrent reports whether no journal was written since pins was built.
func pinsAreCurrent(pins *SnapshotPins) bool { return pins.gen == snapshotPinGeneration() }

// provisionalStaleAfter is how old a still-provisional auto-merge entry must be
// before the audit counts it as stale. It changes what is REPORTED, never what is
// pinned: MergeBooksJournaled patches the entry within seconds of the merge, so a
// provisional entry an hour old is one whose merge failed or whose patch could not
// be written, which is a bug to look at, not a reason to unpin.
const provisionalStaleAfter = time.Hour

// scanAutoMergeJournal calls fn for every auto-merge journal row, in key order.
// A row that cannot be decoded is an error, not a skip: the caller is deciding
// what is safe to delete, and an unreadable row might be the one that matters.
func scanAutoMergeJournal(db *GatedDB, fn func(e AutoMergeJournalEntry) error) error {
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(dedupAutoMergeJournalPfx),
		UpperBound: append([]byte(dedupAutoMergeJournalPfx), 0xff),
	})
	if err != nil {
		return fmt.Errorf("iter automerge journal: %w", err)
	}
	defer func() { _ = iter.Close() }()
	for iter.First(); iter.Valid(); iter.Next() {
		var e AutoMergeJournalEntry
		if err := json.Unmarshal(iter.Value(), &e); err != nil {
			return &MergeJournalRowUnreadableError{Key: string(iter.Key()), Err: fmt.Errorf("it cannot be decoded, so the snapshots it holds cannot be told: %w", err)}
		}
		e.Key = string(iter.Key())
		if err := fn(e); err != nil {
			return err
		}
	}
	return iter.Error()
}

// collectAutoMergeSnapshotPins pins the snapshots of every auto-merge entry that
// is still live: not undone, not aborted. An undone entry is refused by
// UnmergeAuto and an aborted one never merged anything, so neither needs its
// snapshots.
//
// A finalized entry names its two snapshots, which are pinned exactly.
//
// A provisional entry (written BEFORE the merge, patched with the timestamps
// AFTER it) names none, and the snapshot the patch is about to name is one the
// merge itself writes. What the patch will name is the earliest snapshot newer
// than the book's baseline (EarliestSnapshotAbove), so the entry records one
// floor per book and pins exactly that one snapshot per book, resolved when the
// prune decides (SnapshotPins.Pinned). The floor is the baseline the entry
// recorded (BaselinesRecorded; 0 is then a real baseline: the book had no
// snapshot). An entry written before baselines were recorded has no baseline, so
// its floor is MergedAt, set after the baselines were read and before the merge
// wrote anything; 0 only when even that is missing, which pins the book's oldest
// snapshot, the same answer the patch would have given.
//
// A provisional entry written by this version lists EVERY book of its merge with
// the baseline recorded for it (Participants), and holds a floor for each, not
// only for its own winner and loser. The post-merge patch rewrites the merge's
// rows against the winner MergeBooks actually elected, so when the winner was
// predicted wrongly and one row's patch write fails, the surviving provisional row
// is not the pair it names: it still covers every book of the merge, so no real
// loser is left in no row. An entry without the list covers its winner and loser,
// as before.
//
// There is no expiry. A provisional entry stays provisional when its merge failed
// in a way that does not prove nothing was written, or its patch could not be
// written; UnmergeAuto refuses it and nothing else clears it, so each such entry
// keeps ONE snapshot per book for as long as the row exists. A refusal that
// provably wrote nothing closes the row (AbortedAt) and releases it. The rest is
// reported by the dry run (MergeJournalSnapshotAudit.StaleProvisional) rather
// than hidden by unpinning while the row still exists.
func collectAutoMergeSnapshotPins(db *GatedDB, pins *SnapshotPins) error {
	return scanAutoMergeJournal(db, func(e AutoMergeJournalEntry) error {
		if e.UndoneAt != 0 || e.AbortedAt != 0 {
			return nil
		}
		if e.Provisional {
			e.addProvisionalFloors(pins)
			return nil
		}
		pins.add(e.WinnerID, e.WinnerPreMergeTS)
		pins.add(e.LoserID, e.LoserPreMergeTS)
		return nil
	})
}

// SnapshotPrunePlanner is the read side of the snapshot prune: what a prune
// would do, and which merges a past prune already made impossible to undo.
//
// It is deliberately a capability resolved with AsCapability, NOT part of
// Store. Store is a ratchet (store_width_test.go: the flattened method count may
// only fall), and the one caller, the prune-book-snapshots job, needs these two
// methods and nothing else of them. Naming the interface keeps the requirement
// greppable and keeps Store from growing two methods for one job.
type SnapshotPrunePlanner interface {
	// PlanBookSnapshotPrune reports what PruneBookSnapshots(id, keepCount) would
	// do without deleting: how many it would remove and how many it keeps only
	// because a live merge journal row points at them. A dry run takes its
	// numbers from here, not from arithmetic on a snapshot count.
	PlanBookSnapshotPrune(id string, keepCount int) (BookSnapshotPrunePlan, error)
	// AuditMergeJournalSnapshots counts live auto-merge journal entries whose
	// snapshots are already gone (ids only, capped). Read-only; one pass over the
	// journal.
	AuditMergeJournalSnapshots(idCap int) (MergeJournalSnapshotAudit, error)
	// CheckSnapshotPins fails, naming the journal row, when a row that may pin a
	// snapshot cannot be read. Called once before a whole-library prune.
	CheckSnapshotPins() error
}

var (
	_ SnapshotPrunePlanner = (*PebbleStore)(nil)
	_ SnapshotPrunePlanner = (*MockStore)(nil)
)

// MergeJournalSnapshotAudit is a read-only count of live auto-merge journal
// entries whose snapshots are already gone, so an operator learns whether a
// prune (this one before the pin existed, or a manual delete) has already made
// a merge impossible to undo. It repairs nothing.
type MergeJournalSnapshotAudit struct {
	// LiveEntries is the finalized, not-undone entries that name a snapshot.
	LiveEntries int
	// MissingSnapshotEntries is how many of them name a snapshot that no longer
	// exists, so UnmergeAuto would refuse them.
	MissingSnapshotEntries int
	// MissingJournalIDs lists those entries' journal ids (ids only), up to the cap.
	MissingJournalIDs []string
	// Truncated is how many more than the cap there were.
	Truncated int

	// StaleProvisional is how many entries have been provisional for longer than
	// provisionalStaleAfter (or carry no merge time): the merge failed in a way
	// that does not prove nothing was written, or its patch was never written.
	// Each still pins one snapshot per book (the first above its floor), for as
	// long as the row exists. Aborted rows are not counted: they are closed.
	StaleProvisional int
	// StaleProvisionalIDs lists their journal ids (ids only), up to the cap.
	StaleProvisionalIDs []string
	// StaleProvisionalTruncated is how many more than the cap there were.
	StaleProvisionalTruncated int
}

// AuditMergeJournalSnapshots checks every live auto-merge entry's snapshots with
// one point read each. It is one pass over the journal, not one per book.
func (p *PebbleStore) AuditMergeJournalSnapshots(idCap int) (MergeJournalSnapshotAudit, error) {
	var a MergeJournalSnapshotAudit
	exists := func(id string, ts int64) (bool, error) {
		if id == "" || ts == 0 {
			return true, nil // names no snapshot on this side
		}
		_, closer, err := p.db.Get([]byte(fmt.Sprintf("book_ver:%s:%d", id, ts)))
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		_ = closer.Close()
		return true, nil
	}
	now := time.Now().UnixNano()
	err := scanAutoMergeJournal(p.db, func(e AutoMergeJournalEntry) error {
		if e.UndoneAt == 0 && e.AbortedAt == 0 && e.Provisional && (e.MergedAt == 0 || now-e.MergedAt > int64(provisionalStaleAfter)) {
			a.StaleProvisional++
			if len(a.StaleProvisionalIDs) < idCap {
				a.StaleProvisionalIDs = append(a.StaleProvisionalIDs, e.Key)
			} else {
				a.StaleProvisionalTruncated++
			}
		}
		if e.UndoneAt != 0 || e.AbortedAt != 0 || e.Provisional || (e.WinnerPreMergeTS == 0 && e.LoserPreMergeTS == 0) {
			return nil
		}
		a.LiveEntries++
		wok, err := exists(e.WinnerID, e.WinnerPreMergeTS)
		if err != nil {
			return err
		}
		lok, err := exists(e.LoserID, e.LoserPreMergeTS)
		if err != nil {
			return err
		}
		if wok && lok {
			return nil
		}
		a.MissingSnapshotEntries++
		if len(a.MissingJournalIDs) < idCap {
			a.MissingJournalIDs = append(a.MissingJournalIDs, e.Key)
		} else {
			a.Truncated++
		}
		return nil
	})
	if err != nil {
		return MergeJournalSnapshotAudit{}, err
	}
	return a, nil
}

// BookSnapshotPrunePlan is what a prune of one book would do.
type BookSnapshotPrunePlan struct {
	// Total is the book's parseable snapshots.
	Total int
	// Delete is how many a prune would remove: older than the newest keepCount
	// and not pinned.
	Delete int
	// PinnedByJournal is how many a prune would have removed but keeps because a
	// live merge journal row points at them.
	PinnedByJournal int
}

// planSnapshotPrune is the single decision about which snapshots a prune
// deletes. keys is ascending by timestamp (bookSnapshotKeys). The newest
// keepCount are always kept; of the older ones, a pinned snapshot is kept too.
func planSnapshotPrune(id string, keys [][]byte, keepCount int, pins *SnapshotPins) (toDelete [][]byte, plan BookSnapshotPrunePlan) {
	if keepCount < 0 {
		keepCount = 0
	}
	plan.Total = len(keys)
	if len(keys) <= keepCount {
		return nil, plan
	}
	// bookSnapshotKeys already dropped every key that does not parse, so a parse
	// failure cannot happen here; if it did, the key could not match a pin and
	// would be treated like any other unpinned key.
	prefix := fmt.Sprintf("book_ver:%s:", id)
	tss := make([]int64, len(keys))
	for i, k := range keys {
		tss[i], _ = strconv.ParseInt(strings.TrimPrefix(string(k), prefix), 10, 64)
	}
	// Resolved against THIS key list, not against the snapshots that existed when
	// the pins were built: a floor pins the first snapshot above it, and that
	// snapshot may have been written since.
	pinned := pins.Pinned(id, tss)
	for i, k := range keys[:len(keys)-keepCount] {
		if _, ok := pinned[tss[i]]; ok {
			plan.PinnedByJournal++
			continue
		}
		toDelete = append(toDelete, k)
	}
	plan.Delete = len(toDelete)
	return toDelete, plan
}

// PlanBookSnapshotPrune reports what PruneBookSnapshots(id, keepCount) would do,
// deleting nothing. A dry run must come from here rather than from arithmetic on
// a snapshot count: the pinned snapshots are exactly where the two differ.
func (p *PebbleStore) PlanBookSnapshotPrune(id string, keepCount int) (BookSnapshotPrunePlan, error) {
	keys, err := p.bookSnapshotKeys(id)
	if err != nil {
		return BookSnapshotPrunePlan{}, err
	}
	pins, err := p.snapshotPins()
	if err != nil {
		return BookSnapshotPrunePlan{}, err
	}
	_, plan := planSnapshotPrune(id, keys, keepCount, pins)
	return plan, nil
}
