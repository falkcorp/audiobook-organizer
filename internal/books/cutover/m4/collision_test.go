// file: internal/books/cutover/m4/collision_test.go
// version: 1.0.0
// guid: 6f1d93b2-0a58-4c7e-b2e4-91c35d8a70f6
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
)

// TestCutover_LogLinesOfSeveralBootsAllImported: HEAD numbers a log line from an
// in-memory counter that restarts at 1 every process start, so a run that logged
// across a restart has lines whose keys end in the same number (they differ in
// their nanosecond). Both boots' lines import, in time order, numbered 1..n: the
// import's page key is the number, and a repeat would replace the earlier line.
func TestCutover_LogLinesOfSeveralBootsAllImported(t *testing.T) {
	done := epoch.Add(time.Hour)
	spec := runSpec{id: runID(1), def: "dedup.book-scan", st: "succeeded", completed: new(done)}
	raw := map[string]string{}
	line := func(at time.Time, seq int, msg string) {
		raw[fmt.Sprintf("opv3:log:%s:%020d:%010d", spec.id, at.UnixNano(), seq)] = fmt.Sprintf(`{"at":%q,"level":"info","message":%q}`, at.Format(time.RFC3339Nano), msg)
	}
	line(epoch.Add(1*time.Second), 1, "boot one: Court of Owls opened")
	line(epoch.Add(2*time.Second), 2, "boot one: Talon found")
	line(epoch.Add(10*time.Minute), 1, "boot two: Court of Owls resumed")
	line(epoch.Add(11*time.Minute), 2, "boot two: Talon lost")
	line(epoch.Add(12*time.Minute), 3, "boot two: Court of Owls closed")
	r := newRig(t, withRuns(nil, spec), withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Quarantine) != 0 {
			t.Fatalf("quarantined %+v\n%s", s.Quarantine, r.logs)
		}
		got := logLines(t, ctx, s, spec.id)
		var msgs []string
		for i, l := range got {
			if l.Seq != uint64(i+1) {
				t.Errorf("line %d has sequence %d, want %d", i, l.Seq, i+1)
			}
			msgs = append(msgs, l.Log.Message)
		}
		want := []string{"boot one: Court of Owls opened", "boot one: Talon found", "boot two: Court of Owls resumed", "boot two: Talon lost", "boot two: Court of Owls closed"}
		if !reflect.DeepEqual(msgs, want) {
			t.Errorf("log = %q, want all five lines in time order %q", msgs, want)
		}
		rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(spec.id))
		if err != nil || rec.Seq.Log != 5 {
			t.Errorf("the run's log sequence = %d, %v; want 5", rec.Seq.Log, err)
		}
	})
}

// TestCutover_ItemWithTwoOutcomesKeepsBoth: HEAD's item rows are keyed by outcome
// and item, so one item can hold a failed row and a skipped one; the import's
// item page is keyed by the item. The outcome of the later chunk is the item's,
// the other rides whole as a head-item-superseded checkpoint, and nothing is
// quarantined.
func TestCutover_ItemWithTwoOutcomesKeepsBoth(t *testing.T) {
	done := epoch.Add(time.Hour)
	spec := runSpec{id: runID(1), def: "dedup.book-scan", st: "succeeded", completed: new(done)}
	item := func(outcome string, chunk int) string {
		return fmt.Sprintf(`{"key":"year-one","outcome":%q,"error":"mislabeled","chunk":%d}`, outcome, chunk)
	}
	raw := map[string]string{
		"opv3:item:" + spec.id + ":failed:year-one":          item("failed", 1),
		"opv3:item:" + spec.id + ":skipped:resumed:year-one": item("skipped:resumed", 2),
		"opv3:item:" + spec.id + ":failed:the-cult":          item("failed", 1),
	}
	raw["opv3:item:"+spec.id+":failed:the-cult"] = strings.ReplaceAll(item("failed", 1), "year-one", "the-cult")
	r := newRig(t, withRuns(nil, spec), withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Quarantine) != 0 {
			t.Fatalf("quarantined %+v\n%s", s.Quarantine, r.logs)
		}
		got := collect(t, func(y func(tasks.PageEntry, error) bool) {
			s.Stores.Runs.Pages(ctx, tasks.RunID(spec.id), tasks.PageQuery{Page: tasks.PageItems})(y)
		})
		byKey := map[string]string{}
		for _, p := range got {
			byKey[p.Item.Key] = p.Item.Outcome
		}
		if len(got) != 2 || byKey["year-one"] != "skipped:resumed" || byKey["the-cult"] != "failed" {
			t.Errorf("item pages = %v, want year-one skipped:resumed (chunk 2) and the-cult failed", byKey)
		}
		b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(spec.id), "head-item-superseded:failed:year-one")
		if err != nil || !ok || !strings.Contains(string(b), `"outcome":"failed"`) || !strings.Contains(string(b), `"chunk":1`) {
			t.Errorf("the failed outcome of year-one was not carried: %q, %v, %v", b, ok, err)
		}
		if !strings.Contains(r.logs.String(), "item outcomes carried as superseded") {
			t.Errorf("the carried outcome was not logged:\n%s", r.logs)
		}
		if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(spec.id), "head-item-superseded:failed:the-cult"); ok {
			t.Error("an item with one outcome carries a superseded checkpoint")
		}
	})
}

