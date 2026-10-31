// file: internal/database/pebble_store_ops_v2_wait.go
// version: 1.0.0
// guid: f896f9fc-6ce7-4739-91a5-c1a750a0b265
// last-edited: 2026-10-30

// The waiting-deps status index for opv2 operation rows.
//
// ListWaitingDepsOps used to decode every opv2:op: row on every call to find
// the handful whose Status is "waiting_deps" (DepsScheduler.SweepTick,
// reevaluateSubject, the fail-propagation path and rebuildIndex all call it;
// about 7 GB of allocation per 25 minutes on the production heap). One
// empty-valued index family lets it read only those rows:
//
//	opv2:wait:{op_id}   → ""   (the row's Status is "waiting_deps")
//
// It rides the timeline index's machinery rather than building a second copy:
//
//   - WRITERS: stageOpRow stages the wait delta (stageOpWait) in the row's own
//     batch, so the row and its wait key commit or fail together, and
//     TestOpsV2RowWritesGoThroughStageOpRow already keeps every opv2:op:
//     writer on stageOpRow. Writers that delete a row whose prior state is
//     unknown (an undecodable row) delete the wait key by id.
//   - TRUST: the same per-boot trust as the timeline index. A rollback to a
//     binary that predates this index writes status changes with no wait keys,
//     so nothing persisted can vouch for the index. ListWaitingDepsOps serves
//     from the full scan until this process's ReconcileOpsV2TimelineIndex has
//     completed, and that reconcile builds and repairs this family in the
//     same passes (see detectOpsV2TimelineDrift).
//   - SELF-HEALING: a reader that finds a wait key whose row is gone or no
//     longer waiting deletes the key (healStaleWaitKeys) and skips it.
//
// A waiting op the index does not cover is invisible to the indexed read, so
// the reconcile's completeness (every waiting row has its key) is what trust
// vouches for; the reader's heal only ever removes keys.

package database

