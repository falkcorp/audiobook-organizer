// file: core/coretest/tasks.go
// version: 1.0.1
// guid: 3e7a1c58-9d04-4b6f-8a2e-5c1f0b9d7e43
// last-edited: 2026-11-11

package coretest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// runTimeout bounds RunTask's wait for a terminal state.
const runTimeout = 30 * time.Second

// WithRuns stores n queued runs of kind before the App boots (06 §6.4.8): the
// way a test starts an app over runs a module left behind. The kernel inserts
// them at P4c, before the tasks start rule looks for stored runs, so they
// start the tasks whether or not the module that owns kind is enabled. For a
// disabled module they are held: listed, never dispatched (05-tasks 5T.3.11).
// More than one call adds to the same App; kind is not checked against any
// registration, because the point is runs whose code is absent.
func WithRuns(kind tasks.Kind, n int) Option {
	return func(s *settings) {
		s.runs = append(s.runs, bootseam.TaskRunSeed{Kind: string(kind), N: n})
	}
}

// RunResult is a run's end as RunTask saw it (06 §6.4.8).
type RunResult struct {
	ID        tasks.RunID
	State     tasks.RunState
	Succeeded bool            // State is succeeded
	Result    json.RawMessage // the run's result document, as the task returned it
	Error     string          // the run's fixed error token and redacted text; "" on success
}

// RunTask starts one run of kind as the core administrator and waits for it to
// end: params is JSON-encoded (nil starts with none), the start is a Live one,
// and the wait is bounded (30 s). It fails the test when the App has no task
// executor (no registered kind and no stored run, the tasks start rule), the
// start is refused, or the run does not end in time. A run that ends failed
// is the result's: RunTask reports it, it does not fail the test, so a test
// can assert a task's own refusal.
//
// The administrator is the harness's, not a WithUser principal: R1 has no core
// authentication to resolve one (As says so), and a start through the Client
// needs a caller whose checker says what it may start. Its checker holds
// every permission, so Perms.Start never refuses here; a permission test uses
// AssertPermission when authentication lands (R2).
func (h *Harness) RunTask(t testing.TB, kind tasks.Kind, params any) RunResult {
	t.Helper()
	c := h.k.TaskClient()
	if c == nil {
		t.Fatalf("coretest: RunTask(%q): the App has no task executor: no module registered a kind and no run is stored (05-tasks 5T.4 step 1)", kind)
		return RunResult{}
	}
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatalf("coretest: RunTask(%q): params: %v", kind, err)
			return RunResult{}
		}
		raw = b
	}
	ctx, cancel := context.WithTimeout(admin(t.Context()), runTimeout)
	defer cancel()
	ref, err := c.Start(ctx, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: raw, Trigger: tasks.TriggerManual})
	if err != nil {
		t.Fatalf("coretest: RunTask(%q): start: %v", kind, err)
		return RunResult{}
	}
	for {
		v, err := c.Get(ctx, ref.ID)
		if err != nil {
			t.Fatalf("coretest: RunTask(%q): read run %s: %v", kind, ref.ID, err)
			return RunResult{}
		}
		if v.State.Terminal() {
			return h.result(ctx, t, v)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("coretest: RunTask(%q): run %s is still %s after %s", kind, ref.ID, v.State, runTimeout)
			return RunResult{}
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// result is the RunResult of a terminal run: the client's view carries the
// state, and the stored record the result document and the error text.
func (h *Harness) result(ctx context.Context, t testing.TB, v tasks.RunView) RunResult {
	t.Helper()
	out := RunResult{ID: v.ID, State: v.State, Succeeded: v.State == tasks.StateSucceeded}
	rec, err := h.k.TaskRecord(ctx, v.ID)
	if err != nil {
		t.Fatalf("coretest: read the record of run %s: %v", v.ID, err)
		return out
	}
	out.Result, out.Error = rec.Result, rec.Error
	return out
}

// TaskRecord is the stored record of run id, result and error text included,
// which the Client's read model leaves out. It applies no permission rule and
// no redaction: a test seam, never reachable from a request or a module.
func (h *Harness) TaskRecord(t testing.TB, id tasks.RunID) tasks.RunRecord {
	t.Helper()
	rec, err := h.k.TaskRecord(admin(t.Context()), id)
	if err != nil {
		t.Fatalf("coretest: TaskRecord(%s): %v", id, err)
	}
	return rec
}

// Runs lists the runs of kinds (every kind when none is named) newest first, as
// the core administrator sees them: held ones carry RunView.Held. It is the
// non-HTTP form of GET /api/core/v1/tasks/runs, which is not mounted before the
// router carries it (F2-16's notes), and fails the test when the App has no
// task executor.
func (h *Harness) Runs(t testing.TB, kinds ...tasks.Kind) []tasks.RunView {
	t.Helper()
	c := h.k.TaskClient()
	if c == nil {
		t.Fatalf("coretest: Runs: the App has no task executor (05-tasks 5T.4 step 1)")
		return nil
	}
	var out []tasks.RunView
	for v, err := range c.Runs(admin(t.Context()), tasks.RunQuery{Kinds: kinds, Limit: 1000}) {
		if err != nil {
			t.Fatalf("coretest: Runs: %v", err)
			return nil
		}
		out = append(out, v)
	}
	return out
}

// assertHeldRuns is AssertModuleInert's held-runs cell (06 §6.4.8): a disabled
// module's persisted runs, those of every kind its TaskLedger lists, are
// listed as held, and none was dispatched: each is still queued and was never
// started. It is a no-op for an App with no task executor (no stored run to
// hold).
func assertHeldRuns(t testing.TB, h *Harness, id module.ID, man module.Manifest) {
	t.Helper()
	if h.k.TaskClient() == nil || len(man.TaskLedger) == 0 {
		return
	}
	for _, v := range h.Runs(t, man.TaskLedger...) {
		switch {
		case !v.Held:
			t.Errorf("coretest: AssertModuleInert(%q): run %s of %s is listed, but not as held", id, v.ID, v.Kind)
		case v.State != tasks.StateQueued || !v.Started.IsZero():
			t.Errorf("coretest: AssertModuleInert(%q): run %s of %s is %s (started %v): a held run is never dispatched", id, v.ID, v.Kind, v.State, v.Started)
		}
	}
}

// admin is ctx carrying the harness administrator: a session principal whose
// checker holds every permission. An absent identity would be read as a
// kernel-internal call and skip the executor's permission checks, which is not
// what a caller of the Client is.
func admin(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{Method: auth.MethodSession, Perms: adminChecker{}})
}

// adminChecker holds every permission and satisfies every requirement.
type adminChecker struct{}

func (adminChecker) Has(rbac.Permission) bool        { return true }
func (adminChecker) Satisfies(rbac.Requirement) bool { return true }
func (adminChecker) Confined() bool                  { return false }
func (adminChecker) Concrete() []rbac.Permission     { return []rbac.Permission{rbac.CoreAdmin} }
