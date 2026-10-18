// file: internal/plugins/maintenance/vacuum_activity_db_test.go
// version: 1.0.0
// guid: e66fcc21-9f1b-4362-9273-3dbe2ea1a5be
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// vacuumDeps records how the op called VacuumActivityDB.
type vacuumDeps struct {
	fakeDeps
	calls  []bool // dryRun of each call
	report database.ActivityVacuumReport
	err    error
}

func (d *vacuumDeps) VacuumActivityDB(_ context.Context, dryRun bool) (database.ActivityVacuumReport, error) {
	d.calls = append(d.calls, dryRun)
	r := d.report
	r.DryRun = dryRun
	return r, d.err
}

func TestVacuumActivityDB_DefShape(t *testing.T) {
	def := (&Plugin{}).vacuumActivityDBDef()
	if def.ID != "maintenance.vacuum-activity-db" {
		t.Fatalf("def id = %q", def.ID)
	}
	if def.Schedule != nil {
		t.Error("a VACUUM must never be scheduled")
	}
	if want := (&Plugin{}).compactActivityLogDef().ConcurrencyKey; def.ConcurrencyKey != want {
		t.Errorf("ConcurrencyKey = %q, want the activity-maintenance key %q", def.ConcurrencyKey, want)
	}
	if want := (&Plugin{}).activityReclaimDef().ConcurrencyKey; def.ConcurrencyKey != want {
		t.Errorf("ConcurrencyKey = %q, want the reclaim key %q", def.ConcurrencyKey, want)
	}
	if len(def.Permissions) != 0 {
		t.Errorf("Permissions = %v; an undeclared set means settings.manage, like the sibling activity ops", def.Permissions)
	}
}

// Omitted mode, an empty object and null params are all a preview; only an
// explicit dry_run=false (either spelling) is live.
func TestVacuumActivityDB_ModeResolution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		params  string
		wantDry bool
	}{
		{"no params", "", true},
		{"empty object", "{}", true},
		{"explicit true", `{"dry_run":true}`, true},
		{"explicit false", `{"dry_run":false}`, false},
		{"camel false", `{"dryRun":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := &vacuumDeps{report: database.ActivityVacuumReport{Supported: true}}
			rep := &resultReporter{}
			var raw json.RawMessage
			if tc.params != "" {
				raw = json.RawMessage(tc.params)
			}
			if err := New(deps).runVacuumActivityDB(context.Background(), raw, rep); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(deps.calls) != 1 || deps.calls[0] != tc.wantDry {
				t.Errorf("dryRun passed to the dep = %v, want [%v]", deps.calls, tc.wantDry)
			}
		})
	}
}

func TestVacuumActivityDB_DisagreeingFlagsAndBadParamsNeverRun(t *testing.T) {
	for _, params := range []string{`{"dry_run":false,"dryRun":true}`, `{"dry_run":"nope"}`, `{`} {
		deps := &vacuumDeps{}
		err := New(deps).runVacuumActivityDB(context.Background(), json.RawMessage(params), &resultReporter{})
		if err == nil {
			t.Errorf("params %s: want an error", params)
		}
		if len(deps.calls) != 0 {
			t.Errorf("params %s reached the store: %v", params, deps.calls)
		}
	}
}

func TestVacuumActivityDB_PreviewReportsSizesAndPersistsResult(t *testing.T) {
	before := database.ActivityVacuumStats{FileBytes: 8192, PageSize: 4096, PageCount: 2, FreelistCount: 1, ReclaimableBytes: 4096}
	deps := &vacuumDeps{report: database.ActivityVacuumReport{Supported: true, Before: before}}
	rep := &resultReporter{}

	if err := New(deps).runVacuumActivityDB(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, ok := rep.result.(VacuumActivityDBResult)
	if !ok {
		t.Fatalf("persisted result is %T", rep.result)
	}
	if !res.DryRun || res.Before != before || res.BytesReclaimed != 0 {
		t.Errorf("result = %+v", res)
	}
	joined := strings.Join(rep.logs, "\n")
	for _, want := range []string{"PREVIEW", "8192 bytes", "1 of 2 pages are free", "4096 bytes reclaimable", "dry_run=false"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
}

func TestVacuumActivityDB_LiveReportsReclaimedAndFailureKeepsReport(t *testing.T) {
	deps := &vacuumDeps{report: database.ActivityVacuumReport{
		Supported: true, BytesReclaimed: 4096,
		Before: database.ActivityVacuumStats{FileBytes: 8192},
		After:  database.ActivityVacuumStats{FileBytes: 4096},
	}}
	rep := &resultReporter{}
	if err := New(deps).runVacuumActivityDB(context.Background(), json.RawMessage(`{"dry_run":false}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if joined := strings.Join(rep.logs, "\n"); !strings.Contains(joined, "8192 -> 4096 bytes, 4096 bytes reclaimed") {
		t.Errorf("log missing the reclaimed line:\n%s", joined)
	}

	deps = &vacuumDeps{report: deps.report, err: errors.New("space still held")}
	rep = &resultReporter{}
	err := New(deps).runVacuumActivityDB(context.Background(), json.RawMessage(`{"dry_run":false}`), rep)
	if err == nil || !strings.Contains(err.Error(), "space still held") {
		t.Fatalf("run error = %v, want the vacuum failure surfaced", err)
	}
	if _, ok := rep.result.(VacuumActivityDBResult); !ok {
		t.Errorf("result not persisted on failure: %T", rep.result)
	}
}

func TestVacuumActivityDB_UnsupportedBackendIsNotAnError(t *testing.T) {
	rep := &resultReporter{}
	if err := New(&vacuumDeps{}).runVacuumActivityDB(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if joined := strings.Join(rep.logs, "\n"); !strings.Contains(joined, "nothing to vacuum") {
		t.Errorf("log should say there was nothing to vacuum:\n%s", joined)
	}
}
