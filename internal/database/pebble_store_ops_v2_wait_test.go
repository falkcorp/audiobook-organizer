// file: internal/database/pebble_store_ops_v2_wait_test.go
// version: 1.0.0
// guid: 9ac83fe1-2b02-457c-aa05-67edc8262e3f
// last-edited: 2026-10-30

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// waitTestOp is a queued-now row with the given status. Ids sort by their
// number, so the scan and the index agree on order without relying on ULIDs.
func waitTestOp(n int, status string) OperationV2Row {
	return OperationV2Row{
		ID:       fmt.Sprintf("OP%05d", n),
		DefID:    "library.scan",
		Plugin:   "core",
		Status:   status,
		QueuedAt: time.Date(2026, 10, 30, 9, 0, 0, n, time.UTC),
	}
}

func waitKeys(t testing.TB, p *PebbleStore) []string {
	t.Helper()
	return timelineKeys(t, p, opv2WaitPrefix)
}

func reconcileWait(t testing.TB, p *PebbleStore) OpsV2TimelineReconcileResult {
	t.Helper()
	res, err := p.ReconcileOpsV2TimelineIndex(context.Background())
	require.NoError(t, err)
	require.True(t, p.OpsV2TimelineIndexTrusted())
	return res
}

func setWaitKeyRaw(t testing.TB, p *PebbleStore, id string) {
	t.Helper()
	require.NoError(t, p.db.Set(opv2WaitKey(id), nil, pebble.Sync))
}

// requireWaitingMatchesScan fails unless the indexed read returns exactly the
// full scan's rows, in the scan's order.
func requireWaitingMatchesScan(t testing.TB, p *PebbleStore, msg string) []OperationV2Row {
	t.Helper()
	scan, err := p.listWaitingDepsOpsScan()
	require.NoError(t, err)
	idx, err := p.listWaitingDepsOpsIndexed()
	require.NoError(t, err)
	require.Equal(t, rowIDs(scan), rowIDs(idx), msg)
	require.True(t, reflect.DeepEqual(scan, idx) || (len(scan) == 0 && len(idx) == 0), "%s: rows differ", msg)
	return idx
}

// A waiting row's wait key is written in the row's own batch, survives writes
// that leave it waiting, and goes in the batch that moves it out (promotion, a
// terminal status, a delete).
func TestWaitingDepsIndex_WrittenOnStageRemovedOnLeave(t *testing.T) {
	p := newTimelineStore(t)

	require.NoError(t, p.InsertOperationV2(waitTestOp(1, "waiting_deps")))
	require.NoError(t, p.InsertOperationV2(waitTestOp(2, "waiting_deps")))
	require.NoError(t, p.InsertOperationV2(waitTestOp(3, "waiting_deps")))
	require.NoError(t, p.InsertOperationV2(waitTestOp(4, "queued")))
	require.Equal(t, []string{string(opv2WaitKey("OP00001")), string(opv2WaitKey("OP00002")), string(opv2WaitKey("OP00003"))}, waitKeys(t, p),
		"only the waiting rows have a wait key")

	// A write that leaves the row waiting changes nothing.
	require.NoError(t, p.UpdateOpProgressV2("OP00001", 1, 10, "still parked"))
	require.Len(t, waitKeys(t, p), 3)

	// waiting_deps -> queued.
	require.NoError(t, p.PromoteToQueued("OP00001"))
	require.NotContains(t, waitKeys(t, p), string(opv2WaitKey("OP00001")))

	// waiting_deps -> failed.
	require.NoError(t, p.UpdateOperationV2Status("OP00002", "failed", nil, nil, nil))
	require.NotContains(t, waitKeys(t, p), string(opv2WaitKey("OP00002")))

	// waiting_deps -> deleted.
	_, deleted, err := p.DeleteOperationV2("OP00003", []string{"waiting_deps"})
	require.NoError(t, err)
	require.True(t, deleted)
	require.Empty(t, waitKeys(t, p))

	// queued -> waiting_deps (a re-park) brings it back.
	require.NoError(t, p.UpdateOperationV2Status("OP00004", "waiting_deps", nil, nil, nil))
	require.Equal(t, []string{string(opv2WaitKey("OP00004"))}, waitKeys(t, p))
}

