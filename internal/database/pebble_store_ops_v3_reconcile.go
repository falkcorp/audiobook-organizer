// file: internal/database/pebble_store_ops_v3_reconcile.go
// version: 1.0.1
// guid: 65cdda3b-c2ec-4866-88fa-c58559c39f59
// last-edited: 2026-10-11

// The boot reconcile of opv3: against opv2: (05-PR4 roll-forward safety).
//
// A binary that predates 05-PR4 writes only opv2: rows. After a rollback to
// one and a roll-forward, the schema version is still 65, so migration 065's
// Up does not run again, and every run that binary created, changed or
// deleted has a stale opv3: record, none, or one too many: the v3 census is
// wrong until each run's next v2 write. ReconcileOpsV3WithV2 closes that gap
// at every boot: it rebuilds the record of every opv2:op: row whose bytes no
// longer hash to the record's V2RowHash (or that has no record), through the
// same projection migration 065 uses (migrateOpsV3Batch), and removes every
// opv3 run whose opv2 row is gone. It deletes only opv3: keys of such runs:
// never opchange:, op_result:, opsummary:, opstate: or any other journal.

package database

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"
)

// opsV3ReconcilePage is how many keys one detection page carries.
const opsV3ReconcilePage = 1000

// opsV3ReconcileWorkers is the detection pass's checker goroutines. A var so
// a test can force several workers on a small machine.
var opsV3ReconcileWorkers = runtime.NumCPU()

// OpsV3ReconcileReport is what one ReconcileOpsV3WithV2 run did.
type OpsV3ReconcileReport struct {
	Checked     int // opv2:op: rows hashed by the detection pass
	Stale       int // rows whose record was missing or named another version of the row
	Rebuilt     int // records rebuilt from their row
	Current     int // stale at detection, current under the lock (a live write synced it)
	Orphans     int // opv3 runs whose opv2 row is gone, removed
	Undecodable int // rows or records that do not decode (logged; left alone)
	// MirrorOff is true when the reconcile did nothing because the opv2
	// mirror is off: opv2: is then no longer a source of truth.
	MirrorOff bool
	Duration  time.Duration
}

