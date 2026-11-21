// file: internal/books/cutover/m4/cutover_test.go
// version: 1.1.0
// guid: 8e41b7d3-c960-4f25-a1e8-5d3f0b92c7a6
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

func collect[T any](t *testing.T, seq func(yield func(T, error) bool)) []T {
	t.Helper()
	var out []T
	for v, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestCutover_RunFamiliesImportedAndSourceEmptied: one run that owns a row in
// every run-scoped family lands in core's task store with the ID HEAD gave it,
// the kind the rename map gives its def, and every row it owned; and none of
// HEAD's rows is left.
func TestCutover_RunFamiliesImportedAndSourceEmptied(t *testing.T) {
	id := runID(1)
	done := epoch.Add(time.Hour)
	spec := runSpec{
		id: id, def: "dedup.book-scan", st: "succeeded", completed: new(done), started: new(epoch.Add(time.Minute)),
		trigger: "scheduled", priority: 2, evts: 3, intents: 2, items: 2, logs: 4,
		ckpt: true, ledger: true, snaps: 2, rows: 3, rowmarks: 2, marks: 2, derived: true,
	}
	r := newRig(t, withRuns(nil, spec))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 {
			t.Fatalf("%d source rows left: %v", len(s.Left), keysOf(s.Left))
		}
		if len(s.Quarantine) != 0 {
			t.Fatalf("quarantined %+v\n%s", s.Quarantine, r.logs)
		}
		rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(id))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind != "books.dedup.book-scan" || rec.State != tasks.StateSucceeded {
			t.Errorf("kind %q state %q", rec.Kind, rec.State)
		}
		if rec.Priority != tasks.PriorityHigh || rec.Trigger != tasks.TriggerSchedule || rec.Actor.UserID != "bruce.wayne" {
			t.Errorf("priority %v trigger %q actor %+v", rec.Priority, rec.Trigger, rec.Actor)
		}
		if !rec.Created.Equal(epoch) || !rec.Started.Equal(epoch.Add(time.Minute)) || !rec.Finished.Equal(done) {
			t.Errorf("created %v started %v finished %v", rec.Created, rec.Started, rec.Finished)
		}
		if rec.Done != 7 || rec.Total != 9 || rec.Current != "The Long Halloween" {
			t.Errorf("progress %d/%d %q", rec.Done, rec.Total, rec.Current)
		}
		counts := map[tasks.PageKind]int{}
		for _, pk := range []tasks.PageKind{tasks.PageEvents, tasks.PageIntents, tasks.PageItems, tasks.PageLog} {
			counts[pk] = len(collect(t, func(y func(tasks.PageEntry, error) bool) {
				s.Stores.Runs.Pages(ctx, tasks.RunID(id), tasks.PageQuery{Page: pk})(y)
			}))
		}
		want := map[tasks.PageKind]int{tasks.PageEvents: 3, tasks.PageIntents: 2, tasks.PageItems: 2, tasks.PageLog: 4}
		for k, n := range want {
			if counts[k] != n {
				t.Errorf("%v pages: %d, want %d", k, counts[k], n)
			}
		}
		if b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(id), "head"); err != nil || !ok || string(b) != `{"cursor":"Year One"}` {
			t.Errorf("head checkpoint = %q, %v, %v", b, ok, err)
		}
		if b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(id), "v1"); err != nil || !ok || string(b) != `{"v1":true}` {
			t.Errorf("v1 checkpoint = %q, %v, %v", b, ok, err)
		}
		rows := collect(t, func(y func(tasks.Row, error) bool) { s.Stores.Runs.Rows(ctx, tasks.RunID(id))(y) })
		if len(rows) != 3 || rows[0].Class != "safe" {
			t.Errorf("rows = %+v", rows)
		}
		marks := collect(t, func(y func(string, error) bool) { s.Stores.Runs.Marks(ctx, tasks.RunID(id))(y) })
		slices.Sort(marks)
		if len(marks) != 4 { // 2 rowmarks + 2 marks, distinct ids
			t.Errorf("marks = %v", marks)
		}
		if s.Verify.Read != 1 || s.Verify.Written != 1 || s.Verify.Quarantined != 0 || len(s.Verify.Failures) != 0 {
			t.Errorf("verify = %+v", s.Verify)
		}
	})
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestCutover_UndecodableRowQuarantined: a run whose record, or any row it
// owns, does not decode is set aside whole, with a fixed reason class (never a
// decoder's text, which quotes the value), beside runs that import.
func TestCutover_UndecodableRowQuarantined(t *testing.T) {
	good := runSpec{id: runID(1), def: "library.scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), evts: 1}
	badRun := runSpec{id: runID(2), def: "library.scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), evts: 2, corruptRun: true}
	badLog := runSpec{id: runID(3), def: "library.scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), evts: 1, logs: 2, corruptLog: true}
	noQueued := runSpec{id: runID(4), def: "library.scan", st: "queued", noQueued: true}
	noFinish := runSpec{id: runID(5), def: "library.scan", st: "succeeded"} // terminal, HEAD kept no CompletedAt
	badKind := runSpec{id: runID(6), def: "Not A Kind!", st: "queued"}
	badState := runSpec{id: runID(7), def: "library.scan", st: "hibernating"}
	badPrio := runSpec{id: runID(8), def: "library.scan", st: "queued", priority: 9}
	r := newRig(t, withRuns(nil, good, badRun, badLog, noQueued, noFinish, badKind, badState, badPrio),
		withRaw(map[string]string{"opv3:evt:" + runID(99) + ":00000000000000000001:000001": `{"v":1}`}))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 {
			t.Errorf("rows left in the source: %v", keysOf(s.Left))
		}
		if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(good.id)); err != nil {
			t.Errorf("the good run: %v", err)
		}
		quar := map[string]string{}
		for _, q := range s.Quarantine {
			quar[q.Key] = q.Reason
		}
		for _, bad := range []runSpec{badRun, badLog, noQueued, noFinish, badKind, badState, badPrio} {
			if _, ok := quar["opv3:run:"+bad.id]; !ok {
				t.Errorf("run %s was not quarantined", bad.id)
			}
			if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(bad.id)); err == nil {
				t.Errorf("run %s was imported", bad.id)
			}
		}
		// The rows of a quarantined run go with it.
		for k := range quar {
			if strings.HasPrefix(k, "opv3:evt:"+badRun.id) {
				goto evtFound
			}
		}
		t.Errorf("the trail of the quarantined run %s was not quarantined with it: %v", badRun.id, quar)
	evtFound:
		// An orphan, a row of no run record, is set aside by itself.
		var orphan bool
		for k := range quar {
			orphan = orphan || strings.HasPrefix(k, "opv3:evt:"+runID(99))
		}
		if !orphan {
			t.Errorf("the orphan row was not quarantined: %v", quar)
		}
		for k, why := range quar {
			if strings.Contains(why, "not a valid") || strings.Contains(why, "{") || strings.Contains(why, "hibernating") || strings.Contains(why, "Not A Kind") {
				t.Errorf("%s: reason %q quotes a value", k, why)
			}
		}
		if s.Verify.Quarantined == 0 || len(s.Verify.Failures) != 0 {
			t.Errorf("verify = %+v", s.Verify)
		}
	})
}