// ListWaitingDepsOps must return the full scan's rows, in the scan's order, on a
// store holding hundreds of ops in mixed statuses that were moved around through
// the real writers.
func TestWaitingDepsIndex_MatchesFullScanRandomHistory(t *testing.T) {
	statuses := []string{"waiting_deps", "queued", "running", "completed", "failed", "canceled", "interrupted_quiesced"}
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			p := newTimelineStore(t)
			rng := rand.New(rand.NewPCG(seed, 99))
			const n = 300
			for i := range n {
				require.NoError(t, p.InsertOperationV2(waitTestOp(i, statuses[rng.IntN(len(statuses))])))
			}
			// Churn: random status moves, deletes and re-inserts.
			for range 600 {
				id := fmt.Sprintf("OP%05d", rng.IntN(n))
				switch rng.IntN(4) {
				case 0, 1:
					_ = p.UpdateOperationV2Status(id, statuses[rng.IntN(len(statuses))], nil, nil, nil)
				case 2:
					_ = p.PromoteToQueued(id) // errors unless the row is waiting
				default:
					if _, _, err := p.DeleteOperationV2(id, statuses); err != nil {
						t.Fatal(err)
					}
					require.NoError(t, p.InsertOperationV2(waitTestOp(rng.IntN(n), statuses[rng.IntN(len(statuses))])))
				}
			}

			// Before the reconcile the read is the scan itself.
			before, err := p.ListWaitingDepsOps()
			require.NoError(t, err)
			scan, err := p.listWaitingDepsOpsScan()
			require.NoError(t, err)
			require.Equal(t, scan, before)

			res := reconcileWait(t, p)
			require.Zero(t, res.Missing, "writers staged every wait key themselves")
			require.Zero(t, res.Orphans)
			require.Equal(t, len(scan), res.Waiting)
			got := requireWaitingMatchesScan(t, p, "after reconcile")
			require.NotEmpty(t, got, "the history must leave some ops waiting or the test proves nothing")
			require.Less(t, len(got), n, "and some not waiting")

			pub, err := p.ListWaitingDepsOps()
			require.NoError(t, err)
			require.Equal(t, scan, pub)
			require.Len(t, waitKeys(t, p), len(scan))
		})
	}
}

// A wait key whose row is gone, or whose row moved on, is skipped by the reader
// and deleted; a live waiting op next to them is still returned.
func TestWaitingDepsIndex_StaleKeyIsHealed(t *testing.T) {
	p := newTimelineStore(t)
	require.NoError(t, p.InsertOperationV2(waitTestOp(1, "waiting_deps")))
	require.NoError(t, p.InsertOperationV2(waitTestOp(2, "completed")))
	reconcileWait(t, p)
	require.Len(t, waitKeys(t, p), 1)

	setWaitKeyRaw(t, p, "OP00002") // row exists but is completed
	setWaitKeyRaw(t, p, "OP00099") // no such row
	require.Len(t, waitKeys(t, p), 3)

	// The read after the heal must not trust the index to be exact: probe the
	// indexed path directly so the scan fallback cannot mask a regression.
	rows, err := p.ListWaitingDepsOps()
	require.NoError(t, err)
	require.Equal(t, []string{"OP00001"}, rowIDs(rows))
	require.Equal(t, []string{string(opv2WaitKey("OP00001"))}, waitKeys(t, p), "both stale keys are deleted by the read")

	// And the heal keeps working on a second pass (nothing stale, nothing changes).
	rows, err = p.ListWaitingDepsOps()
	require.NoError(t, err)
	require.Equal(t, []string{"OP00001"}, rowIDs(rows))
}

