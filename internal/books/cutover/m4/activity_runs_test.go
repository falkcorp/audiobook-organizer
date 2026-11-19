// file: internal/books/cutover/m4/activity_runs_test.go
// version: 1.0.0
// guid: 2c8e5a17-b3d9-4f60-91a4-e7d03b6c5f82
// last-edited: 2026-11-18

package m4_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCutover_RepairsFeedRowsFollowTheirRun: through m4.Steps (the run families,
// then the activity rewrite after them), a feed row of a repairs run names the
// kind the run was imported as, books.repairs.<fixer_id>, in type, details.def_id
// and its def: tag, and its type index entry moves with it. A repairs row whose
// run was never imported (here, a run M4 quarantines) is held as stored: no row
// is left naming a def ID the task store has no kind for without being listed.
func TestCutover_RepairsFeedRowsFollowTheirRun(t *testing.T) {
	done := epoch.Add(time.Hour)
	plan := runSpec{id: runID(1), def: "repairs.plan", st: "succeeded", completed: tptr(done), params: map[string]any{"fixer_id": "hush"}}
	apply := runSpec{id: runID(2), def: "repairs.apply", st: "succeeded", completed: tptr(done), params: map[string]any{"fixer_id": "two-face-asins", "plan_op_id": runID(1)}}
	bad := runSpec{id: runID(3), def: "repairs.plan", st: "succeeded", completed: tptr(done), corruptRun: true}

	raw := map[string]string{}
	n := func(i int) string { return fmt.Sprintf("%020d", 1_700_000_000_000_000_000+int64(i)) }
	rows := []act{
		{"audit", n(1), "01HBAT0001", map[string]any{"type": "repairs.plan", "level": "info", "source": "maintenance", "summary": "planned", "operation_id": plan.id,
			"tags": []string{"operation", "def:repairs.plan"}, "details": map[string]any{"def_id": "repairs.plan", "op_name": "Repairs"}}},
		{"audit", n(2), "01HBAT0002", map[string]any{"type": "repairs.apply", "level": "info", "source": "maintenance", "summary": "applied", "operation_id": apply.id,
			"tags": []string{"operation", "def:repairs.apply"}, "details": map[string]any{"def_id": "repairs.apply"}}},
		{"audit", n(3), "01HBAT0003", map[string]any{"type": "repairs.plan", "level": "info", "source": "maintenance", "summary": "of a quarantined run", "operation_id": bad.id,
			"tags": []string{"def:repairs.plan"}, "details": map[string]any{"def_id": "repairs.plan"}}},
	}
	for _, a := range rows {
		seedAct(raw, a)
	}
	before := map[string]string{}
	for k, v := range raw {
		before[k] = v
	}
	r := newRig(t, withRuns(nil, plan, apply, bad), withRaw(raw))
	s := r.runSteps(t)

	for i, want := range []string{"books.repairs.hush", "books.repairs.two-face-asins"} {
		a := rows[i]
		got := decodeAct(t, s.Act[a.key()])
		if got["type"] != want || got["details"].(map[string]any)["def_id"] != want || got["tags"].([]any)[1] != "def:"+want {
			t.Errorf("%s: type %v, def_id %v, tags %v; want kind %s everywhere", a.key(), got["type"], got["details"].(map[string]any)["def_id"], got["tags"], want)
		}
		if got["operation_id"] != a.body["operation_id"] || got["summary"] != a.body["summary"] {
			t.Errorf("%s: another field changed: %v", a.key(), got)
		}
		if _, old := s.Act[a.typ(a.body["type"].(string))]; old {
			t.Errorf("%s: the type index entry under the def ID is still there", a.key())
		}
		if ref := s.Act[a.typ(want)]; ref != a.ref() {
			t.Errorf("%s: the type index entry under %s = %q, want %q", a.key(), want, ref, a.ref())
		}
	}
	// The quarantined run's row stays as stored, and is counted held.
	if k := rows[2].key(); s.Act[k] != before[k] || s.Act[rows[2].typ("repairs.plan")] != rows[2].ref() {
		t.Errorf("the row of a quarantined run changed: %s", s.Act[k])
	}
	if s.Verify.Held != 1 || s.Verify.Written != 2 {
		t.Errorf("verify = %+v, want 2 written and 1 held", s.Verify)
	}
	if !strings.Contains(r.logs.String(), "an activity row is held") {
		t.Errorf("the held row was not logged:\n%s", r.logs)
	}
	// Run 3 is the one M4 quarantined (its record does not decode).
	for _, e := range s.Quarantine {
		if !strings.Contains(e.Key, bad.id) {
			t.Errorf("an unexpected quarantine: %+v", e)
		}
	}
	if len(s.Quarantine) == 0 {
		t.Error("the run with an unreadable record was not quarantined")
	}
}
