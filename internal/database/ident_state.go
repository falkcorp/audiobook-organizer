// file: internal/database/ident_state.go
// version: 1.0.0
// guid: c4038689-1fb5-485d-86f8-f91b20eeac6a
// last-edited: 2026-10-11

package database

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Book identification state (spec 02 section 3.2, "Book identification
// sub-state"): every book is in exactly one IdentState, kept as an index that
// is maintained by write-through in the same batch as the change that moves
// it, so the count of each state is a counter read (O(1)), never a scan.
//
// Storage, all under the "ident:" families (registered in keyfamilies.go):
//
//	ident:state:<bookID>          -> identRow JSON {state, reason, primary}
//	ident:by:<state>:<bookID>     -> one byte: '1' when the book is in the
//	                                 primary scope, '0' otherwise. Membership
//	                                 is the key; the byte lets a recount and
//	                                 the primary counters be checked from the
//	                                 keys alone, without reading state rows.
//	ident:n:<scope>:<state>       -> 8-byte big-endian counter, scope all|primary
//	ident:dirty:<bookID>          -> 8-byte big-endian epoch (the dirty set)
//	ident:meta:built              -> the index format version, as decimal text
//
// Who keeps it current, and under which lock: see stageIdent and the writer
// list on MetadataCacheGeneration. Every writer of an input -- a cache row, a
// book's review status, its primary flag, its trash or quarantine bit --
// holds the book's metadataCacheLock stripe from its read of the old state row
// through its commit, and identMu (identIndex.mu) from the counter read
// through the commit. Lock order: book-family stripes, then the cache stripe,
// then identMu. Nothing holding identMu takes another lock.

// IdentState is a book's identification state.
type IdentState string

// The five identification states. They are exhaustive: every book is in
// exactly one.
const (
	// IdentUnasked: no metadata_cache row. Nobody has asked the providers
	// about this book, or its row was removed.
	IdentUnasked IdentState = "unasked"
	// IdentAskedEmpty: a cache row with no candidate. The providers were
	// asked and had nothing (LastEmptyFetchAt / EmptyAnswers record when).
	IdentAskedEmpty IdentState = "asked_empty"
	// IdentHasCandidates: a cache row with at least one candidate that nobody
	// has applied. Reason "stale" when the row is marked Stale.
	IdentHasCandidates IdentState = "has_candidates"
	// IdentApplied: the book's MetadataReviewStatus records an applied match
	// (MetadataApplied: "matched" or "audio_confirmed"), whatever its row
	// holds.
	IdentApplied IdentState = "applied"
	// IdentHeld: the owner ruled the book has no match ("no_match"). See
	// DeriveIdentState for why owner-rejected candidates do not put a book
	// here yet.
	IdentHeld IdentState = "held"
)

// Reasons this package emits. Later work adds others (fragment,
// manual_only, quota_deferred, no_usable_title).
const (
	// IdentReasonStale: has_candidates from a row marked Stale (the book was
	// retitled or re-credited after its candidates were fetched; D29).
	IdentReasonStale = "stale"
	// IdentReasonNoMatch: held because MetadataReviewStatus is "no_match".
	IdentReasonNoMatch = "no_match"
	// IdentReasonRejected is reserved for a book the owner rejected. Nothing
	// emits it yet: there is no book-level rejection marker, only
	// per-candidate metadata_rejection rows. See DeriveIdentState.
	IdentReasonRejected = "rejected"
)

// AllIdentStates lists the states in counter order.
var AllIdentStates = []IdentState{IdentUnasked, IdentAskedEmpty, IdentHasCandidates, IdentApplied, IdentHeld}

const numIdentStates = 5

// identStateIndex is the counter slot of s, or -1 for an unknown state.
func identStateIndex(s IdentState) int {
	for i, v := range AllIdentStates {
		if v == s {
			return i
		}
	}
	return -1
}

// ValidIdentState reports whether s names one of the five states.
func ValidIdentState(s IdentState) bool { return identStateIndex(s) >= 0 }

// ErrIdentIndexBuilding is returned by the identification-state reads while
// the index is being (re)built: its counts and membership are not yet
// trustworthy. The Library answers it with 503 and Retry-After, never with a
// count that may be wrong.
var ErrIdentIndexBuilding = errors.New("identification index is being built; retry shortly")

// identIndexVersion is the index format. A marker with another version (or
// none) makes the next EnsureIdentIndex rebuild from scratch with the index
// reported not ready.
const identIndexVersion = 1

const (
	identStatePrefix = "ident:state:"
	identByPrefix    = "ident:by:"
	identCountPrefix = "ident:n:"
	identDirtyPrefix = "ident:dirty:"
	identMetaPrefix  = "ident:meta:"
	identBuiltKey    = identMetaPrefix + "built"
)

// identScopeAll and identScopePrimary index identIndex.counts.
const (
	identScopeAll     = 0
	identScopePrimary = 1
)

var identScopeNames = [2]string{"all", "primary"}

func identStateKey(bookID string) []byte { return []byte(identStatePrefix + bookID) }

func identByKey(s IdentState, bookID string) []byte {
	return []byte(identByPrefix + string(s) + ":" + bookID)
}

func identByStatePrefix(s IdentState) []byte { return []byte(identByPrefix + string(s) + ":") }

func identCountKey(scope int, s IdentState) []byte {
	return []byte(identCountPrefix + identScopeNames[scope] + ":" + string(s))
}

func identDirtyKey(bookID string) []byte { return []byte(identDirtyPrefix + bookID) }

var identLog = logger.New("database")

// identRow is the stored state of one book.
type identRow struct {
	State  IdentState `json:"s"`
	Reason string     `json:"r,omitempty"`
	// Primary is the book's membership of the primary scope (identInScope).
	Primary bool `json:"p,omitempty"`
}

func (r identRow) scopeByte() []byte {
	if r.Primary {
		return []byte{'1'}
	}
	return []byte{'0'}
}

// identInScope reports whether book belongs to the "primary" counter scope:
// the books the Library lists by default. That is a primary version
// (EffectiveIsPrimaryVersion: a nil flag counts as primary, as every Library
// filter reads it), not in the trash, and not quarantined -- the list
// excludes quarantined books unless show_quarantined is set. The scope must
// be exactly the default listing's, or the O(1) count of ident:missing would
// disagree with the "Select all N" the same listing shows.
func identInScope(b *Book) bool {
	return b != nil && EffectiveIsPrimaryVersion(b.IsPrimaryVersion) && !bookIsSoftDeleted(b) && b.QuarantinedAt == nil
}

// DeriveIdentState is the one rule that maps a book and its metadata_cache
// row to an identification state and reason. The rules, first match wins:
//
//	book's MetadataReviewStatus is "matched" or "audio_confirmed"
//	    (MetadataApplied)                    -> applied, whatever the row says
//	MetadataReviewStatus is "no_match"       -> held, reason "no_match"
//	no cache row (cache == nil)              -> unasked
//	row with zero candidates                 -> asked_empty
//	row with at least one candidate, Stale   -> has_candidates, reason "stale"
//	row with at least one candidate          -> has_candidates
//
// "held" covers only the owner's no_match ruling. An owner-rejected
// CANDIDATE (a metadata_rejection row) does not hold its book: the book may
// still have other candidates, and there is no book-level rejection marker
// to read. That is the one judgement call in this mapping and is left for
// the owner to confirm.
//
// The goal number ("missing" in appendix C) is unasked + asked_empty over the
// primary scope: fetched-but-unapproved books are not missing.
func DeriveIdentState(book *Book, cache *MetadataCandidateCache) (IdentState, string) {
	var status *string
	if book != nil {
		status = book.MetadataReviewStatus
	}
	if cache == nil {
		return deriveIdent(status, false, 0, false)
	}
	return deriveIdent(status, true, len(cache.Candidates), cache.Stale)
}

// deriveIdent is DeriveIdentState over the few inputs it reads, so the
// rebuild can derive from a partial decode of the rows.
func deriveIdent(status *string, hasRow bool, candidates int, stale bool) (IdentState, string) {
	switch {
	case MetadataApplied(status):
		return IdentApplied, ""
	case status != nil && *status == "no_match":
		return IdentHeld, IdentReasonNoMatch
	case !hasRow:
		return IdentUnasked, ""
	case candidates == 0:
		return IdentAskedEmpty, ""
	case stale:
		return IdentHasCandidates, IdentReasonStale
	}
	return IdentHasCandidates, ""
}

// identBookInputsChanged reports whether a book write changes anything
// DeriveIdentState or the scope reads from the book row.
func identBookInputsChanged(oldBook, newBook *Book) bool {
	if (oldBook == nil) != (newBook == nil) {
		return true
	}
	if oldBook == nil {
		return false
	}
	oldS, _ := deriveIdent(oldBook.MetadataReviewStatus, false, 0, false)
	newS, _ := deriveIdent(newBook.MetadataReviewStatus, false, 0, false)
	return oldS != newS || identInScope(oldBook) != identInScope(newBook)
}

// identIndex is the in-process half of the index.
type identIndex struct {
	// mu is identMu: it serializes the counter read-modify-write AND the
	// commit that carries it, so two batches can never stage the same old
	// counter value. It also serializes the rebuild's final recount and the
	// ready flip against every writer's commit.
	mu sync.Mutex
	// counts mirrors the persisted ident:n: counters. Guarded by mu; only
	// meaningful while ready.
	counts [2][numIdentStates]int64
	// published is counts as last published (setCounts / publish), read
	// without mu by IdentCounts: a counter read must not wait behind a
	// writer holding mu across its synced commit.
	published [2][numIdentStates]atomic.Int64
	// ready is true when the counters and membership keys can be trusted.
	// Written under mu; read without it by the query paths.
	ready atomic.Bool
	// rebuildMu serializes RebuildIdentIndex runs.
	rebuildMu sync.Mutex
	// lastEpoch is the last dirty-set epoch handed out.
	lastEpoch atomic.Uint64
}

// publish copies counts into published. Called with mu held, after every
// change of counts.
func (x *identIndex) publish() {
	for scope := range x.counts {
		for i := range x.counts[scope] {
			x.published[scope][i].Store(x.counts[scope][i])
		}
	}
}

// setCounts replaces counts and publishes them. Called with mu held.
func (x *identIndex) setCounts(c [2][numIdentStates]int64) {
	x.counts = c
	x.publish()
}

// IdentCounts is a snapshot of the per-state counters.
type IdentCounts struct {
	// Ready is false while the index is being built; the counts are then
	// zero and must not be shown.
	Ready   bool
	All     map[IdentState]int64
	Primary map[IdentState]int64
}

// Missing is the goal number for a scope's counts: unasked + asked_empty.
func IdentMissing(m map[IdentState]int64) int64 {
	return m[IdentUnasked] + m[IdentAskedEmpty]
}

// IdentIndexReader is the narrow capability the Library filter and the
// review summary read the index through (AsCapability).
type IdentIndexReader interface {
	IdentCounts() IdentCounts
	IdentBookIDs(states []IdentState) (map[string]struct{}, error)
}

var _ IdentIndexReader = (*PebbleStore)(nil)

// IdentCounts returns the counters. O(1) and lock-free: it reads the
// published mirror of the persisted counters, never waiting on identMu
// (which a writer holds across its synced commit). A read that races a
// write can see that one write half applied (a book leaving one state
// before it arrives in the other); the next read sees it whole.
func (p *PebbleStore) IdentCounts() IdentCounts {
	out := IdentCounts{
		All:     make(map[IdentState]int64, numIdentStates),
		Primary: make(map[IdentState]int64, numIdentStates),
	}
	if !p.ident.ready.Load() {
		return out
	}
	out.Ready = true
	for i, s := range AllIdentStates {
		out.All[s] = p.ident.published[identScopeAll][i].Load()
		out.Primary[s] = p.ident.published[identScopePrimary][i].Load()
	}
	return out
}

// IdentBookIDs returns the IDs of every book (any scope) in one of states,
// read from the ident:by: membership keys (keys only). It returns
// ErrIdentIndexBuilding while the index is not ready.
func (p *PebbleStore) IdentBookIDs(states []IdentState) (map[string]struct{}, error) {
	if !p.ident.ready.Load() {
		return nil, ErrIdentIndexBuilding
	}
	out := make(map[string]struct{})
	seen := make(map[IdentState]bool, len(states))
	for _, s := range states {
		if !ValidIdentState(s) {
			return nil, fmt.Errorf("unknown identification state %q", s)
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		prefix := identByStatePrefix(s)
		if err := forEachKeyInRange(p.db, prefix, prefixUpperBound(prefix), func(key, _ []byte) error {
			out[string(key[len(prefix):])] = struct{}{}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read ident:by:%s: %w", s, err)
		}
	}
	if !p.ident.ready.Load() {
		// A wipe dropped the index while this read ran.
		return nil, ErrIdentIndexBuilding
	}
	return out, nil
}

// readIdentRow reads bookID's stored state row.
func (p *PebbleStore) readIdentRow(bookID string) (identRow, bool, error) {
	val, closer, err := p.db.Get(identStateKey(bookID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return identRow{}, false, nil
		}
		return identRow{}, false, fmt.Errorf("read ident:state:%s: %w", bookID, err)
	}
	defer closer.Close()
	var r identRow
	if err := json.Unmarshal(val, &r); err != nil || !ValidIdentState(r.State) {
		// An unreadable row is treated as absent: the write that follows
		// replaces it. Its ident:by: key, if any, is found by the rebuild's
		// membership check; the counters are corrected by the drift repair.
		identLog.Warn("ident:state:%s unreadable, treating it as absent: %v", logger.SanitizeLogValue(bookID), err)
		return identRow{}, false, nil
	}
	return r, true, nil
}

// identTxn stages the identification changes of one batch. The protocol:
//
//	t := p.beginIdent(batch)
//	defer t.end(false)           // releases identMu on every early return
//	t.stage(id, book, cache)     // per book, under that book's cache stripe
//	t.prepare()                  // takes identMu when any row changed
//	batch.Commit(...)
//	t.end(true)                  // mirrors the counters, releases identMu
//
// stage reads each book's old row and stages the new row and membership key.
// prepare takes identMu (held through the commit) and, when the index is
// ready, stages the new absolute counter values. A batch that changes no row
// never takes identMu.
type identTxn struct {
	p       *PebbleStore
	batch   *GatedBatch
	changed bool
	locked  bool
	counted bool
	delta   [2][numIdentStates]int64
	// staged maps book id -> the row this batch leaves (present=false for a
	// removal), so a second stage of one book in the same batch reads it.
	staged map[string]identStaged
}

type identStaged struct {
	row     identRow
	present bool
}

func (p *PebbleStore) beginIdent(batch *GatedBatch) *identTxn {
	return &identTxn{p: p, batch: batch}
}

// stage stages bookID's identification state as derived from book (nil: the
// book does not exist, so its rows are removed) and cache (nil: no row). The
// caller holds bookID's metadataCacheLock stripe until the batch commits.
func (t *identTxn) stage(bookID string, book *Book, cache *MetadataCandidateCache) error {
	if book == nil {
		return t.stageRow(bookID, identRow{}, false)
	}
	s, reason := DeriveIdentState(book, cache)
	return t.stageRow(bookID, identRow{State: s, Reason: reason, Primary: identInScope(book)}, true)
}

func (t *identTxn) stageRow(bookID string, want identRow, wantPresent bool) error {
	var old identRow
	var had bool
	if prev, ok := t.staged[bookID]; ok {
		old, had = prev.row, prev.present
	} else {
		var err error
		old, had, err = t.p.readIdentRow(bookID)
		if err != nil {
			return err
		}
	}
	if had == wantPresent && (!had || old == want) {
		return nil
	}
	if had {
		if !wantPresent || old.State != want.State {
			if err := t.batch.Delete(identByKey(old.State, bookID), nil); err != nil {
				return err
			}
		}
		t.count(old, -1)
	}
	if wantPresent {
		data, err := json.Marshal(want)
		if err != nil {
			return fmt.Errorf("encode ident:state:%s: %w", bookID, err)
		}
		if err := t.batch.Set(identStateKey(bookID), data, nil); err != nil {
			return err
		}
		if err := t.batch.Set(identByKey(want.State, bookID), want.scopeByte(), nil); err != nil {
			return err
		}
		t.count(want, +1)
	} else if err := t.batch.Delete(identStateKey(bookID), nil); err != nil {
		return err
	}
	if t.staged == nil {
		t.staged = make(map[string]identStaged, 1)
	}
	t.staged[bookID] = identStaged{row: want, present: wantPresent}
	t.changed = true
	return nil
}

func (t *identTxn) count(r identRow, d int64) {
	i := identStateIndex(r.State)
	t.delta[identScopeAll][i] += d
	if r.Primary {
		t.delta[identScopePrimary][i] += d
	}
}

// prepare takes identMu when the batch changes any row, and, when the index
// is ready, stages the counters this batch moves at their new absolute
// values. identMu is then held until end.
func (t *identTxn) prepare() error {
	if !t.changed || t.locked {
		return nil
	}
	t.p.ident.mu.Lock()
	t.locked = true
	if !t.p.ident.ready.Load() {
		// Not ready: the rebuild's final recount (under identMu, after this
		// commit) counts these rows.
		return nil
	}
	for scope := range t.delta {
		for i, d := range t.delta[scope] {
			if d == 0 {
				continue
			}
			v := t.p.ident.counts[scope][i] + d
			if err := t.batch.Set(identCountKey(scope, AllIdentStates[i]), encodeIdentCounter(v), nil); err != nil {
				return err
			}
		}
	}
	t.counted = true
	return nil
}

// end mirrors the staged counters into memory when the batch committed, and
// releases identMu. It is idempotent, so a deferred end(false) is safe after
// an explicit end(true).
func (t *identTxn) end(committed bool) {
	if !t.locked {
		return
	}
	if committed && t.counted {
		for scope := range t.delta {
			for i, d := range t.delta[scope] {
				t.p.ident.counts[scope][i] += d
			}
		}
		t.p.ident.publish()
	}
	t.locked, t.counted = false, false
	t.p.ident.mu.Unlock()
}

// encodeIdentCounter encodes a counter. A negative value (only reachable
// through a writer that bypassed the index) is stored as 0; the drift check
// reports and repairs it.
func encodeIdentCounter(v int64) []byte {
	if v < 0 {
		v = 0
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

func decodeIdentCounter(b []byte) (int64, bool) {
	if len(b) != 8 {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(b)), true
}

// writeIdentForBook re-derives bookID's state from what is stored now and
// commits it on its own (sync is the commit's durability). It takes the
// book's cache stripe itself; the caller must hold none of the ident locks.
//
// A book row that will not decode is treated as no book: its ident: rows are
// removed and undecodable is true. Memdb warmup skips such a row too
// (skipped_total), so the Library never lists it, and a count that included
// it would disagree with the listing. Every write path refuses the row
// (UpdateBook's read of it fails), so nothing can move it until it is
// repaired; the next rebuild then indexes it.
func (p *PebbleStore) writeIdentForBook(bookID string, sync *pebble.WriteOptions) (changed, undecodable bool, err error) {
	mu := p.metadataCacheLock(bookID)
	mu.Lock()
	defer mu.Unlock()
	book, err := p.GetBookByID(bookID)
	if err != nil {
		if !isJSONDecodeError(err) {
			return false, false, err
		}
		book, undecodable = nil, true
	}
	var cache *MetadataCandidateCache
	if book != nil {
		cache, err = p.getMetadataCacheForIdent(bookID)
		if err != nil {
			return false, false, err
		}
	}
	batch := p.db.NewBatch()
	defer func() { _ = batch.Close() }()
	t := p.beginIdent(batch)
	defer t.end(false)
	if err := t.stage(bookID, book, cache); err != nil {
		return false, undecodable, err
	}
	if !t.changed {
		return false, undecodable, nil
	}
	if err := t.prepare(); err != nil {
		return false, undecodable, err
	}
	if err := batch.Commit(sync); err != nil {
		return false, undecodable, err
	}
	t.end(true)
	return true, undecodable, nil
}

// isJSONDecodeError reports whether err is a JSON decode failure (a row
// that will not decode), as opposed to a read error.
func isJSONDecodeError(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syn) || errors.As(err, &typ) || errors.Is(err, io.ErrUnexpectedEOF)
}

// getMetadataCacheForIdent reads bookID's cache row for derivation. A row
// that exists but will not decode is read as a row with no candidates
// (asked_empty): it exists, and nothing in it can be applied.
func (p *PebbleStore) getMetadataCacheForIdent(bookID string) (*MetadataCandidateCache, error) {
	entry, err := p.GetMetadataCache(bookID)
	if err == nil {
		return entry, nil
	}
	if isJSONDecodeError(err) {
		return &MetadataCandidateCache{BookID: bookID}, nil
	}
	return nil, err
}

// identCacheFromRaw decodes a raw metadata_cache value for derivation, with
// getMetadataCacheForIdent's rule for an undecodable value.
func identCacheFromRaw(bookID string, value []byte) *MetadataCandidateCache {
	var entry MetadataCandidateCache
	if err := json.Unmarshal(value, &entry); err != nil {
		return &MetadataCandidateCache{BookID: bookID}
	}
	return &entry
}

// lockIdentStripes takes the metadataCacheLock stripes of every id, each
// once, in ascending stripe order (so two multi-book writers cannot
// deadlock), and returns the unlock func.
func (p *PebbleStore) lockIdentStripes(ids []string) func() {
	seen := make(map[int]bool, len(ids))
	var idx []int
	for _, id := range ids {
		i := p.metadataCacheStripe(id)
		if !seen[i] {
			seen[i] = true
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)
	for _, i := range idx {
		p.metadataCacheLocks[i].Lock()
	}
	return func() {
		for k := len(idx) - 1; k >= 0; k-- {
			p.metadataCacheLocks[idx[k]].Unlock()
		}
	}
}

// stageIdentForRawCacheWrites stages the identification state of every book
// whose metadata_cache: row a raw write sets (values[i] non-nil) or deletes
// (values[i] nil). The caller holds every such book's stripe.
func (p *PebbleStore) stageIdentForRawCacheWrites(t *identTxn, ids []string, values [][]byte) error {
	for i, id := range ids {
		book, err := p.GetBookByID(id)
		if err != nil {
			return err
		}
		var cache *MetadataCandidateCache
		if values[i] != nil {
			cache = identCacheFromRaw(id, values[i])
		}
		if err := t.stage(id, book, cache); err != nil {
			return err
		}
	}
	return nil
}

// --- load, rebuild, drift ---

// loadIdentIndexState runs at open. A built index (marker at the current
// version) is loaded and served at once. An absent marker on a store with no
// book and no cache row is stamped as built (an empty index is a correct
// one). Otherwise the index stays not ready until EnsureIdentIndex rebuilds
// it.
func (p *PebbleStore) loadIdentIndexState() error {
	ver, ok, err := p.readIdentMarker()
	if err != nil {
		return err
	}
	if ok && ver == identIndexVersion {
		return p.loadIdentCounters()
	}
	if ok {
		return nil
	}
	empty, err := p.identSourcesEmpty()
	if err != nil || !empty {
		return err
	}
	if err := p.db.Set([]byte(identBuiltKey), []byte(fmt.Sprint(identIndexVersion)), pebble.Sync); err != nil {
		return fmt.Errorf("stamp empty identification index: %w", err)
	}
	p.ident.mu.Lock()
	p.ident.setCounts([2][numIdentStates]int64{})
	p.ident.ready.Store(true)
	p.ident.mu.Unlock()
	return nil
}

func (p *PebbleStore) readIdentMarker() (int, bool, error) {
	val, closer, err := p.db.Get([]byte(identBuiltKey))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read %s: %w", identBuiltKey, err)
	}
	defer closer.Close()
	var v int
	if _, err := fmt.Sscan(string(val), &v); err != nil {
		return 0, true, nil // unreadable marker: an old/unknown format
	}
	return v, true, nil
}

// identSourcesEmpty reports whether the store has no book row and no cache
// row: an index built over it is empty.
func (p *PebbleStore) identSourcesEmpty() (bool, error) {
	empty := true
	if err := forEachBookRow(p.db, func(string, []byte) error {
		empty = false
		return errStopScan
	}); err != nil {
		return false, err
	}
	if !empty {
		return false, nil
	}
	prefix := []byte(metadataCacheKeyPrefix)
	if err := forEachKeyInRange(p.db, prefix, prefixUpperBound(prefix), func(_, _ []byte) error {
		empty = false
		return errStopScan
	}); err != nil {
		return false, err
	}
	return empty, nil
}

func (p *PebbleStore) loadIdentCounters() error {
	counts, err := p.identPersistedCounters()
	if err != nil {
		return fmt.Errorf("read identification counters: %w", err)
	}
	p.ident.mu.Lock()
	p.ident.setCounts(counts)
	p.ident.ready.Store(true)
	p.ident.mu.Unlock()
	return nil
}

// invalidateIdentIndex marks the index not ready and removes its marker, so
// a crash before the next rebuild finishes leaves a store that rebuilds on
// boot. Used by a wipe that removed book or cache rows the change paths
// could not see.
func (p *PebbleStore) invalidateIdentIndex() error {
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	p.ident.ready.Store(false)
	return p.db.Delete([]byte(identBuiltKey), pebble.Sync)
}

// IdentIndexReady reports whether the counters and membership can be read.
func (p *PebbleStore) IdentIndexReady() bool { return p.ident.ready.Load() }

// EnsureIdentIndex is the boot entry point, run after memdb warmup on a
// tracked background goroutine. With no marker (or an old one) it rebuilds
// from scratch while the index reports not ready. With a current marker the
// index is already being served and this reconciles it in place: a build
// that predates the index (a rollback) wrote books and cache rows without
// maintaining it, and the reconcile finds and fixes those rows, with every
// fix moving the counters as an ordinary write does.
func (p *PebbleStore) EnsureIdentIndex(ctx context.Context) error {
	_, err := p.RebuildIdentIndex(ctx)
	return err
}

// IdentRebuildStats reports what a rebuild did.
type IdentRebuildStats struct {
	Books int
	// Undecodable counts book rows that would not decode: not indexed, and
	// any ident: rows they had were removed.
	Undecodable  int
	RowsFixed    int
	RowsRemoved  int
	KeysRemoved  int
	FromScratch  bool
	Duration     time.Duration
	CounterDrift int64
}

// RebuildIdentIndex makes the index equal to a derivation over every stored
// book and cache row. It is idempotent: a row already correct is not
// rewritten.
//
// From scratch (no current marker) the index reports not ready for the whole
// run, and Library ident: queries answer 503. Over a built index it stays
// ready, and each row it fixes moves the counters through the same identTxn
// a write uses.
//
// Concurrency (the repo's whole-library rule): the book rows are read by ONE
// sequential Pebble iterator -- the scan is IO-bound on it -- and derived on a
// bounded worker pool sized runtime.NumCPU(), which also reads each book's
// cache row and stored state. Rows that differ go to ONE writer, which takes
// the book's cache stripe, re-reads the book and its cache row and derives
// again before writing (writeIdentForBook): a worker's derivation may be
// older than a write-through that landed since, and must never overwrite it.
// At the end, under identMu, the counters are recounted from the membership
// keys, compared with the counters as they stood (the drift, logged at WARN
// when non-zero) and rewritten, the marker is stamped and the index is ready.
func (p *PebbleStore) RebuildIdentIndex(ctx context.Context) (IdentRebuildStats, error) {
	p.ident.rebuildMu.Lock()
	defer p.ident.rebuildMu.Unlock()
	began := time.Now()
	var st IdentRebuildStats

	ver, ok, err := p.readIdentMarker()
	if err != nil {
		return st, err
	}
	st.FromScratch = !ok || ver != identIndexVersion || !p.ident.ready.Load()
	if st.FromScratch {
		if err := p.invalidateIdentIndex(); err != nil {
			return st, err
		}
	}

	// Pass 1: every book row.
	books, fixed, undecodable, err := p.identReconcileBooks(ctx)
	st.Books, st.RowsFixed, st.Undecodable = books, fixed, undecodable
	if err != nil {
		return st, err
	}
	// Pass 2: state rows of books that no longer exist, and membership keys
	// that disagree with their state row.
	removed, keys, err := p.identReconcileOrphans(ctx)
	st.RowsRemoved, st.KeysRemoved = removed, keys
	if err != nil {
		return st, err
	}
	// Final: recount, rewrite counters, stamp, ready.
	drift, err := p.identFinishRebuild()
	st.CounterDrift = drift
	st.Duration = time.Since(began)
	if err != nil {
		return st, err
	}
	identLog.Info("identification index rebuilt: books=%d rows_fixed=%d rows_removed=%d keys_removed=%d from_scratch=%v duration=%s",
		st.Books, st.RowsFixed, st.RowsRemoved, st.KeysRemoved, st.FromScratch, st.Duration.Round(time.Millisecond))
	return st, nil
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// identRebuildBookView is the part of a book row the derivation reads.
type identRebuildBookView struct {
	MetadataReviewStatus *string    `json:"metadata_review_status,omitempty"`
	IsPrimaryVersion     *bool      `json:"is_primary_version,omitempty"`
	MarkedForDeletion    *bool      `json:"marked_for_deletion,omitempty"`
	QuarantinedAt        *time.Time `json:"quarantined_at,omitempty"`
}

// identRebuildCacheView is the part of a cache row the derivation reads.
type identRebuildCacheView struct {
	Candidates []json.RawMessage `json:"candidates"`
	Stale      bool              `json:"stale,omitempty"`
}

// identRebuildWorkers sizes the rebuild's derive pool: the work per book is
// JSON decoding (CPU-bound), so one worker per CPU.
func identRebuildWorkers() int { return max(1, runtime.NumCPU()) }

type identRebuildJob struct {
	id    string
	value []byte
}

// identReconcileBooks is pass 1 of RebuildIdentIndex.
//
// Failure handling: the first read or write error cancels the pass (the scan
// stops, the workers and the writer drain without working) and is returned;
// every channel send also gives up on cancellation, so neither an error
// storm nor a shutdown can leave the scan blocked on a full channel. An
// undecodable book row is not an error (writeIdentForBook): it is counted
// and logged once at the end, and the index still becomes ready.
func (p *PebbleStore) identReconcileBooks(parent context.Context) (books, fixed, undecodable int, err error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	var errMu sync.Mutex
	var firstErr error
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancel(err)
	}

	workers := identRebuildWorkers()
	jobs := make(chan identRebuildJob, workers*4)
	mismatches := make(chan string, workers*4)

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				if ctx.Err() != nil {
					continue // drain: the scan may still be sending
				}
				same, err := p.identRowMatches(j.id, j.value)
				if err != nil {
					fail(err)
					continue
				}
				if !same {
					select {
					case mismatches <- j.id:
					case <-ctx.Done():
					}
				}
			}
		})
	}
	// The one writer. fixed and undecodable are read only after writerDone.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for id := range mismatches {
			if ctx.Err() != nil {
				continue
			}
			changed, bad, err := p.writeIdentForBook(id, pebble.NoSync)
			if err != nil {
				fail(fmt.Errorf("identification rebuild of %s: %w", id, err))
				continue
			}
			if changed {
				fixed++
			}
			if bad {
				undecodable++
			}
		}
	}()

	scanErr := forEachBookRow(p.db, func(id string, value []byte) error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		books++
		select {
		case jobs <- identRebuildJob{id: id, value: append([]byte(nil), value...)}:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	})
	close(jobs)
	wg.Wait()
	close(mismatches)
	<-writerDone

	errMu.Lock()
	err = firstErr
	errMu.Unlock()
	if err == nil && scanErr != nil {
		err = scanErr
	}
	if err == nil {
		err = parent.Err()
	}
	if err != nil {
		return books, fixed, undecodable, err
	}
	if undecodable > 0 {
		identLog.Warn("identification rebuild skipped %d book rows that will not decode; they are not indexed until repaired", undecodable)
	}
	// The writer committed NoSync; one synced write makes them durable
	// before the marker can claim the index is built.
	if err := p.db.LogData(nil, pebble.Sync); err != nil {
		return books, fixed, undecodable, fmt.Errorf("sync identification rebuild: %w", err)
	}
	return books, fixed, undecodable, nil
}

