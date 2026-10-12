// file: internal/database/ident_state.go
// version: 1.1.1
// guid: c4038689-1fb5-485d-86f8-f91b20eeac6a
// last-edited: 2026-10-12

package database

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
//	                                 is the key; the byte lets the counters be
//	                                 recounted from the keys alone, without
//	                                 reading state rows.
//	ident:dirty:<bookID>          -> 8-byte big-endian epoch (the dirty set)
//	ident:meta:built              -> the index format version, as decimal text
//
// The counters are NOT stored. They live in memory only (identIndex.published)
// and are recounted from the ident:by: keys at open (a keys-only scan: about
// 4 ms at 40,000 books, 9 ms at 120,000) and at the end of every rebuild.
// The membership keys commit in the same batch as the change that moves them,
// so they are crash-atomic with it, and a counter derived from them can never
// disagree with them after a restart. A persisted counter could (the first
// version of this index stored them, and serializing their read-modify-write
// put every identification write behind one fsync).
//
// Who keeps it current, and under which lock: see identTxn and the writer
// list on MetadataCacheGeneration. Every writer of an input -- a cache row, a
// book's review status, its primary flag, its trash or quarantine bit --
// holds the book's metadataCacheLock stripe from its read of the old state row
// through its commit AND the in-memory counter move that follows it, so one
// book's transitions are applied one at a time and in commit order. It also
// holds identMu (identIndex.mu) for READ over the same span: writers of
// different books run, and fsync, concurrently. Only the whole-index
// operations -- a rebuild's final recount, Reset, an invalidation, the drift
// check -- take identMu for WRITE, which waits for every in-flight writer to
// finish its commit and counter move. Lock order: book-family stripes, then
// the cache stripe, then identMu. Nothing holding identMu takes another lock,
// and nothing holding it for read takes it again (an RWMutex read lock is not
// reentrant once a writer waits).

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
	// deriveIdent for why owner-rejected candidates do not put a book
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
	// per-candidate metadata_rejection rows. See deriveIdent.
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
	identDirtyPrefix = "ident:dirty:"
	identMetaPrefix  = "ident:meta:"
	identBuiltKey    = identMetaPrefix + "built"
)

// identScopeAll and identScopePrimary index identIndex.published.
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

// deriveIdent is the one rule that maps a book and its metadata_cache row to
// an identification state and reason. It takes the few inputs it reads (the
// book's MetadataReviewStatus; whether a cache row exists, its candidate
// count and Stale flag) so the rebuild can derive from a partial decode of
// the rows. The rules, first match wins:
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
// "missing" (IdentMissing) is unasked + asked_empty over the primary scope:
// fetched-but-unapproved books are not missing, and an ASIN alone does not
// make a book identified. It is appendix C's count BEFORE that appendix's
// exclusion of the excluded franchise (about 281 books), which nothing here
// can tell apart yet: it waits on a later manual_only reason. Until then
// missing exceeds the goal number by the franchise books in those two
// states.
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
// deriveIdent or the scope reads from the book row.
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
	// mu is identMu. Writers (identTxn) hold it for READ from their read of
	// the old state row through their commit and counter move; the
	// whole-index operations (identFinishRebuild, Reset,
	// invalidateIdentIndex, IdentCountersDrift) hold it for WRITE, so they
	// see no commit whose counter move is still pending.
	mu sync.RWMutex
	// published is the per-scope, per-state counter: the number of
	// ident:by: keys. A writer adds its batch's delta after a successful
	// commit (atomic, under mu for read); a recount stores over it under mu
	// for write. Read without any lock by IdentCounts.
	published [2][numIdentStates]atomic.Int64
	// ready is true when the counters and membership keys can be trusted.
	// Written under mu for write; read without it by the query paths.
	ready atomic.Bool
	// gen counts invalidations (bumped under mu for write). A rebuild that
	// sees it move between its start and its final recount was overtaken by
	// a wipe, and leaves the index not ready for the rebuild that follows the
	// wipe, rather than declaring ready an index built over rows the wipe
	// removed.
	gen atomic.Uint64
	// rebuildMu serializes RebuildIdentIndex runs.
	rebuildMu sync.Mutex
	// lastEpoch is the last dirty-set epoch handed out.
	lastEpoch atomic.Uint64
	// wake nudges SuperviseIdentIndex: sent (without blocking) when the
	// index is invalidated or a rebuild fails.
	wake chan struct{}
}

