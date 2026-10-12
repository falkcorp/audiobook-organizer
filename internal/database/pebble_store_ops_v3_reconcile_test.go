// file: internal/database/pebble_store_ops_v3_reconcile_test.go
// version: 1.1.2
// guid: ea4b7eaf-18c6-45d2-9344-3838c68c905f
// last-edited: 2026-10-11

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// countOpsCommits counts the ops batches p commits (every opv2/opv3 batch
// writer runs opsV2BeforeCommitHook first) until the returned stop is called.
func countOpsCommits(t testing.TB, p *PebbleStore) (count func() int64, stop func()) {
	t.Helper()
	var n atomic.Int64
	hook := func(s *PebbleStore) error {
		if s == p {
			n.Add(1)
		}
		return nil
	}
	opsV2BeforeCommitHook.Store(&hook)
	stop = func() { opsV2BeforeCommitHook.Store(nil) }
	t.Cleanup(stop)
	return n.Load, stop
}

// walBytes is the WAL's logical bytes written so far: a store-wide count of
// every committed write, hooked or not.
func walBytes(t testing.TB, p *PebbleStore) uint64 {
	t.Helper()
	m, err := p.db.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	return m.WAL.BytesIn
}

// assertReconcileNoop runs the reconcile and fails unless it found nothing
// to do and wrote nothing at all: no ops batch, no WAL byte, no key changed.
func assertReconcileNoop(t testing.TB, p *PebbleStore, after string) {
	t.Helper()
	before := v3Digest(t, p, "opv2:", "opv3:")
	wal := walBytes(t, p)
	commits, stop := countOpsCommits(t, p)
	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	stop()
	if err != nil {
		t.Fatalf("%s: reconcile: %v", after, err)
	}
	if rep.Stale != 0 || rep.Rebuilt != 0 || rep.Orphans != 0 {
		stale, _, _ := p.findStaleRunsV3(context.Background())
		t.Errorf("%s: reconcile found work on a store every writer kept current: %+v (stale %v)", after, rep, stale)
	}
	if n := commits(); n != 0 {
		t.Errorf("%s: reconcile committed %d batches on a current store", after, n)
	}
	if w := walBytes(t, p) - wal; w != 0 {
		t.Errorf("%s: reconcile wrote %d WAL bytes on a current store", after, w)
	}
	if v3Digest(t, p, "opv2:", "opv3:") != before {
		t.Errorf("%s: reconcile changed keys on a current store", after)
	}
}

// assertRecordSynced fails unless id's record names the hash of its stored
// opv2 row bytes.
func assertRecordSynced(t testing.TB, p *PebbleStore, id string) *RunRecord {
	t.Helper()
	_, raw, err := p.getOpV2Raw(id)
	if err != nil || raw == nil {
		t.Fatalf("opv2 row %s: %v (raw %d bytes)", id, err, len(raw))
	}
	r := mustRun(t, p, id)
	if r.V2RowHash != opsV3RowHash(raw) {
		t.Errorf("run %s: V2RowHash %q, row hashes to %q", id, r.V2RowHash, opsV3RowHash(raw))
	}
	return r
}

// keysMentioning returns every opv3: key containing id.
func keysMentioning(t testing.TB, p *PebbleStore, id string) []string {
	t.Helper()
	var out []string
	for _, k := range timelineKeys(t, p, opv3Prefix) {
		if strings.Contains(k, id) {
			out = append(out, k)
		}
	}
	return out
}

