// file: internal/database/migration065_test.go
// version: 1.0.2
// guid: e5ee898b-db8a-404e-9646-fac05de9b33d
// last-edited: 2026-10-11

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

func TestMigration065MapsEveryV2Status(t *testing.T) {
	p := newTimelineStore(t)
	rows, want := everyV2StatusRows(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	rep, err := p.MigrateOpsV2ToV3(500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != len(rows) || rep.Written != len(rows) || rep.Unmapped["pending"] != 1 || len(rep.Unmapped) != 1 {
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

func TestMigration065TimeoutPrefix(t *testing.T) {
	p := newTimelineStore(t)
	q := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	c := q.Add(time.Hour)
	a, b := idAt(q), idAt(q.Add(time.Second))
	seedV2Rows(t, p, []OperationV2Row{
		v2Row(a, "test.t", "canceled", "timeout: run exceeded 1h0m0s", q, &c),
		v2Row(b, "test.t", "canceled", "canceled by user", q, &c),
	})
	if _, err := p.MigrateOpsV2ToV3(500, nil); err != nil {
		t.Fatal(err)
	}
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

func TestMigration065SuccessorHeuristic(t *testing.T) {
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
	rep, err := p.MigrateOpsV2ToV3(500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, p, old).SuccessorID; got != succ {
		t.Errorf("successor = %q, want %q", got, succ)
	}
	if got := mustRun(t, p, lonely).SuccessorID; got != "" {
		t.Errorf("a row with no run within 1 s got successor %q", got)
	}
	if rep.SuccessorsFilled != 1 || rep.SuccessorsMissing != 1 {
		t.Errorf("report: filled %d missing %d, want 1 and 1", rep.SuccessorsFilled, rep.SuccessorsMissing)
	}
	// The mirror of a dropped run with a successor is the v2 requeue row.
	if st, msg := state.ToV2(state.V3Dropped, "", "", true, false, ""); st != "interrupted_dropped" || msg != state.RequeuedMessage {
		t.Errorf("dropped{successor} mirrors as (%q, %q)", st, msg)
	}
}

func TestMigration065LeavesV2Untouched(t *testing.T) {
	p := newTimelineStore(t)
	rows, _ := everyV2StatusRows(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	if err := p.UpsertOpStateV2(OpStateV2Row{OperationID: rows[2].ID, StateBlob: []byte(`{"cursor":7}`)}); err != nil {
		t.Fatal(err)
	}
	before := v3Digest(t, p, "opv2:")
	if _, err := p.MigrateOpsV2ToV3(500, nil); err != nil {
		t.Fatal(err)
	}
	if v3Digest(t, p, "opv2:") != before {
		t.Fatal("migration 065 changed an opv2: key")
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

func TestMigration065Idempotent(t *testing.T) {
	p := newTimelineStore(t)
	rows, _ := everyV2StatusRows(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	if _, err := p.MigrateOpsV2ToV3(500, nil); err != nil {
		t.Fatal(err)
	}
	first := v3Digest(t, p, "opv2:", "opv3:")
	firstKeys := len(timelineKeys(t, p, "opv3:"))
	rep, err := p.MigrateOpsV2ToV3(500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Written != 0 || rep.Skipped != len(rows) {
		t.Errorf("second pass: %+v", rep)
	}
	if v3Digest(t, p, "opv2:", "opv3:") != first || len(timelineKeys(t, p, "opv3:")) != firstKeys {
		t.Fatal("a second migration pass changed the key set")
	}

	// A run the live façade wrote after the first pass is current with its
	// row (the write stamped V2RowHash): never re-derived. A row that changed
	// under an older binary is (TestReconcileOpsV3RepairsOldBinaryWritesOnReboot).
	now := time.Now().UTC()
	if err := p.UpdateOperationV2Status(rows[0].ID, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	rep, err = p.MigrateOpsV2ToV3(500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Written != 0 || rep.Skipped != len(rows) {
		t.Errorf("after a live write: %+v (the façade write must keep the record current)", rep)
	}
	if s := mustRun(t, p, rows[0].ID).State; s != state.V3Running {
		t.Errorf("the live write's state was overwritten by the migration: %s", s)
	}
	checkV3Consistency(t, p)
}

func TestMigration065Batches(t *testing.T) {
	p := newTimelineStore(t)
	base := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	rows := make([]OperationV2Row, 1250)
	for i := range rows {
		q := base.Add(time.Duration(i) * time.Second)
		c := q.Add(time.Millisecond)
		rows[i] = v2Row(idAt(q), fmt.Sprintf("test.batch-%d", i%7), "completed", "", q, &c)
	}
	seedV2Rows(t, p, rows)
	var calls []int
	rep, err := p.MigrateOpsV2ToV3(500, func(done, total int) {
		if total != 1250 {
			t.Errorf("progress total = %d", total)
		}
		calls = append(calls, done)
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Batches != 3 || fmt.Sprint(calls) != "[500 1000 1250]" {
		t.Fatalf("batches = %d, progress = %v; want 3 batches at 500/1000/1250", rep.Batches, calls)
	}
	c, err := p.OpCountsV3()
	if err != nil || c.Total != 1250 || c.ByState[state.V3Succeeded] != 1250 {
		t.Fatalf("counts = %+v %v", c, err)
	}
}

func TestMigration065DownKeepsKeys(t *testing.T) {
	p := newTimelineStore(t)
	rows, _ := everyV2StatusRows(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC))
	seedV2Rows(t, p, rows)
	if err := migration065Up(p); err != nil {
		t.Fatal(err)
	}
	if err := setVersion(p, 65); err != nil {
		t.Fatal(err)
	}
	before := v3Digest(t, p, "opv3:")
	if err := migration065Down(p); err != nil {
		t.Fatal(err)
	}
	if v, err := getCurrentVersion(p); err != nil || v != 64 {
		t.Fatalf("version after Down = %d, %v; want 64", v, err)
	}
	if v3Digest(t, p, "opv3:") != before || len(timelineKeys(t, p, opv3RunPrefix)) != len(rows) {
		t.Fatal("Down touched opv3: keys")
	}
	// Up again (a roll forward) reuses them: nothing is rewritten.
	if err := migration065Up(p); err != nil {
		t.Fatal(err)
	}
	if v3Digest(t, p, "opv3:") != before {
		t.Fatal("re-running Up rewrote opv3: keys")
	}
}

// TestMigration065Timing100k seeds 100,000 v2 rows and times the migration.
// Opt-in (OPSV3_MIGRATION_100K=1): it exists to produce the figure the
// 05-PR4 report asks for, not to gate CI.
func TestMigration065Timing100k(t *testing.T) {
	if os.Getenv("OPSV3_MIGRATION_100K") == "" {
		t.Skip("set OPSV3_MIGRATION_100K=1 to time migration 065 on 100,000 rows")
	}
	p := newTimelineStore(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	statuses := []string{"completed", "completed", "completed", "failed", "canceled", "interrupted_dropped", "interrupted_quiesced"}
	rows := make([]OperationV2Row, 100000)
	for i := range rows {
		q := base.Add(time.Duration(i) * 7 * time.Second)
		c := q.Add(3 * time.Second)
		rows[i] = v2Row(idAt(q), fmt.Sprintf("test.def-%02d", i%40), statuses[i%len(statuses)], "", q, &c)
		rows[i].ProgressCurrent, rows[i].ProgressTotal = i%500, 500
	}
	seedV2Rows(t, p, rows)
	start := time.Now()
	rep, err := p.MigrateOpsV2ToV3(opsV3MigrationBatch, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("migration 065 on %d rows: %s (%d batches, %d written)", len(rows), time.Since(start).Round(time.Millisecond), rep.Batches, rep.Written)
	start = time.Now()
	if _, err := p.MigrateOpsV2ToV3(opsV3MigrationBatch, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("second (idempotent) pass: %s", time.Since(start).Round(time.Millisecond))
	rep2, err := p.ReconcileOpsV3WithV2(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("boot reconcile, all %d runs current: %s (%d workers)", rep2.Checked, rep2.Duration.Round(time.Millisecond), opsV3ReconcileWorkers)
	for _, row := range rows[:1000] {
		row.Status = "failed"
		rawSetOp(t, p, row)
	}
	rep2, err = p.ReconcileOpsV3WithV2(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("boot reconcile, %d of %d runs stale: %s (%d rebuilt)", rep2.Stale, rep2.Checked, rep2.Duration.Round(time.Millisecond), rep2.Rebuilt)
}