// identRowMatches derives a book's state from its row value and its cache
// row, and reports whether the stored state row already says the same.
func (p *PebbleStore) identRowMatches(id string, value []byte) (bool, error) {
	var bv identRebuildBookView
	if err := json.Unmarshal(value, &bv); err != nil {
		// Undecodable book row: let the writer decide from a full read.
		return false, nil
	}
	book := &Book{
		MetadataReviewStatus: bv.MetadataReviewStatus,
		IsPrimaryVersion:     bv.IsPrimaryVersion,
		MarkedForDeletion:    bv.MarkedForDeletion,
		QuarantinedAt:        bv.QuarantinedAt,
	}
	hasRow, nCand, stale := false, 0, false
	val, closer, err := p.db.Get(metadataCacheKey(id))
	switch {
	case errors.Is(err, pebble.ErrNotFound):
	case err != nil:
		return false, fmt.Errorf("read metadata_cache:%s: %w", id, err)
	default:
		hasRow = true
		var cv identRebuildCacheView
		if json.Unmarshal(val, &cv) == nil {
			nCand, stale = len(cv.Candidates), cv.Stale
		}
		closer.Close()
	}
	s, reason := deriveIdent(book.MetadataReviewStatus, hasRow, nCand, stale)
	want := identRow{State: s, Reason: reason, Primary: identInScope(book)}
	have, ok, err := p.readIdentRow(id)
	if err != nil {
		return false, err
	}
	if !ok || have != want {
		return false, nil
	}
	// The row is right; its membership key must be too.
	v, c, err := p.db.Get(identByKey(want.State, id))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	same := bytes.Equal(v, want.scopeByte())
	c.Close()
	return same, nil
}

