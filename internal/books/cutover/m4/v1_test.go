// file: internal/books/cutover/m4/v1_test.go
// version: 1.0.0
// guid: d27b5e81-3c94-4a60-9f18-6e0a41c7b952
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
)

// v1Op is one v1 operation and the rows HEAD keeps for it.
type v1Op struct {
	id, typ, status string
	user            string
	created         time.Time
	started, done   *time.Time
	folder          string
	errText, result string
	logs, results   int
	summary, state  bool
	journal         bool
	badLog          bool
}

func str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// put writes the operation's rows into raw, as HEAD's store keys them.
func (o v1Op) put(raw map[string]string) {
	mk := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return string(b)
	}
	if o.created.IsZero() {
		o.created = epoch
	}
	raw["operation:"+o.id] = mk(store.Operation{
		ID: o.id, UserID: o.user, Type: o.typ, Status: o.status, Progress: 7, Total: 9, Message: "Knightfall",
		FolderPath: str(o.folder), CreatedAt: o.created, StartedAt: o.started, CompletedAt: o.done, ErrorMessage: str(o.errText), ResultData: str(o.result),
	})
	for i := 1; i <= o.logs; i++ {
		at := o.created.Add(time.Duration(i) * time.Second)
		key := fmt.Sprintf("operationlog:%s:%d:%d", o.id, at.UnixNano(), i)
		if o.badLog && i == 1 {
			raw[key] = "not json"
			continue
		}
		raw[key] = mk(store.OperationLog{ID: i, OperationID: o.id, Level: "info", Message: fmt.Sprintf("Year One part %d", i), Details: str(`{"phase":"scan"}`), CreatedAt: at})
	}
	for i := 1; i <= o.results; i++ {
		book := fmt.Sprintf("book-%d", i)
		raw["op_result:"+o.id+":"+book] = mk(store.OperationResult{ID: i, OperationID: o.id, BookID: book, ResultJSON: `{"title":"Hush"}`, Status: "matched", CreatedAt: o.created})
	}
	if o.summary {
		raw["opsummary:"+o.id] = mk(store.OperationSummaryLog{ID: o.id, Type: o.typ, Status: o.status, CreatedAt: o.created, UpdatedAt: o.created})
	}
	if o.state {
		raw["opstate:"+o.id] = `{"operation_id":"` + o.id + `","phase":"scan","phase_index":2,"phase_total":5}`
	}
	if o.journal {
		raw["opstate:"+o.id+":params"] = `{"chunk":4}`
	}
}

func collectLog(ctx context.Context, t *testing.T, s *snap, id string) []tasks.PageEntry {
	return collect(t, func(y func(tasks.PageEntry, error) bool) {
		s.Stores.Runs.Pages(ctx, tasks.RunID(id), tasks.PageQuery{Page: tasks.PageLog})(y)
	})
}

