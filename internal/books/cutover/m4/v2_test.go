// file: internal/books/cutover/m4/v2_test.go
// version: 1.2.0
// guid: 4a9c61d8-e307-4b25-8f1a-b2d05e73c9f6
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

func logLines(t *testing.T, ctx context.Context, s *snap, id string) []tasks.PageEntry {
	t.Helper()
	return collect(t, func(y func(tasks.PageEntry, error) bool) {
		s.Stores.Runs.Pages(ctx, tasks.RunID(id), tasks.PageQuery{Page: tasks.PageLog})(y)
	})
}

// TestCutover_V2MirrorFolded: a run HEAD wrote to both halves (the v3 record
// and the opv2: mirror) lands once. The mirror's log and error lines become the
// run's log history after the v3 lines, its resume state the v1 checkpoint when
// the checkpoint row did not carry one, its operation row and the indexes
// HEAD rebuilds from it are deleted, and no opv2: row is left.
func TestCutover_V2MirrorFolded(t *testing.T) {
	done := epoch.Add(time.Hour)
	withCkpt := runSpec{id: runID(1), def: "dedup.book-scan", st: "succeeded", completed: new(done), logs: 2, ckpt: true,
		v2op: true, v2state: true, v2logs: 3, v2errs: 2, v2derived: true}
	noCkpt := runSpec{id: runID(2), def: "maintenance.itunes-heal", st: "failed", completed: new(done), errText: "Joker laughed",
		v2op: true, v2state: true, v2logs: 2, v2derived: true}
	v2Only := runSpec{id: runID(3), def: "dedup.book-scan", st: "succeeded", completed: new(done), v2op: true, v2logs: 1}
	r := newRig(t, withRuns(nil, withCkpt, noCkpt, v2Only))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 || len(s.Quarantine) != 0 {
			t.Fatalf("left %v, quarantined %+v\n%s", keysOf(s.Left), s.Quarantine, r.logs)
		}

		// One page, numbered 1..n in merged time order (family breaks a tie as HEAD's
		// log cursor does: v2, then v3): the v3 and mirror lines interleave by the millisecond they
		// were written at, and the two errors (a second apart) follow them.
		got := logLines(t, ctx, s, withCkpt.id)
		var order []string
		for i, l := range got {
			if l.Seq != uint64(i+1) {
				t.Fatalf("log line %d has sequence %d, want %d", i, l.Seq, i+1)
			}
			order = append(order, l.Log.Message)
		}
		want := []string{"Arkham intake 1", "Knightfall fingerprinted", "Arkham intake 2", "Knightfall fingerprinted", "Arkham intake 3", "Scarecrow escaped 1", "Scarecrow escaped 2"}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("log lines %q, want %q", order, want)
		}
		v2first := got[0].Log
		if v2first.Level != "warn" || v2first.Fields["phase"] != "xml" || v2first.Fields["inmates"] != "12" {
			t.Errorf("first v2 line = %+v", v2first)
		}
		e1, e2 := got[5].Log, got[6].Log
		if e1.Level != "error" || e2.Level != "error" {
			t.Errorf("error lines = %+v / %+v", e1, e2)
		}
		if e1.Fields["error"] != "fear gas" || e1.Fields["plugin"] != "maintenance" || e1.Fields["def_id"] != "dedup.book-scan" {
			t.Errorf("error line fields = %v", e1.Fields)
		}
		rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(withCkpt.id))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Seq.Log != 7 {
			t.Errorf("the run's log sequence is %d, want 7", rec.Seq.Log)
		}

		// The checkpoint row's own LegacyState wins over the mirror's state row.
		if b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(withCkpt.id), "v1"); err != nil || !ok || string(b) != `{"v1":true}` {
			t.Errorf("v1 checkpoint of the run with a checkpoint row = %q, %v, %v", b, ok, err)
		}
		// Without one, the mirror's whole state row is the v1 checkpoint (the bytes
		// HEAD's own migration copies into LegacyState).
		b, ok, err := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(noCkpt.id), "v1")
		if err != nil || !ok || !strings.Contains(string(b), `"SchemaVersion":2`) || !strings.Contains(string(b), noCkpt.id) {
			t.Errorf("v1 checkpoint from opv2:state: = %q, %v, %v", b, ok, err)
		}
		if _, ok, _ := s.Stores.Runs.Checkpoint(ctx, tasks.RunID(v2Only.id), "v1"); ok {
			t.Error("a run with no state row has a v1 checkpoint")
		}
		// A mirror-only run's lines are numbered from 1 like any other.
		if got := logLines(t, ctx, s, v2Only.id); len(got) != 1 || got[0].Seq != 1 || got[0].Log.Message != "Arkham intake 1" {
			t.Errorf("v2-only run log = %+v", got)
		}
	})
}

