// file: internal/database/pebble_store_ops_v2_timeline.go
// version: 1.3.0
// guid: f45601a9-ba63-4626-ad91-75720525f1be
// last-edited: 2026-10-04

// Timeline indexes for opv2 operation rows.
//
// ListOperationsV2Since used to decode every opv2:op: row on every call
// (5.46-5.95 s on prod with ~1,780 new operations a day). Two empty-valued
// index families let it read only the rows its predicate can admit:
//
//	opv2:open:{op_id}                          → ""  (row has CompletedAt == nil)
//	opv2:done:{completed_nanos:020d}:{op_id}   → ""  (row has CompletedAt != nil)
//
// Exactly one of the two exists for every decodable opv2:op: row once the
// index is built. Every writer of an opv2:op: row stages the index delta in
// the row's own batch (stageOpRow, via commitOpV2Row or directly in the
// batch-based status writers), so the row and its index key commit or fail
// together. TestOpsV2RowWritesGoThroughStageOpRow keeps it that way.
//
// TRUST MODEL (same as the opchange_by_book index, #3704): the index is
// UNTRUSTED at every boot. ListOperationsV2Since serves from the full scan,
// which is always correct, until this process's ReconcileOpsV2TimelineIndex
// has completed; only then does it read the index. A persisted "built"
// sentinel cannot work here: a binary that predates the index ignores both
// families, so after a rollback it writes and rewrites rows with no index
// keys (or leaves a stale done key on a row it resumes), and a sentinel from
// before the rollback would vouch for an index that no longer covers them.
// Re-checking at every boot makes rollback and roll-forward safe with no
// operator step. COST, which grows with the operation count until op
// retention (D2) bounds it: passes 1-2 are one row walk plus two key walks
// with no per-row point Get. The review measured the earlier per-row-Get
// version on disk at 50k rows 0.71 s steady / 4.96 s first boot and 200k rows
// 3.97 s / 20.8 s (prod estimate 15-20 s every boot). BenchmarkReconcile-
// OpsV2Timeline (in-memory fs, M1 Max, loaded host), before -> after the
// set-based passes: steady 50k 0.49 -> 0.12 s, 200k 1.96 -> 0.51 s; first
// boot 50k 1.01 -> 1.11 s, 200k 5.42 -> 4.29 s (first boot is dominated by
// pass 3's locked per-candidate re-check, which correctness requires).

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// opsV2TimelineLog is the subsystem logger for the opv2 timeline index.
var opsV2TimelineLog = logger.New("database.opsv2-timeline")

const (
	opv2OpPrefix   = "opv2:op:"
	opv2OpenPrefix = "opv2:open:"
	opv2DonePrefix = "opv2:done:"

	// opsV2TimelineReconcileChunk is how many fixes one locked pass-3 batch
	// holds. opsMu is released between chunks so live writers are never
	// stalled for the length of a whole repair.
	opsV2TimelineReconcileChunk = 500

	// opv2DoneDigits is the width of the zero-padded nanosecond field.
	opv2DoneDigits = 20
)

// opsV2TimelineReconcileWorkers is the reconcile's bounded pool size. A var
// only so tests can pin it; never reassign in prod code.
var opsV2TimelineReconcileWorkers = runtime.NumCPU()

// opsV2BeforeCommitHook, when set, runs just before every opv2:op: row batch
// commits (commitOpV2Row, the batch-based status writers and both delete
// sites). A non-nil error aborts the commit and is returned to the caller.
// Test-only: nil in production. Atomic so a test that sets it cannot race
// writers in parallel tests, and handed the store so a test's hook can leave
// every other store alone.
var opsV2BeforeCommitHook atomic.Pointer[func(*PebbleStore) error]

// runOpsV2BeforeCommitHook calls opsV2BeforeCommitHook when one is set.
func (p *PebbleStore) runOpsV2BeforeCommitHook() error {
	if h := opsV2BeforeCommitHook.Load(); h != nil && *h != nil {
		return (*h)(p)
	}
	return nil
}

var (
	unixEpoch = time.Unix(0, 0)
	// maxUnixNanoTime is the last instant UnixNano can represent.
	maxUnixNanoTime = time.Unix(0, math.MaxInt64)
)

// errOpv2OpRawKey is returned by the raw-key entry points for an opv2:op:
// row key or an opv2:open: / opv2:done: timeline index key.
var errOpv2OpRawKey = errors.New("opv2: raw write refused")