// setCounts stores c as the counters. Called with mu held for write.
func (x *identIndex) setCounts(c [2][numIdentStates]int64) {
	for scope := range c {
		for i := range c[scope] {
			x.published[scope][i].Store(c[scope][i])
		}
	}
}

// loadCounts reads the counters, unclamped.
func (x *identIndex) loadCounts() [2][numIdentStates]int64 {
	var c [2][numIdentStates]int64
	for scope := range c {
		for i := range c[scope] {
			c[scope][i] = x.published[scope][i].Load()
		}
	}
	return c
}

// nudge wakes SuperviseIdentIndex, if one runs, without blocking.
func (x *identIndex) nudge() {
	if x.wake == nil {
		return
	}
	select {
	case x.wake <- struct{}{}:
	default:
	}
}

// IdentCounts is a snapshot of the per-state counters.
type IdentCounts struct {
	// Ready is false while the index is being built; the counts are then
	// zero and must not be shown.
	Ready   bool
	All     map[IdentState]int64
	Primary map[IdentState]int64
}

// IdentMissing is a scope's "missing" count: unasked + asked_empty. It does
// not exclude the franchise appendix C excludes (see deriveIdent).
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
// in-memory counters, never waiting on identMu. A read that races a write can
// see that one write half applied (a book leaving one state before it arrives
// in the other); the next read sees it whole.
//
// A counter cannot go negative while every writer follows identTxn's
// protocol: a book's decrement out of a state is applied only after the
// increment that put it there (the book's stripe orders them). A negative
// value would mean a writer bypassed the index; it reads as 0 here, the one
// place a count leaves the store, and IdentCountersDrift reports the raw
// value.
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
		out.All[s] = max(0, p.ident.published[identScopeAll][i].Load())
		out.Primary[s] = max(0, p.ident.published[identScopePrimary][i].Load())
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
//	// the caller holds every staged book's cache stripe
//	t := p.beginIdent(batch)          // takes identMu for read
//	defer t.end(false)                // releases it on every early return
//	t.stage(id, book, cache)          // per book: old row -> new row, delta
//	p.commitIdentBatch(batch, t, ...) // commit, then t.end(err == nil)
//	// release the stripes
//
// stage reads each book's old row and stages the new row and membership key
// in the batch, and adds the move to the transaction's delta. end(true) adds
// the delta to the in-memory counters; end(false) -- a failed or abandoned
// commit -- drops it, so a batch that did not commit moves no counter. The
// read lock spans the old-row read, the commit and the counter move, so a
// recount (which takes the lock for write) never sees a commit without its
// move, or a move without its commit. The cache stripes, held by the caller
// over the same span, make one book's transactions apply in commit order.
type identTxn struct {
	p       *PebbleStore
	batch   *GatedBatch
	changed bool
	locked  bool
	delta   [2][numIdentStates]int64
	// staged maps book id -> the row this batch leaves (present=false for a
	// removal), so a second stage of one book in the same batch reads it.
	staged map[string]identStaged
}

type identStaged struct {
	row     identRow
	present bool
}

// beginIdent opens an identification transaction over batch and takes
// identMu for read. The caller must already hold the cache stripe of every
// book it will stage, and must call end.
func (p *PebbleStore) beginIdent(batch *GatedBatch) *identTxn {
	p.ident.mu.RLock()
	return &identTxn{p: p, batch: batch, locked: true}
}