// identReconcileOrphans is pass 2 of RebuildIdentIndex: a state row whose
// book is gone is removed, and a membership key that disagrees with its
// state row (another state, another scope byte, or no row) is deleted.
func (p *PebbleStore) identReconcileOrphans(ctx context.Context) (rows, keys int, err error) {
	var orphans []string
	prefix := []byte(identStatePrefix)
	if err := forEachKeyInRange(p.db, prefix, prefixUpperBound(prefix), func(key, _ []byte) error {
		if err := ctxErr(ctx); err != nil {
			return err
		}
		id := string(key[len(prefix):])
		_, closer, gerr := p.db.Get([]byte(bookRowPrefix + id))
		switch {
		case errors.Is(gerr, pebble.ErrNotFound):
			orphans = append(orphans, id)
		case gerr != nil:
			return gerr
		default:
			closer.Close()
		}
		return nil
	}); err != nil {
		return 0, 0, err
	}
	for _, id := range orphans {
		changed, _, err := p.writeIdentForBook(id, pebble.NoSync)
		if err != nil {
			return rows, keys, err
		}
		if changed {
			rows++
		}
	}

	type badKey struct {
		key []byte
		id  string
	}
	var bad []badKey
	byPrefix := []byte(identByPrefix)
	if err := forEachKeyInRange(p.db, byPrefix, prefixUpperBound(byPrefix), func(key, value []byte) error {
		rest := string(key[len(byPrefix):])
		i := strings.LastIndexByte(rest, ':')
		if i < 0 {
			bad = append(bad, badKey{key: append([]byte(nil), key...)})
			return nil
		}
		state, id := IdentState(rest[:i]), rest[i+1:]
		r, ok, err := p.readIdentRow(id)
		if err != nil {
			return err
		}
		if !ok || r.State != state || !bytes.Equal(value, r.scopeByte()) {
			bad = append(bad, badKey{key: append([]byte(nil), key...), id: id})
		}
		return nil
	}); err != nil {
		return rows, 0, err
	}
	for _, b := range bad {
		if err := p.deleteBadIdentByKey(b.key, b.id); err != nil {
			return rows, keys, err
		}
		keys++
	}
	if rows > 0 || keys > 0 {
		if err := p.db.LogData(nil, pebble.Sync); err != nil {
			return rows, keys, err
		}
	}
	return rows, keys, nil
}