// TestCutover_V1HistoryImported: a v1 operation with no v3 record is imported
// as a terminal run (5T.8.3): the kind the rename map gives its type text (rule
// 5's retired kind for a type it never knew), the state its status means, the
// log lines as the run's log page, the folder in the params, the result and
// the error as recorded, and the per-book results and the summary carried
// whole; an operation that never finished is interrupted_dropped, finished at
// its last recorded activity; a finished operation's checkpoint is deleted
// with it. Nothing of any v1 family is left.
func TestCutover_V1HistoryImported(t *testing.T) {
	done := epoch.Add(time.Hour)
	last := epoch.Add(4 * time.Second)
	late := epoch.Add(30 * time.Minute) // a start after every log line: the operation's last sign of life
	ops := []v1Op{
		{id: runID(1), typ: "dedup.book-scan", status: "completed", user: "bruce.wayne", done: &done, folder: "/Gotham/Arkham", result: `{"scanned":12}`, logs: 3, results: 2, summary: true, state: true},
		{id: runID(2), typ: "legacy.tidy", status: "failed", done: &done, errText: "mislabeled", logs: 1},
		{id: runID(3), typ: "dedup.book-scan", status: "canceled", done: &done},
		{id: runID(4), typ: "dedup.book-scan", status: "running", logs: 4, state: true},
		{id: runID(5), typ: "dedup.book-scan", status: "timed_out", done: &done, result: "plain text"},
		{id: runID(6), typ: "dedup.book-scan", status: "cancelled", done: &done},
		{id: runID(7), typ: "dedup.book-scan", status: "queued", logs: 1, started: &late},
	}
	raw := map[string]string{}
	for _, o := range ops {
		o.put(raw)
	}
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 || len(s.Quarantine) != 0 {
			t.Fatalf("left %v, quarantined %+v\n%s", keysOf(s.Left), s.Quarantine, r.logs)
		}
		get := func(i int) tasks.RunRecord {
			rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(i)))
			if err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
			return rec
		}
		a := get(1)
		if a.Kind != "books.dedup.book-scan" || a.State != tasks.StateSucceeded || a.Actor.UserID != "bruce.wayne" || !a.Finished.Equal(done) || !a.Created.Equal(epoch) {
			t.Errorf("run 1 = %+v", a)
		}
		if a.Done != 7 || a.Total != 9 || string(a.Params) != `{"folder_path":"/Gotham/Arkham"}` || string(a.Result) != `{"scanned":12}` {
			t.Errorf("run 1 progress %d/%d params %s result %s", a.Done, a.Total, a.Params, a.Result)
		}
		if a.Mode != tasks.ModeLive || a.Trigger != tasks.TriggerManual || a.Priority != tasks.PriorityNormal {
			t.Errorf("run 1 mode %q trigger %q priority %v", a.Mode, a.Trigger, a.Priority)
		}
		logs := collectLog(ctx, t, s, runID(1))
		if len(logs) != 3 || logs[0].Log.Message != "Year One part 1" || logs[0].Log.Fields["phase"] != "scan" || logs[2].Seq != 3 {
			t.Errorf("run 1 log = %+v", logs)
		}
		for name, want := range map[string]string{
			"head-v1-result:book-1": "Hush",
			"head-v1-result:book-2": "Hush",
			"head-v1-summary":       "dedup.book-scan",
		} {
			b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(1)), name)
			if err != nil || !ok || !contains(string(b), want) {
				t.Errorf("checkpoint %q = %q, %v, %v; want it to carry %q", name, b, ok, err, want)
			}
		}
		if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(1)), "v1"); ok {
			t.Error("a finished operation kept its v1 checkpoint")
		}

		b := get(2)
		if b.Kind != "books.retired.legacy.tidy" || b.State != tasks.StateFailed || b.Error != "mislabeled" || b.Actor.System != "books" {
			t.Errorf("run 2 = %+v", b)
		}
		if c := get(3); c.State != tasks.StateCanceled {
			t.Errorf("run 3 state %q", c.State)
		}
		d := get(4)
		if d.State != tasks.StateInterruptedDropped || d.Reason != "v1_unfinished" || !d.Finished.Equal(last) {
			t.Errorf("run 4 = state %q reason %q finished %v, want interrupted_dropped, v1_unfinished, %v", d.State, d.Reason, d.Finished, last)
		}
		if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(4)), "v1"); ok {
			t.Error("a dropped operation kept its v1 checkpoint: it has nothing to resume")
		}
		if f := get(6); f.State != tasks.StateCanceled {
			t.Errorf("run 6 (the British spelling) state %q", f.State)
		}
		if g := get(7); g.State != tasks.StateInterruptedDropped || !g.Finished.Equal(late) {
			t.Errorf("run 7 = state %q finished %v, want interrupted_dropped finished at its start %v", g.State, g.Finished, late)
		}
		if e := get(5); e.State != tasks.StateTimedOut || string(e.Result) != `"plain text"` {
			t.Errorf("run 5 state %q result %s", e.State, e.Result)
		}
		if s.Verify.Written != 7 || len(s.Verify.Failures) != 0 {
			t.Errorf("verify = %+v", s.Verify)
		}
	})
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestCutover_V1RowsFoldIntoTheV3Run: an operation HEAD also has a v3 record for
// is imported once, from the v3 record. Its v1 log lines follow the v3 lines on
// the log page, its v1 checkpoint is the open run's checkpoint named v1 (over
// the copy the v3 checkpoint row staged), its results and summary are carried,
// and its v1 operation row is deleted with it.
func TestCutover_V1RowsFoldIntoTheV3Run(t *testing.T) {
	raw := map[string]string{}
	v1Op{id: runID(1), typ: "dedup.book-scan", status: "running", logs: 2, results: 1, summary: true, state: true}.put(raw)
	spec := runSpec{id: runID(1), def: "dedup.book-scan", st: "running", logs: 2, ckpt: true}
	r := newRig(t, withRuns(nil, spec), withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 || len(s.Quarantine) != 0 {
			t.Fatalf("left %v, quarantined %+v\n%s", keysOf(s.Left), s.Quarantine, r.logs)
		}
		if n := r.imp.runCalls(tasks.RunID(runID(1))); n != 1 {
			t.Errorf("the importer saw the run %d times, want 1", n)
		}
		rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(1)))
		if err != nil || rec.State != tasks.StateInterrupted {
			t.Fatalf("run = %+v, %v", rec, err)
		}
		logs := collectLog(ctx, t, s, runID(1))
		if len(logs) != 4 || logs[2].Log.Message != "Year One part 1" || logs[3].Seq <= logs[1].Seq {
			t.Errorf("log = %+v, want the two v3 lines then the two v1 lines", logs)
		}
		b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(1)), "v1")
		if err != nil || !ok || !contains(string(b), `"phase_index":2`) {
			t.Errorf("v1 checkpoint = %q, %v, %v: want HEAD's opstate row, not the staged copy", b, ok, err)
		}
		for _, name := range []string{"head-v1-result:book-1", "head-v1-summary", "head-v1-operation"} {
			if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(1)), name); !ok {
				t.Errorf("checkpoint %q is missing", name)
			}
		}
	})
}