// stage stages bookID's identification state as derived from book (nil: the
// book does not exist or will not decode, so its rows are removed) and cache
// (the book's metadata_cache row as this batch leaves it).
func (t *identTxn) stage(bookID string, book *Book, cache identCacheView) error {
	if book == nil {
		return t.stageRow(bookID, identRow{}, false)
	}
	s, reason := deriveIdent(book.MetadataReviewStatus, cache.present, cache.candidates, cache.stale)
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

// end settles the transaction and releases identMu: with committed true it
// adds the staged delta to the in-memory counters first. It applies the
// delta whether or not the index is ready: while it is not, the rebuild's
// final recount (under identMu for write, so after this) stores over it.
// end is idempotent, so a deferred end(false) is safe after an explicit
// end(true).
func (t *identTxn) end(committed bool) {
	if t == nil || !t.locked {
		return
	}
	if committed && t.changed {
		if h := identBeforeCountMove.Load(); h != nil {
			(*h)(t.p)
		}
		for scope := range t.delta {
			for i, d := range t.delta[scope] {
				if d != 0 {
					t.p.ident.published[scope][i].Add(d)
				}
			}
		}
	}
	t.locked = false
	t.p.ident.mu.RUnlock()
}

// identCommitFault, when set, is consulted by commitIdentBatch before the
// commit with the committing store; a non-nil error is returned as the
// commit's. Tests only: it is how a failed commit is produced to prove it
// moves no counter (the store argument keeps a fault aimed at one test's
// store from failing another test's commits).
var identCommitFault atomic.Pointer[func(*PebbleStore) error]

// identBeforeCountMove, when set, is called by end after a successful
// commit and before its counter move, with identMu still held for read.
// Tests only: it holds a committed write at exactly the point a recount must
// not run, to prove the recount waits for it.
var identBeforeCountMove atomic.Pointer[func(*PebbleStore)]

// commitIdentBatch commits batch and settles t (nil: no identification
// change in the batch) with the commit's outcome: the counters move only
// for a batch that committed.
func (p *PebbleStore) commitIdentBatch(batch *GatedBatch, t *identTxn, o *pebble.WriteOptions) error {
	var err error
	if f := identCommitFault.Load(); f != nil {
		err = (*f)(p)
	}
	if err == nil {
		err = batch.Commit(o)
	}
	t.end(err == nil)
	return err
}

// writeIdentForBook re-derives bookID's state from what is stored now and
// commits it on its own (sync is the commit's durability). It takes the
// book's cache stripe itself; the caller must hold none of the ident locks.
// undecodable reports a book row that will not decode (readIdentBook): its
// ident: rows are removed.
func (p *PebbleStore) writeIdentForBook(bookID string, sync *pebble.WriteOptions) (changed, undecodable bool, err error) {
	mu := p.metadataCacheLock(bookID)
	mu.Lock()
	defer mu.Unlock()
	batch := p.db.NewBatch()
	defer func() { _ = batch.Close() }()
	t := p.beginIdent(batch)
	defer t.end(false)
	book, undecodable, err := p.readIdentBook(bookID)
	if err != nil {
		return false, false, err
	}
	var cache identCacheView
	if book != nil {
		cache, err = p.readIdentCacheView(bookID)
		if err != nil {
			return false, undecodable, err
		}
	}
	if err := t.stage(bookID, book, cache); err != nil {
		return false, undecodable, err
	}
	if !t.changed {
		return false, undecodable, nil
	}
	if err := p.commitIdentBatch(batch, t, sync); err != nil {
		return false, undecodable, err
	}
	return true, undecodable, nil
}

// identBookView is the part of a book row the derivation reads. Every
// identification read of a book row decodes this view and nothing more:
// the write-through, the rebuild's comparison and its repair, so they can
// never disagree about a row (a full decode that failed where this one
// succeeded used to make the rebuild "repair" the same row on every boot).
type identBookView struct {
	MetadataReviewStatus *string    `json:"metadata_review_status,omitempty"`
	IsPrimaryVersion     *bool      `json:"is_primary_version,omitempty"`
	MarkedForDeletion    *bool      `json:"marked_for_deletion,omitempty"`
	QuarantinedAt        *time.Time `json:"quarantined_at,omitempty"`
}

// identBookFromRaw decodes a book row value for derivation. ok is false for
// a value that will not decode (as JSON, or in one of the four fields): such
// a book is treated as no book -- not indexed, its ident: rows removed. Memdb
// warmup skips a row that will not decode too (skipped_total), so the Library
// never lists it, and a count that included it would disagree with the
// listing. A row that decodes here but fails Book's full decode elsewhere is
// indexed by these four fields; warmup skips it, so it is the one case where
// the count can exceed the listing, by the number of such rows (memdb
// warmup's skipped_total names them).
func identBookFromRaw(value []byte) (*Book, bool) {
	var v identBookView
	if err := json.Unmarshal(value, &v); err != nil {
		return nil, false
	}
	return &Book{
		MetadataReviewStatus: v.MetadataReviewStatus,
		IsPrimaryVersion:     v.IsPrimaryVersion,
		MarkedForDeletion:    v.MarkedForDeletion,
		QuarantinedAt:        v.QuarantinedAt,
	}, true
}

// readIdentBook reads bookID's book row for derivation: (nil, false, nil)
// when there is none, (nil, true, nil) when it will not decode. It reads the
// raw row, never GetBookByID: the derivation needs four fields, not the
// signature sidecar, and a sidecar or full-decode failure must not fail a
// metadata_cache write (the cache row is written either way).
func (p *PebbleStore) readIdentBook(bookID string) (book *Book, undecodable bool, err error) {
	val, closer, err := p.db.Get([]byte(bookRowPrefix + bookID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read book:%s for identification state: %w", bookID, err)
	}
	defer closer.Close()
	b, ok := identBookFromRaw(val)
	if !ok {
		return nil, true, nil
	}
	return b, false, nil
}

// identCacheView is what the derivation reads of a book's metadata_cache row.
type identCacheView struct {
	present    bool
	candidates int
	stale      bool
}

// identCacheViewOf is the view of a decoded row (nil: no row).
func identCacheViewOf(c *MetadataCandidateCache) identCacheView {
	if c == nil {
		return identCacheView{}
	}
	return identCacheView{present: true, candidates: len(c.Candidates), stale: c.Stale}
}

// identCacheJSON is the part of a cache row identCacheViewFromRaw decodes.
type identCacheJSON struct {
	Candidates []json.RawMessage `json:"candidates"`
	Stale      bool              `json:"stale,omitempty"`
}

// identCacheViewFromRaw is the view of a stored row value. A row that exists
// but will not decode is a row with no candidate (asked_empty): it exists,
// and nothing in it can be applied.
func identCacheViewFromRaw(value []byte) identCacheView {
	var v identCacheJSON
	if json.Unmarshal(value, &v) != nil {
		return identCacheView{present: true}
	}
	return identCacheView{present: true, candidates: len(v.Candidates), stale: v.Stale}
}

// readIdentCacheView reads bookID's cache row for derivation.
func (p *PebbleStore) readIdentCacheView(bookID string) (identCacheView, error) {
	val, closer, err := p.db.Get(metadataCacheKey(bookID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return identCacheView{}, nil
		}
		return identCacheView{}, fmt.Errorf("read metadata_cache:%s for identification state: %w", bookID, err)
	}
	defer closer.Close()
	return identCacheViewFromRaw(val), nil
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
// (values[i] nil). The caller holds every such book's stripe. A book row that
// will not decode is staged as no book, and the raw write goes ahead.
func (p *PebbleStore) stageIdentForRawCacheWrites(t *identTxn, ids []string, values [][]byte) error {
	for i, id := range ids {
		book, _, err := p.readIdentBook(id)
		if err != nil {
			return err
		}
		var cache identCacheView
		if values[i] != nil {
			cache = identCacheViewFromRaw(values[i])
		}
		if err := t.stage(id, book, cache); err != nil {
			return err
		}
	}
	return nil
}

// --- load, rebuild, drift ---

// loadIdentIndexState runs at open. A built index (marker at the current
// version) has its counters recounted from the membership keys and is served
// at once. An absent marker on a store with no book and no cache row is
// stamped as built (an empty index is a correct one). Otherwise the index
// stays not ready until EnsureIdentIndex rebuilds it.
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

// loadIdentCounters recounts the counters from the membership keys and makes
// the index ready: the every-boot path of a built index.
func (p *PebbleStore) loadIdentCounters() error {
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	counts, err := p.identRecount()
	if err != nil {
		return fmt.Errorf("count identification membership keys: %w", err)
	}
	p.ident.setCounts(counts)
	p.ident.ready.Store(true)
	return nil
}

// invalidateIdentIndex marks the index not ready and removes its marker, so
// a crash before the next rebuild finishes leaves a store that rebuilds on
// boot. Used by a rebuild from scratch and by a wipe that removed book or
// cache rows the change paths could not see. It bumps the invalidation
// generation, which makes a rebuild already running leave the index not
// ready at its end.
func (p *PebbleStore) invalidateIdentIndex() error {
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	p.ident.ready.Store(false)
	p.ident.gen.Add(1)
	return p.db.Delete([]byte(identBuiltKey), pebble.Sync)
}

// IdentIndexReady reports whether the counters and membership can be read.
func (p *PebbleStore) IdentIndexReady() bool { return p.ident.ready.Load() }

// EnsureIdentIndex is one boot build: with no marker (or an old one) it
// rebuilds from scratch while the index reports not ready. With a current
// marker the index is already being served and this reconciles it in place:
// a build that predates the index (a rollback) wrote books and cache rows
// without maintaining it, and the reconcile finds and fixes those rows, with
// every fix moving the counters as an ordinary write does.
// SuperviseIdentIndex runs it, and retries it, on the server's background
// goroutine.
func (p *PebbleStore) EnsureIdentIndex(ctx context.Context) error {
	_, err := p.RebuildIdentIndex(ctx)
	return err
}

// identRetryDelays are SuperviseIdentIndex's waits between failed builds:
// six retries over about two minutes, then it stops and logs an ERROR naming
// the manual op. A variable so tests can shorten it.
var identRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second}

// IdentIndexRebuildOpID is the maintenance op that rebuilds the index by
// hand (internal/plugins/maintenance/ident_index.go).
const IdentIndexRebuildOpID = "maintenance.ident-index-rebuild"

// SuperviseIdentIndex is the identification index's background keeper, run
// for the life of the server on a tracked goroutine after memdb warmup. It
// runs the boot build (EnsureIdentIndex) at once, retrying a failure with
// backoff (identRetryDelays), and afterwards sleeps until a wipe's own
// rebuild fails (WipeByPrefixes nudges it) and then builds again, with the
// same retries, if the index is still not ready. After the last retry of an
// episode it logs an ERROR naming the manual op and waits for the next
// nudge. It returns when ctx ends.
func (p *PebbleStore) SuperviseIdentIndex(ctx context.Context) {
	boot := true
	for {
		if boot || !p.ident.ready.Load() {
			p.ensureIdentIndexRetrying(ctx, boot)
		}
		boot = false
		select {
		case <-ctx.Done():
			return
		case <-p.ident.wake: // nil (no supervisor channel): waits for ctx
		}
	}
}

// ensureIdentIndexRetrying runs EnsureIdentIndex until it succeeds, ctx
// ends, or identRetryDelays is spent. Outside the boot build it stops as soon
// as the index is ready (another rebuild -- a wipe's -- finished it).
func (p *PebbleStore) ensureIdentIndexRetrying(ctx context.Context, boot bool) {
	for attempt := 0; ; attempt++ {
		if !boot && p.ident.ready.Load() {
			return
		}
		err := p.EnsureIdentIndex(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		if attempt >= len(identRetryDelays) {
			identLog.Error("identification index build failed %d times; ident: queries answer 503 until it is rebuilt (run %s, or restart): %v",
				attempt+1, IdentIndexRebuildOpID, err)
			return
		}
		wait := identRetryDelays[attempt]
		identLog.Warn("identification index build failed (attempt %d of %d); retrying in %s: %v",
			attempt+1, len(identRetryDelays)+1, wait, err)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// errIdentRebuildSuperseded is a rebuild that a wipe invalidated while it
// ran: it leaves the index not ready, for the rebuild that follows the wipe.
var errIdentRebuildSuperseded = errors.New("identification index was invalidated while it was being rebuilt")

// IdentRebuildStats reports what a rebuild did.
type IdentRebuildStats struct {
	Books int
	// Undecodable counts book rows that would not decode: not indexed, and
	// any ident: rows they had were removed.
	Undecodable int
	RowsFixed   int
	RowsRemoved int
	KeysRemoved int
	FromScratch bool
	Duration    time.Duration
	// CounterDrift is the sum over every counter of |recount - counter| at
	// the final recount of a rebuild over a ready index: 0 unless a writer
	// bypassed the write-through.
	CounterDrift int64
}

// RebuildIdentIndex makes the index equal to a derivation over every stored
// book and cache row. It is idempotent: a row already correct is not
// rewritten.
//
// From scratch (no current marker, or the index not ready) the index reports
// not ready for the whole run, and Library ident: queries answer 503. Over a
// built index it stays ready, and each row it fixes moves the counters
// through the same identTxn a write uses.
//
// Concurrency (the repo's whole-library rule): the book rows are read by ONE
// sequential Pebble iterator -- the scan is IO-bound on it -- and derived on a
// bounded worker pool sized runtime.NumCPU(), which also reads each book's
// cache row and stored state. Rows that differ go to ONE writer, which takes
// the book's cache stripe, re-reads the book and its cache row and derives
// again before writing (writeIdentForBook): a worker's derivation may be
// older than a write-through that landed since, and must never overwrite it.
// At the end, under identMu for write, the counters are recounted from the
// membership keys, compared with the counters as they stood (the drift,
// logged at WARN when non-zero) and replaced, the marker is stamped and the
// index is ready -- unless a wipe invalidated the index meanwhile
// (errIdentRebuildSuperseded).
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
	gen := p.ident.gen.Load()

	// Pass 1: every book row.
	books, fixed, undecodable, err := p.identReconcileBooks(ctx, false)
	st.Books, st.RowsFixed, st.Undecodable = books, fixed, undecodable
	if err != nil {
		return st, err
	}
	// Pass 2: state rows of books that no longer exist, and membership keys
	// that disagree with their state row.
	removed, keys, err := p.identReconcileOrphans(ctx, false)
	st.RowsRemoved, st.KeysRemoved = removed, keys
	if err != nil {
		return st, err
	}
	// Final: recount, replace the counters, stamp, ready.
	drift, err := p.identFinishRebuild(gen)
	st.CounterDrift = drift
	st.Duration = time.Since(began)
	if err != nil {
		return st, err
	}
	identLog.Info("identification index rebuilt: books=%d rows_fixed=%d rows_removed=%d keys_removed=%d undecodable=%d from_scratch=%v duration=%s",
		st.Books, st.RowsFixed, st.RowsRemoved, st.KeysRemoved, st.Undecodable, st.FromScratch, st.Duration.Round(time.Millisecond))
	return st, nil
}

// IdentIndexReport is VerifyIdentIndex's read-only finding: what a rebuild
// would change.
type IdentIndexReport struct {
	Ready bool
	Books int
	// Undecodable book rows are not indexed (identBookFromRaw).
	Undecodable int
	// RowsWrong: books whose state row or membership key is missing or
	// differs from a derivation over their stored rows.
	RowsWrong int
	// OrphanRows: state rows of books that no longer exist.
	OrphanRows int
	// BadKeys: membership keys that disagree with their state row.
	BadKeys int
	// CounterDrift: sum of |recount - counter| (0 while not ready).
	CounterDrift int64
}

// Clean reports whether a rebuild would change nothing.
func (r IdentIndexReport) Clean() bool {
	return r.Ready && r.RowsWrong == 0 && r.OrphanRows == 0 && r.BadKeys == 0 && r.CounterDrift == 0
}

// VerifyIdentIndex compares the index with a derivation over every stored
// book and cache row, writing nothing: the preview of RebuildIdentIndex (the
// maintenance.ident-index-rebuild op). It runs the rebuild's two passes in
// their read-only form, with the same worker pool. It does not stop writers:
// a book written while it runs can show as a difference that is already gone
// (the rebuild re-checks each one under the book's stripe before changing
// anything), so a non-zero preview on a busy store is an upper bound.
func (p *PebbleStore) VerifyIdentIndex(ctx context.Context) (IdentIndexReport, error) {
	p.ident.rebuildMu.Lock()
	defer p.ident.rebuildMu.Unlock()
	var r IdentIndexReport
	r.Ready = p.ident.ready.Load()
	var err error
	r.Books, r.RowsWrong, r.Undecodable, err = p.identReconcileBooks(ctx, true)
	if err != nil {
		return r, err
	}
	r.OrphanRows, r.BadKeys, err = p.identReconcileOrphans(ctx, true)
	if err != nil {
		return r, err
	}
	if r.Ready {
		d, err := p.IdentCountersDrift()
		if err != nil {
			return r, err
		}
		r.CounterDrift = d.Total()
	}
	return r, nil
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

// identRebuildWorkers sizes the rebuild's derive pool: the work per book is
// JSON decoding (CPU-bound), so one worker per CPU.
func identRebuildWorkers() int { return max(1, runtime.NumCPU()) }

type identRebuildJob struct {
	id    string
	value []byte
}

// identReconcileBooks is pass 1 of RebuildIdentIndex (and, with dryRun, of
// VerifyIdentIndex, where fixed counts the books a rebuild would rewrite).
//
// Failure handling: the first read or write error cancels the pass (the scan
// stops, the workers and the writer drain without working) and is returned;
// every channel send also gives up on cancellation, so neither an error
// storm nor a shutdown can leave the scan blocked on a full channel. An
// undecodable book row is not an error (identBookFromRaw): it is counted
// and logged once at the end, and the index still becomes ready.
func (p *PebbleStore) identReconcileBooks(parent context.Context, dryRun bool) (books, fixed, undecodable int, err error) {
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
	var undecodableN atomic.Int64

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				if ctx.Err() != nil {
					continue // drain: the scan may still be sending
				}
				same, bad, err := p.identRowMatches(j.id, j.value)
				if err != nil {
					fail(err)
					continue
				}
				if bad {
					undecodableN.Add(1)
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
	// The one writer. fixed is read only after writerDone.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for id := range mismatches {
			if ctx.Err() != nil {
				continue
			}
			if dryRun {
				fixed++
				continue
			}
			changed, _, err := p.writeIdentForBook(id, pebble.NoSync)
			if err != nil {
				fail(fmt.Errorf("identification rebuild of %s: %w", id, err))
				continue
			}
			if changed {
				fixed++
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
	undecodable = int(undecodableN.Load())

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
	if undecodable > 0 && !dryRun {
		identLog.Warn("identification rebuild skipped %d book rows that will not decode; they are not indexed until repaired", undecodable)
	}
	if dryRun || fixed == 0 {
		return books, fixed, undecodable, nil
	}
	// The writer committed NoSync; one synced write makes them durable
	// before the marker can claim the index is built.
	if err := p.db.LogData(nil, pebble.Sync); err != nil {
		return books, fixed, undecodable, fmt.Errorf("sync identification rebuild: %w", err)
	}
	return books, fixed, undecodable, nil
}

// identRowMatches derives a book's state from its row value and its cache
// row, and reports whether the stored state row and membership key already
// say the same. undecodable reports a book row identBookFromRaw refuses: it
// must have no ident: rows.
func (p *PebbleStore) identRowMatches(id string, value []byte) (same, undecodable bool, err error) {
	book, ok := identBookFromRaw(value)
	have, haveOK, err := p.readIdentRow(id)
	if err != nil {
		return false, !ok, err
	}
	if !ok {
		return !haveOK, true, nil
	}
	cache, err := p.readIdentCacheView(id)
	if err != nil {
		return false, false, err
	}
	s, reason := deriveIdent(book.MetadataReviewStatus, cache.present, cache.candidates, cache.stale)
	want := identRow{State: s, Reason: reason, Primary: identInScope(book)}
	if !haveOK || have != want {
		return false, false, nil
	}
	// The row is right; its membership key must be too.
	v, c, err := p.db.Get(identByKey(want.State, id))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	same = bytes.Equal(v, want.scopeByte())
	c.Close()
	return same, false, nil
}

// identReconcileOrphans is pass 2 of RebuildIdentIndex: a state row whose
// book is gone is removed, and a membership key that disagrees with its
// state row (another state, another scope byte, or no row) is deleted. With
// dryRun it only counts them.
func (p *PebbleStore) identReconcileOrphans(ctx context.Context, dryRun bool) (rows, keys int, err error) {
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
	if dryRun {
		rows = len(orphans)
	} else {
		for _, id := range orphans {
			changed, _, err := p.writeIdentForBook(id, pebble.NoSync)
			if err != nil {
				return rows, keys, err
			}
			if changed {
				rows++
			}
		}
	}

	type badKey struct {
		key []byte
		id  string
	}
	var bad []badKey
	byPrefix := []byte(identByPrefix)
	if err := forEachKeyInRange(p.db, byPrefix, prefixUpperBound(byPrefix), func(key, value []byte) error {
		if err := ctxErr(ctx); err != nil {
			return err
		}
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
	if dryRun {
		return rows, len(bad), nil
	}
	for _, b := range bad {
		changed, err := p.deleteBadIdentByKey(b.key, b.id)
		if err != nil {
			return rows, keys, err
		}
		if changed {
			keys++
		}
	}
	if rows > 0 || keys > 0 {
		if err := p.db.LogData(nil, pebble.Sync); err != nil {
			return rows, keys, err
		}
	}
	return rows, keys, nil
}

// deleteBadIdentByKey removes a membership key that disagrees with its state
// row, re-checking under the book's stripe (a write may have fixed it, or
// moved the book so that the write itself deleted the key), and reports
// whether it changed anything. It moves no counter: the counters are
// recounted from the keys at the end of the rebuild that calls it
// (identFinishRebuild), and its NoSync write is made durable by that
// rebuild's synced marker write. It holds identMu for read like any writer,
// so that recount cannot run mid-write.
func (p *PebbleStore) deleteBadIdentByKey(key []byte, id string) (bool, error) {
	if id != "" {
		mu := p.metadataCacheLock(id)
		mu.Lock()
		defer mu.Unlock()
	}
	p.ident.mu.RLock()
	defer p.ident.mu.RUnlock()
	v, c, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil // gone since the scan
	}
	if err != nil {
		return false, err
	}
	have := append([]byte(nil), v...)
	c.Close()
	if id != "" {
		rest := string(key[len(identByPrefix):])
		state := IdentState(rest[:strings.LastIndexByte(rest, ':')])
		r, ok, err := p.readIdentRow(id)
		if err != nil {
			return false, err
		}
		if ok && r.State == state {
			if bytes.Equal(have, r.scopeByte()) {
				return false, nil // fixed since the scan
			}
			// Right state, wrong scope byte: rewrite it.
			return true, p.db.Set(key, r.scopeByte(), pebble.NoSync)
		}
	}
	return true, p.db.Delete(key, pebble.NoSync)
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

// identFinishRebuild, under identMu for write: unless the invalidation
// generation moved since gen (a wipe overtook the rebuild), it recounts the
// membership keys, logs any drift from the counters (when the index was
// ready), replaces the counters, stamps the marker and makes the index
// ready.
func (p *PebbleStore) identFinishRebuild(gen uint64) (int64, error) {
	p.ident.mu.Lock()
	defer p.ident.mu.Unlock()
	if p.ident.gen.Load() != gen {
		return 0, errIdentRebuildSuperseded
	}
	recount, err := p.identRecount()
	if err != nil {
		return 0, err
	}
	var drift int64
	if p.ident.ready.Load() {
		var details []string
		drift, details = identDriftOf(recount, p.ident.loadCounts())
		if drift != 0 {
			identLog.Warn("identification counters drifted from the membership keys; replacing them: total=%d %s",
				drift, strings.Join(details, " "))
		}
	}
	if err := p.db.Set([]byte(identBuiltKey), []byte(fmt.Sprint(identIndexVersion)), pebble.Sync); err != nil {
		return drift, err
	}
	p.ident.setCounts(recount)
	p.ident.ready.Store(true)
	return drift, nil
}

// identDriftOf compares a recount with the counters: the total absolute
// difference and one "<scope>/<state>:<+d>" entry per counter that differs.
func identDriftOf(recount, counts [2][numIdentStates]int64) (int64, []string) {
	var total int64
	var details []string
	for scope := range recount {
		for i, s := range AllIdentStates {
			if d := recount[scope][i] - counts[scope][i]; d != 0 {
				total += abs64(d)
				details = append(details, fmt.Sprintf("%s/%s:%+d", identScopeNames[scope], s, d))
			}
		}
	}
	return total, details
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// IdentDrift is IdentCountersDrift's report: per "<scope>/<state>", the
// recount from the membership keys minus the counter.
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
// in-memory counters, under identMu for write (so no commit is between its
// write and its counter move). It is the guard for a writer that was missed:
// the tests call it after every batch of writes, and the rebuild performs
// the same comparison at its end and repairs what it finds. It returns
// ErrIdentIndexBuilding while the index is not ready.
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
	counts := p.ident.loadCounts()
	out := IdentDrift{Deltas: map[string]int64{}}
	for scope := range recount {
		for i, s := range AllIdentStates {
			if d := recount[scope][i] - counts[scope][i]; d != 0 {
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