// ReconcileOpsV3WithV2 brings every opv3 run record back in line with its
// opv2:op: row (see the file comment). It is idempotent, and a store whose
// records are all current is only read: no batch is committed.
//
// DETECTION is a read-only walk without opsMu. One iterator streams each
// keyspace in key order in pages of opsV3ReconcilePage keys, and
// opsV3ReconcileWorkers goroutines check the pages: hashing each opv2 row and
// reading its opv3:prog: hash (CPU and point reads, so a bounded pool sized
// to the CPUs, per the repo CLAUDE.md concurrency rule), and checking each
// opv3 run for its opv2 row.
//
// REPAIR is single-writer and sequential, in batches of opsV3MigrationBatch
// ids, each under opsMu, the lock every ops writer takes: every batch
// read-modify-writes the shared opv3:meta:counts key, so a second writer
// would only queue on the lock. Each batch re-reads the row and record under
// the lock and skips a record a live write has synced since detection, so
// the reconcile is correct alongside live writers, not only before them.
//
// WHEN. InitializeStore runs it synchronously after the migrations, before
// the server and the operations registry exist, because the registry's boot
// resume acts on opv3 records (bootSweepV3 and retireForRequeue read
// GetRunV3 and compare-and-set the stored state), so they must be current
// before it runs. A failure is logged and boot continues: opv2: is still the
// source of truth the registry runs from.
//
// MIRROR OFF. With the opv2 mirror off (05-PR16) native v3 writes no longer
// update opv2:, so a stale opv2 row is expected, and rebuilding from it (or
// deleting a v3-native run with no opv2 row as an orphan) would destroy v3
// state. The guard below returns early when the switch is off, but the
// switch is in-memory, not persisted, and reads "on" at every process start,
// so at InitializeStore it is ALWAYS on: the guard only covers a caller that
// flipped it earlier in the same process. 05-PR16 MUST therefore delete the
// InitializeStore call (and this reconcile) in the same change that turns
// the mirror off; leaving the call in place would run it with the mirror
// "on" against a store whose opv2 rows have gone stale.
func (p *PebbleStore) ReconcileOpsV3WithV2(ctx context.Context) (rep OpsV3ReconcileReport, err error) {
	defer recoverPebbleClosed("ReconcileOpsV3WithV2", &err)
	start := time.Now()
	defer func() { rep.Duration = time.Since(start) }()
	if !p.opsV2MirrorEnabled() {
		rep.MirrorOff = true
		opsV3Log.Info("opv3 reconcile: the opv2 mirror is off; opv2: is not a source of truth, nothing to reconcile")
		return rep, nil
	}

	stale, checked, err := p.findStaleRunsV3(ctx)
	if err != nil {
		return rep, err
	}
	rep.Checked, rep.Stale = checked, len(stale)
	orphans, err := p.findOrphanRunsV3(ctx)
	if err != nil {
		return rep, err
	}

	mrep := OpsV3MigrationReport{PerStatus: map[string]int{}, Unmapped: map[string]int{}}
	var requeued []string
	for i := 0; i < len(stale); i += opsV3MigrationBatch {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		more, err := p.migrateOpsV3Batch(stale[i:min(i+opsV3MigrationBatch, len(stale))], &mrep)
		if err != nil {
			return rep, err
		}
		requeued = append(requeued, more...)
	}
	rep.Rebuilt, rep.Current, rep.Undecodable = mrep.Written, mrep.Skipped, mrep.Undecodable
	if len(requeued) > 0 {
		if err := p.fillMigratedSuccessors(requeued, opsV3MigrationBatch, &mrep); err != nil {
			return rep, err
		}
	}
	for i := 0; i < len(orphans); i += opsV3MigrationBatch {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n, err := p.deleteOrphanRunsV3Batch(orphans[i:min(i+opsV3MigrationBatch, len(orphans))])
		if err != nil {
			return rep, err
		}
		rep.Orphans += n
	}

	if rep.Rebuilt > 0 || rep.Orphans > 0 {
		opsV3Log.Warn("opv3 reconcile: %d of %d runs were changed by a binary without the opv3 projection and were rebuilt from opv2, %d runs it deleted were removed (%d current under the lock, %d undecodable) in %s",
			rep.Rebuilt, rep.Checked, rep.Orphans, rep.Current, rep.Undecodable, time.Since(start).Round(time.Millisecond))
		if len(mrep.Unmapped) > 0 {
			opsV3Log.Warn("opv3 reconcile: v2 status string(s) not in the mapping table, recorded as failed: %v", mrep.Unmapped)
		}
	} else {
		opsV3Log.Info("opv3 reconcile: %d runs checked, all current (%d undecodable) in %s",
			rep.Checked, rep.Undecodable, time.Since(start).Round(time.Millisecond))
	}
	return rep, nil
}

// findStaleRunsV3 returns, sorted, the ids of opv2:op: rows whose opv3:prog:
// V2RowHash is missing or is not the hash of the row's bytes, and how many
// rows it checked.
func (p *PebbleStore) findStaleRunsV3(ctx context.Context) (stale []string, checked int, err error) {
	var mu sync.Mutex
	open := func() (*GatedIter, error) {
		return p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(opv2OpPrefix), UpperBound: prefixEnd([]byte(opv2OpPrefix))})
	}
	err = p.walkPagesParallel(ctx, open, true, func(keys, vals [][]byte) error {
		var mine []string
		for i, k := range keys {
			id := strings.TrimPrefix(string(k), opv2OpPrefix)
			var hot struct {
				V2RowHash string `json:"v2_row_hash"`
			}
			found, gerr := p.getJSONFound(opv3ProgKey(id), &hot)
			// An unreadable progress key is stale too: the rebuild reads
			// the record and reports it if it cannot.
			if gerr != nil || !found || hot.V2RowHash != opsV3RowHash(vals[i]) {
				mine = append(mine, id)
			}
		}
		mu.Lock()
		stale = append(stale, mine...)
		checked += len(keys)
		mu.Unlock()
		return nil
	})
	slices.Sort(stale)
	return stale, checked, err
}

