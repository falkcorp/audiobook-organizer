// file: internal/database/pebble_activity_filter_index.go
// version: 1.1.0
// guid: 419286ad-e5d1-42f3-8085-b930b8834c0b
// last-edited: 2026-09-19

// Pebble activity store — secondary indexes for the source, type and level
// filters, the query planner that uses them, and the backfill that builds them
// over rows written before they existed.
//
// WHY: before these indexes the only indexed filters were operation_id and
// book_id. Every other filter walked the newest activityQueryScanBudget (20,000)
// rows of the log and stopped, so a match older than that window was silently
// never examined — `/activity?source=X` answered "nothing" for a source that
// last wrote 21,000 rows ago. SQLite answered those from an index, so switching
// the primary backend back to Pebble (plan 2026-09-19) would have reintroduced
// that miss for every rare filter over old history.
//
// Key layout (one family per filter, added to the families in
// pebble_activity_store.go):
//
//	act:src:<esc(source)>:<tier>:<20d-unix-nano>:<ulid> = "<tier>:<20d-unix-nano>:<ulid>"
//	act:typ:<esc(type)>:<tier>:<20d-unix-nano>:<ulid>   = "<tier>:<20d-unix-nano>:<ulid>"
//	act:lvl:<esc(level)>:<tier>:<20d-unix-nano>:<ulid>  = "<tier>:<20d-unix-nano>:<ulid>"
//
// The value is the same ref the op/book families store, so RepairActivityIndexes
// treats these families exactly like the old ones (a ref whose primary row is
// gone is an orphan and is deleted).
//
// WHY THE TIER IS IN THE KEY. Query's result order is TWO-GROUP: every
// non-digest row before every digest row, newest-first within each group
// (pactSelectTiers). A single newest-first range per value would interleave the
// groups. With the tier as a key component, the rows for (value, tier) are one
// contiguous time-ordered range, and the planner runs the same k-way merge over
// per-tier ranges that scanNewestFirst runs over the primary tier ranges — so
// the order, the Since/Until bounds and the tier/exclude-tier selection are all
// decided on key ranges, exactly as the unindexed path decides them. The tier
// is the one baked into the PRIMARY KEY, never the decoded body's Tier (see
// pactIndexPushdownEligible for why those can disagree); every candidate row is
// still decoded and run through matchesFilter, so a body/key disagreement can
// cost a candidate but never leak a wrong row.
//
// WHY NO TIER INDEX. The tier already IS the primary key's leading component:
// a tier filter is a range scan over act:<tier>: today. A tier family would
// duplicate the primary keyspace.
//
// WHY THE VALUE IS ESCAPED. A source is free text. "a:change" as a raw
// component would make act:src:a:change: a prefix of BOTH source "a" in tier
// "change" and source "a:change" in any tier. Escaping ':' (and '%', the escape
// character itself) makes every component colon-free, so a component boundary
// is always a ':' and every (value, tier) range is exact.
//
// FAMILY TOKENS MUST NOT BE TIER NAMES: "act:info:" is the info TIER's primary
// range, so a family called "info" would be returned by every info-tier scan.
// TestPactFilterFamiliesAreNotTiers pins that.
package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var filterIndexLog = logger.New("activity.filter-index")

// ActivityFilterIndexBackfillKey is the persistent sentinel the backfill op
// writes once every row that existed before the filter indexes did has been
// indexed. The planner uses the indexes ONLY when it is set: before that, an
// index range is missing every older row, and answering from it would turn
// "older matches were not examined" (a lower bound, logged) into "older matches
// do not exist" (a wrong answer, silent).
//
// It deliberately lives OUTSIDE "act:". WipeAllActivity range-deletes the whole
// act: prefix; an empty store is trivially fully indexed, so the sentinel must
// survive the wipe rather than send every later query back to the budget scan.
const ActivityFilterIndexBackfillKey = "system:backfill:activity_filter_index_v1_done"

// pactFilterFamily is one indexed filter predicate.
type pactFilterFamily struct {
	name   string // for logs and plans
	prefix string // "act:<token>:"
	value  func(ActivityEntry) string
	filter func(ActivityFilter) string
}

// pactFilterFamilies is every indexed ActivityFilter predicate, in the planner's
// static tie-break order (used when the size estimates cannot tell the families
// apart, e.g. everything is still in the memtable). Source first because it is
// the filter the UI's picker sends and its values are the most numerous; type
// next; level last because it has five values and is the least selective.
var pactFilterFamilies = []pactFilterFamily{
	{
		name: "source", prefix: "act:src:",
		value:  func(e ActivityEntry) string { return e.Source },
		filter: func(f ActivityFilter) string { return f.Source },
	},
	{
		name: "type", prefix: "act:typ:",
		value:  func(e ActivityEntry) string { return e.Type },
		filter: func(f ActivityFilter) string { return f.Type },
	},
	{
		name: "level", prefix: "act:lvl:",
		value:  func(e ActivityEntry) string { return e.Level },
		filter: func(f ActivityFilter) string { return f.Level },
	},
}