// TestCutover_V1RowsSetAside: what cannot be imported is quarantined, never
// dropped: an operation row that does not decode, one with a status the table
// does not list, a terminal one with no completion time, one with a log line
// that does not decode (the whole operation, with its rows), log lines of no
// operation, and the chunk journal opstate:<id>:params (a live record), which
// is set aside beside the run it belongs to while the run itself imports.
func TestCutover_V1RowsSetAside(t *testing.T) {
	raw := map[string]string{
		"operation:" + runID(1):                               "{not json",
		"operationlog:" + runID(1) + ":0000000000000000001:1": `{"id":1}`,
		"operationlog:" + runID(9) + ":0000000000000000001:1": `{"id":1}`, // no operation
		"op_result:" + runID(9) + ":book-1":                   `{}`,       // no operation
	}
	v1Op{id: runID(2), typ: "dedup.book-scan", status: "exploded", logs: 1}.put(raw)
	v1Op{id: runID(3), typ: "dedup.book-scan", status: "completed"}.put(raw) // no completion time
	v1Op{id: runID(4), typ: "dedup.book-scan", status: "completed", done: tptr(epoch.Add(time.Hour)), logs: 2, badLog: true}.put(raw)
	v1Op{id: runID(5), typ: "dedup.book-scan", status: "completed", done: tptr(epoch.Add(time.Hour)), journal: true}.put(raw)
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		want := map[string]bool{
			"operation:" + runID(1): true, "operationlog:" + runID(1) + ":0000000000000000001:1": true,
			"operation:" + runID(2): true, "operationlog:" + runID(9) + ":0000000000000000001:1": true,
			"op_result:" + runID(9) + ":book-1": true, "operation:" + runID(3): true, "operation:" + runID(4): true,
			"opstate:" + runID(5) + ":params": true,
		}
		got := map[string]bool{}
		for _, q := range s.Quarantine {
			got[q.Key] = true
		}
		for k := range want {
			if !got[k] {
				t.Errorf("%s is not in the quarantine", k)
			}
		}
		for k := range got {
			if !want[k] && !(len(k) > len("operationlog:") && k[:len("operationlog:")] == "operationlog:") {
				t.Errorf("%s was quarantined unexpectedly", k)
			}
		}
		for i := 1; i <= 4; i++ {
			if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(i))); err == nil {
				t.Errorf("run %d was imported", i)
			}
		}
		if rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(5))); err != nil || rec.State != tasks.StateSucceeded {
			t.Errorf("run 5 (a live journal beside it) = %+v, %v", rec, err)
		}
		// Quarantined rows leave the source (the runner moves them), so nothing is left behind.
		if len(s.Left) != 0 {
			t.Errorf("left %v", keysOf(s.Left))
		}
	})
}

