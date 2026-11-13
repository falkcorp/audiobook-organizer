// file: core/health/startup_test.go
// version: 1.0.0
// guid: 6d2e9b4c-1a85-4f73-b0c6-3e8a7d5f2b19
// last-edited: 2026-11-13

package health

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func okStep(context.Context) error { return nil }

func testSteps() []StartupStep {
	return []StartupStep{
		{Name: "openCave", Class: Fatal, Reason: "nothing to serve without the cave"},
		{Name: "searchindex", Class: Degraded, Token: "search_index", Reason: "search falls back"},
		{Name: "metricsInit", Class: Degraded, Token: "metrics_disabled", Reason: "the server runs without metrics"},
	}
}

// TestStartup_UnknownStepRefused: a name that is not a row is an error, never a
// silently unclassified step, and its function does not run (fail closed).
func TestStartup_UnknownStepRefused(t *testing.T) {
	su, err := NewStartup(nil, nil, testSteps()...)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	err = su.Run(t.Context(), "openCaves", func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, ErrUnknownStep) {
		t.Fatalf("Run of an unknown step = %v, want ErrUnknownStep", err)
	}
	if ran {
		t.Error("an unknown step's function ran")
	}
}

// TestStartup_NoRegistry: a table with no registry still classifies and runs,
// both outcomes of a Degraded step included.
func TestStartup_NoRegistry(t *testing.T) {
	su, err := NewStartup(nil, nil, testSteps()...)
	if err != nil {
		t.Fatal(err)
	}
	if err := su.Run(t.Context(), "searchindex", okStep); err != nil {
		t.Fatal(err)
	}
	if err := su.Run(t.Context(), "searchindex", func(context.Context) error { return errors.New("closed") }); err != nil {
		t.Fatal(err)
	}
	if got := su.Degraded(); !slices.Equal(got, []string{"search_index"}) {
		t.Errorf("Degraded = %v", got)
	}
	su.Complete()
}

func TestStartup_FatalReturnsErrorAndRecordsNoToken(t *testing.T) {
	r := NewRegistry()
	su, _ := NewStartup(r, nil, testSteps()...)
	boom := errors.New("the cave flooded")
	if err := su.Run(t.Context(), "openCave", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fatal step = %v, want %v", err, boom)
	}
	if len(su.Degraded()) != 0 || len(r.Snapshot().Startup) != 0 {
		t.Errorf("a fatal step recorded tokens: %v / %v", su.Degraded(), r.Snapshot().Startup)
	}
}

// TestStartup_DegradedRecordsTokenAndComponent: a failed Degraded step is
// logged, returns nil, records its fixed token, registers core.startup.<token>
// as Degraded (which readiness passes) and is shown in the Snapshot.
func TestStartup_DegradedRecordsTokenAndComponent(t *testing.T) {
	r := NewRegistry()
	su, _ := NewStartup(r, nil, testSteps()...)
	fail := func(context.Context) error { return errors.New("bleve closed") }
	if err := su.Run(t.Context(), "searchindex", fail); err != nil {
		t.Fatalf("degraded step returned %v, want nil", err)
	}
	if err := su.Run(t.Context(), "searchindex", fail); err != nil { // twice: one token
		t.Fatal(err)
	}
	_ = su.Run(t.Context(), "metricsInit", fail)
	_ = su.Run(t.Context(), "openCave", okStep)
	if got, want := su.Degraded(), []string{"metrics_disabled", "search_index"}; !slices.Equal(got, want) {
		t.Fatalf("Degraded = %v, want %v", got, want)
	}
	c := stateOf(r, "core.startup.search_index")
	if c.Result.Status != StatusDegraded || c.Result.Reason != "search_index" {
		t.Errorf("component = %+v, want Degraded search_index", c)
	}
	if got := r.Snapshot().Startup; !slices.Equal(got, []string{"metrics_disabled", "search_index"}) {
		t.Errorf("Snapshot.Startup = %v", got)
	}
	su.Complete()
	if ready, nr := r.Ready(); !ready {
		t.Errorf("degraded startup steps must not fail readiness: %v", nr)
	}
	if got := r.Snapshot().Startup; len(got) != 2 {
		t.Errorf("Complete dropped the degraded tokens: %v", got)
	}
}