// pactEscapeIndexComponent makes a free-text value safe as one ':'-delimited
// key component. '%' is escaped first so the encoding stays reversible and two
// different values can never produce the same component.
func pactEscapeIndexComponent(v string) string {
	if !strings.ContainsAny(v, "%:") {
		return v
	}
	v = strings.ReplaceAll(v, "%", "%25")
	return strings.ReplaceAll(v, ":", "%3A")
}

// pactFilterRangePrefix is the prefix of every key for (family, value, tier).
func pactFilterRangePrefix(fam pactFilterFamily, value, tier string) string {
	return fam.prefix + pactEscapeIndexComponent(value) + ":" + tier + ":"
}

// pactPrimaryKeyTier returns the tier component of a primary key.
func pactPrimaryKeyTier(key []byte) (string, bool) {
	s := string(key)
	if !strings.HasPrefix(s, "act:") {
		return "", false
	}
	rest := s[len("act:"):]
	i := strings.IndexByte(rest, ':')
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// pactFilterIndexKeysFor returns the source/type/level index keys for the row
// stored at primaryKey. It is called by pactIndexKeysFor, so Record,
// RecordBatch and every pactDeleteEntry caller write and delete these keys in
// the same batch as the row — there is no second derivation to drift.
func pactFilterIndexKeysFor(primaryKey []byte, e ActivityEntry) ([][]byte, bool) {
	tier, ok := pactPrimaryKeyTier(primaryKey)
	if !ok {
		return nil, false
	}
	suffix, ok := pactPrimaryKeySuffix(primaryKey)
	if !ok {
		return nil, false
	}
	var keys [][]byte
	for _, fam := range pactFilterFamilies {
		v := fam.value(e)
		if v == "" {
			// An empty value is never a filter predicate (ActivityFilter treats
			// "" as "no filter"), so it needs no index entry.
			continue
		}
		keys = append(keys, []byte(pactFilterRangePrefix(fam, v, tier)+suffix))
	}
	return keys, true
}

// pactStageFilterIndexes writes the filter-index keys for a row that is staged
// WITHOUT going through prepareEntry — Summarize's summary rows and
// CompactByDay's digests. Those writers deliberately do not add op/book index
// keys (that is their pre-existing contract and out of this file's scope), but
// they must add these, or a source=summarize / type=daily_digest query would
// miss every such row once the planner trusts the index.
func pactStageFilterIndexes(batch *pebble.Batch, primaryKey []byte, e ActivityEntry) error {
	keys, ok := pactFilterIndexKeysFor(primaryKey, e)
	if !ok {
		return fmt.Errorf("pebble_activity_store: derive filter index keys for %q", primaryKey)
	}
	ref := primaryKey[len("act:"):]
	for _, k := range keys {
		if err := batch.Set(k, ref, nil); err != nil {
			return fmt.Errorf("pebble_activity_store: set filter index %q: %w", k, err)
		}
	}
	return nil
}

// stageDeleteFilterIndexesForKey removes the filter-index keys of the row at
// primaryKey, reading the row to learn its source/type/level. Used where a row
// is deleted by key without a decoded entry in hand (the digest a new digest
// replaces). A row that is already gone has nothing to remove. A row that will
// not decode cannot have its keys derived; that is logged, and any key left
// behind is an orphan RepairActivityIndexes removes and the planner skips (it
// verifies every candidate's primary row exists).
func (s *PebbleActivityStore) stageDeleteFilterIndexesForKey(batch *pebble.Batch, primaryKey []byte) error {
	val, closer, err := s.db.Get(primaryKey)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pebble_activity_store: read row for filter index delete: %w", err)
	}
	var p pactEntryNoDetails
	jsonErr := json.Unmarshal(val, &p)
	closer.Close()
	if jsonErr != nil {
		filterIndexLog.Warn("undecodable row %s: its filter index keys (if any) are left for index repair: %v",
			string(primaryKey), jsonErr)
		return nil
	}
	keys, ok := pactFilterIndexKeysFor(primaryKey, p.entry())
	if !ok {
		return nil
	}
	for _, k := range keys {
		if err := batch.Delete(k, nil); err != nil {
			return err
		}
	}
	return nil
}

// ── Sentinel and seen-through mark ──────────────────────────────────────────

// ActivityFilterIndexSeenThroughKey persists a unix-nano high-water mark: every
// primary row with a key timestamp at or below it was written by an indexing
// build (or indexed by a backfill). Record and RecordBatch advance it in the
// same batch as the rows; ReconcileFilterIndexCoverage reads it at boot to find
// rows a ROLLED-BACK build wrote without filter keys.
//
// It may lag (two concurrent commits can land in either order, so the stored
// value can go backwards by one batch); a lagging mark only widens the boot
// check, which confirms a gap by looking for missing keys before acting on it.
const ActivityFilterIndexSeenThroughKey = "system:activity_filter_index_seen_through"