import (
	"encoding/json"
	"errors"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// opsV2WaitLog is the subsystem logger for the waiting-deps index.
var opsV2WaitLog = logger.New("database.opsv2-wait")

const opv2WaitPrefix = "opv2:wait:"

// opv2WaitKey is the index key for an operation whose Status is waiting_deps.
func opv2WaitKey(opID string) []byte {
	return []byte(opv2WaitPrefix + opID)
}

// isWaitingDepsStatus reports whether status is the parked-on-dependencies one.
func isWaitingDepsStatus(status string) bool {
	return status == string(state.WaitingDeps)
}

// stageOpWait stages the waiting-deps index delta for one opv2:op: row write
// into b. prev is the row as stored before this write (nil: none, or unknown);
// next is the row being written (nil: the row is being deleted). A write that
// does not change whether the row is waiting stages nothing, so progress ticks
// cost the index zero extra keys. Called only from stageOpRow.
func stageOpWait(b *GatedBatch, prev, next *OperationV2Row) error {
	prevWait := prev != nil && isWaitingDepsStatus(prev.Status)
	nextWait := next != nil && isWaitingDepsStatus(next.Status)
	if prevWait && !nextWait {
		if err := b.Delete(opv2WaitKey(prev.ID), nil); err != nil {
			return err
		}
	}
	if nextWait && !prevWait {
		if err := b.Set(opv2WaitKey(next.ID), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// ListWaitingDepsOps returns all OperationV2Row entries whose Status is
// "waiting_deps", in op id order. It reads the opv2:wait: index once this
// boot's ReconcileOpsV2TimelineIndex has completed and the full opv2:op: scan
// before that; both return the same rows in the same order.
func (p *PebbleStore) ListWaitingDepsOps() (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("ListWaitingDepsOps", &err)
	if p.OpsV2TimelineIndexTrusted() {
		return p.listWaitingDepsOpsIndexed()
	}
	return p.listWaitingDepsOpsScan()
}

// listWaitingDepsOpsScan is the full scan ListWaitingDepsOps always used:
// decode every opv2:op: row and keep the waiting ones. It stays in production
// as the fallback until this boot's reconcile completes, and it is the oracle
// the equivalence test compares the indexed read against.
func (p *PebbleStore) listWaitingDepsOpsScan() (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("listWaitingDepsOpsScan", &err)
	prefix := []byte(opv2OpPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var result []OperationV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		var row OperationV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			continue
		}
		if isWaitingDepsStatus(row.Status) {
			result = append(result, row)
		}
	}
	return result, iter.Error()
}

// listWaitingDepsOpsIndexed answers ListWaitingDepsOps from the wait index.
// The keys and the rows come from ONE snapshot, so a row that leaves waiting
// while this runs is seen in exactly the state it had at the snapshot. A key
// whose row is gone, hollow or no longer waiting is stale: it is skipped and
// handed to healStaleWaitKeys. A key whose row is present but does not decode
// is skipped too (the scan skips such a row) and kept, because its status is
// unknown.
func (p *PebbleStore) listWaitingDepsOpsIndexed() (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("listWaitingDepsOpsIndexed", &err)
	snap := p.db.NewSnapshot()
	prefix := []byte(opv2WaitPrefix)
	var ids []string
	if err := forEachSnapKey(snap, prefix, prefixUpperBound(prefix), func(k, _ []byte) error {
		ids = append(ids, string(k[len(prefix):]))
		return nil
	}); err != nil {
		snap.Close()
		return nil, err
	}

	var result []OperationV2Row
	var stale []string
	for _, id := range ids {
		val, closer, gerr := snap.Get(opv2OpKey(id))
		if errors.Is(gerr, pebble.ErrNotFound) {
			stale = append(stale, id)
			continue
		}
		if gerr != nil {
			snap.Close()
			return nil, gerr
		}
		var row OperationV2Row
		uerr := json.Unmarshal(val, &row)
		_ = closer.Close()
		switch {
		case uerr != nil:
			// Present but undecodable: the scan skips it, and its status is unknown.
		case row.ID == "" || !isWaitingDepsStatus(row.Status):
			stale = append(stale, id)
		default:
			result = append(result, row)
		}
	}
	snap.Close()

	if len(stale) > 0 {
		p.healStaleWaitKeys(stale)
	}
	return result, nil
}

// healStaleWaitKeys deletes the wait keys in ids whose row is gone or no
// longer waiting. Each id is re-checked against the live row under opsMu (the
// lock every row writer holds), because the id came from a snapshot and the op
// may have been parked again since. Best effort: a failure is logged and the
// next read retries, so the read that found the stale keys never fails on it.
func (p *PebbleStore) healStaleWaitKeys(ids []string) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	b := p.db.NewBatch()
	defer b.Close()
	removed := 0
	for _, id := range ids {
		stale, err := p.waitKeyIsStale(id)
		if err != nil {
			opsV2WaitLog.Warn("waiting-deps index heal: reading op %s failed: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
			return
		}
		if !stale {
			continue
		}
		if err := b.Delete(opv2WaitKey(id), nil); err != nil {
			opsV2WaitLog.Warn("waiting-deps index heal: staging delete for op %s failed: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
			return
		}
		removed++
	}
	if removed == 0 {
		return
	}
	if err := b.Commit(pebble.Sync); err != nil {
		opsV2WaitLog.Warn("waiting-deps index heal: commit failed: %s", logger.SanitizeLogValue(err.Error()))
		return
	}
	opsV2WaitLog.Info("waiting-deps index heal: removed stale wait keys: count=%d", removed)
}

// waitKeyIsStale reports whether id's wait key no longer matches its row, read
// live: the row is gone, is a hollow shell, or has another status. A row that is
// present but does not decode is NOT stale (its status is unknown), matching the
// reconcile, which reads the same narrow fields.
func (p *PebbleStore) waitKeyIsStale(id string) (bool, error) {
	val, closer, err := p.db.Get(opv2OpKey(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer closer.Close()
	var row opV2IndexFields
	if err := json.Unmarshal(val, &row); err != nil {
		return false, nil
	}
	return row.ID == "" || !isWaitingDepsStatus(row.Status), nil
}