// The heal re-checks the live row under the lock: a key the snapshot called stale
// but whose op has been parked again since is kept.
func TestWaitingDepsIndex_HealKeepsKeyOfOpParkedAgain(t *testing.T) {
	p := newTimelineStore(t)
	require.NoError(t, p.InsertOperationV2(waitTestOp(1, "waiting_deps")))
	reconcileWait(t, p)
	p.healStaleWaitKeys([]string{"OP00001", "OP00404"})
	require.Equal(t, []string{string(opv2WaitKey("OP00001"))}, waitKeys(t, p))
}

// A row that is present but does not decode has an unknown status: the reader
// skips it (as the scan does) and leaves its key alone.
func TestWaitingDepsIndex_UndecodableRowKeepsKey(t *testing.T) {
	p := newTimelineStore(t)
	require.NoError(t, p.InsertOperationV2(waitTestOp(1, "waiting_deps")))
	reconcileWait(t, p)
	require.NoError(t, p.db.Set(opv2OpKey("OP00005"), []byte("{not json"), pebble.Sync))
	setWaitKeyRaw(t, p, "OP00005")

	rows, err := p.ListWaitingDepsOps()
	require.NoError(t, err)
	require.Equal(t, []string{"OP00001"}, rowIDs(rows))
	require.Contains(t, waitKeys(t, p), string(opv2WaitKey("OP00005")))
}

// A store written without the index (a pre-index binary, or a rollback): the read
// is the scan until the boot reconcile runs, and the reconcile builds the family
// from the rows and removes keys that match no waiting row.
func TestWaitingDepsIndex_BootReconcilePopulatesFromRows(t *testing.T) {
	p := newTimelineStore(t)
	rawSetOp(t, p, waitTestOp(1, "waiting_deps"))
	rawSetOp(t, p, waitTestOp(2, "queued"))
	rawSetOp(t, p, waitTestOp(3, "waiting_deps"))
	rawSetOp(t, p, waitTestOp(4, "completed"))
	setWaitKeyRaw(t, p, "OP00002") // a stale key left by a pre-index binary's resume
	setWaitKeyRaw(t, p, "OP00404") // and one for a row that is gone
	require.NotContains(t, waitKeys(t, p), string(opv2WaitKey("OP00001")), "no key for a waiting row yet")

	require.False(t, p.OpsV2TimelineIndexTrusted())
	rows, err := p.ListWaitingDepsOps()
	require.NoError(t, err)
	require.Equal(t, []string{"OP00001", "OP00003"}, rowIDs(rows), "untrusted: the scan answers, covering rows the index lacks")
	idx, err := p.listWaitingDepsOpsIndexed()
	require.NoError(t, err)
	require.Empty(t, idx, "probe is void if the unrepaired index already finds them")
	// That probe healed the two stale keys; put them back for the reconcile to find.
	setWaitKeyRaw(t, p, "OP00002")
	setWaitKeyRaw(t, p, "OP00404")

	res := reconcileWait(t, p)
	require.Equal(t, 2, res.Waiting)
	require.GreaterOrEqual(t, res.Missing, 2)
	require.GreaterOrEqual(t, res.Orphans, 2)
	require.Equal(t, []string{string(opv2WaitKey("OP00001")), string(opv2WaitKey("OP00003"))}, waitKeys(t, p))
	rows = requireWaitingMatchesScan(t, p, "after reconcile")
	require.Equal(t, []string{"OP00001", "OP00003"}, rowIDs(rows))

	// A second boot finds the index exact and changes nothing.
	again := reconcileWait(t, p)
	require.Zero(t, again.Fixed)
}

// The wait row is also JSON-visible through the public read: the rows the indexed
// path returns are complete decodes, not the narrow index fields.
func TestWaitingDepsIndex_ReturnsFullRows(t *testing.T) {
	p := newTimelineStore(t)
	row := waitTestOp(7, "waiting_deps")
	row.Params = `{"book":"Year One"}`
	require.NoError(t, p.InsertOperationV2(row))
	reconcileWait(t, p)
	rows, err := p.ListWaitingDepsOps()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	want, _ := json.Marshal(&row)
	got, _ := json.Marshal(&rows[0])
	require.JSONEq(t, string(want), string(got))
}