// FilterIndexBackfillDone reports whether the source/type/level indexes cover
// every stored row, i.e. whether the planner may use them. A positive answer is
// cached; a negative one is re-read (one point Get) so a backfill finishing in
// another goroutine takes effect on the next query without a restart.
//
// The read-then-cache runs under filterIndexMu, the same lock
// ClearFilterIndexBackfillDone takes: otherwise a reader that fetched the
// sentinel just before a clear could cache "done" just after it, and the
// planner would trust an index that is being rebuilt.
func (s *PebbleActivityStore) FilterIndexBackfillDone() bool {
	if s.filterIndexReady.Load() {
		return true
	}
	s.filterIndexMu.Lock()
	defer s.filterIndexMu.Unlock()
	val, closer, err := s.db.Get([]byte(ActivityFilterIndexBackfillKey))
	if err != nil {
		return false
	}
	done := len(val) > 0
	closer.Close()
	if done {
		s.filterIndexReady.Store(true)
	}
	return done
}

// MarkFilterIndexBackfillDone persists the sentinel and advances the
// seen-through mark to the newest stored row. Called only once every row
// present has been indexed (the backfill op's open-ended last window, or the
// boot reconcile), so every row at or below that mark is covered.
func (s *PebbleActivityStore) MarkFilterIndexBackfillDone() error {
	s.filterIndexMu.Lock()
	defer s.filterIndexMu.Unlock()
	newest, ok, err := s.newestPrimaryNanos()
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if ok {
		if err := s.stageSeenThrough(batch, newest); err != nil {
			return err
		}
	}
	if err := batch.Set([]byte(ActivityFilterIndexBackfillKey), []byte("1"), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("pebble_activity_store: set filter index sentinel: %w", err)
	}
	s.filterIndexReady.Store(true)
	return nil
}

// ClearFilterIndexBackfillDone withdraws the sentinel, persistently and from
// the cache, so the planner falls back to the budgeted scan (which reports
// partial) until the indexes are complete again. A forced rebuild and the boot
// reconcile call it BEFORE writing anything.
func (s *PebbleActivityStore) ClearFilterIndexBackfillDone() error {
	s.filterIndexMu.Lock()
	defer s.filterIndexMu.Unlock()
	s.filterIndexReady.Store(false)
	if err := s.db.Delete([]byte(ActivityFilterIndexBackfillKey), pebble.Sync); err != nil {
		return fmt.Errorf("pebble_activity_store: clear filter index sentinel: %w", err)
	}
	return nil
}

// loadSeenThrough reads the persisted mark into memory (0 when absent).
func (s *PebbleActivityStore) loadSeenThrough() {
	val, closer, err := s.db.Get([]byte(ActivityFilterIndexSeenThroughKey))
	if err != nil {
		return
	}
	n, perr := strconv.ParseInt(string(val), 10, 64)
	closer.Close()
	if perr == nil {
		s.seenThrough.Store(n)
	}
}

// stageSeenThrough stages the mark at nanos into batch when it advances the
// in-memory mark. Monotonic in memory; see ActivityFilterIndexSeenThroughKey
// for why the stored value may lag.
func (s *PebbleActivityStore) stageSeenThrough(batch *pebble.Batch, nanos int64) error {
	for {
		cur := s.seenThrough.Load()
		if nanos <= cur {
			return nil
		}
		if s.seenThrough.CompareAndSwap(cur, nanos) {
			break
		}
	}
	return batch.Set([]byte(ActivityFilterIndexSeenThroughKey), []byte(strconv.FormatInt(nanos, 10)), nil)
}

// stageSeenThroughForKeys advances the mark to the newest of primary keys.
func (s *PebbleActivityStore) stageSeenThroughForKeys(batch *pebble.Batch, primaries ...[]byte) error {
	var newest int64
	for _, k := range primaries {
		if ns, ok := pactPrimaryKeyNanos(k); ok && ns > newest {
			newest = ns
		}
	}
	if newest == 0 {
		return nil
	}
	return s.stageSeenThrough(batch, newest)
}

// newestPrimaryNanos returns the largest primary-key timestamp in any tier.
func (s *PebbleActivityStore) newestPrimaryNanos() (int64, bool, error) {
	var best int64
	found := false
	for _, tier := range actTiers {
		it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: pactPrimaryPrefix(tier), UpperBound: pactPrimaryUpperBound(tier)})
		if err != nil {
			return 0, false, err
		}
		for ok := it.Last(); ok; ok = it.Prev() {
			if ns, parsed := pactPrimaryKeyNanos(it.Key()); parsed {
				if !found || ns > best {
					best, found = ns, true
				}
				break
			}
		}
		if err := it.Close(); err != nil {
			return 0, false, err
		}
	}
	return best, found, nil
}

// FilterIndexReconcileResult reports what ReconcileFilterIndexCoverage did.
type FilterIndexReconcileResult struct {
	SeenThrough int64 // the mark the check started from
	GapFound    bool  // some row newer than the mark had no filter keys
	Reindexed   int   // rows indexed to close the gap
}

