// file: internal/plugins/maintenance/dedup_triage_apply_test.go
// version: 1.0.0
// guid: a05e1939-0f66-42d7-9393-97dffc9064bd
// last-edited: 2026-07-18

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// triageDeps wraps fakeDeps and overrides HasDedupEngine (true, so
// runDedupExactTriage doesn't short-circuit) and DedupTriageExactPending, so
// tests can capture the apply argument the op actually passed through from
// its JSON params and control the returned TriageReport.
type triageDeps struct {
	fakeDeps
	report      *TriageReport
	err         error
	gotApply    bool
	applyCalled bool
}

func (d *triageDeps) HasDedupEngine() bool { return true }

func (d *triageDeps) DedupTriageExactPending(_ context.Context, apply bool) (*TriageReport, error) {
	d.applyCalled = true
	d.gotApply = apply
	if d.err != nil {
		return nil, d.err
	}
	if d.report != nil {
		return d.report, nil
	}
	return &TriageReport{Populations: map[TriageClass]TriagePopulation{}}, nil
}

func newTriagePlugin(deps *triageDeps) *Plugin {
	deps.fakeDeps = fakeDeps{}
	return &Plugin{deps: deps}
}

// TestRunDedupExactTriage_NilParams_DefaultsApplyFalse proves the preserved
// contract: no params (nil rawParams, the shape every existing scheduled/
// manual enqueue of this op already uses) must default to apply=false —
// report-only, same as before this op grew an apply path.
func TestRunDedupExactTriage_NilParams_DefaultsApplyFalse(t *testing.T) {
	deps := &triageDeps{}
	p := newTriagePlugin(deps)
	reporter := &fakeReporter{}

	err := p.runDedupExactTriage(context.Background(), nil, reporter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deps.applyCalled {
		t.Fatal("DedupTriageExactPending was never called")
	}
	if deps.gotApply {
		t.Error("nil params must default apply=false")
	}
}

// TestRunDedupExactTriage_EmptyObjectParams_DefaultsApplyFalse covers the
// `{}` params shape (as opposed to nil) — same default-false contract.
func TestRunDedupExactTriage_EmptyObjectParams_DefaultsApplyFalse(t *testing.T) {
	deps := &triageDeps{}
	p := newTriagePlugin(deps)
	reporter := &fakeReporter{}

	err := p.runDedupExactTriage(context.Background(), json.RawMessage(`{}`), reporter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.gotApply {
		t.Error("{} params must default apply=false")
	}
}

// TestRunDedupExactTriage_ApplyTrueParam_PassedThrough proves {"apply":true}
// reaches DedupTriageExactPending unchanged.
func TestRunDedupExactTriage_ApplyTrueParam_PassedThrough(t *testing.T) {
	deps := &triageDeps{}
	p := newTriagePlugin(deps)
	reporter := &fakeReporter{}

	err := p.runDedupExactTriage(context.Background(), json.RawMessage(`{"apply":true}`), reporter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deps.gotApply {
		t.Error(`{"apply":true} must pass apply=true through to DedupTriageExactPending`)
	}
}

// TestRunDedupExactTriage_ApplyFalseParam_Explicit proves an explicit
// {"apply":false} behaves identically to the default (belt-and-suspenders on
// the report-only contract).
func TestRunDedupExactTriage_ApplyFalseParam_Explicit(t *testing.T) {
	deps := &triageDeps{}
	p := newTriagePlugin(deps)
	reporter := &fakeReporter{}

	err := p.runDedupExactTriage(context.Background(), json.RawMessage(`{"apply":false}`), reporter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deps.gotApply {
		t.Error(`{"apply":false} must pass apply=false through`)
	}
}

// TestRunDedupExactTriage_InvalidParamsJSON_ReturnsError proves malformed
// params fail loudly instead of silently defaulting.
func TestRunDedupExactTriage_InvalidParamsJSON_ReturnsError(t *testing.T) {
	deps := &triageDeps{}
	p := newTriagePlugin(deps)
	reporter := &fakeReporter{}

	err := p.runDedupExactTriage(context.Background(), json.RawMessage(`not json`), reporter)
	if err == nil {
		t.Fatal("expected an error for malformed params JSON")
	}
	if deps.applyCalled {
		t.Error("DedupTriageExactPending must not be called when param parsing fails")
	}
}

// TestRunDedupExactTriage_LogsDismissedCountInSummary proves the completion
// summary line surfaces DismissedCount so an operator watching op logs (e.g.
// the sandbox purge-wave run) can see how many candidates were actually
// dismissed, not just how many were purgeable.
func TestRunDedupExactTriage_LogsDismissedCountInSummary(t *testing.T) {
	deps := &triageDeps{
		report: &TriageReport{
			TotalScanned:   9,
			PurgeableCount: 7,
			KeepCount:      1,
			ReviewCount:    1,
			DismissedCount: 7,
			Populations:    map[TriageClass]TriagePopulation{},
		},
	}
	p := newTriagePlugin(deps)
	reporter := &fakeReporter{}

	if err := p.runDedupExactTriage(context.Background(), json.RawMessage(`{"apply":true}`), reporter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var found bool
	for _, line := range reporter.logs {
		if strings.Contains(line, "dismissed=7") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a summary log line containing 'dismissed=7', got: %v", reporter.logs)
	}
}

// TestDedupExactTriageDef_DescriptionMentionsApply locks in that the op's
// self-description tells operators about the apply=true purge path, not just
// the original dry-run-only text.
func TestDedupExactTriageDef_DescriptionMentionsApply(t *testing.T) {
	p := &Plugin{}
	def := p.dedupExactTriageDef()
	if !strings.Contains(def.Description, "apply=true") {
		t.Errorf("Description must mention apply=true, got: %q", def.Description)
	}
	if !strings.Contains(def.Description, "dismiss") {
		t.Errorf("Description must mention dismissing purgeable candidates, got: %q", def.Description)
	}
}