// deleteBadIdentByKey removes a membership key that disagrees with its state
// row, re-checking under the book's stripe (a write may have fixed it). It
// goes through identMu so a ready index's final recount stays serialized
// with it; the counters themselves are recounted at the end of the rebuild.
func (p *PebbleStore) deleteBadIdentByKey(key []byte, id string) error {
	if id != "" {
		mu := p.metadataCacheLock(id)
		mu.Lock()
		defer mu.Unlock()
		rest := string(key[len(identByPrefix):])
		state := IdentState(rest[:strings.LastIndexByte(rest, ':')])
		r, ok, err := p.readIdentRow(id)
		if err != nil {
			return err
		}
		if ok && r.State == state {
			v, c, gerr := p.db.Get(key)
			if gerr == nil {
				same := bytes.Equal(v, r.scopeByte())
				c.Close()
				if same {
					return nil // fixed since the scan
				}
				// Right state, wrong scope byte: rewrite it.
				p.ident.mu.Lock()
				defer p.ident.mu.Unlock()
				return p.db.Set(key, r.scopeByte(), pebble.NoSync)
			}
		}
	}
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	return p.db.Delete(key, pebble.NoSync)
}

// identRecount counts the membership keys per scope and state.
func (p *PebbleStore) identRecount() ([2][numIdentStates]int64, error) {
	var counts [2][numIdentStates]int64
	byPrefix := []byte(identByPrefix)
	err := forEachKeyInRange(p.db, byPrefix, prefixUpperBound(byPrefix), func(key, value []byte) error {
		rest := string(key[len(byPrefix):])
		i := strings.LastIndexByte(rest, ':')
		if i < 0 {
			return nil
		}
		si := identStateIndex(IdentState(rest[:i]))
		if si < 0 {
			return nil
		}
		counts[identScopeAll][si]++
		if bytes.Equal(value, []byte{'1'}) {
			counts[identScopePrimary][si]++
		}
		return nil
	})
	return counts, err
}