// ReconcileFilterIndexCoverage closes the rollback hole: an indexing build
// completes the backfill, the service is rolled back to an older build that
// writes rows WITHOUT filter keys, then rolled forward. The sentinel still
// says "complete", so a filter on those rows would come back short with
// partial=false.
//
// At boot it looks only at rows newer than the seen-through mark (normally
// none: the mark advances with every write). If any of them lacks its filter
// keys it withdraws the sentinel FIRST — so queries fall back to the budgeted
// scan and say partial — then indexes every row above the mark, then re-sets
// the sentinel (which advances the mark). Nothing happens when the sentinel is
// not set: the backfill op has not finished and will cover those rows.
//
// If it fails or is cancelled after withdrawing the sentinel, the sentinel
// stays withdrawn (safe: queries are partial, never silently short) and
// maintenance.activity-filter-index-backfill must be run to restore it.
func (s *PebbleActivityStore) ReconcileFilterIndexCoverage(ctx context.Context) (res FilterIndexReconcileResult, err error) {
	defer recoverPebbleClosed("pebble_activity_store.ReconcileFilterIndexCoverage", &err)
	if !s.FilterIndexBackfillDone() {
		return res, nil
	}
	mark := s.seenThrough.Load()
	res.SeenThrough = mark
	gap, err := s.filterIndexGapAbove(ctx, mark)
	if err != nil || !gap {
		return res, err
	}
	res.GapFound = true
	filterIndexLog.Warn("activity rows newer than the filter-index mark (%d) have no filter keys — a rollback to an older build wrote them; filtering falls back to the budgeted scan until they are indexed", mark)
	if err := s.ClearFilterIndexBackfillDone(); err != nil {
		return res, err
	}
	n, err := s.BackfillFilterIndexWindow(ctx, mark+1, 0, true)
	res.Reindexed = n
	if err != nil {
		return res, fmt.Errorf("pebble_activity_store: reindex rollback gap: %w", err)
	}
	if err := s.MarkFilterIndexBackfillDone(); err != nil {
		return res, err
	}
	filterIndexLog.Info("indexed %d activity row(s) written without filter keys; indexed filtering re-enabled", n)
	return res, nil
}

// filterIndexGapAbove reports whether any decodable row with a key timestamp
// above mark lacks one of its filter-index keys.
func (s *PebbleActivityStore) filterIndexGapAbove(ctx context.Context, mark int64) (bool, error) {
	for _, tier := range actTiers {
		it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: pactNanosKey(tier, mark+1), UpperBound: pactPrimaryUpperBound(tier)})
		if err != nil {
			return false, err
		}
		gap, err := func() (bool, error) {
			defer it.Close()
			seen := 0
			for it.First(); it.Valid(); it.Next() {
				if seen%activityCtxCheckInterval == 0 {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return false, ctxErr
					}
				}
				seen++
				var p pactEntryNoDetails
				if json.Unmarshal(it.Value(), &p) != nil {
					continue // never indexed by any path; not a gap
				}
				keys, ok := pactFilterIndexKeysFor(it.Key(), p.entry())
				if !ok {
					continue
				}
				for _, k := range keys {
					_, closer, gerr := s.db.Get(k)
					if errors.Is(gerr, pebble.ErrNotFound) {
						return true, nil
					}
					if gerr != nil {
						return false, gerr
					}
					closer.Close()
				}
			}
			return false, it.Error()
		}()
		if err != nil || gap {
			return gap, err
		}
	}
	return false, nil
}

// ── Backfill ─────────────────────────────────────────────────────────────────

// ActivityFilterIndexBackfiller is what the maintenance.activity-filter-index-
// backfill op needs from the store. Narrow on purpose: the op never sees the
// store's read or delete surface.
type ActivityFilterIndexBackfiller interface {
	FilterIndexBackfillDone() bool
	MarkFilterIndexBackfillDone() error
	// ClearFilterIndexBackfillDone withdraws the sentinel so the planner stops
	// trusting the indexes (a forced rebuild calls it before its first window).
	ClearFilterIndexBackfillDone() error
	// FilterIndexEarliestNanos returns the oldest primary-key timestamp across
	// every tier, or ok=false for an empty store.
	FilterIndexEarliestNanos(ctx context.Context) (nanos int64, ok bool, err error)
	// BackfillFilterIndexWindow indexes every row in every tier whose key
	// timestamp is in [fromNanos, toNanos) — or [fromNanos, ∞) when openEnded —
	// and returns how many rows it indexed. Idempotent: index keys are Sets of
	// the exact bytes Record writes.
	BackfillFilterIndexWindow(ctx context.Context, fromNanos, toNanos int64, openEnded bool) (int, error)
}

var _ ActivityFilterIndexBackfiller = (*PebbleActivityStore)(nil)

// activityFilterBackfillCommitEvery bounds one backfill batch. A var so tests
// can exercise the multi-commit path on a small fixture.
var activityFilterBackfillCommitEvery = 2000

