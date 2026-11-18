// file: internal/books/cutover/m4/cutover_test.go
// version: 1.0.0
// guid: 8e41b7d3-c960-4f25-a1e8-5d3f0b92c7a6
// last-edited: 2026-11-18

package m4_test

import (
	"context"
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
		id: id, def: "dedup.book-scan", st: "succeeded", completed: tptr(done), started: tptr(epoch.Add(time.Minute)),
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
	good := runSpec{id: runID(1), def: "library.scan", st: "succeeded", completed: tptr(epoch.Add(time.Hour)), evts: 1}
	badRun := runSpec{id: runID(2), def: "library.scan", st: "succeeded", completed: tptr(epoch.Add(time.Hour)), evts: 2, corruptRun: true}
	badLog := runSpec{id: runID(3), def: "library.scan", st: "succeeded", completed: tptr(epoch.Add(time.Hour)), evts: 1, logs: 2, corruptLog: true}
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
		specs = append(specs, runSpec{id: runID(i), def: "library.scan", st: "succeeded", completed: tptr(epoch.Add(time.Hour)), evts: 2})
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