// TestCutover_DuplicatePageKeyQuarantinesTheRun: two transition rows that carry
// one sequence number would import to one page key, the second replacing the
// first without a trace. The run is quarantined whole with the duplicate reason
// and its rows stay in the quarantine, readable; its neighbour imports.
func TestCutover_DuplicatePageKeyQuarantinesTheRun(t *testing.T) {
	done := epoch.Add(time.Hour)
	dup := runSpec{id: runID(1), def: "dedup.book-scan", st: "succeeded", completed: new(done), evts: 2}
	ok := runSpec{id: runID(2), def: "dedup.book-scan", st: "succeeded", completed: new(done), evts: 2}
	raw := map[string]string{}
	// A third transition row whose body repeats sequence 2.
	at := epoch.Add(5 * time.Second)
	raw[fmt.Sprintf("opv3:evt:%s:%020d:%06d", dup.id, at.UnixNano(), 3)] = fmt.Sprintf(`{"seq":2,"from":"running","to":"succeeded","event":"finish","attempt":1,"fence_epoch":2,"at":%q}`, at.Format(time.RFC3339Nano))
	r := newRig(t, withRuns(nil, dup, ok), withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		const duplicate = "two rows of the run would import to the same page key, so one would silently replace the other"
		driver := false
		for _, q := range s.Quarantine {
			if !strings.Contains(q.Key, dup.id) {
				t.Errorf("a row of the other run was quarantined: %+v", q)
			}
			if q.Key == "opv3:run:"+dup.id {
				driver = q.Reason == duplicate
			}
		}
		// The driver row carries the run's reason; its other rows say they belong
		// to a quarantined run. Three transition rows, the driver and the progress
		// row.
		if len(s.Quarantine) != 5 || !driver {
			t.Errorf("quarantine = %+v, want the run's five rows set aside, the driver with the duplicate reason", s.Quarantine)
		}
		if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(dup.id)); err == nil {
			t.Error("the run with a duplicate page key was imported")
		}
		if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(ok.id)); err != nil {
			t.Errorf("the neighbouring run was not imported: %v", err)
		}
	})
}

// TestCutover_RunFieldsWithoutANativeHomeRideAlong: the v3 record's approval
// (with its note), the count of item outcomes HEAD refused past its cap, the
// pipeline stage, idempotency key, successor and the trace identifiers have no
// field on the native run record. They ride in one head-run-extras checkpoint so
// nothing HEAD stored about the run is dropped silently; a run with none carries
// no such checkpoint.
func TestCutover_RunFieldsWithoutANativeHomeRideAlong(t *testing.T) {
	done := epoch.Add(time.Hour)
	rich := runSpec{id: runID(1), def: "dedup.book-scan", st: "succeeded", completed: new(done), mutateRec: func(r *store.RunRecord) {
		r.Approval = &store.ApprovalRecord{ApprovedBy: store.Actor{UserID: "alfred"}, ApprovedAt: done, Decision: "approve", Note: "the Batcave may proceed"}
		r.ItemOutcomes = &store.ItemOutcomeCounts{Stored: 10, Dropped: 4}
		r.PipelineStage, r.IdemKey, r.SuccessorID, r.TraceID, r.SpanID = "plan", "idem-1", runID(9), "trace-1", "span-1"
	}}
	plain := runSpec{id: runID(2), def: "dedup.book-scan", st: "succeeded", completed: new(done), mutateRec: func(r *store.RunRecord) {
		r.ItemOutcomes = &store.ItemOutcomeCounts{Stored: 3} // nothing dropped: nothing to say
	}}
	r := newRig(t, withRuns(nil, rich, plain))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Quarantine) != 0 {
			t.Fatalf("quarantined %+v\n%s", s.Quarantine, r.logs)
		}
		b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(rich.id), "head-run-extras")
		if err != nil || !ok {
			t.Fatalf("head-run-extras = %q, %v, %v", b, ok, err)
		}
		for _, want := range []string{"the Batcave may proceed", `"dropped":4`, `"pipeline_stage":"plan"`, `"idem_key":"idem-1"`, `"successor_id":"` + runID(9) + `"`, `"trace_id":"trace-1"`, `"span_id":"span-1"`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("head-run-extras lacks %s: %s", want, b)
			}
		}
		if b, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(plain.id), "head-run-extras"); ok {
			t.Errorf("a run with nothing extra carries head-run-extras: %s", b)
		}
	})
}