// findOrphanRunsV3 returns, sorted, the ids of opv3 runs with no opv2:op:
// row. While the mirror is on every run has one (each write path commits the
// two together), so an orphan is a run a binary without the projection
// deleted.
func (p *PebbleStore) findOrphanRunsV3(ctx context.Context) (orphans []string, err error) {
	var mu sync.Mutex
	open := func() (*GatedIter, error) {
		return p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(opv3RunPrefix), UpperBound: prefixEnd([]byte(opv3RunPrefix))})
	}
	err = p.walkPagesParallel(ctx, open, false, func(keys, _ [][]byte) error {
		var mine []string
		for _, k := range keys {
			id := strings.TrimPrefix(string(k), opv3RunPrefix)
			ok, herr := p.opv2RowExists(id)
			if herr != nil {
				return herr
			}
			if !ok {
				mine = append(mine, id)
			}
		}
		mu.Lock()
		orphans = append(orphans, mine...)
		mu.Unlock()
		return nil
	})
	slices.Sort(orphans)
	return orphans, err
}

// deleteOrphanRunsV3Batch removes, in one batch under opsMu, every run in ids
// that still has no opv2:op: row, through the façade's delete projection
// (stageRunV3DeleteForV2: the record, its indexes, counters and per-run opv3
// families). It returns how many it removed.
func (p *PebbleStore) deleteOrphanRunsV3Batch(ids []string) (n int, err error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	b := p.db.NewBatch()
	defer b.Close()
	s := p.newOpsV3Stage(b)
	for _, id := range ids {
		has, err := p.opv2RowExists(id)
		if err != nil {
			return 0, err
		}
		if has {
			continue // re-created since detection: the live write projected it
		}
		ok, err := p.hasKey(opv3RunKey(id))
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if err := p.stageRunV3DeleteForV2(s, id); err != nil {
			return 0, err
		}
		n++
	}
	if b.Empty() {
		return 0, nil
	}
	if err := s.flush(); err != nil {
		return 0, err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return 0, err
	}
	return n, b.Commit(pebble.Sync)
}

// walkPagesParallel streams the iterator open returns, in key order, in
// pages of opsV3ReconcilePage keys (with copies of the values when
// withValues), and hands each page to one of opsV3ReconcileWorkers goroutines
// running check. The first error (or ctx's) stops the walk. (open, not a
// prefix, so the opv2:op: key stays inside its reviewed reader:
// TestOpsV2RowWritesGoThroughStageOpRow.)
func (p *PebbleStore) walkPagesParallel(ctx context.Context, open func() (*GatedIter, error), withValues bool, check func(keys, vals [][]byte) error) error {
	workers := max(opsV3ReconcileWorkers, 1)
	type page struct{ keys, vals [][]byte }
	g, gctx := errgroup.WithContext(ctx)
	pages := make(chan page, workers)
	g.Go(func() (err error) {
		defer close(pages)
		defer recoverPebbleClosed("walkPagesParallel", &err)
		iter, err := open()
		if err != nil {
			return err
		}
		defer func() {
			if cerr := iter.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}()
		send := func(pg page) error {
			select {
			case pages <- pg:
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		var pg page
		for iter.First(); iter.Valid(); iter.Next() {
			pg.keys = append(pg.keys, append([]byte(nil), iter.Key()...))
			if withValues {
				v, verr := iter.ValueAndErr()
				if verr != nil {
					return verr
				}
				pg.vals = append(pg.vals, append([]byte(nil), v...))
			}
			if len(pg.keys) == opsV3ReconcilePage {
				if err := send(pg); err != nil {
					return err
				}
				pg = page{}
			}
		}
		if err := iter.Error(); err != nil {
			return err
		}
		if len(pg.keys) > 0 {
			return send(pg)
		}
		return nil
	})
	for range workers {
		g.Go(func() error {
			for pg := range pages {
				if err := gctx.Err(); err != nil {
					return err
				}
				if err := check(pg.keys, pg.vals); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return g.Wait()
}