func TestStartup_SuccessRecordsNothing(t *testing.T) {
	r := NewRegistry()
	su, _ := NewStartup(r, nil, testSteps()...)
	for _, st := range su.Steps() {
		if err := su.Run(t.Context(), st.Name, okStep); err != nil {
			t.Fatalf("%s: %v", st.Name, err)
		}
	}
	if len(su.Degraded()) != 0 || len(r.Snapshot().Components) != 0 {
		t.Errorf("a clean run recorded %v / %v", su.Degraded(), r.Snapshot().Components)
	}
}

// TestStartup_PendingUntilComplete: attaching a Startup makes the registry not
// ready with core.kernel:starting until Complete; a registry nothing attached
// to is unaffected.
func TestStartup_PendingUntilComplete(t *testing.T) {
	r := NewRegistry()
	if ready, _ := r.Ready(); !ready {
		t.Fatal("an empty registry must be ready")
	}
	su, _ := NewStartup(r, nil, testSteps()...)
	ready, nr := r.Ready()
	if ready || !slices.Equal(nr, []string{"core.kernel:starting"}) {
		t.Fatalf("Ready during startup = %v %v", ready, nr)
	}
	if r.Snapshot().Ready {
		t.Error("Snapshot.Ready true during startup")
	}
	su.Complete()
	if ready, nr := r.Ready(); !ready || len(nr) != 0 {
		t.Fatalf("Ready after Complete = %v %v", ready, nr)
	}
}

func TestStartup_TableValidation(t *testing.T) {
	cases := map[string][]StartupStep{
		"duplicate name":              {{Name: "a", Class: Fatal}, {Name: "a", Class: Fatal}},
		"fatal with a token":          {{Name: "a", Class: Fatal, Token: "oops"}},
		"degraded without a token":    {{Name: "a", Class: Degraded}},
		"degraded with a bad token":   {{Name: "a", Class: Degraded, Token: "Search Index"}},
		"degraded with a long token":  {{Name: "a", Class: Degraded, Token: strings.Repeat("x", 41)}},
		"no class":                    {{Name: "a"}},
		"empty name":                  {{Name: "", Class: Fatal}},
		"name with surrounding space": {{Name: " a", Class: Fatal}},
	}
	for name, steps := range cases {
		if _, err := NewStartup(nil, nil, steps...); err == nil {
			t.Errorf("%s: NewStartup accepted the table", name)
		}
	}
	su, err := NewStartup(nil, nil, testSteps()...)
	if err != nil {
		t.Fatal(err)
	}
	// Add is all-or-none: a bad row adds none of its batch.
	if err := su.Add(StartupStep{Name: "late", Class: Fatal}, StartupStep{Name: "openCave", Class: Fatal}); err == nil {
		t.Fatal("Add accepted a duplicate row")
	}
	if err := su.Run(t.Context(), "late", okStep); !errors.Is(err, ErrUnknownStep) {
		t.Errorf("a failed Add left %q in the table: %v", "late", err)
	}
	if err := su.Add(StartupStep{Name: "late", Class: Fatal}); err != nil {
		t.Fatal(err)
	}
	if err := su.Run(t.Context(), "late", okStep); err != nil {
		t.Errorf("Run of an added step = %v", err)
	}
}

