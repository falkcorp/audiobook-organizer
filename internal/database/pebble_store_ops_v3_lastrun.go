// file: internal/database/pebble_store_ops_v3_lastrun.go
// version: 1.1.0
// guid: a8bcfede-9c68-4566-9772-5c687b225780
// last-edited: 2026-10-18

package database

import (
	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// latestSettledLookahead is how many runs queued before the newest finished
// one LatestSettledRunV3 also reads, in case one of them finished later.
const latestSettledLookahead = 8

// latestSettledScanCap bounds how many of the definition's own index rows
// LatestSettledRunV3 examines (rows of other defs sharing the key prefix are
// skipped for free and do not count). One ConcurrencyKey keeps a def's
// in-flight runs to a handful, and queued-but-undispatched runs are bounded by
// the enqueue limits, so a def that has settled before reaches a finished run
// within a few rows; 256 is two orders of magnitude over that, while capping
// the cost of a def with a backlog of never-finishing runs at 256 record
// loads instead of its whole history.
const latestSettledScanCap = 256

// LatestSettledRunV3 returns the definition's newest finished run (settled,
// with completed_at set), or nil when it has none.
//
// It walks the definition's own index (opv3:ix:def:{def_id}:{queued_ns}:)
// backwards from the newest queued run, so the cost is the def's unfinished
// runs at the top of the index plus one record, not a scan of every finished
// run in the store. The index is ordered by queue time, not completion time,
// so it also reads the latestSettledLookahead runs queued before the first
// finished one and returns whichever of those finished last. That is exact
// for a definition whose runs do not overlap (one ConcurrencyKey serializes
// almost every def), and for overlapping runs it is exact unless a run queued
// more than latestSettledLookahead runs earlier finished later.
//
// The walk examines at most latestSettledScanCap rows of the def. When that
// many unfinished runs sit above its newest finished one, it stops and returns
// nil, the same as a def with no finished run: the caller's last_run is null
// even though older settled runs exist. That is acceptable because a def with
// 256 newer runs still queued or running has a backlog the runs list shows
// directly, and a bounded wrong-looking null on a summary field beats an
// unbounded read on every /api/v3/ops request.
//
// Not part of database.Store: /api/v3/ops resolves it with AsCapability.
func (p *PebbleStore) LatestSettledRunV3(defID string) (best *RunRecord, err error) {
	defer recoverPebbleClosed("LatestSettledRunV3", &err)
	prefix := []byte(opv3IxDefPrefix + defID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	after, examined := 0, 0
	for iter.Last(); iter.Valid(); iter.Prev() {
		// A def id containing ':' shares its prefix with a longer id; the key
		// names its own def, so such rows are dropped before any load and
		// count against neither the lookahead nor the scan cap.
		if kDef, _, _, ok := parseIxDefKey(iter.Key()); !ok || kDef != defID {
			continue
		}
		if examined == latestSettledScanCap {
			break
		}
		examined++
		if best != nil {
			if after == latestSettledLookahead {
				break
			}
			after++
		}
		r, lerr := p.loadRunV3(lastSegment(iter.Key()))
		if lerr != nil {
			_ = iter.Close()
			return nil, lerr
		}
		if r == nil || r.CompletedAt == nil || !state.ClassifyV3(r.State).Settled {
			continue
		}
		if best == nil || r.CompletedAt.After(*best.CompletedAt) {
			best = r
		}
	}
	iterErr := iter.Error()
	if cerr := iter.Close(); cerr != nil && iterErr == nil {
		iterErr = cerr
	}
	if iterErr != nil {
		return nil, iterErr
	}
	return best, nil
}
