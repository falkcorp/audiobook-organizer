// file: internal/books/store/pebble_store_ops_v3_project_test.go
// version: 1.3.1
// guid: e5ee898b-db8a-404e-9646-fac05de9b33d
// last-edited: 2026-11-09

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// seedV2Rows writes opv2:op: rows straight into the store, the way a binary
// that predates 05-PR4 left them: no opv3: keys at all. Rows are fictional.
func seedV2Rows(t testing.TB, p *PebbleStore, rows []OperationV2Row) {
	t.Helper()
	const chunk = 1000
	for start := 0; start < len(rows); start += chunk {
		b := p.db.NewBatch()
		for _, row := range rows[start:min(start+chunk, len(rows))] {
			data, err := json.Marshal(&row)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Set(opv2OpKey(row.ID), data, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
	}
}

func v2Row(id, def, status, msg string, queued time.Time, completed *time.Time) OperationV2Row {
	row := OperationV2Row{ID: id, DefID: def, Plugin: "test", Status: status, QueuedAt: queued, CompletedAt: completed, Params: `{}`}
	if msg != "" {
		row.ErrorMessage = &msg
	}
	return row
}

func idAt(t time.Time) string { return ulid.MustNew(ulid.Timestamp(t), ulid.DefaultEntropy()).String() }

// everyV2StatusRows is one row per row of the section 3 mapping table.
func everyV2StatusRows(base time.Time) ([]OperationV2Row, map[string]state.V3) {
	done := base.Add(time.Minute)
	cases := []struct {
		status, msg string
		want        state.V3
	}{
		{"queued", "", state.V3Queued},
		{"waiting_deps", "", state.V3Queued},
		{"running", "", state.V3Interrupted},
		{"completed", "", state.V3Succeeded},
		{"failed", "boom", state.V3Failed},
		{"canceled", "timeout: run exceeded 5m0s", state.V3TimedOut},
		{"canceled", "", state.V3Canceled},
		{"interrupted_quiesced", "", state.V3Interrupted},
		{"interrupted_ask", "awaiting user decision", state.V3AwaitingDecision},
		{"interrupted_dropped", state.RequeuedMessage, state.V3Dropped},
		{"interrupted_dropped", "force-dropped", state.V3Dropped},
		{"interrupted_restart", "", state.V3Dropped},
		{"interrupted", "", state.V3Interrupted},
		{"pending", "", state.V3Failed},
	}
	var rows []OperationV2Row
	want := map[string]state.V3{}
	for i, c := range cases {
		q := base.Add(time.Duration(i) * time.Hour)
		var comp *time.Time
		if c.status != "queued" && c.status != "waiting_deps" && c.status != "running" {
			d := done.Add(time.Duration(i) * time.Hour)
			comp = &d
		}
		id := idAt(q)
		rows = append(rows, v2Row(id, fmt.Sprintf("test.map-%02d", i), c.status, c.msg, q, comp))
		want[id] = c.want
	}
	return rows, want
}

// reconcileAll runs the boot reconcile (the production entry of the opv2 ->
// opv3 projection that migration 065 once drove over a whole store, before R3
// deleted it behind the upgrade floor) and fails the test on an error. A store
// seeded with opv2: rows and no opv3: records has every run stale, so one call
// builds every record.
func reconcileAll(t testing.TB, p *PebbleStore) OpsV3ReconcileReport {
	t.Helper()
	rep, err := p.ReconcileOpsV3WithV2(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestOpsV3ProjectionMapsEveryV2Status(t *testing.T) {
	p := newTimelineStore(t)
	rows, want := everyV2StatusRows(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	rep := reconcileAll(t, p)
	if rep.Checked != len(rows) || rep.Stale != len(rows) || rep.Rebuilt != len(rows) {
		t.Fatalf("report = %+v", rep)
	}
	for id, w := range want {
		r := mustRun(t, p, id)
		if r.State != w {
			t.Errorf("%s (%s): v3 %s, want %s", id, r.FromV2.Status, r.State, w)
		}
		if r.FromV2 == nil || r.V2RowHash == "" {
			t.Errorf("%s: no V2Origin", id)
		}
	}
	byStatus := map[string]*RunRecord{}
	for id := range want {
		r := mustRun(t, p, id)
		byStatus[r.FromV2.Status+"|"+r.FromV2.ErrMessage] = r
	}
	if r := byStatus["waiting_deps|"]; r.WaitReason != state.WaitDeps {
		t.Errorf("waiting_deps -> wait reason %q", r.WaitReason)
	}
	if r := byStatus["running|"]; r.InterruptedWhy != state.WhyCrash {
		t.Errorf("running -> interrupted why %q", r.InterruptedWhy)
	}
	if r := byStatus["interrupted|"]; r.InterruptedWhy != state.WhyLegacy {
		t.Errorf("interrupted -> why %q", r.InterruptedWhy)
	}
	if r := byStatus["pending|"]; r.UnmappedV2Status != "pending" {
		t.Errorf("unmapped status not kept: %+v", r)
	}
	evts, _ := p.ListRunEventsV3(rows[0].ID, 0)
	if len(evts) != 1 || evts[0].Event != string(state.EvMigrate) {
		t.Errorf("migrated run events = %+v, want one migrate event", evts)
	}
	checkV3Consistency(t, p)
}

func TestOpsV3ProjectionTimeoutPrefix(t *testing.T) {
	p := newTimelineStore(t)
	q := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	c := q.Add(time.Hour)
	a, b := idAt(q), idAt(q.Add(time.Second))
	seedV2Rows(t, p, []OperationV2Row{
		v2Row(a, "test.t", "canceled", "timeout: run exceeded 1h0m0s", q, &c),
		v2Row(b, "test.t", "canceled", "canceled by user", q, &c),
	})
	reconcileAll(t, p)
	if s := mustRun(t, p, a).State; s != state.V3TimedOut {
		t.Errorf("timeout row -> %s", s)
	}
	if s := mustRun(t, p, b).State; s != state.V3Canceled {
		t.Errorf("cancel row -> %s", s)
	}
	// The mirror of timed_out is the row v2 wrote.
	if got := v2StatusOf(mustRun(t, p, a), false); got != "canceled" {
		t.Errorf("timed_out mirrors as %q", got)
	}
}

func TestOpsV3ProjectionSuccessorHeuristic(t *testing.T) {
	p := newTimelineStore(t)
	base := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	retired := base.Add(time.Hour)
	old := idAt(base)
	succ := idAt(retired.Add(200 * time.Millisecond))
	otherDef := idAt(retired.Add(100 * time.Millisecond))
	lonely := idAt(base.Add(time.Minute))
	lonelyDone := base.Add(5 * time.Hour)
	tooLate := idAt(lonelyDone.Add(3 * time.Second))
	seedV2Rows(t, p, []OperationV2Row{
		v2Row(old, "test.requeue", "interrupted_dropped", state.RequeuedMessage, base, &retired),
		v2Row(succ, "test.requeue", "queued", "", retired.Add(200*time.Millisecond), nil),
		v2Row(otherDef, "test.other", "queued", "", retired.Add(100*time.Millisecond), nil),
		v2Row(lonely, "test.requeue", "interrupted_dropped", state.RequeuedMessage, base.Add(time.Minute), &lonelyDone),
		v2Row(tooLate, "test.requeue", "queued", "", lonelyDone.Add(3*time.Second), nil),
	})
	reconcileAll(t, p)
	if got := mustRun(t, p, old).SuccessorID; got != succ {
		t.Errorf("successor = %q, want %q", got, succ)
	}
	if got := mustRun(t, p, lonely).SuccessorID; got != "" {
		t.Errorf("a row with no run within 1 s got successor %q", got)
	}
	// The mirror of a dropped run with a successor is the v2 requeue row.
	if st, msg := state.ToV2(state.V3Dropped, "", "", true, false, ""); st != "interrupted_dropped" || msg != state.RequeuedMessage {
		t.Errorf("dropped{successor} mirrors as (%q, %q)", st, msg)
	}
}

func TestOpsV3ProjectionLeavesV2Untouched(t *testing.T) {
	p := newTimelineStore(t)
	rows, _ := everyV2StatusRows(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	if err := p.UpsertOpStateV2(OpStateV2Row{OperationID: rows[2].ID, StateBlob: []byte(`{"cursor":7}`)}); err != nil {
		t.Fatal(err)
	}
	before := v3Digest(t, p, "opv2:")
	reconcileAll(t, p)
	if v3Digest(t, p, "opv2:") != before {
		t.Fatal("the projection changed an opv2: key")
	}
	ck, err := p.GetCheckpointV3(rows[2].ID)
	if err != nil || ck == nil {
		t.Fatalf("no opv3:ckpt: for a run with opv2:state: (%v)", err)
	}
	var legacy OpStateV2Row
	if err := json.Unmarshal(ck.LegacyState, &legacy); err != nil || !strings.Contains(string(legacy.StateBlob), "cursor") {
		t.Fatalf("opv2:state: not copied byte for byte into legacy_state: %q (%v)", ck.LegacyState, err)
	}
}

func TestOpsV3ProjectionIdempotent(t *testing.T) {
	p := newTimelineStore(t)
	rows, _ := everyV2StatusRows(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	reconcileAll(t, p)
	first := v3Digest(t, p, "opv2:", "opv3:")
	firstKeys := len(timelineKeys(t, p, "opv3:"))
	rep := reconcileAll(t, p)
	if rep.Rebuilt != 0 || rep.Stale != 0 || rep.Checked != len(rows) {
		t.Errorf("second pass: %+v", rep)
	}
	if v3Digest(t, p, "opv2:", "opv3:") != first || len(timelineKeys(t, p, "opv3:")) != firstKeys {
		t.Fatal("a second pass changed the key set")
	}

	// A run the live façade wrote after the first pass is current with its
	// row (the write stamped V2RowHash): never re-derived. A row that changed
	// under an older binary is (TestReconcileOpsV3RepairsOldBinaryWritesOnReboot).
	now := time.Now().UTC()
	if err := p.UpdateOperationV2Status(rows[0].ID, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	rep = reconcileAll(t, p)
	if rep.Rebuilt != 0 || rep.Stale != 0 || rep.Checked != len(rows) {
		t.Errorf("after a live write: %+v (the façade write must keep the record current)", rep)
	}
	if s := mustRun(t, p, rows[0].ID).State; s != state.V3Running {
		t.Errorf("the live write's state was overwritten by the projection: %s", s)
	}
	checkV3Consistency(t, p)
}

// TestOpsV3ProjectionBatchesManyRows: 1250 rows are three repair batches, and
// the counts the batches maintain are exact.
func TestOpsV3ProjectionBatchesManyRows(t *testing.T) {
	p := newTimelineStore(t)
	base := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	rows := make([]OperationV2Row, 1250)
	for i := range rows {
		q := base.Add(time.Duration(i) * time.Second)
		c := q.Add(time.Millisecond)
		rows[i] = v2Row(idAt(q), fmt.Sprintf("test.batch-%d", i%7), "completed", "", q, &c)
	}
	seedV2Rows(t, p, rows)
	if rep := reconcileAll(t, p); rep.Rebuilt != len(rows) {
		t.Fatalf("report = %+v, want %d rebuilt", rep, len(rows))
	}
	c, err := p.OpCountsV3()
	if err != nil || c.Total != 1250 || c.ByState[state.V3Succeeded] != 1250 {
		t.Fatalf("counts = %+v %v", c, err)
	}
}

// TestOpsV3ProjectionRerunRewritesNothing: the projection run again over the
// records it built rewrites no opv3: key and keeps one run per opv2 row.
func TestOpsV3ProjectionRerunRewritesNothing(t *testing.T) {
	p := newTimelineStore(t)
	rows, _ := everyV2StatusRows(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	reconcileAll(t, p)
	before := v3Digest(t, p, "opv3:")
	if len(timelineKeys(t, p, opv3RunPrefix)) != len(rows) {
		t.Fatalf("the projection built %d runs for %d opv2 rows", len(timelineKeys(t, p, opv3RunPrefix)), len(rows))
	}
	reconcileAll(t, p)
	if v3Digest(t, p, "opv3:") != before {
		t.Fatal("running the projection again rewrote opv3: keys")
	}
}

// failOpsCommitN makes the nth ops batch commit of p (1-based, counting
// from now) fail, as a crash at that point would: nothing of that batch
// lands. The returned func clears the hook.
func failOpsCommitN(t testing.TB, p *PebbleStore, n int64) func() {
	t.Helper()
	var calls atomic.Int64
	boom := errors.New("injected crash")
	hook := func(s *PebbleStore) error {
		if s == p && calls.Add(1) == n {
			return boom
		}
		return nil
	}
	opsV2BeforeCommitHook.Store(&hook)
	clear := func() { opsV2BeforeCommitHook.Store(nil) }
	t.Cleanup(clear)
	return clear
}

// TestOpsV3ProjectionResumesAfterACrash: a pass that dies part-way (here at
// the second repair batch) leaves only whole batches behind, and the next boot's
// reconcile finishes the job: every run built once, one migrate event each,
// the requeued run's successor filled, counts exact. (A crash between the last
// batch and the successor fill, which migration 065's own driver could resume,
// is not resumable by the reconcile: the run it left without a successor is
// current by its hash, so the reconcile does not revisit it. That driver is
// deleted; the window is a single batch wide.)
func TestOpsV3ProjectionResumesAfterACrash(t *testing.T) {
	p := newTimelineStore(t)
	base := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	rows := make([]OperationV2Row, 1250)
	for i := range rows {
		q := base.Add(time.Duration(i) * time.Minute)
		c := q.Add(time.Second)
		rows[i] = v2Row(idAt(q), "test.resume", "completed", "", q, &c)
	}
	// The last two rows: a v2 requeue and its successor (sort last, so the
	// successor fill is the one batch after the main pass).
	retired := base.Add(2000 * time.Minute)
	old, succ := idAt(retired.Add(-time.Hour)), idAt(retired.Add(300*time.Millisecond))
	rows = append(rows,
		v2Row(old, "test.resume-requeue", "interrupted_dropped", state.RequeuedMessage, retired.Add(-time.Hour), &retired),
		v2Row(succ, "test.resume-requeue", "queued", "", retired.Add(300*time.Millisecond), nil))
	seedV2Rows(t, p, rows)

	clear := failOpsCommitN(t, p, 2) // die at the second repair batch
	if _, err := p.ReconcileOpsV3WithV2(context.Background()); err == nil {
		t.Fatal("the injected crash was not reported")
	}
	clear()
	if n := len(timelineKeys(t, p, opv3RunPrefix)); n != 500 {
		t.Fatalf("after a crash at batch 2: %d records, want exactly batch 1's 500", n)
	}

	rep := reconcileAll(t, p)
	if rep.Rebuilt != len(rows)-500 {
		t.Fatalf("resumed pass = %+v; want %d rebuilt", rep, len(rows)-500)
	}
	if got := mustRun(t, p, old).SuccessorID; got != succ {
		t.Errorf("successor = %q, want %q", got, succ)
	}
	if n := len(timelineKeys(t, p, opv3EvtPrefix)); n != len(rows) {
		t.Errorf("%d migrate events for %d runs; a resumed pass duplicated or lost some", n, len(rows))
	}
	c, err := p.OpCountsV3()
	if err != nil || c.Total != int64(len(rows)) {
		t.Errorf("counts = %+v, %v; want %d", c, err, len(rows))
	}
	checkV3Consistency(t, p)
}