// FilterIndexEarliestNanos returns the smallest key timestamp across all tiers
// with one Seek per tier.
func (s *PebbleActivityStore) FilterIndexEarliestNanos(ctx context.Context) (int64, bool, error) {
	var (
		best  int64
		found bool
	)
	for _, tier := range actTiers {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		it, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: pactPrimaryPrefix(tier),
			UpperBound: pactPrimaryUpperBound(tier),
		})
		if err != nil {
			return 0, false, fmt.Errorf("pebble_activity_store: earliest new iter (tier=%s): %w", tier, err)
		}
		for ok := it.First(); ok; ok = it.Next() {
			if ns, parsed := pactPrimaryKeyNanos(it.Key()); parsed {
				if !found || ns < best {
					best, found = ns, true
				}
				break
			}
		}
		if err := it.Close(); err != nil {
			return 0, false, fmt.Errorf("pebble_activity_store: earliest iter close (tier=%s): %w", tier, err)
		}
	}
	return best, found, nil
}

// pactNanosKey renders a tier's primary key bound at nanos.
func pactNanosKey(tier string, nanos int64) []byte {
	return []byte(fmt.Sprintf("act:%s:%020d:", tier, nanos))
}

// BackfillFilterIndexWindow — see ActivityFilterIndexBackfiller.
//
// Rows are decoded without Details (pactDecodeNoDetails): the families read
// only scalar fields, and a Details blob can be a ~9.6 MB iTunes dump. A row
// that will not decode is counted and logged and left unindexed — the same
// choice every other activity scan makes for such a row, and one no filter
// query can observe, since the unindexed scan drops that row too.
func (s *PebbleActivityStore) BackfillFilterIndexWindow(ctx context.Context, fromNanos, toNanos int64, openEnded bool) (indexed int, err error) {
	defer recoverPebbleClosed("pebble_activity_store.BackfillFilterIndexWindow", &err)
	tally := &pactDecodeTally{}
	defer tally.log("filter index backfill")

	for _, tier := range actTiers {
		upper := pactPrimaryUpperBound(tier)
		if !openEnded {
			upper = pactNanosKey(tier, toNanos)
		}
		n, err := s.backfillFilterIndexRange(ctx, pactNanosKey(tier, fromNanos), upper, tally)
		indexed += n
		if err != nil {
			return indexed, fmt.Errorf("pebble_activity_store: filter index backfill (tier=%s): %w", tier, err)
		}
	}
	return indexed, nil
}

// pactFilterBackfillBeforeCommit is a test hook run after a chunk's iterator
// is closed and before its batch commits.
var pactFilterBackfillBeforeCommit func()

// backfillFilterIndexRange indexes [lower, upper) in chunks of
// activityFilterBackfillCommitEvery rows. Each chunk opens its OWN iterator,
// seeks past the previous chunk's last key, and closes it before committing —
// the same shape as claimDayChunk/streamTierEntries. One iterator held across
// every commit of a window would pin the memtables and sstables it opened on
// for as long as the window runs, times NumCPU concurrent windows.
func (s *PebbleActivityStore) backfillFilterIndexRange(ctx context.Context, lower, upper []byte, tally *pactDecodeTally) (indexed int, err error) {
	next := lower
	for {
		batch := s.db.NewBatch()
		staged, last, done, err := s.stageFilterBackfillChunk(ctx, batch, next, upper, tally)
		if err == nil && staged > 0 {
			if pactFilterBackfillBeforeCommit != nil {
				pactFilterBackfillBeforeCommit()
			}
			// The window's final commit is synced: the op checkpoints after the
			// item returns, and a checkpoint must never name a window whose
			// index writes could still be lost to a crash.
			opts := pebble.NoSync
			if done {
				opts = pebble.Sync
			}
			err = batch.Commit(opts)
		}
		batch.Close()
		if err != nil {
			return indexed, err
		}
		indexed += staged
		if done {
			return indexed, nil
		}
		// Resume strictly after the last key read: append 0x00, the smallest
		// byte, to form the immediate successor.
		next = append(last, 0)
	}
}

// stageFilterBackfillChunk stages up to activityFilterBackfillCommitEvery rows
// from [lower, upper) into batch and closes its iterator before returning.
// done reports that the range is exhausted.
func (s *PebbleActivityStore) stageFilterBackfillChunk(ctx context.Context, batch *pebble.Batch, lower, upper []byte, tally *pactDecodeTally) (staged int, last []byte, done bool, err error) {
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return 0, nil, false, err
	}
	defer func() {
		if cerr := it.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	read := 0
	for it.First(); it.Valid(); it.Next() {
		if read%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, nil, false, ctxErr
			}
		}
		read++
		last = append(last[:0], it.Key()...)
		var p pactEntryNoDetails
		s.entriesDecoded.Add(1)
		if jsonErr := json.Unmarshal(it.Value(), &p); jsonErr != nil {
			s.decodeFailures.Add(1)
			tally.record(it.Key(), jsonErr)
		} else {
			if err := pactStageFilterIndexes(batch, slices.Clone(it.Key()), p.entry()); err != nil {
				return 0, nil, false, err
			}
			staged++
		}
		if read >= activityFilterBackfillCommitEvery {
			return staged, last, false, it.Error()
		}
	}
	return staged, last, true, it.Error()
}

// ── Planner ──────────────────────────────────────────────────────────────────