// TestCoreStartupSteps_TableIsValid: the core rows build, and every Fatal row
// states its review reason.
func TestCoreStartupSteps_TableIsValid(t *testing.T) {
	su, err := NewStartup(nil, nil, CoreStartupSteps()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range su.Steps() {
		if st.Reason == "" {
			t.Errorf("core step %q has no review reason", st.Name)
		}
	}
}

// TestStartup_ExtendPerStepNeverTimer: EXTEND_TIMEOUT_USEC is sent once per
// ExtendStart call, each asking for StartupStepBudget, and never on a timer: an
// hour of fake time with no step sends nothing (a hung step must still be
// killed at its own budget), and nothing is sent without a supervisor.
func TestStartup_ExtendPerStepNeverTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &fakeNotifier{active: true}
		sup := NewSupervisor(nil, n)
		sup.ExtendStart("open the store")
		sup.ExtendStart("migration 4")
		sup.ExtendStart("open the index")
		time.Sleep(time.Hour)
		synctest.Wait()
		got := n.extensions()
		if len(got) != 3 {
			t.Fatalf("%d extensions after 3 steps and an idle hour, want exactly 3", len(got))
		}
		if StartupStepBudget != 10*time.Minute {
			t.Errorf("StartupStepBudget = %s, want HEAD's 10m", StartupStepBudget)
		}
		for _, d := range got {
			if d != StartupStepBudget {
				t.Errorf("extension = %s, want StartupStepBudget (10m)", d)
			}
		}

		idle := &fakeNotifier{active: false}
		NewSupervisor(nil, idle).ExtendStart("open the store")
		if len(idle.extensions()) != 0 {
			t.Error("extended the start timeout without a supervisor")
		}
		// A failed write is logged, never returned or fatal: ExtendStart has no result.
		n.failNext.Store(true)
		sup.ExtendStart("after a refusal")
	})
}

// TestSupervisor_ExtendStop: granted at most once per shutdown, capped by
// core.tasks.shutdown_extend_max, refused without a supervisor or a cap.
func TestSupervisor_ExtendStop(t *testing.T) {
	n := &fakeNotifier{active: true}
	sup := NewSupervisor(nil, n)
	if sup.ExtendStop(time.Minute) {
		t.Error("granted with no cap configured")
	}
	sup.SetStopExtendMax(func() time.Duration { return 30 * time.Second })
	if sup.ExtendStop(0) {
		t.Error("granted a zero extension")
	}
	if !sup.ExtendStop(time.Minute) {
		t.Fatal("first request refused")
	}
	if got := n.extensions(); len(got) != 1 || got[0] != 30*time.Second {
		t.Errorf("extensions = %v, want one capped at 30s", got)
	}
	if sup.ExtendStop(time.Second) {
		t.Error("granted a second extension in one shutdown")
	}

	idle := NewSupervisor(nil, &fakeNotifier{})
	idle.SetStopExtendMax(func() time.Duration { return time.Minute })
	if idle.ExtendStop(time.Second) {
		t.Error("granted without a supervisor")
	}
	var nilSup *Supervisor
	if nilSup.ExtendStop(time.Second) {
		t.Error("a nil Supervisor granted an extension")
	}
	nilSup.Ready()
	nilSup.Status("x")
	nilSup.Stopping()
	nilSup.ExtendStart("x")
	nilSup.WatchdogTick(time.Now())
}

// TestSupervisor_PassThroughs: READY, STATUS and STOPPING go to the notifier.
func TestSupervisor_PassThroughs(t *testing.T) {
	n := &fakeNotifier{active: true}
	sup := NewSupervisor(nil, n)
	sup.Ready()
	sup.Status("core.storage:warming")
	sup.Stopping()
	if got := n.sent(); !slices.Equal(got, []string{"READY", "STATUS=core.storage:warming", "STOPPING"}) {
		t.Errorf("sent = %v", got)
	}
}

// TestSupervisor_WatchdogTickPingsAtHalfThePeriod and stays silent without a unit watchdog.
func TestSupervisor_WatchdogTickPingsAtHalfThePeriod(t *testing.T) {
	n := &fakeNotifier{active: true, watchdog: 30 * time.Second}
	sup := NewSupervisor(nil, n)
	t0 := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{0, 5 * time.Second, 14 * time.Second, 15 * time.Second, 20 * time.Second, 30 * time.Second} {
		sup.WatchdogTick(t0.Add(off))
	}
	if got := n.pings.Load(); got != 3 { // at 0, 15 and 30
		t.Errorf("pings = %d, want 3 (every 15s)", got)
	}
	none := &fakeNotifier{active: true}
	NewSupervisor(nil, none).WatchdogTick(t0)
	if none.pings.Load() != 0 {
		t.Error("pinged a watchdog the unit did not enable")
	}
}