// TestCutover_V2OnlyOperationQuarantined: a v2 operation row HEAD's reconcile
// could not turn into a v3 record (it did not decode, or the mirror is off and
// opv2: is no source of truth) has no run to belong to: it and the rows that
// name it are set aside with the orphan reason, never deleted unseen.
func TestCutover_V2OnlyOperationQuarantined(t *testing.T) {
	raw := map[string]string{
		"opv2:op:" + runID(9):    `{"ID":"` + runID(9) + `","Status":"completed"}`,
		"opv2:state:" + runID(9): `{"OperationID":"` + runID(9) + `"}`,
		"opv2:log:" + runID(9) + ":00000000000000000001:0000000001": `{"OperationID":"` + runID(9) + `","Level":"info","Message":"Ace chemicals"}`,
		"opv2:err:" + runID(9) + ":00000000000000000002":            `{"OperationID":"` + runID(9) + `","Message":"Gas leak"}`,
	}
	r := newRig(t, withRaw(raw))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if len(s.Left) != 0 {
			t.Fatalf("left %v", keysOf(s.Left))
		}
		got := map[string]string{}
		for _, q := range s.Quarantine {
			got[q.Key] = q.Reason
		}
		if len(got) != len(raw) {
			t.Fatalf("quarantined %v, want the %d v2 rows", got, len(raw))
		}
		for k := range raw {
			if !strings.Contains(got[k], "belongs to no run") {
				t.Errorf("%s: reason %q", k, got[k])
			}
		}
		if s.Verify.Quarantined != int64(len(raw)) {
			t.Errorf("report: %+v", s.Verify)
		}
	})
}

// TestCutover_V2OperationRowNotJSONQuarantined: the one v2 row of an otherwise
// good run that is not JSON is set aside by itself; the run still imports.
func TestCutover_V2OperationRowNotJSONQuarantined(t *testing.T) {
	spec := runSpec{id: runID(4), def: "dedup.book-scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), v2state: true}
	r := newRig(t, withRuns(nil, spec), withRaw(map[string]string{"opv2:op:" + runID(4): "{not json"}))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(spec.id)); err != nil {
			t.Fatalf("the run was not imported: %v", err)
		}
		if len(s.Quarantine) != 1 || s.Quarantine[0].Key != "opv2:op:"+spec.id || !strings.Contains(s.Quarantine[0].Reason, "not JSON") {
			t.Errorf("quarantine = %+v", s.Quarantine)
		}
		if len(s.Left) != 0 {
			t.Errorf("left %v", keysOf(s.Left))
		}
	})
}

// TestCutover_V2LineUndecodableQuarantinesRun: a half-imported log is not
// history, so a v2 line that does not decode sets the whole run aside.
func TestCutover_V2LineUndecodableQuarantinesRun(t *testing.T) {
	spec := runSpec{id: runID(5), def: "dedup.book-scan", st: "succeeded", completed: new(epoch.Add(time.Hour)), v2logs: 2}
	r := newRig(t, withRuns(nil, spec), withRaw(map[string]string{"opv2:err:" + runID(5) + ":00000000000000000007": "nope"}))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		if _, err := s.Stores.Runs.Get(ctx, tasks.RunID(spec.id)); err == nil {
			t.Fatal("the run was imported with an undecodable v2 line")
		}
		reasons := map[string]int{}
		for _, q := range s.Quarantine {
			reasons[q.Reason]++
		}
		if reasons["a v2 log or error line of the run does not decode"] != 1 || len(s.Left) != 0 {
			t.Errorf("reasons %v, left %v", reasons, keysOf(s.Left))
		}
	})
}