// identPersistedCounters reads the ident:n: counters as stored.
func (p *PebbleStore) identPersistedCounters() ([2][numIdentStates]int64, error) {
	var counts [2][numIdentStates]int64
	for scope := range counts {
		for i, s := range AllIdentStates {
			val, closer, err := p.db.Get(identCountKey(scope, s))
			if errors.Is(err, pebble.ErrNotFound) {
				continue
			}
			if err != nil {
				return counts, err
			}
			counts[scope][i], _ = decodeIdentCounter(val)
			closer.Close()
		}
	}
	return counts, nil
}

// identFinishRebuild recounts under identMu, logs and repairs any drift,
// stamps the marker and makes the index ready.
func (p *PebbleStore) identFinishRebuild() (int64, error) {
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	recount, err := p.identRecount()
	if err != nil {
		return 0, err
	}
	persisted, err := p.identPersistedCounters()
	if err != nil {
		return 0, err
	}
	wasReady := p.ident.ready.Load()
	var drift int64
	var details []string
	for scope := range recount {
		for i, s := range AllIdentStates {
			d := recount[scope][i] - persisted[scope][i]
			if wasReady && p.ident.counts[scope][i] != persisted[scope][i] {
				// The in-memory mirror and the store disagree too.
				d2 := recount[scope][i] - p.ident.counts[scope][i]
				if abs64(d2) > abs64(d) {
					d = d2
				}
			}
			if d != 0 {
				drift += abs64(d)
				details = append(details, fmt.Sprintf("%s/%s:%+d", identScopeNames[scope], s, d))
			}
		}
	}
	if drift != 0 && wasReady {
		identLog.Warn("identification counters drifted from the membership keys; rewriting them: total=%d %s",
			drift, strings.Join(details, " "))
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	for scope := range recount {
		for i, s := range AllIdentStates {
			if err := batch.Set(identCountKey(scope, s), encodeIdentCounter(recount[scope][i]), nil); err != nil {
				return drift, err
			}
		}
	}
	if err := batch.Set([]byte(identBuiltKey), []byte(fmt.Sprint(identIndexVersion)), nil); err != nil {
		return drift, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return drift, err
	}
	p.ident.setCounts(recount)
	p.ident.ready.Store(true)
	return drift, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// IdentDrift is IdentCountersDrift's report: per "<scope>/<state>", the
// recount from the membership keys minus the counter (persisted, or the
// in-memory mirror when that one is further off).
type IdentDrift struct {
	Deltas map[string]int64
}

// Total is the sum of the absolute deltas: 0 means every counter equals the
// recount.
func (d IdentDrift) Total() int64 {
	var t int64
	for _, v := range d.Deltas {
		t += abs64(v)
	}
	return t
}

// IdentCountersDrift recounts the membership keys and compares them with the
// counters, both the persisted ones and the in-memory mirror. It is the guard
// for a writer that was missed: the rebuild runs it at its end and repairs
// what it finds, and the tests call it after every batch of writes. It
// returns ErrIdentIndexBuilding while the index is not ready.
func (p *PebbleStore) IdentCountersDrift() (IdentDrift, error) {
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	if !p.ident.ready.Load() {
		return IdentDrift{}, ErrIdentIndexBuilding
	}
	recount, err := p.identRecount()
	if err != nil {
		return IdentDrift{}, err
	}
	persisted, err := p.identPersistedCounters()
	if err != nil {
		return IdentDrift{}, err
	}
	out := IdentDrift{Deltas: map[string]int64{}}
	for scope := range recount {
		for i, s := range AllIdentStates {
			d := recount[scope][i] - persisted[scope][i]
			if dm := recount[scope][i] - p.ident.counts[scope][i]; abs64(dm) > abs64(d) {
				d = dm
			}
			if d != 0 {
				out.Deltas[identScopeNames[scope]+"/"+string(s)] = d
			}
		}
	}
	return out, nil
}

// --- dirty set ---

// IdentDirtyEntry is one mark of the dirty set.
type IdentDirtyEntry struct {
	BookID string
	Epoch  uint64
}

// nextIdentEpoch hands out a strictly increasing epoch: the wall clock in
// nanoseconds, or one more than the last epoch when the clock has not moved
// (or went back). Across restarts it stays increasing as long as the clock
// does, which is what a compare-and-delete needs: an epoch is only compared
// within the process that read it.
func (p *PebbleStore) nextIdentEpoch() uint64 {
	for {
		last := p.ident.lastEpoch.Load()
		next := uint64(time.Now().UnixNano())
		if next <= last {
			next = last + 1
		}
		if p.ident.lastEpoch.CompareAndSwap(last, next) {
			return next
		}
	}
}

// MarkIdentDirty records that bookID's identification needs another look,
// with a new epoch, and returns that epoch. A mark already present is
// replaced by the newer epoch, so a drain that read the older one cannot
// clear it (ClearIdentDirty).
//
// It takes the book's identification lock (its metadataCacheLock stripe):
// the lock that serializes every identification write of one book. It may
// be called while holding the book's write stripe (book -> cache order), and
// must not be called while holding the cache stripe itself.
func (p *PebbleStore) MarkIdentDirty(bookID string) (uint64, error) {
	if err := ValidateBookID(bookID); err != nil || bookID == "" {
		return 0, fmt.Errorf("MarkIdentDirty: invalid book id %q", bookID)
	}
	mu := p.metadataCacheLock(bookID)
	mu.Lock()
	defer mu.Unlock()
	epoch := p.nextIdentEpoch()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], epoch)
	if err := p.db.Set(identDirtyKey(bookID), b[:], pebble.Sync); err != nil {
		return 0, fmt.Errorf("mark ident:dirty:%s: %w", bookID, err)
	}
	return epoch, nil
}