// TestReconcileOpsV3NoWritesAfterEveryWriter drives every opv2 row writer
// and every native v3 writer, and after each one proves the boot reconcile
// would find nothing to do and write nothing. A writer that forgot to stamp
// V2RowHash (or stamped the hash of other bytes than it stored) fails here,
// instead of making every boot rewrite every record.
func TestReconcileOpsV3NoWritesAfterEveryWriter(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	errMsg := "boom"
	phase := "match"
	a, b, w := newRunID(), newRunID(), newRunID()
	steps := []struct {
		name string
		run  func() error
	}{
		{"InsertOperationV2", func() error {
			return p.InsertOperationV2(OperationV2Row{ID: a, DefID: "test.every-writer", Plugin: "test", Status: "queued", QueuedAt: now, Params: `{"k":1}`})
		}},
		{"InsertOperationV2 waiting_deps", func() error {
			return p.InsertOperationV2(OperationV2Row{ID: w, DefID: "test.every-writer", Plugin: "test", Status: "waiting_deps", QueuedAt: now})
		}},
		{"SetOpQueuedProgressV2", func() error { _, err := p.SetOpQueuedProgressV2(a, 0, 10, "queued estimate"); return err }},
		{"PromoteToQueued", func() error { return p.PromoteToQueued(w) }},
		{"UpdateOperationV2StatusIf", func() error {
			_, err := p.UpdateOperationV2StatusIf(a, "queued", "running", &now, nil, nil)
			return err
		}},
		{"UpdateOpProgressV2", func() error { return p.UpdateOpProgressV2(a, 3, 10, "item 3") }},
		{"UpdateOpPhaseV2", func() error { return p.UpdateOpPhaseV2(a, &phase) }},
		{"UpdateOpCheckpointV2", func() error { return p.UpdateOpCheckpointV2(a, 3) }},
		{"UpdateOperationV2Params", func() error { return p.UpdateOperationV2Params(a, []byte(`{"k":2}`)) }},
		{"SetOperationV2Result", func() error { return p.SetOperationV2Result(a, `{"partial":true}`) }},
		{"IncrementResumeCountV2", func() error { return p.IncrementResumeCountV2(a) }},
		{"UpdateOperationV2Status", func() error { return p.UpdateOperationV2Status(a, "failed", nil, &now, &errMsg) }},
		{"MarkOperationV2ManualRetry", func() error { return p.MarkOperationV2ManualRetry(a) }},
		{"ResetOperationV2ForResume", func() error { return p.ResetOperationV2ForResume(a) }},
		{"RepairOpsV2MissingCompletedAt", func() error {
			// A terminal row with no completed_at, as an older binary left
			// it (raw, so stale until the repair rewrites it).
			rawSetOp(t, p, OperationV2Row{ID: b, DefID: "test.every-writer", Plugin: "test", Status: "completed", QueuedAt: now})
			_, err := p.RepairOpsV2MissingCompletedAt()
			return err
		}},
		{"DeleteOperationV2", func() error { _, _, err := p.DeleteOperationV2(b, []string{"completed"}); return err }},
		{"SweepHollowOperationsV2", func() error {
			// A run with a record whose row an older binary left as a shell
			// (raw, so stale until the sweep deletes both).
			h := newRunID()
			if err := p.InsertOperationV2(OperationV2Row{ID: h, DefID: "test.every-writer", Plugin: "test", Status: "queued", QueuedAt: now}); err != nil {
				return err
			}
			if err := p.db.Set(opv2OpKey(h), []byte(`{}`), pebble.Sync); err != nil {
				return err
			}
			n, err := p.SweepHollowOperationsV2()
			if err == nil && n != 1 {
				err = fmt.Errorf("swept %d rows, want 1", n)
			}
			if r, gerr := p.GetRunV3(h); err == nil && (gerr != nil || r != nil) {
				err = fmt.Errorf("the swept row's run record survived: %+v, %v", r, gerr)
			}
			return err
		}},
	}
	var nat string
	steps = append(steps, []struct {
		name string
		run  func() error
	}{
		{"InsertRunV3", func() error {
			nat = newRunID()
			return p.InsertRunV3(RunRecord{ID: nat, DefID: "test.every-writer-v3", Priority: 3})
		}},
		{"TransitionRunV3 dispatch", func() error { transition(t, p, nat, state.EvDispatch, nil); return nil }},
		{"SaveCheckpointV3", func() error { return p.SaveCheckpointV3(nat, Checkpoint{Phase: "scan"}) }},
		{"CommitLedgerV3", func() error {
			return p.CommitLedgerV3(nat, LedgerBatch{Ledger: Ledger{ChunkCount: 2}, Progress: ProgressSnapshot{Done: 5}})
		}},
		{"PutItemOutcomesV3", func() error {
			return p.PutItemOutcomesV3(nat, []ItemOutcome{{Key: "item-1", Outcome: "skipped:dup"}})
		}},
		{"TransitionRunV3 stop", func() error {
			transition(t, p, nat, state.EvStopRequested, func(r *TransitionRequest) { r.StopReason = state.StopReasons[0] })
			return nil
		}},
		{"DeleteRunV3", func() error { _, err := p.DeleteRunV3(nat, reachableV3States()); return err }},
	}...)
	for _, st := range steps {
		if err := st.run(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		assertReconcileNoop(t, p, st.name)
	}
	assertRecordSynced(t, p, a)
	assertRecordSynced(t, p, w)
	checkV3Consistency(t, p)
}

// TestReconcileOpsV3RepairsOldBinaryWritesOnReboot simulates a rollback: a
// binary without the opv3 projection writes, creates and deletes opv2 rows
// directly (no opv3 change); the next boot of this binary (InitializeStore:
// version already 65, so migration 065 does not run) must rebuild every
// stale record, create the missing one, remove the orphan, and leave the
// untouched runs alone. A further reconcile then writes nothing.
func TestReconcileOpsV3RepairsOldBinaryWritesOnReboot(t *testing.T) {
	dir := t.TempDir() + "/pebble"
	open := func() *PebbleStore {
		t.Helper()
		s, err := InitializeStore("pebble", dir)
		if err != nil {
			t.Fatalf("InitializeStore: %v", err)
		}
		return s.(*PebbleStore)
	}
	shut := func() {
		t.Helper()
		if err := CloseStore(); err != nil {
			t.Fatalf("CloseStore: %v", err)
		}
	}
	t.Cleanup(func() { _ = CloseStore() })

	p := open()
	now := time.Now().UTC().Truncate(time.Second)
	ins := func(status string) string {
		id := newRunID()
		if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.rollback", Plugin: "test", Status: "queued", QueuedAt: now, Params: `{}`}); err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			if err := p.UpdateOperationV2Status(id, status, &now, &now, nil); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	untouched := ins("completed")
	finished := ins("queued")  // the old binary runs it to completed
	deleted := ins("canceled") // the old binary purges it
	quiesced := ins("running") // the old binary stops it at shutdown
	native := newRunID()       // a v3-native run the old binary requeues
	if err := p.InsertRunV3(RunRecord{ID: native, DefID: "test.rollback-v3", Priority: 2}); err != nil {
		t.Fatal(err)
	}
	transition(t, p, native, state.EvDispatch, nil)
	untouchedBefore := v3Digest(t, p, opv3RunPrefix+untouched, opv3ProgPrefix+untouched, opv3EvtPrefix+untouched+":")
	shut()

	// ── the previous binary ──────────────────────────────────────────────
	raw, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) OperationV2Row {
		row, err := raw.GetOperationV2(id)
		if err != nil || row == nil {
			t.Fatalf("GetOperationV2(%s): %v", id, err)
		}
		return *row
	}
	done := now.Add(time.Minute)
	r := get(finished)
	r.Status, r.StartedAt, r.CompletedAt, r.ProgressCurrent, r.ProgressTotal = "completed", &now, &done, 10, 10
	rawSetOp(t, raw, r)
	r = get(quiesced)
	r.Status, r.CompletedAt = "interrupted_quiesced", &done
	rawSetOp(t, raw, r)
	r = get(native)
	r.Status, r.CompletedAt = "interrupted_quiesced", &done
	rawSetOp(t, raw, r)
	created := newRunID()
	errMsg := "boom"
	rawSetOp(t, raw, OperationV2Row{ID: created, DefID: "test.rollback", Plugin: "test", Status: "failed", QueuedAt: now, CompletedAt: &done, ErrorMessage: &errMsg, Params: `{}`})
	if err := raw.db.Delete(opv2OpKey(deleted), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// The deleted run's journals (undo journal, results, summaries, v1
	// state) are not opv3 keys: removing its opv3 run must leave every one.
	journals := []string{"opchange:" + deleted + ":0001", "op_result:" + deleted + ":book-1", "opsummary:" + deleted, "opstate:" + deleted}
	for _, k := range journals {
		if err := raw.db.Set([]byte(k), []byte(`{"synthetic":true}`), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// ── roll forward ─────────────────────────────────────────────────────
	p = open()
	if v, err := getCurrentVersion(p); err != nil || v < 65 {
		t.Fatalf("version = %d, %v", v, err)
	}
	if got := assertRecordSynced(t, p, finished); got.State != state.V3Succeeded || got.Progress.Done != 10 || got.FromV2 == nil {
		t.Errorf("finished: %s done=%d from_v2=%v; want succeeded with the old binary's progress", got.State, got.Progress.Done, got.FromV2)
	}
	if got := assertRecordSynced(t, p, quiesced); got.State != state.V3Interrupted || got.InterruptedWhy != state.WhyQuiesced {
		t.Errorf("quiesced: %s/%s", got.State, got.InterruptedWhy)
	}
	if got := assertRecordSynced(t, p, native); got.State != state.V3Interrupted || got.Attempt != 1 {
		t.Errorf("native: %s attempt %d; want interrupted, attempt carried over", got.State, got.Attempt)
	}
	if got := assertRecordSynced(t, p, created); got.State != state.V3Failed || got.Error != "boom" {
		t.Errorf("created: %s %q", got.State, got.Error)
	}
	if r, err := p.GetRunV3(deleted); err != nil || r != nil {
		t.Errorf("deleted: record %+v, %v; want removed", r, err)
	}
	if ks := keysMentioning(t, p, deleted); len(ks) != 0 {
		t.Errorf("deleted run left opv3 keys: %v", ks)
	}
	for _, k := range journals {
		if v, closer, err := p.db.Get([]byte(k)); err != nil {
			t.Errorf("the reconcile removed %s with the orphaned run: %v", k, err)
		} else {
			if string(v) != `{"synthetic":true}` {
				t.Errorf("the reconcile rewrote %s: %q", k, v)
			}
			_ = closer.Close()
		}
	}
	if v3Digest(t, p, opv3RunPrefix+untouched, opv3ProgPrefix+untouched, opv3EvtPrefix+untouched+":") != untouchedBefore {
		t.Error("the reconcile rewrote a run the old binary never touched")
	}
	c, err := p.OpCountsV3()
	if err != nil || c.Total != 5 || c.ByState[state.V3Succeeded] != 2 || c.ByState[state.V3Interrupted] != 2 || c.ByState[state.V3Failed] != 1 {
		t.Errorf("counts after reconcile = %+v, %v; want 5 runs: 2 succeeded, 2 interrupted, 1 failed", c, err)
	}
	checkV3Consistency(t, p)
	assertReconcileNoop(t, p, "second reconcile after the roll-forward")
}

// TestReconcileOpsV3MirrorOffDoesNothing: with the mirror off, opv2 rows are
// legitimately stale and must not be rebuilt from.
func TestReconcileOpsV3MirrorOffDoesNothing(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.mirror-off", "")
	row, err := p.GetOperationV2(id)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	row.Status = "failed"
	rawSetOp(t, p, *row) // stale versus the record, as if the mirror had been off
	p.setOpsV2MirrorEnabled(false)
	defer p.setOpsV2MirrorEnabled(true)
	before := v3Digest(t, p, "opv2:", "opv3:")
	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	if err != nil || !rep.MirrorOff || rep.Checked != 0 {
		t.Fatalf("report = %+v, %v; want MirrorOff and nothing checked", rep, err)
	}
	if v3Digest(t, p, "opv2:", "opv3:") != before || mustRun(t, p, id).State != state.V3Queued {
		t.Fatal("the reconcile acted with the mirror off")
	}
}

// TestReconcileOpsV3SafeAgainstLiveWrites runs the reconcile over 300 stale
// runs while live façade writes hit the same runs, with several detection
// workers. Whatever the interleaving, every record ends current with its row
// and the indexes and counters agree with a full scan.
func TestReconcileOpsV3SafeAgainstLiveWrites(t *testing.T) {
	old := opsV3ReconcileWorkers
	opsV3ReconcileWorkers = 4
	t.Cleanup(func() { opsV3ReconcileWorkers = old })
	p := newTimelineStore(t)
	now := time.Now().UTC()
	ids := make([]string, 300)
	for i := range ids {
		ids[i] = newRunID()
		if err := p.InsertOperationV2(OperationV2Row{ID: ids[i], DefID: fmt.Sprintf("test.live-%d", i%5), Plugin: "test", Status: "queued", QueuedAt: now}); err != nil {
			t.Fatal(err)
		}
		row, _ := p.GetOperationV2(ids[i])
		row.Status, row.StartedAt = "running", &now
		rawSetOp(t, p, *row) // stale: an older binary dispatched it
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, id := range ids {
			if i%2 == 0 {
				_ = p.UpdateOpProgressV2(id, i, 300, "live")
			} else {
				msg := "live"
				_ = p.UpdateOperationV2Status(id, "failed", nil, &now, &msg)
			}
		}
	}()
	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rebuilt+rep.Current != rep.Stale {
		t.Errorf("report = %+v: every stale run is either rebuilt or found current under the lock", rep)
	}
	for _, id := range ids {
		assertRecordSynced(t, p, id)
	}
	checkV3Consistency(t, p)
	assertReconcileNoop(t, p, "after the concurrent pass")
}

// TestOpsV3ProgressTickWritesOnlyProgKey: a v2 progress tick leaves the
// opv3:run: value byte-identical and changes only opv3:prog:, which commits
// in the v2 row's batch (a failed commit changes neither).
func TestOpsV3ProgressTickWritesOnlyProgKey(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	id := newRunID()
	if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.tick", Plugin: "test", Status: "queued", QueuedAt: now, Params: `{"big":"` + strings.Repeat("p", 2048) + `"}`}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateOperationV2Status(id, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	runBefore := v3Digest(t, p, opv3RunPrefix)
	progBefore := v3Digest(t, p, opv3ProgPrefix)
	wal := walBytes(t, p)
	if err := p.UpdateOpProgressV2(id, 7, 9, "item 7"); err != nil {
		t.Fatal(err)
	}
	if v3Digest(t, p, opv3RunPrefix) != runBefore {
		t.Error("a progress tick rewrote opv3:run:")
	}
	// The digest cannot see an identical rewrite of opv3:run:; the WAL can.
	// The tick's batch is the v2 row (which carries the 2 KiB params) plus
	// small keys; a rewritten record would add the params a second time.
	_, raw, err := p.getOpV2Raw(id)
	if err != nil {
		t.Fatal(err)
	}
	if w := walBytes(t, p) - wal; w > uint64(len(raw))+1024 {
		t.Errorf("a progress tick wrote %d WAL bytes for a %d-byte v2 row; the opv3:run: record (params included) was rewritten", w, len(raw))
	}
	if v3Digest(t, p, opv3ProgPrefix) == progBefore {
		t.Error("a progress tick did not write opv3:prog:")
	}
	r := assertRecordSynced(t, p, id)
	if r.Progress.Done != 7 || r.Progress.Total == nil || *r.Progress.Total != 9 || r.Progress.Current != "item 7" || r.LastProgressAt == nil {
		t.Errorf("GetRunV3 progress = %+v last %v", r.Progress, r.LastProgressAt)
	}

	boom := errors.New("injected commit failure")
	hook := func(s *PebbleStore) error {
		if s == p {
			return boom
		}
		return nil
	}
	opsV2BeforeCommitHook.Store(&hook)
	defer opsV2BeforeCommitHook.Store(nil)
	before := v3Digest(t, p, "opv2:", "opv3:")
	if err := p.UpdateOpProgressV2(id, 8, 9, "item 8"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if v3Digest(t, p, "opv2:", "opv3:") != before {
		t.Fatal("a failed tick commit changed opv2: or opv3:")
	}
}

// batchKeys lists the user keys staged in b, in staging order.
func batchKeys(t testing.TB, b *GatedBatch) []string {
	t.Helper()
	r := b.b.Reader()
	var out []string
	for {
		_, k, _, ok, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, string(k))
	}
}

// TestStageRecordWritesOnlyChangedHalf pins the run record split: a change to
// the runHot half stages only opv3:prog:, a change to the rest stages only
// opv3:run:, and no change stages nothing. (A digest of the stored values
// cannot see an identical rewrite; the staged batch can.)
func TestStageRecordWritesOnlyChangedHalf(t *testing.T) {
	p := newTimelineStore(t)
	id := newRunID()
	total := int64(10)
	prev := &RunRecord{ID: id, DefID: "test.split", State: state.V3Running, Attempt: 1,
		Params:    json.RawMessage(`{"big":"` + strings.Repeat("p", 4096) + `"}`),
		Progress:  ProgressSnapshot{Done: 1, Total: &total, Current: "item 1"},
		V2RowHash: "0123"}
	stage := func(next *RunRecord) []string {
		t.Helper()
		b := p.db.NewBatch()
		defer b.Close()
		if err := p.newOpsV3Stage(b).stageRecord(prev, next); err != nil {
			t.Fatal(err)
		}
		return batchKeys(t, b)
	}
	tick := *prev
	tick.Progress.Done, tick.Progress.Current, tick.V2RowHash = 2, "item 2", "4567"
	if got := stage(&tick); len(got) != 1 || got[0] != string(opv3ProgKey(id)) {
		t.Errorf("a progress tick staged %v; want only %s", got, opv3ProgKey(id))
	}
	core := *prev
	core.Error = "boom"
	if got := stage(&core); len(got) != 1 || got[0] != string(opv3RunKey(id)) {
		t.Errorf("a record change with the same progress staged %v; want only %s", got, opv3RunKey(id))
	}
	same := *prev
	if got := stage(&same); len(got) != 0 {
		t.Errorf("an unchanged record staged %v; want nothing", got)
	}
}

// TestNativeWritesRefuseUnsyncedRecord: with the mirror on, a native write
// whose stored record does not name the current opv2 row bytes (a previous
// binary changed or deleted the row, and the boot reconcile has not
// repaired the record) is refused with ErrStaleTransition and writes
// nothing: no stale state, timestamp or result is mirrored over the newer
// row, no deleted row is re-created, and no run is discarded by a state it
// has left. After the reconcile the same writes go through.
func TestNativeWritesRefuseUnsyncedRecord(t *testing.T) {
	p := newTimelineStore(t)
	changed := insertV3(t, p, "test.unsynced", "")
	transition(t, p, changed, state.EvDispatch, nil)
	gone := insertV3(t, p, "test.unsynced", "")

	// The previous binary: changes one row, deletes the other, no opv3.
	row, err := p.GetOperationV2(changed)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	done := time.Now().UTC()
	row.Status, row.CompletedAt = "interrupted_quiesced", &done
	rawSetOp(t, p, *row)
	if err := p.db.Delete(opv2OpKey(gone), pebble.Sync); err != nil {
		t.Fatal(err)
	}

	before := v3Digest(t, p, "opv2:", "opv3:")
	cur := mustRun(t, p, changed)
	_, err = p.TransitionRunV3(TransitionRequest{ID: changed, ExpectState: cur.State, ExpectAttempt: cur.Attempt,
		ExpectFenceEpoch: cur.FenceEpoch, Event: state.EvStopRequested, StopReason: state.StopUser})
	if !errors.Is(err, ErrStaleTransition) {
		t.Errorf("transition of a run whose row changed under it: err = %v, want ErrStaleTransition", err)
	}
	if ok, err := p.DeleteRunV3(changed, reachableV3States()); ok || !errors.Is(err, ErrStaleTransition) {
		t.Errorf("discard of a run whose row changed under it: deleted %v, err %v; want refused", ok, err)
	}
	g := mustRun(t, p, gone)
	_, err = p.TransitionRunV3(TransitionRequest{ID: gone, ExpectState: g.State, ExpectAttempt: g.Attempt,
		ExpectFenceEpoch: g.FenceEpoch, Event: state.EvDispatch})
	if !errors.Is(err, ErrStaleTransition) {
		t.Errorf("transition of a run whose row was deleted: err = %v, want ErrStaleTransition", err)
	}
	if v3Digest(t, p, "opv2:", "opv3:") != before {
		t.Fatal("a refused native write changed opv2: or opv3: (a deleted row re-created, or a stale record mirrored)")
	}

	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	if err != nil || rep.Rebuilt != 1 || rep.Orphans != 1 {
		t.Fatalf("reconcile = %+v, %v; want 1 rebuilt, 1 orphan", rep, err)
	}
	r := transition(t, p, changed, state.EvDecideDrop, nil)
	if r.State != state.V3Dropped {
		t.Errorf("after the reconcile: %s, want dropped", r.State)
	}
	checkV3Consistency(t, p)
}

// TestReconcileOpsV3LockedRecheckKeepsLiveWrite: a run stale at detection
// that a live write synced before its repair batch is left as the live write
// left it. Rebuilding it anyway would push the live "running" row through
// migration 065's map, which reads "running" as interrupted{crash}.
func TestReconcileOpsV3LockedRecheckKeepsLiveWrite(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	id := newRunID()
	if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.recheck", Plugin: "test", Status: "queued", QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	row, _ := p.GetOperationV2(id)
	row.ProgressMessage = "written by a binary without the projection"
	rawSetOp(t, p, *row)

	stale, _, err := p.findStaleRunsV3(context.Background())
	if err != nil || len(stale) != 1 || stale[0] != id {
		t.Fatalf("detection = %v, %v; want [%s]", stale, err, id)
	}
	// The live dispatch lands between detection and repair.
	if err := p.UpdateOperationV2Status(id, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	rep := OpsV3MigrationReport{PerStatus: map[string]int{}, Unmapped: map[string]int{}}
	if _, err := p.migrateOpsV3Batch(stale, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Written != 0 || rep.Skipped != 1 {
		t.Errorf("repair = %+v; want the synced run skipped under the lock", rep)
	}
	if got := assertRecordSynced(t, p, id); got.State != state.V3Running {
		t.Errorf("live run is %s/%s after the repair; want running", got.State, got.InterruptedWhy)
	}
	checkV3Consistency(t, p)
}

// TestReconcileOpsV3OrphanRecheckUnderLock: a run orphaned at detection whose
// opv2 row a live write re-created before the delete batch is kept.
func TestReconcileOpsV3OrphanRecheckUnderLock(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	id := newRunID()
	row := OperationV2Row{ID: id, DefID: "test.orphan-recheck", Plugin: "test", Status: "queued", QueuedAt: now}
	if err := p.InsertOperationV2(row); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Delete(opv2OpKey(id), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	orphans, err := p.findOrphanRunsV3(context.Background())
	if err != nil || len(orphans) != 1 || orphans[0] != id {
		t.Fatalf("detection = %v, %v; want [%s]", orphans, err, id)
	}
	if err := p.InsertOperationV2(row); err != nil { // the live re-create
		t.Fatal(err)
	}
	undecodable := 0
	n, err := p.deleteOrphanRunsV3Batch(orphans, &undecodable)
	if err != nil || n != 0 {
		t.Fatalf("delete batch removed %d (%v); want 0: the row exists again", n, err)
	}
	assertRecordSynced(t, p, id)
	checkV3Consistency(t, p)
}

// TestReconcileOpsV3KeepsNeverMirroredRuns: a run created natively with the
// mirror off has no opv2 row and an empty V2RowHash. It never had a row to
// lose, so the orphan pass keeps it even when the reconcile runs with the
// switch reading "on" (as it does at every boot).
func TestReconcileOpsV3KeepsNeverMirroredRuns(t *testing.T) {
	p := newTimelineStore(t)
	p.setOpsV2MirrorEnabled(false)
	id := insertV3(t, p, "test.never-mirrored", "")
	p.setOpsV2MirrorEnabled(true)
	if row, _ := p.GetOperationV2(id); row != nil {
		t.Fatal("a mirror-off insert wrote an opv2 row")
	}
	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	if err != nil || rep.Orphans != 0 {
		t.Fatalf("reconcile = %+v, %v; want no orphan", rep, err)
	}
	if r := mustRun(t, p, id); r.State != state.V3Queued {
		t.Errorf("never-mirrored run is %s", r.State)
	}
}

// TestReconcileOpsV3SkipsUndecodableKeepsGoing: a stale run whose checkpoint
// does not decode and a stale run whose record does not decode are counted
// and left alone, and the stale run sorted after them is still rebuilt: one
// bad value must not fail the pass at the same id on every boot.
func TestReconcileOpsV3SkipsUndecodableKeepsGoing(t *testing.T) {
	p := newTimelineStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	ids := []string{idAt(base), idAt(base.Add(time.Second)), idAt(base.Add(2 * time.Second))}
	badCkpt, badRec, good := ids[0], ids[1], ids[2]
	for _, id := range ids {
		if err := p.InsertOperationV2(OperationV2Row{ID: id, DefID: "test.undecodable", Plugin: "test", Status: "queued", QueuedAt: base}); err != nil {
			t.Fatal(err)
		}
		row, _ := p.GetOperationV2(id)
		done := base.Add(time.Minute)
		row.Status, row.CompletedAt = "completed", &done
		rawSetOp(t, p, *row) // stale: a previous binary finished it
	}
	if err := p.UpsertOpStateV2(OpStateV2Row{OperationID: badCkpt, StateBlob: []byte(`{"cursor":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv3CkptKey(badCkpt), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opv3RunKey(badRec), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	if err != nil {
		t.Fatalf("reconcile failed on one bad value: %v", err)
	}
	if rep.Undecodable != 1 || rep.Rebuilt != 2 {
		t.Errorf("report = %+v; want 1 undecodable (the record), 2 rebuilt", rep)
	}
	if r := assertRecordSynced(t, p, good); r.State != state.V3Succeeded {
		t.Errorf("good run: %s", r.State)
	}
	if r := assertRecordSynced(t, p, badCkpt); r.State != state.V3Succeeded {
		t.Errorf("run with the bad checkpoint: %s", r.State)
	}
	if raw, closer, err := p.db.Get(opv3CkptKey(badCkpt)); err != nil || string(raw) != "{not json" {
		t.Errorf("the undecodable checkpoint was overwritten: %q, %v", raw, err)
	} else {
		_ = closer.Close()
	}
}