// ActivityQueryResult is a Query answer plus whether it is PARTIAL: the walk
// stopped at its budget before it had either filled the page or exhausted the
// matching rows, so older matches were not examined and Total is a lower bound
// that does not even guarantee another page exists. The UI shows that instead
// of letting "no more results" stand as a fact.
type ActivityQueryResult struct {
	Entries []ActivityEntry
	Total   int
	Partial bool
}

// ActivityPartialQuerier is implemented by stores that can report a partial
// answer. Optional, like ActivityCounter, so a backend whose Query is always
// complete is not forced to implement it; activity.Service falls back to Query
// with Partial=false. Every activity-store WRAPPER forwards it (see
// MigratingActivityStore and InstrumentedActivityStorer) — a wrapper that did
// not would hide the flag with no error.
type ActivityPartialQuerier interface {
	QueryWithPartial(ctx context.Context, f ActivityFilter) (ActivityQueryResult, error)
}

var _ ActivityPartialQuerier = (*PebbleActivityStore)(nil)

// QueryWithPartial is Query plus the partial flag.
func (s *PebbleActivityStore) QueryWithPartial(ctx context.Context, f ActivityFilter) (res ActivityQueryResult, err error) {
	defer recoverPebbleClosed("pebble_activity_store.QueryWithPartial", &err)
	entries, total, partial, err := s.query(ctx, f)
	if err != nil {
		return ActivityQueryResult{}, err
	}
	return ActivityQueryResult{Entries: entries, Total: total, Partial: partial}, nil
}

// pactFilterIndexFamilies returns the indexed families f constrains, in the
// static order of pactFilterFamilies.
func pactFilterIndexFamilies(f ActivityFilter) []pactFilterFamily {
	var fams []pactFilterFamily
	for _, fam := range pactFilterFamilies {
		if fam.filter(f) != "" {
			fams = append(fams, fam)
		}
	}
	return fams
}

// pactHasResidualPredicates reports whether f carries a predicate no index
// decides — so candidates can fail matchesFilter and the walk needs its decode
// budget. Search is a substring match and cannot be indexed by a key range.
func pactHasResidualPredicates(f ActivityFilter) bool {
	return f.Search != "" || len(f.Tags) > 0 || len(f.ExcludeSources) > 0 || len(f.ExcludeTags) > 0
}

// pickFilterFamily chooses the family to range-scan: the one whose ranges over
// the selected tiers are smallest on disk, by Pebble's metadata-only size
// estimate. When the estimates cannot separate the candidates (all zero, e.g.
// data still in the memtable) the static order of pactFilterFamilies decides.
// A poor pick costs only speed: every other indexed predicate is still checked
// by key probe, and every candidate by matchesFilter.
func (s *PebbleActivityStore) pickFilterFamily(fams []pactFilterFamily, f ActivityFilter, tiers []string) (chosen pactFilterFamily, others []pactFilterFamily) {
	best := -1
	var bestSize uint64
	for i, fam := range fams {
		var size uint64
		for _, tier := range tiers {
			p := pactFilterRangePrefix(fam, fam.filter(f), tier)
			n, err := s.db.EstimateDiskUsage([]byte(p), []byte(p[:len(p)-1]+";"))
			if err == nil {
				size += n
			}
		}
		if best < 0 || size < bestSize {
			best, bestSize = i, size
		}
	}
	chosen = fams[best]
	for i, fam := range fams {
		if i != best {
			others = append(others, fam)
		}
	}
	return chosen, others
}

// queryByFilterIndex answers f from the source/type/level indexes. Same
// contract as the unindexed path in Query — same two-group order, same
// Since/Until key bounds, same "total is exact when exhausted, else a lower
// bound", same matchesFilter on every row — but the walk visits only rows the
// chosen index names, so a match older than the newest 20,000 rows is found.
//
// Budgets: every candidate that passes the key probes is read and decoded, and
// those decodes are capped at activityQueryScanBudget PLUS the page probe, so a
// filter with no residual predicate (every candidate matches) always fills its
// page, while a residual predicate that rejects most candidates (Search) stops
// at the budget and reports partial. Key-only steps (probes that fail, orphaned
// refs) are capped separately at activityFilterIndexKeyBudget.
func (s *PebbleActivityStore) queryByFilterIndex(ctx context.Context, f ActivityFilter, fams []pactFilterFamily) ([]ActivityEntry, int, bool, error) {
	nonDigest, digestTiers := pactSelectTiers(f)
	all := append(append([]string(nil), nonDigest...), digestTiers...)
	if len(all) == 0 {
		return []ActivityEntry{}, 0, false, nil
	}
	chosen, others := s.pickFilterFamily(fams, f, all)

	probe := f.Offset + f.Limit + 1
	w := &pactFilterWalk{
		s: s, f: f, chosen: chosen, others: others,
		probeAt:      probe,
		page:         make([]ActivityEntry, 0, max(f.Limit, 0)),
		decodeBudget: activityQueryScanBudget + probe,
		keyBudget:    activityFilterIndexKeyBudget,
	}
	defer w.close()

	exhausted := true
	for _, phase := range [][]string{nonDigest, digestTiers} {
		if len(phase) == 0 {
			continue
		}
		if w.matched >= probe {
			exhausted = false
			break
		}
		phaseExhausted, err := w.walk(ctx, phase)
		if err != nil {
			return nil, 0, false, err
		}
		if !phaseExhausted {
			exhausted = false
			break
		}
	}

	partial := !exhausted && w.matched < probe
	if partial {
		filterIndexLog.Warn("indexed query hit its budget; total is a lower bound and older matches were not examined "+
			"(index=%s decoded=%d keys=%d matched=%d limit=%d offset=%d search=%q)",
			chosen.name, w.decoded, w.keys, w.matched, f.Limit, f.Offset, f.Search)
	}
	return w.page, w.matched, partial, nil
}