// IdentDirtyPage returns up to limit marks in book-id key order, starting
// after the book id after ("" = from the start).
func (p *PebbleStore) IdentDirtyPage(after string, limit int) ([]IdentDirtyEntry, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("IdentDirtyPage: limit must be positive, got %d", limit)
	}
	prefix := []byte(identDirtyPrefix)
	lower := prefix
	if after != "" {
		// The first key strictly after identDirtyKey(after).
		lower = append(identDirtyKey(after), 0)
	}
	out := make([]IdentDirtyEntry, 0, min(limit, 1024))
	err := forEachKeyInRange(p.db, lower, prefixUpperBound(prefix), func(key, value []byte) error {
		if len(out) == limit {
			return errStopScan
		}
		e := IdentDirtyEntry{BookID: string(key[len(prefix):])}
		if len(value) == 8 {
			e.Epoch = binary.BigEndian.Uint64(value)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClearIdentDirty deletes bookID's mark only when its stored epoch is still
// epoch (compare-and-delete under the book's identification lock), and
// reports whether it did. A mark re-set after the caller read it carries a
// newer epoch and survives.
func (p *PebbleStore) ClearIdentDirty(bookID string, epoch uint64) (bool, error) {
	mu := p.metadataCacheLock(bookID)
	mu.Lock()
	defer mu.Unlock()
	key := identDirtyKey(bookID)
	val, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read ident:dirty:%s: %w", bookID, err)
	}
	same := len(val) == 8 && binary.BigEndian.Uint64(val) == epoch
	closer.Close()
	if !same {
		return false, nil
	}
	if err := p.db.Delete(key, pebble.Sync); err != nil {
		return false, fmt.Errorf("clear ident:dirty:%s: %w", bookID, err)
	}
	return true, nil
}