// TestCutover_V1OperationRowsSetAside: an operation row whose own ID is not the
// ID in its key, one whose type is no kind, and a v1 operation whose ID is also
// a v2 operation row with no v3 record (the v2 row is an orphan; the v1 row
// must not import over it and delete it unread) are all quarantined whole.
func TestCutover_V1OperationRowsSetAside(t *testing.T) {
	done := tptr(epoch.Add(time.Hour))
	raw := map[string]string{}
	v1Op{id: runID(1), typ: "dedup.book-scan", status: "completed", done: done}.put(raw)
	raw["operation:"+runID(1)] = strings.Replace(raw["operation:"+runID(1)], `"id":"`+runID(1)+`"`, `"id":"`+runID(9)+`"`, 1)
	v1Op{id: runID(2), typ: "Not A Kind!", status: "completed", done: done}.put(raw)
	v1Op{id: runID(3), typ: "dedup.book-scan", status: "completed", done: done, logs: 1}.put(raw)
	raw["opv2:op:"+runID(3)] = `{"ID":"` + runID(3) + `"}`
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		got := map[string]bool{}
		for _, q := range s.Quarantine {
			got[q.Key] = true
		}
		for _, k := range []string{"operation:" + runID(1), "operation:" + runID(2), "operation:" + runID(3), "opv2:op:" + runID(3)} {
			if !got[k] {
				t.Errorf("%s is not in the quarantine: %+v", k, s.Quarantine)
			}
		}
		for i := 1; i <= 3; i++ {
			if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(i))); err == nil {
				t.Errorf("run %d was imported", i)
			}
		}
	})
}

// TestCutover_V1CheckpointOfAFinishedRunDeleted: a finished v3 run's v1
// checkpoint (opstate:<id>) has nothing to resume, so it is deleted with the
// run and never becomes the run's v1 checkpoint; an open run's becomes it.
func TestCutover_V1CheckpointOfAFinishedRunDeleted(t *testing.T) {
	raw := map[string]string{}
	v1Op{id: runID(1), typ: "dedup.book-scan", status: "completed", state: true}.put(raw)
	v1Op{id: runID(2), typ: "dedup.book-scan", status: "running", state: true}.put(raw)
	specs := []runSpec{
		{id: runID(1), def: "dedup.book-scan", st: "succeeded", completed: tptr(epoch.Add(time.Hour))},
		{id: runID(2), def: "dedup.book-scan", st: "running"},
	}
	r := newRig(t, withRuns(nil, specs...), withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(1)), "v1"); ok {
			t.Error("a finished run kept the v1 checkpoint")
		}
		if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(runID(2)), "v1"); !ok {
			t.Error("an open run lost its v1 checkpoint")
		}
		if len(s.Left) != 0 {
			t.Errorf("left %v", keysOf(s.Left))
		}
	})
}