// activityQueryMaxOffset caps how deep a filtered query may page. Reaching
// offset N means reading and checking N matching rows, so without a cap a
// request with offset=2,000,000 was ~2M Gets and decodes. 100,000 is 4,000
// pages of the UI's 25 rows; anything deeper should narrow the filter or the
// time range instead.
const activityQueryMaxOffset = 100_000

// ErrActivityOffsetTooDeep is returned for an offset above
// activityQueryMaxOffset. GET /activity maps it to 400.
var ErrActivityOffsetTooDeep = fmt.Errorf("activity query offset exceeds %d; narrow the filter or the time range", activityQueryMaxOffset)

// pactEntryCheck decodes everything matchesFilter reads plus Details as RAW
// bytes, so the walk can tell — without materializing a Details map that may
// be a multi-megabyte iTunes dump — whether the FULL decode would succeed.
// encoding/json accepts an object or null for a map[string]any and rejects
// every other JSON kind, and every other field has the same type here as in
// ActivityEntry, so "this decodes and Details is absent, null or an object"
// is exactly "json.Unmarshal into ActivityEntry succeeds". That is what lets
// a row be COUNTED toward the offset without a full decode and still be
// counted exactly as the unindexed path counts it (a wrong-typed Details row
// is dropped there, so it must not consume an offset slot here).
type pactEntryCheck struct {
	pactEntryNoDetails
	Details json.RawMessage `json:"details,omitempty"`
}

func (c *pactEntryCheck) fullDecodeOK() bool {
	d := bytes.TrimSpace(c.Details)
	return len(d) == 0 || d[0] == '{' || bytes.Equal(d, []byte("null"))
}

// activityFilterIndexKeyBudget caps the key-only steps one indexed query may
// take (index keys read plus probes of the other families). Key steps cost
// ~1µs, not a decode, so this is far larger than the decode budget: 2M steps is
// a few seconds in the worst case of two large, nearly disjoint families.
var activityFilterIndexKeyBudget = 2_000_000

// pactFilterWalk is one indexed query's walk state across both tier phases.
type pactFilterWalk struct {
	s      *PebbleActivityStore
	f      ActivityFilter
	chosen pactFilterFamily
	others []pactFilterFamily

	probeAt int             // offset+limit+1: stop once this many rows matched
	matched int             // rows matched so far (the returned total)
	page    []ActivityEntry // the requested window

	decodeBudget, keyBudget int
	decoded, keys           int

	probeIters map[string]*pebble.Iterator // "<family prefix>|<tier>" → iterator
	tally      pactDecodeTally
}

func (w *pactFilterWalk) close() {
	for _, it := range w.probeIters {
		_ = it.Close()
	}
	w.tally.log("query filter index")
}

// probe reports whether the index key for (fam, f's value, tier, suffix)
// exists — i.e. whether the row also satisfies that family's predicate —
// without reading the row.
func (w *pactFilterWalk) probe(fam pactFilterFamily, tier string, suffix []byte) (bool, error) {
	prefix := pactFilterRangePrefix(fam, fam.filter(w.f), tier)
	id := fam.prefix + "|" + tier
	it := w.probeIters[id]
	if it == nil {
		var err error
		it, err = w.s.db.NewIter(&pebble.IterOptions{
			LowerBound: []byte(prefix),
			UpperBound: []byte(prefix[:len(prefix)-1] + ";"),
		})
		if err != nil {
			return false, err
		}
		if w.probeIters == nil {
			w.probeIters = make(map[string]*pebble.Iterator)
		}
		w.probeIters[id] = it
	}
	target := make([]byte, 0, len(prefix)+len(suffix))
	target = append(append(target, prefix...), suffix...)
	w.keys++
	return it.SeekGE(target) && bytes.Equal(it.Key(), target), it.Error()
}

type pactFilterCursor struct {
	tier   string
	prefix string
	iter   *pebble.Iterator
}

// sortKey is what the unindexed merge orders by: the key's timestamp, then the
// primary key bytes ("<tier>:<nanos>:<ulid>" after the shared "act:").
func (c *pactFilterCursor) suffix() []byte { return c.iter.Key()[len(c.prefix):] }

