// file: internal/books/store/snapshot_pins.go
// version: 1.0.0
// guid: 4a7d1c93-e5b2-4f08-9c61-2d8f0b3a6e75
// last-edited: 2026-11-17

package store

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// (pins.add), and every book a row pins from a floor (pins.addAfter). It
	// returns an error, rather than skipping, for a row it cannot decode: a
	// row that cannot be read might be the one that names the snapshot.
	collect func(db *GatedDB, pins *SnapshotPins) error
}

var snapshotPinSources = []snapshotPinSource{
	{name: dedupAutoMergeJournalPfx, generation: AutoMergeJournalGeneration, collect: collectAutoMergeSnapshotPins},
}

// SnapshotPins is what a live journal row points at: exact (book, snapshot
// timestamp) pairs, and per book a RANGE, every snapshot written after a floor.
// The zero value and nil pin nothing.
//
// The range exists for the one journal state that names no timestamp yet: a
// provisional auto-merge entry (see collectAutoMergeSnapshotPins). A list built
// from the snapshots present at build time would miss one written after the set
// was built, which is exactly when the merge's own writes land, so the answer
// for such a book is computed from the floor at Has time and the cached set never
// goes stale for it.
type SnapshotPins struct {
	gen    uint64
	byBook map[string]map[int64]struct{}
	// after maps a book to the floor of its range: snapshots with a timestamp
	// strictly greater than the floor are pinned. 0 pins every snapshot, which is
	// what a floor that cannot be known means.
	after map[string]int64
	count int
}

// Has reports whether the snapshot book_ver:<bookID>:<tsNanos> is pinned.
func (s *SnapshotPins) Has(bookID string, tsNanos int64) bool {
	if s == nil {
		return false
	}
	if floor, ok := s.after[bookID]; ok && tsNanos > floor {
		return true
	}
	_, ok := s.byBook[bookID][tsNanos]
	return ok
}

// Len is the number of distinct exact pins.
func (s *SnapshotPins) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}

// RangedBooks is how many books are pinned by a range.
func (s *SnapshotPins) RangedBooks() int {
	if s == nil {
		return 0
	}
	return len(s.after)
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

// addAfter pins every snapshot of bookID newer than floor. Two ranges on one book
// merge to the lower floor, the wider of the two.
func (s *SnapshotPins) addAfter(bookID string, floor int64) {
	if bookID == "" {
		return
	}
	if cur, ok := s.after[bookID]; !ok || floor < cur {
		s.after[bookID] = floor
	}
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
}

// buildSnapshotPins scans every journal source once.
func buildSnapshotPins(db *GatedDB) (*SnapshotPins, error) {
	// The generation is read BEFORE the scan. A write that lands during the scan
	// is committed before its counter moves, so either the scan saw it or the
	// counter is already ahead of this value and the set reads as stale.
	pins := &SnapshotPins{gen: snapshotPinGeneration(), byBook: map[string]map[int64]struct{}{}, after: map[string]int64{}}
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
	if c.built && c.cur.gen == snapshotPinGeneration() {
		return c.cur, nil
	}
	pins, err := buildSnapshotPins(p.db)
	if err != nil {
		c.built, c.cur = false, nil
		return nil, err
	}
	c.built, c.cur = true, pins
	return pins, nil
}

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
			return fmt.Errorf("automerge journal entry %s cannot be decoded, so the snapshots it holds cannot be told: %w", iter.Key(), err)
		}
		e.Key = string(iter.Key())
		if err := fn(e); err != nil {
			return err
		}
	}
	return iter.Error()
}

// collectAutoMergeSnapshotPins pins the snapshots of every auto-merge entry that
// has not been undone. An undone entry is refused by UnmergeAuto, so its
// snapshots are no longer needed and become prunable again.
//
// A finalized entry names its two snapshots, which are pinned exactly.
//
// A provisional entry (written BEFORE the merge, patched with the timestamps
// AFTER it) names none, and the snapshot the patch is about to name is one the
// merge itself writes. It therefore pins a RANGE on each of its two books: every
// snapshot newer than the book's baseline, the newest snapshot that existed when
// MergeBooksJournaled began, which the entry records. The pre-merge record the
// patch looks for is "the earliest snapshot newer than the baseline", so the
// range covers it wherever and whenever it is written, including after this set
// was built. A baseline that was not recorded (an entry from before the field
// existed, or a book with no snapshot then) is 0, which pins every snapshot of
// the book: when the floor cannot be known the answer is "all of them".
//
// There is no expiry. A provisional entry stays provisional when its merge
// failed or its patch could not be written, UnmergeAuto refuses it, and nothing
// else clears it, so those entries keep the later snapshots of their two books;
// that cost is reported by the dry run (MergeJournalSnapshotAudit.StaleProvisional)
// rather than hidden by unpinning while the row still exists.
func collectAutoMergeSnapshotPins(db *GatedDB, pins *SnapshotPins) error {
	return scanAutoMergeJournal(db, func(e AutoMergeJournalEntry) error {
		if e.UndoneAt != 0 {
			return nil
		}
		if e.Provisional {
			pins.addAfter(e.WinnerID, e.WinnerBaselineTS)
			pins.addAfter(e.LoserID, e.LoserBaselineTS)
			return nil
		}
		pins.add(e.WinnerID, e.WinnerPreMergeTS)
		pins.add(e.LoserID, e.LoserPreMergeTS)
		return nil
	})
}

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
	// provisionalStaleAfter (or carry no merge time): the merge failed or its
	// patch was never written. Each still pins every snapshot of its two books
	// newer than the recorded baseline, for as long as the row exists.
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
		if e.UndoneAt == 0 && e.Provisional && (e.MergedAt == 0 || now-e.MergedAt > int64(provisionalStaleAfter)) {
			a.StaleProvisional++
			if len(a.StaleProvisionalIDs) < idCap {
				a.StaleProvisionalIDs = append(a.StaleProvisionalIDs, e.Key)
			} else {
				a.StaleProvisionalTruncated++
			}
		}
		if e.UndoneAt != 0 || e.Provisional || (e.WinnerPreMergeTS == 0 && e.LoserPreMergeTS == 0) {
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
	prefix := fmt.Sprintf("book_ver:%s:", id)
	for _, k := range keys[:len(keys)-keepCount] {
		// bookSnapshotKeys already dropped every key that does not parse, so a
		// parse failure cannot happen here; if it did, the key could not match a
		// pin and would be treated like any other unpinned key.
		if ts, err := strconv.ParseInt(strings.TrimPrefix(string(k), prefix), 10, 64); err == nil && pins.Has(id, ts) {
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
