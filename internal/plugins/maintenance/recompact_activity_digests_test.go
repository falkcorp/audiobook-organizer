// file: internal/plugins/maintenance/recompact_activity_digests_test.go
// version: 1.0.0
// guid: 1c7e5a93-6b2d-4f08-9e4a-3d8b7c1f5e26
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// recompactDeps overrides RecompactActivityDigests with a canned result and
// optional error, and records whether it was called.
type recompactDeps struct {
	fakeDeps
	called bool
	res    database.RecompactResult
	err    error
}

func (d *recompactDeps) RecompactActivityDigests(_ context.Context) (database.RecompactResult, error) {
	d.called = true
	return d.res, d.err
}

func TestRecompactActivityDigests_PersistsCountersAndLogs(t *testing.T) {
	deps := &recompactDeps{res: database.RecompactResult{Touched: 4, Skipped: 9}}
	p := New(deps)
	rep := &resultReporter{}

	if err := p.runRecompactActivityDigests(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !deps.called {
		t.Fatal("the dep was never called")
	}
	res, ok := rep.result.(RecompactActivityDigestsResult)
	if !ok {
		t.Fatalf("persisted result is %T, want RecompactActivityDigestsResult", rep.result)
	}
	if res.Touched != 4 || res.Skipped != 9 {
		t.Errorf("persisted result = %+v, want Touched=4 Skipped=9", res)
	}
	joined := strings.Join(rep.logs, "\n")
	if !strings.Contains(joined, "Recompacted 4 digest(s), 9 already current") {
		t.Errorf("log missing the completion line; got:\n%s", joined)
	}
	// Start and end liveness stamps, so a run that finishes in milliseconds
	// is never recorded as never_reported.
	if len(rep.progress) < 2 {
		t.Errorf("UpdateProgress calls = %d (%v), want start and end", len(rep.progress), rep.progress)
	}
}

func TestRecompactActivityDigests_FailureIsSurfacedWithPartialCounters(t *testing.T) {
	deps := &recompactDeps{res: database.RecompactResult{Touched: 2}, err: errors.New("disk full")}
	p := New(deps)
	rep := &resultReporter{}

	err := p.runRecompactActivityDigests(context.Background(), nil, rep)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("run error = %v, want the backend failure surfaced", err)
	}
	res, ok := rep.result.(RecompactActivityDigestsResult)
	if !ok {
		t.Fatalf("result not persisted on failure: %T", rep.result)
	}
	if res.Touched != 2 {
		t.Errorf("partial work lost on failure: %+v", res)
	}
	if joined := strings.Join(rep.logs, "\n"); !strings.Contains(joined, "FAILED after recompacting 2 digest(s)") {
		t.Errorf("log missing the failure line; got:\n%s", joined)
	}
}

// TestRecompactActivityDigests_DefIsRegistered pins the wiring: the handler
// enqueues by RecompactActivityDigestsDefID, so a def that is not registered
// turns every admin request into a 500 with no op behind it.
func TestRecompactActivityDigests_DefIsRegistered(t *testing.T) {
	p := New(fakeDeps{})
	def := p.recompactActivityDigestsDef()
	if def.ID != RecompactActivityDigestsDefID {
		t.Fatalf("def id = %q, want %q", def.ID, RecompactActivityDigestsDefID)
	}
	if def.ConcurrencyKey != p.cleanupActivityLogDef().ConcurrencyKey {
		t.Errorf("concurrency key = %q, want the nightly cleanup's so the two never rewrite the same digest at once", def.ConcurrencyKey)
	}
	reg := &phantomCaptureRegistry{}
	if err := p.Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, id := range reg.ids {
		if id == RecompactActivityDigestsDefID {
			return
		}
	}
	t.Errorf("Register did not register %q; got %v", RecompactActivityDigestsDefID, reg.ids)
}