// opv2RawRefusedPrefixes are the families the raw entry points refuse: the
// operation rows and both timeline index families. Matched as exact key
// prefixes ("opv2:op:" is not a prefix of "opv2:open:").
var opv2RawRefusedPrefixes = []string{opv2OpPrefix, opv2OpenPrefix, opv2DonePrefix}

// rejectOpv2OpRawKey is the runtime half of the row-write ratchet: the raw
// key-value entry points (SetRaw, DeleteRaw, DeleteRawBatch) take a key
// built anywhere, which the AST ratchet cannot follow, so they refuse the
// operation-row and timeline-index prefixes outright. A raw write to either
// would let the row and its index key disagree; rows must go through
// commitOpV2Row (or a stageOpRow writer), index keys through stageOpRow or
// the reconcile.
func rejectOpv2OpRawKey(key string) error {
	for _, prefix := range opv2RawRefusedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return fmt.Errorf("%w: %q is in the %s family; operation rows and their timeline index change only together, through commitOpV2Row / stageOpRow", errOpv2OpRawKey, key, prefix)
		}
	}
	return nil
}

// opv2OpenKey is the index key for an operation with no completion time.
func opv2OpenKey(opID string) []byte {
	return []byte(opv2OpenPrefix + opID)
}

// completedNanos maps a completion time to the index's sortable integer:
// UnixNano, clamped to 0 before 1970 and to MaxInt64 past 2262 (where
// UnixNano is undefined). Zero-padded to 20 digits, byte order is time order.
func completedNanos(t time.Time) int64 {
	if t.Before(unixEpoch) {
		return 0
	}
	if t.After(maxUnixNanoTime) {
		return math.MaxInt64
	}
	return t.UnixNano()
}

// opv2DoneKey is the index key for an operation completed at completedAt.
func opv2DoneKey(completedAt time.Time, opID string) []byte {
	return []byte(fmt.Sprintf("%s%020d:%s", opv2DonePrefix, completedNanos(completedAt), opID))
}

// opv2DoneLowerBound is the first done key at or after nanos.
func opv2DoneLowerBound(nanos int64) []byte {
	return []byte(fmt.Sprintf("%s%020d", opv2DonePrefix, nanos))
}

// parseOpv2DoneKey splits a done key into its nanos and op id. The layout is
// fixed: prefix (10 bytes), 20 digits, ':', then the id.
func parseOpv2DoneKey(k []byte) (nanos int64, opID string, ok bool) {
	const idStart = len(opv2DonePrefix) + opv2DoneDigits + 1
	if len(k) <= idStart || !bytes.HasPrefix(k, []byte(opv2DonePrefix)) || k[idStart-1] != ':' {
		return 0, "", false
	}
	n, err := strconv.ParseInt(string(k[len(opv2DonePrefix):idStart-1]), 10, 64)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, string(k[idStart:]), true
}