// TestCutover_ReconcileRunsOnceBeforeAnyRow: HEAD's every-boot repair is run
// exactly once per boot, however many rows and shards there are.
func TestCutover_ReconcileRunsOnceBeforeAnyRow(t *testing.T) {
	var specs []runSpec
	for i := 1; i <= 12; i++ {
		specs = append(specs, runSpec{id: runID(i), def: "library.scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), evts: 2})
	}
	r := newRig(t, withRuns(nil, specs...))
	if _, err := r.run(t); err != nil {
		t.Fatalf("%v\n%s", err, r.logs)
	}
	if n := r.reconcil.Load(); n != 1 {
		t.Errorf("reconcile ran %d times, want 1", n)
	}
}

// TestM4_VerifyFailsWhenARowIsLeft: Verify is the gate that no row of any
// source family is left. A step that leaves a row behind (here: one that does
// nothing) fails the boot rather than finishing with HEAD's rows still there.
func TestM4_VerifyFailsWhenARowIsLeft(t *testing.T) {
	r := newRig(t, withRuns(nil, runSpec{id: runID(1), def: "library.scan", st: "queued"}))
	dm := r.migration()
	dm.Step = func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil }
	s, err := r.runWith(t, r.seed(t), dm)
	if err == nil {
		t.Fatalf("a boot that left HEAD's rows finished: %+v", s)
	}
	if !strings.Contains(err.Error(), "still present") {
		t.Errorf("the boot failed, but not on Verify's finding: %v", err)
	}
}

// TestCutover_RowOfACommittedRunIsNotAnOrphan: a worker handed a run-owned row
// whose driver's chunk has committed since (the run record and the row are both
// gone from the live store, though the runner's iterator still holds the row)
// leaves it alone. It is not an orphan: the driver imported it.
func TestCutover_RowOfACommittedRunIsNotAnOrphan(t *testing.T) {
	id := runID(1)
	r := newRig(t, withRuns(nil, runSpec{id: id, def: "library.scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), evts: 2}), func(r *rig) {
		r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
		// Every row of the run sorts before its driver row except the run's own
		// logs-and-later families; the dependent path reads the run record and
		// then the row itself, and both reads see the commit.
		r.view = func(key string) ([]byte, bool, error) {
			return nil, strings.Contains(key, id), storage.ErrNotFound
		}
	})
	dm := r.migration()
	inner := dm.Step
	dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if strings.HasPrefix(string(kv.Key), "opv3:run:") {
			r.view = nil // the driver reads its own run with the live store
		}
		return inner(ctx, kv, w)
	}
	s, err := r.runWith(t, r.seed(t), dm)
	if err != nil {
		t.Fatalf("%v\n%s", err, r.logs)
	}
	for _, q := range s.Quarantine {
		if q.Reason == "the row belongs to no run record" {
			t.Errorf("a row of an imported run was quarantined as an orphan: %+v", q)
		}
	}
}

// TestCutover_DriverReadsItsRunAfterReconcile: HEAD's reconcile runs inside the
// first Step, after the runner's iterator took its snapshot, so the bytes the
// runner hands the driver can predate it. The driver imports what the store
// holds when it runs, not what the snapshot held.
func TestCutover_DriverReadsItsRunAfterReconcile(t *testing.T) {
	id := runID(1)
	done := epoch.Add(time.Hour)
	var repaired []byte
	r := newRig(t, withRuns(nil, runSpec{id: id, def: "library.scan", st: "queued"}), func(r *rig) {
		r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
		r.reconcileFn = func(ctx context.Context) error {
			// The v2 row says the op finished: reconcile rewrites the v3 record.
			repaired = []byte(`{"v":3,"id":"` + id + `","def_id":"library.scan","state":"failed","error":"mislabeled","queued_at":"` + epoch.Format(time.RFC3339) + `","completed_at":"` + done.Format(time.RFC3339) + `","priority":1}`)
			return nil
		}
		r.view = func(key string) ([]byte, bool, error) {
			if key == "opv3:run:"+id && repaired != nil {
				return repaired, true, nil
			}
			return nil, false, nil
		}
	})
	r.runObserve(t, func(ctx context.Context, s *snap) {
		rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(id))
		if err != nil {
			t.Fatal(err)
		}
		if rec.State != tasks.StateFailed || rec.Error != "mislabeled" || !rec.Finished.Equal(done) {
			t.Errorf("imported %q (error %q, finished %v): the driver used the snapshot's bytes, not the store's", rec.State, rec.Error, rec.Finished)
		}
	})
}