func pactFilterCursorNewer(a, b *pactFilterCursor) bool {
	as, bs := a.suffix(), b.suffix()
	if len(as) >= 20 && len(bs) >= 20 {
		if c := bytes.Compare(as[:20], bs[:20]); c != 0 {
			return c > 0
		}
	}
	if a.tier != b.tier {
		return a.tier > b.tier
	}
	return bytes.Compare(as, bs) > 0
}

// walk runs the newest-first merge over the chosen family's per-tier ranges for
// one tier phase. Returns whether the phase was exhausted (false: the visitor
// stopped it or a budget ran out).
func (w *pactFilterWalk) walk(ctx context.Context, tiers []string) (exhausted bool, err error) {
	cursors := make([]*pactFilterCursor, 0, len(tiers))
	defer func() {
		for _, c := range cursors {
			if cerr := c.iter.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	}()
	value := w.chosen.filter(w.f)
	for _, tier := range tiers {
		prefix := pactFilterRangePrefix(w.chosen, value, tier)
		lower, upper := []byte(prefix), []byte(prefix[:len(prefix)-1]+";")
		// The same Since/Until semantics as pactTierBounds, applied to the
		// "<20d-nanos>:<ulid>" suffix this range shares with the primary key.
		if w.f.Since != nil {
			lower = []byte(fmt.Sprintf("%s%020d:", prefix, w.f.Since.UnixNano()))
		}
		if w.f.Until != nil {
			upper = []byte(fmt.Sprintf("%s%020d:%s", prefix, w.f.Until.UnixNano(), "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"))
		}
		it, err := w.s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
		if err != nil {
			return false, fmt.Errorf("pebble_activity_store: filter index iter (tier=%s): %w", tier, err)
		}
		c := &pactFilterCursor{tier: tier, prefix: prefix, iter: it}
		cursors = append(cursors, c)
		it.Last()
	}

	steps := 0
	for {
		if steps%activityCtxCheckInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return false, ctxErr
			}
		}
		steps++

		var best *pactFilterCursor
		for _, c := range cursors {
			if !c.iter.Valid() {
				continue
			}
			if best == nil || pactFilterCursorNewer(c, best) {
				best = c
			}
		}
		if best == nil {
			for _, c := range cursors {
				if ierr := c.iter.Error(); ierr != nil {
					return false, fmt.Errorf("pebble_activity_store: filter index scan (tier=%s): %w", c.tier, ierr)
				}
			}
			return true, nil
		}
		if w.keys >= w.keyBudget || w.decoded >= w.decodeBudget {
			return false, nil
		}
		w.keys++

		key := best.iter.Key()
		cont, err := w.candidate(best, key)
		if err != nil {
			return false, err
		}
		if !cont {
			return false, nil
		}
		best.iter.Prev()
	}
}

// candidate evaluates one index key. Returns false to stop the walk.
func (w *pactFilterWalk) candidate(c *pactFilterCursor, key []byte) (bool, error) {
	if !pactIndexKeyNamesExactly(key, c.prefix) {
		return true, nil
	}
	suffix := key[len(c.prefix):]
	for _, fam := range w.others {
		ok, err := w.probe(fam, c.tier, suffix)
		if err != nil {
			return false, fmt.Errorf("pebble_activity_store: filter index probe (%s): %w", fam.name, err)
		}
		if !ok {
			return true, nil
		}
	}

	primary := make([]byte, 0, len("act:")+len(c.tier)+1+len(suffix))
	primary = append(append(append(append(primary, "act:"...), c.tier...), ':'), suffix...)
	val, closer, err := w.s.db.Get(primary)
	if errors.Is(err, pebble.ErrNotFound) {
		return true, nil // orphaned ref: not a row, not counted
	}
	if err != nil {
		return false, fmt.Errorf("pebble_activity_store: filter index get: %w", err)
	}
	defer closer.Close()

	w.decoded++
	w.s.entriesDecoded.Add(1)
	var p pactEntryCheck
	if jsonErr := json.Unmarshal(val, &p); jsonErr != nil {
		w.s.decodeFailures.Add(1)
		w.tally.record(primary, jsonErr)
		return true, nil
	}
	if !p.fullDecodeOK() {
		// Exactly the rows a full decode rejects: the unindexed path drops
		// them from page AND total, so they are neither counted nor paged.
		w.s.decodeFailures.Add(1)
		w.tally.record(primary, fmt.Errorf("details is not an object"))
		return true, nil
	}
	if !matchesFilter(p.entry(), w.f) {
		return true, nil
	}
	// Counted only now, after the row is known to decode in full: nothing is
	// ever counted and then un-counted, so an offset cannot drift.
	w.matched++
	if w.matched > w.f.Offset && len(w.page) < w.f.Limit {
		var fe ActivityEntry
		w.s.entriesDecoded.Add(1)
		if jsonErr := json.Unmarshal(val, &fe); jsonErr != nil {
			// Unreachable while fullDecodeOK is exact; fail loudly rather
			// than return a page that silently disagrees with total.
			return false, fmt.Errorf("pebble_activity_store: page row %q passed the decode check but failed: %w", primary, jsonErr)
		}
		w.page = append(w.page, fe)
	}
	return w.matched < w.probeAt, nil
}