// stageOpRow stages the timeline-index delta for one opv2:op: row write into
// b. prev is the row as stored before this write (nil: none, or unknown);
// next is the row being written (nil: the row is being deleted). When the
// row's CompletedAt does not change (the progress-tick case) it stages
// nothing, so the index costs those writes zero extra keys.
//
// Every writer of an opv2:op: row must call this in the SAME batch as the
// row, including any future record pruner (release D), which deletes through
// stageOpRow(b, old, nil).
func stageOpRow(b *pebble.Batch, prev, next *OperationV2Row) error {
	prevDone := prev != nil && prev.CompletedAt != nil
	prevOpen := prev != nil && prev.CompletedAt == nil
	nextDone := next != nil && next.CompletedAt != nil
	nextOpen := next != nil && next.CompletedAt == nil
	sameDone := prevDone && nextDone && completedNanos(*prev.CompletedAt) == completedNanos(*next.CompletedAt)

	if prevDone && !sameDone {
		if err := b.Delete(opv2DoneKey(*prev.CompletedAt, prev.ID), nil); err != nil {
			return err
		}
	}
	if prevOpen && !nextOpen {
		if err := b.Delete(opv2OpenKey(prev.ID), nil); err != nil {
			return err
		}
	}
	if nextOpen && !prevOpen {
		if err := b.Set(opv2OpenKey(next.ID), nil, nil); err != nil {
			return err
		}
	}
	if nextDone && !sameDone {
		if err := b.Set(opv2DoneKey(*next.CompletedAt, next.ID), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// commitOpV2Row writes row under opv2:op:{row.ID} together with its timeline
// index delta against prev and whatever extra stages (queue and active keys),
// as one batch committed with pebble.Sync. It is the only non-batch path
// allowed to write an opv2:op: row.
func (p *PebbleStore) commitOpV2Row(prev *OperationV2Row, row *OperationV2Row, extra func(*pebble.Batch) error) (err error) {
	defer recoverPebbleClosed("commitOpV2Row", &err)
	if row == nil || row.ID == "" {
		return errors.New("opv2: commitOpV2Row: row has no id")
	}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(opv2OpKey(row.ID), data, nil); err != nil {
		return err
	}
	if err := stageOpRow(b, prev, row); err != nil {
		return err
	}
	if extra != nil {
		if err := extra(b); err != nil {
			return err
		}
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// doneKeysForIDs returns every opv2:done: key whose id part is in ids. It
// reads keys only (no row values). Used where a row's CompletedAt is unknown
// because the row did not decode, so its done key cannot be derived.
func (p *PebbleStore) doneKeysForIDs(ids map[string]struct{}) ([][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	prefix := []byte(opv2DonePrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		if _, id, ok := parseOpv2DoneKey(iter.Key()); ok {
			if _, hit := ids[id]; hit {
				out = append(out, bytes.Clone(iter.Key()))
			}
		}
	}
	iterErr := iter.Error()
	if cerr := iter.Close(); cerr != nil && iterErr == nil {
		iterErr = cerr
	}
	return out, iterErr
}

// ── reader ──────────────────────────────────────────────────────────────────

// opV2InWindow is ListOperationsV2Since's predicate: is row part of the
// timeline that starts at since?
//
// The window bounds HISTORY, not live work. An operation that has not
// completed is current by definition, however long ago it was queued —
// filtering on QueuedAt alone meant an op simply had to RUN longer than
// the window to disappear from its own timeline. A library.scan running
// 1h50m returned an empty timeline in production on 2026-08-16 while it
// was logging once a second, and an empty list reads as "nothing is
// running."
//
// Keyed on CompletedAt rather than a set of status strings on purpose: a
// status list has to be updated every time a new terminal state is added,
// and silently under-reports until someone remembers.
//
// For a FINISHED operation the window is tested against CompletedAt, not
// QueuedAt. Asking "what completed in the last 24h" and answering "what
// was QUEUED in the last 24h" drops exactly the operations most worth
// seeing: the long ones. A backfill queued 30h ago that finished twenty
// minutes ago is history from the last twenty minutes, but a QueuedAt test
// rules it out — and the longer an operation runs, the more likely it is to
// fall outside. That is the same defect the in-flight clause above was
// added for (an op vanishing from its own timeline for running too long),
// left half-fixed: live rows were rescued, finished ones were not.
//
// QueuedAt is still accepted as an alternative rather than replaced.
// CompletedAt >= QueuedAt should make it redundant, so it changes nothing
// in normal operation; it means a row with a clock-skewed or malformed
// CompletedAt degrades to the old behaviour instead of disappearing.
func opV2InWindow(row *OperationV2Row, since time.Time) bool {
	return row.CompletedAt == nil ||
		!row.CompletedAt.Before(since) ||
		!row.QueuedAt.Before(since)
}

// opV2TimelineLess orders the timeline: started_at DESC NULLS LAST, then
// queued_at DESC, then id DESC. The id tie-break is new with the index:
// sort.Slice is not stable, so the order of rows equal on both timestamps
// was undefined before, and the scan and the indexed read must agree.
func opV2TimelineLess(a, b *OperationV2Row) bool {
	sa, sb := a.StartedAt, b.StartedAt
	switch {
	case sa == nil && sb != nil:
		return false // NULLS LAST
	case sa != nil && sb == nil:
		return true
	case sa != nil && sb != nil && !sa.Equal(*sb):
		return sa.After(*sb)
	}
	if !a.QueuedAt.Equal(b.QueuedAt) {
		return a.QueuedAt.After(b.QueuedAt)
	}
	return a.ID > b.ID
}

// sortAndLimitTimeline sorts rows with opV2TimelineLess and truncates them.
func sortAndLimitTimeline(rows []OperationV2Row, limit int) []OperationV2Row {
	sort.Slice(rows, func(i, j int) bool { return opV2TimelineLess(&rows[i], &rows[j]) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// OpsV2TimelineIndexTrusted reports whether ListOperationsV2Since reads the
// timeline index in this process (true only after a completed reconcile).
func (p *PebbleStore) OpsV2TimelineIndexTrusted() bool {
	return p.opsV2TimelineTrusted.Load()
}

// setOpsV2TimelineTrusted flips the per-process trust and publishes the
// opsv2_timeline_index_trusted gauge (process-global, like
// opchange_by_book_index_trusted: production has one store). A flip to
// trusted on a closed store is refused and reports false; the flag and the
// gauge change under opsV2TimelineTrustMu, the lock Close's
// markOpsV2TimelineClosed takes, so the two can never interleave.
func (p *PebbleStore) setOpsV2TimelineTrusted(v bool) bool {
	p.opsV2TimelineTrustMu.Lock()
	defer p.opsV2TimelineTrustMu.Unlock()
	if v && p.opsV2TimelineClosed {
		return false
	}
	p.opsV2TimelineTrusted.Store(v)
	metrics.SetOpsV2TimelineIndexTrusted(v)
	return true
}

// markOpsV2TimelineClosed is Close's half: untrusted from now on, for good.
func (p *PebbleStore) markOpsV2TimelineClosed() {
	p.opsV2TimelineTrustMu.Lock()
	defer p.opsV2TimelineTrustMu.Unlock()
	p.opsV2TimelineClosed = true
	if p.opsV2TimelineTrusted.Swap(false) {
		metrics.SetOpsV2TimelineIndexTrusted(false)
	}
}

// listOperationsV2SinceScan is the full scan ListOperationsV2Since always
// used: decode every opv2:op: row, filter, sort, truncate. It stays in
// production as the fallback until this boot's reconcile completes, and it
// is the oracle the equivalence test compares the indexed read against.
//
// Rows that decode with an empty ID are skipped, as in the indexed read: a
// zero row is a hollow shell (see getExistingOpV2), it can carry no index
// key, and it rendered as a blank Activity card.
func (p *PebbleStore) listOperationsV2SinceScan(since time.Time, limit int) (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("listOperationsV2SinceScan", &err)
	if limit <= 0 {
		limit = 200
	}
	prefix := []byte(opv2OpPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []OperationV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		var row OperationV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil || row.ID == "" {
			continue
		}
		if opV2InWindow(&row, since) {
			all = append(all, row)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return sortAndLimitTimeline(all, limit), nil
}

// opv2ULIDSeekBound is the lower bound of the ULID leg: the first opv2:op:
// key whose ULID was minted at or after since minus one minute, or the bare
// prefix when that instant is before 1970 or outside the ULID time range.
//
// ASSUMPTION (the one-minute slack): every op id is a ULID minted no more
// than one minute before the row's QueuedAt is set. True for every writer
// today: registry.go, batch.go, resume.go and activity/sql_migration_report.go
// call ulid.Make() and set QueuedAt from time.Now() (or the same `now`) on the
// next lines, so the gap is microseconds. QueuedAt is never rewritten after
// insert (ResetOperationV2ForResume keeps it). If a writer ever mints an id
// earlier than that, or sets QueuedAt from a later clock, or uses a non-ULID
// id that sorts below the bound, a row whose ONLY in-window clause is
// QueuedAt (its CompletedAt is before since: clock skew) would be missed by
// the indexed read. The equivalence test (which mints ids at QueuedAt) and
// this comment are the guard; widen the slack rather than drop the leg.
func opv2ULIDSeekBound(since time.Time) []byte {
	t := since.Add(-time.Minute)
	if since.IsZero() || t.Before(unixEpoch) {
		return []byte(opv2OpPrefix)
	}
	var u ulid.ULID
	if err := u.SetTime(ulid.Timestamp(t)); err != nil {
		return []byte(opv2OpPrefix)
	}
	return []byte(opv2OpPrefix + u.String())
}

// listOperationsV2SinceIndexed answers ListOperationsV2Since from the
// timeline index. Every read comes from ONE snapshot, so a row that moves
// between the open and done families while this runs is seen in exactly the
// state it had at the snapshot, never in neither family.
//
// Legs:
//  1. every opv2:open: id (in flight: always in the window);
//  2. every opv2:done: id completed at or after since;
//  3. every opv2:op: row whose ULID was minted at or after since minus a
//     minute, decoded from the iterator. This leg exists only for the
//     predicate's QueuedAt clause: a row whose CompletedAt is before since
//     (clock skew) but which was queued inside the window. Op ids are ULIDs
//     minted immediately before QueuedAt is set (registry.go, batch.go,
//     resume.go, activity/sql_migration_report.go); the minute covers an id
//     minted slightly before its QueuedAt;
//  4. a point Get for every id from legs 1-2. Legs 1-2 keep only ids keyed
//     below leg 3's bound, so leg 3 and leg 4 never see the same row.
//
// Then opV2InWindow, sort, truncate, exactly as the scan does.
//
// Rows that decode with an empty ID are skipped, exactly as the scan skips
// them.
func (p *PebbleStore) listOperationsV2SinceIndexed(since time.Time, limit int) (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("listOperationsV2SinceIndexed", &err)
	if limit <= 0 {
		limit = 200
	}
	snap := p.db.NewSnapshot()
	defer snap.Close()

	// Leg 3 decodes EVERY row keyed at or above seek, so legs 1-2 only need
	// the ids below it. That keeps the legs disjoint (no dedupe of leg-3 rows)
	// and keeps a wide window from building a map of every op id: with
	// since=90d nearly every id is above seek and leg 3 is the whole read.
	seek := opv2ULIDSeekBound(since)
	opPrefix := []byte(opv2OpPrefix)
	belowSeek := func(id string) bool {
		return bytes.Compare(append(opPrefix[:len(opPrefix):len(opPrefix)], id...), seek) < 0
	}
	ids := make(map[string]struct{})

	// Leg 1: open.
	openPrefix := []byte(opv2OpenPrefix)
	if err := forEachSnapKey(snap, openPrefix, prefixUpperBound(openPrefix), func(k, _ []byte) error {
		if id := string(k[len(openPrefix):]); belowSeek(id) {
			ids[id] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Leg 2: done at or after since.
	donePrefix := []byte(opv2DonePrefix)
	lower := donePrefix
	if !since.IsZero() && !since.Before(unixEpoch) {
		lower = opv2DoneLowerBound(completedNanos(since))
	}
	if err := forEachSnapKey(snap, lower, prefixUpperBound(donePrefix), func(k, _ []byte) error {
		if _, id, ok := parseOpv2DoneKey(k); ok && belowSeek(id) {
			ids[id] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Leg 3: ULID seek, decoded from the iterator.
	var all []OperationV2Row
	if err := forEachSnapKey(snap, seek, prefixEnd(opPrefix), func(_, v []byte) error {
		var row OperationV2Row
		if err := json.Unmarshal(v, &row); err != nil || row.ID == "" {
			return nil
		}
		if opV2InWindow(&row, since) {
			all = append(all, row)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Leg 4: point Gets for the index ids below the seek bound.
	for id := range ids {
		row, ok, err := getOpV2FromSnapshot(snap, id)
		if err != nil {
			return nil, err
		}
		if ok && opV2InWindow(&row, since) {
			all = append(all, row)
		}
	}
	return sortAndLimitTimeline(all, limit), nil
}

// forEachSnapKey iterates [lower, upper) on snap. k and v are valid only for
// the duration of fn.
func forEachSnapKey(snap *pebble.Snapshot, lower, upper []byte, fn func(k, v []byte) error) error {
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if err := fn(iter.Key(), iter.Value()); err != nil {
			_ = iter.Close()
			return err
		}
	}
	iterErr := iter.Error()
	if cerr := iter.Close(); cerr != nil && iterErr == nil {
		iterErr = cerr
	}
	return iterErr
}

// getOpV2FromSnapshot point-reads one op row from snap. A missing,
// undecodable or id-less row reports ok=false with a nil error, the same as
// the scan's skip; any other read error is returned.
func getOpV2FromSnapshot(snap *pebble.Snapshot, id string) (OperationV2Row, bool, error) {
	var row OperationV2Row
	val, closer, err := snap.Get(opv2OpKey(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	defer closer.Close()
	if err := json.Unmarshal(val, &row); err != nil || row.ID == "" {
		return OperationV2Row{}, false, nil
	}
	return row, true, nil
}

// ── startup reconcile ───────────────────────────────────────────────────────

// OpsV2TimelineReconcileResult reports one ReconcileOpsV2TimelineIndex run.
//   - Rows: decodable opv2:op: rows seen by pass 1.
//   - Missing: rows whose desired index key pass 2's walk did not find
//     (computed after pass 2, from pass 1's desired set).
//   - Orphans: index keys pass 2 found that are not their id's desired key
//     (no row, an undecodable row, the other state, another completion
//     time, or a malformed key).
//   - Fixed: keys set or deleted by pass 3 after re-checking under opsMu.
type OpsV2TimelineReconcileResult struct {
	Rows     int           `json:"rows"`
	Missing  int           `json:"missing"`
	Orphans  int           `json:"orphans"`
	Fixed    int           `json:"fixed"`
	Duration time.Duration `json:"duration"`
}

// opV2IndexFields is the narrow decode the reconcile uses: the only two
// OperationV2Row fields the desired index key depends on, under the same
// JSON names (OperationV2Row has no tags and no custom marshalling, so the
// field names are the keys). Decoding just these skips allocating every
// other field of every row. One difference from a full decode: a row whose
// OTHER fields are malformed decodes here but not for readers; the
// reconcile then keeps an index key for it, and readers still skip the row
// on their point Get, so the timeline result is unchanged.
type opV2IndexFields struct {
	ID          string
	CompletedAt *time.Time
}

// desiredOpv2IndexKey is the one index key a row completed at completedAt
// (nil: not completed) should have under id.
func desiredOpv2IndexKey(id string, completedAt *time.Time) []byte {
	if completedAt == nil {
		return opv2OpenKey(id)
	}
	return opv2DoneKey(*completedAt, id)
}

// readOpV2Raw reads one op row's index fields from the live db. ok=false for
// a missing, undecodable or id-less row.
func (p *PebbleStore) readOpV2Raw(id string) (opV2IndexFields, bool, error) {
	var row opV2IndexFields
	val, closer, err := p.db.Get(opv2OpKey(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	defer closer.Close()
	if err := json.Unmarshal(val, &row); err != nil || row.ID == "" {
		return opV2IndexFields{}, false, nil
	}
	return row, true, nil
}

// keyExists reports whether key is present in the live db.
func (p *PebbleStore) keyExists(key []byte) (bool, error) {
	_, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = closer.Close()
	return true, nil
}

// opv2IndexKeyIsOrphan reports whether an open or done index key no longer
// matches its row. Used only by pass 3, to re-check an orphan candidate
// under opsMu (pass 2 finds candidates set-based, without it).
func (p *PebbleStore) opv2IndexKeyIsOrphan(key []byte) (bool, error) {
	switch {
	case bytes.HasPrefix(key, []byte(opv2OpenPrefix)):
		id := string(key[len(opv2OpenPrefix):])
		row, ok, err := p.readOpV2Raw(id)
		if err != nil {
			return false, err
		}
		return !ok || row.CompletedAt != nil, nil
	case bytes.HasPrefix(key, []byte(opv2DonePrefix)):
		nanos, id, ok := parseOpv2DoneKey(key)
		if !ok {
			return true, nil // malformed: nothing can read it back
		}
		row, found, err := p.readOpV2Raw(id)
		if err != nil {
			return false, err
		}
		return !found || row.CompletedAt == nil || completedNanos(*row.CompletedAt) != nanos, nil
	}
	return false, nil
}

// opsV2TimelineKV is one copied iterator entry handed from a producer to the workers.
type opsV2TimelineKV struct {
	k, v []byte
}

// runOpsV2TimelinePool runs one producer over [lower, upper) of snap
// (copying keys, and values when withValues) and opsV2TimelineReconcileWorkers
// workers; work(w, item) runs on worker w (0 <= w < workers), so a worker can
// keep its own unshared state. This is the CLAUDE.md bounded worker pool: one
// iterator, a bounded channel, NumCPU workers.
func runOpsV2TimelinePool(ctx context.Context, snap *pebble.Snapshot, workers int, lower, upper []byte, withValues bool, work func(w int, item opsV2TimelineKV) error) error {
	g, gctx := errgroup.WithContext(ctx)
	ch := make(chan opsV2TimelineKV, workers*64)
	// Every pool goroutine carries its own recoverPebbleClosed: the caller's
	// guard covers only the caller's goroutine, and a Close racing a boot
	// reconcile at shutdown makes these iterators panic with pebble.ErrClosed
	// (TestOpsV2Timeline_CloseRacesReconcile). It comes back as an error.
	g.Go(func() (err error) {
		defer recoverPebbleClosed("runOpsV2TimelinePool producer", &err)
		defer close(ch)
		iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
		if err != nil {
			return err
		}
		for iter.First(); iter.Valid(); iter.Next() {
			item := opsV2TimelineKV{k: bytes.Clone(iter.Key())}
			if withValues {
				item.v = bytes.Clone(iter.Value())
			}
			select {
			case ch <- item:
			case <-gctx.Done():
				_ = iter.Close()
				return gctx.Err()
			}
		}
		iterErr := iter.Error()
		if cerr := iter.Close(); cerr != nil && iterErr == nil {
			iterErr = cerr
		}
		return iterErr
	})
	for w := 0; w < workers; w++ {
		g.Go(func() (err error) {
			defer recoverPebbleClosed("runOpsV2TimelinePool worker", &err)
			for item := range ch {
				if err := work(w, item); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return g.Wait()
}

// detectOpsV2TimelineDrift is passes 1 and 2 of the reconcile, on ONE
// snapshot that it closes before returning, so the snapshot is not held
// through pass 3's locked fixes. It returns the decodable row count, the ids
// whose desired index key is missing, and the index keys that are not their
// id's desired key.
func (p *PebbleStore) detectOpsV2TimelineDrift(ctx context.Context) (rows int, candidates []string, orphanKeys [][]byte, err error) {
	snap := p.db.NewSnapshot()
	defer snap.Close()
	workers := max(1, opsV2TimelineReconcileWorkers)

	// Pass 1: id -> desired index key, one map per worker (no shared lock on
	// the hot path), merged after. Row ids are disjoint across workers.
	opPrefix := []byte(opv2OpPrefix)
	perWorker := make([]map[string]string, workers)
	for w := range perWorker {
		perWorker[w] = make(map[string]string)
	}
	if err := runOpsV2TimelinePool(ctx, snap, workers, opPrefix, prefixEnd(opPrefix), true, func(w int, item opsV2TimelineKV) error {
		var row opV2IndexFields
		if err := json.Unmarshal(item.v, &row); err != nil || row.ID == "" {
			return nil // SweepHollowOperationsV2's job
		}
		id := string(item.k[len(opPrefix):])
		perWorker[w][id] = string(desiredOpv2IndexKey(id, row.CompletedAt))
		return nil
	}); err != nil {
		return 0, nil, nil, fmt.Errorf("opsv2 timeline reconcile pass 1: %w", err)
	}
	desired := perWorker[0]
	for _, m := range perWorker[1:] {
		for id, k := range m {
			desired[id] = k
		}
	}
	rows = len(desired)

	// Pass 2: one sequential key-only walk per index family.
	found := make(map[string]struct{}, len(desired))
	for _, fam := range []string{opv2OpenPrefix, opv2DonePrefix} {
		prefix := []byte(fam)
		if err := ctx.Err(); err != nil {
			return 0, nil, nil, err
		}
		if err := forEachSnapKey(snap, prefix, prefixUpperBound(prefix), func(k, _ []byte) error {
			var id string
			if fam == opv2OpenPrefix {
				id = string(k[len(prefix):])
			} else if _, did, ok := parseOpv2DoneKey(k); ok {
				id = did
			}
			if want, ok := desired[id]; ok && id != "" && want == string(k) {
				found[id] = struct{}{}
				return nil
			}
			orphanKeys = append(orphanKeys, bytes.Clone(k))
			return nil
		}); err != nil {
			return 0, nil, nil, fmt.Errorf("opsv2 timeline reconcile pass 2 (%s): %w", fam, err)
		}
	}
	for id := range desired {
		if _, ok := found[id]; !ok {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)

	return rows, candidates, orphanKeys, nil
}

// ReconcileOpsV2TimelineIndex builds and repairs the opv2:open: / opv2:done:
// timeline index, then marks it trusted for this process so
// ListOperationsV2Since starts reading it. The server runs it in the
// background after memdb warmup on EVERY boot (see the trust model above).
//
//   - Pass 1 (no lock, set-based): one walk of every opv2:op: row, decoded
//     on a bounded pool, builds id -> the one index key the row should have.
//   - Pass 2 (no lock, set-based): one sequential key-only walk of each index
//     family. A key equal to its id's desired key marks that id found; any
//     other key is an orphan. Desired keys never found are missing. Passes 1
//     and 2 read ONE snapshot and make no per-row point Get, so a boot where
//     the index is already exact costs one row walk plus two key walks.
//   - Pass 3 (opsMu, chunks of opsV2TimelineReconcileChunk): every candidate
//     is re-read and re-checked under the lock, then fixed in one batch per
//     chunk. A row can change between the scan and the fix; the re-check is
//     the same rule RepairOpsV2MissingCompletedAt follows.
//
// Resumable: every committed chunk is correct on its own, readers stay on the
// scan until trust is set, and trust is set only after all three passes
// finish cleanly with ctx live. An interrupted run loses nothing and the next
// boot runs again. No cursor: the passes are read-mostly and
// idempotent, and a cursor would skip rows a pre-index binary changed between
// attempts (the book_atpath backfill argument).
func (p *PebbleStore) ReconcileOpsV2TimelineIndex(ctx context.Context) (res OpsV2TimelineReconcileResult, err error) {
	defer recoverPebbleClosed("ReconcileOpsV2TimelineIndex", &err)
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()

	rows, candidates, orphanKeys, err := p.detectOpsV2TimelineDrift(ctx)
	if err != nil {
		return res, err
	}
	res.Rows = rows
	res.Missing = len(candidates)
	res.Orphans = len(orphanKeys)

	// Pass 3: fix under the lock, chunked. Candidates and orphans share one
	// work list; each item is either an id (missing) or a key (orphan).
	work := make([]opsV2TimelineFix, 0, len(candidates)+len(orphanKeys))
	for _, id := range candidates {
		work = append(work, opsV2TimelineFix{id: id})
	}
	for _, k := range orphanKeys {
		work = append(work, opsV2TimelineFix{key: k})
	}
	for startIdx := 0; startIdx < len(work); startIdx += opsV2TimelineReconcileChunk {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		end := min(startIdx+opsV2TimelineReconcileChunk, len(work))
		fixed, err := p.reconcileOpsV2TimelineChunk(work[startIdx:end])
		res.Fixed += fixed
		if err != nil {
			return res, fmt.Errorf("opsv2 timeline reconcile pass 3: %w", err)
		}
	}

	if err := ctx.Err(); err != nil {
		return res, err
	}
	if !p.setOpsV2TimelineTrusted(true) {
		return res, fmt.Errorf("opsv2 timeline reconcile: %w", pebble.ErrClosed)
	}
	res.Duration = time.Since(start)
	opsV2TimelineLog.Info("opsv2-timeline-reconcile: index trusted for this boot: rows=%d missing=%d orphans=%d fixed=%d duration_ms=%d",
		res.Rows, res.Missing, res.Orphans, res.Fixed, res.Duration.Milliseconds())
	return res, nil
}

// opsV2TimelineFix is one pass-3 work item: an op id whose desired index key
// was missing (key == nil), or an index key that looked orphaned (id == "").
type opsV2TimelineFix struct {
	id  string
	key []byte
}

// reconcileOpsV2TimelineChunk re-checks and fixes one chunk under opsMu, in
// one batch, and returns how many keys it set or deleted.
func (p *PebbleStore) reconcileOpsV2TimelineChunk(items []opsV2TimelineFix) (int, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	b := p.db.NewBatch()
	defer b.Close()
	fixed := 0
	for _, it := range items {
		id, key := it.id, it.key
		if key == nil {
			row, ok, err := p.readOpV2Raw(id)
			if err != nil {
				return 0, err
			}
			if !ok {
				continue // deleted or broken since pass 1
			}
			want := desiredOpv2IndexKey(id, row.CompletedAt)
			have, err := p.keyExists(want)
			if err != nil {
				return 0, err
			}
			if have {
				continue // a live writer fixed it since pass 1
			}
			if err := b.Set(want, nil, nil); err != nil {
				return 0, err
			}
			if row.CompletedAt != nil {
				// A row that completed under a pre-index binary may still have
				// the open key from before; it is stale now.
				if err := b.Delete(opv2OpenKey(id), nil); err != nil {
					return 0, err
				}
			}
			fixed++
			continue
		}
		orphan, err := p.opv2IndexKeyIsOrphan(key)
		if err != nil {
			return 0, err
		}
		if !orphan {
			continue
		}
		if err := b.Delete(key, nil); err != nil {
			return 0, err
		}
		fixed++
	}
	if b.Empty() {
		return 0, nil
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return fixed, nil
}