// TestCutover_RunRecordFullyMapped compares one imported run to the whole
// record it must become: the fields no other test asserts one by one (Result,
// Error, Parent, Attempt, FenceEpoch, the system actor, the repairs run-level
// rule's Mode and PlanRef, the counters).
func TestCutover_RunRecordFullyMapped(t *testing.T) {
	id, plan, parent := runID(1), runID(2), runID(3)
	done := epoch.Add(2 * time.Hour)
	params := `{"fixer_id":"two-face-asins","plan_op_id":"` + plan + `","dry_run":true}`
	r := newRig(t, withRuns(nil, runSpec{
		id: id, def: "repairs.apply", st: "failed", completed: new(done), started: new(epoch.Add(time.Minute)),
		mode: "preview", params: json.RawMessage(params), parent: parent, priority: 1, result: `{"merged":3}`, errText: "mislabeled",
		system: true, evts: 2, intents: 1, logs: 3,
	}))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		got, err := s.Stores.Runs.Get(ctx, tasks.RunID(id))
		if err != nil {
			t.Fatal(err)
		}
		want := tasks.RunRecord{
			ID: tasks.RunID(id), Kind: "books.repairs.two-face-asins", Mode: tasks.ModeLive, State: tasks.StateFailed,
			Actor: tasks.Actor{AuthMethod: "system", System: "books"}, Trigger: tasks.TriggerManual,
			Parent: tasks.RunID(parent), PlanRef: tasks.RunID(plan),
			Created: epoch, Started: epoch.Add(time.Minute), Finished: done, Done: 7, Total: 9, Current: "The Long Halloween",
			Priority: tasks.PriorityNormal, Params: json.RawMessage(params), Result: json.RawMessage(`{"merged":3}`), Error: "mislabeled",
			Attempt: 1, FenceEpoch: 2, Seq: tasks.RunSeqs{Event: 2, Intent: 1, Log: 3},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("imported record differs:\n got %+v\nwant %+v", got, want)
		}
	})
}

// TestCutover_RunModeFollowsTheRecord: a run keeps the mode HEAD stored for it
// (a preview run that carries no dry_run param stays a preview), and the two
// repairs defs override the stored mode: a plan is always a Preview, an apply
// always Live (05-tasks 5T.8.2 rule 2).
func TestCutover_RunModeFollowsTheRecord(t *testing.T) {
	done := epoch.Add(time.Hour)
	run := func(i int, def, mode string, params any) runSpec {
		return runSpec{id: runID(i), def: def, st: "succeeded", completed: new(done), mode: mode, params: params}
	}
	r := newRig(t, withRuns(nil,
		run(1, "dedup.book-scan", "preview", map[string]any{}),
		run(2, "dedup.book-scan", "live", map[string]any{"dry_run": true}),
		run(3, "repairs.plan", "live", map[string]any{"fixer_id": "hush"}),
		run(4, "repairs.apply", "preview", map[string]any{"fixer_id": "hush", "plan_op_id": runID(3)}),
	))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		for i, want := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive, tasks.ModePreview, tasks.ModeLive} {
			rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(i+1)))
			if err != nil {
				t.Fatalf("run %d: %v", i+1, err)
			}
			if rec.Mode != want {
				t.Errorf("run %d (%s): mode %q, want %q", i+1, rec.Kind, rec.Mode, want)
			}
		}
	})
}
