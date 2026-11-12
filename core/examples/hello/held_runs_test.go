// file: core/examples/hello/held_runs_test.go
// version: 1.0.0
// guid: 4f8b2d61-7a39-4c05-b1e7-9d3a6c0e5f28
// last-edited: 2026-11-11

package hello_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/examples/hello"
	"github.com/falkcorp/mediacore/tasks"
)

// count is the kind hello registers.
const count tasks.Kind = "hello.count"

// settle is how long a held run is watched for a dispatch that must not
// happen: several of the executor's 100 ms dispatch ticks.
const settle = 600 * time.Millisecond

// awaitRuns polls until every run of count is in state, or fails the test.
func awaitRuns(t *testing.T, h *coretest.Harness, n int, state tasks.RunState) []tasks.RunView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		runs := h.Runs(t, count)
		ok := len(runs) == n
		for _, v := range runs {
			ok = ok && v.State == state
		}
		if ok {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d runs of %s in state %s, have %+v", n, count, state, runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDisabledModuleWithRunsBoots (06 §6.10; 05-tasks 5T.4 step 1): hello is
// disabled and its runs are stored. The boot succeeds, the runs are what start
// tasks (no kind is registered while hello is disabled), they are listed held
// and none is dispatched, and re-enabling hello resumes them. The runs are
// listed through the Client: GET /api/core/v1/tasks/runs is not mounted until
// the router can carry it (F2-16's notes). There is no op-ledger line: HEAD's
// op ledger has no F2 equivalent.
func TestDisabledModuleWithRunsBoots(t *testing.T) {
	h := coretest.New(t, coretest.WithModules(hello.New()), coretest.WithDisabled(hello.ID), coretest.WithRuns(count, 3))
	if h.App().TaskClient() == nil {
		t.Fatal("three stored runs did not start the tasks")
	}
	time.Sleep(settle)
	for _, v := range h.Runs(t, count) {
		if !v.Held || v.State != tasks.StateQueued || !v.Started.IsZero() {
			t.Errorf("run %s: held %v, %s, started %v; want held, queued, never started", v.ID, v.Held, v.State, v.Started)
		}
	}
	if got := len(h.Runs(t, count)); got != 3 {
		t.Fatalf("%d runs listed, want 3", got)
	}
	coretest.AssertModuleInert(t, h, hello.ID)

	// Re-enabling hello resumes them: the same store, hello enabled.
	h2 := h.Reboot(t, coretest.WithModules(hello.New()))
	for _, v := range awaitRuns(t, h2, 3, tasks.StateSucceeded) {
		if v.Held {
			t.Errorf("run %s is still held after hello was enabled", v.ID)
		}
	}
}

// TestHeld_KindsKnownWhileDisabled (05-tasks 5T.9, COR-13): with hello
// disabled its kinds are known from its manifest's TaskLedger, so its runs
// list as held. They are not treated as unknown kinds, which the executor
// drops: after a restart they are still queued, not interrupted_dropped.
func TestHeld_KindsKnownWhileDisabled(t *testing.T) {
	h := coretest.New(t, coretest.WithModules(hello.New()), coretest.WithDisabled(hello.ID), coretest.WithRuns(count, 2))
	time.Sleep(settle)
	h2 := h.Reboot(t, coretest.WithModules(hello.New()), coretest.WithDisabled(hello.ID))
	time.Sleep(settle)
	runs := h2.Runs(t, count)
	if len(runs) != 2 {
		t.Fatalf("%d runs after a restart, want 2: %+v", len(runs), runs)
	}
	for _, v := range runs {
		if !v.Held || v.State != tasks.StateQueued {
			t.Errorf("run %s after a restart: held %v, state %s; want held, queued (a known kind of a disabled module)", v.ID, v.Held, v.State)
		}
	}
}

// TestHelloCount_RunsOnCoretest: hello.count runs on a coretest App whose only
// module is hello (06 §6.12.6's line), and the run it leaves is the stored
// round trip of 05-tasks 5T.9's TestNativeStores_RoundTrip.
func TestHelloCount_RunsOnCoretest(t *testing.T) {
	h := coretest.New(t, coretest.WithModules(hello.New()))
	res := h.RunTask(t, count, hello.CountParams{})
	if !res.Succeeded || res.State != tasks.StateSucceeded {
		t.Fatalf("hello.count ended %s (%q), want succeeded", res.State, res.Error)
	}
	var got hello.CountResult
	if err := json.Unmarshal(res.Result, &got); err != nil {
		t.Fatalf("result %s: %v", res.Result, err)
	}
	// The seed migration wrote one row, name/world = 1.
	if want := (hello.CountResult{Names: 1, Greetings: 1}); got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}

	t.Run("TestNativeStores_RoundTrip_HelloCount", func(t *testing.T) {
		rec, err := h.App().TaskRecord(t.Context(), res.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind != count || rec.State != tasks.StateSucceeded || rec.Finished.IsZero() || rec.Held {
			t.Errorf("stored run = %+v", rec.RunView)
		}
		// The run survives a restart: the next App over the same store lists
		// it, finished, and reads the same result.
		h2 := h.Reboot(t, coretest.WithModules(hello.New()))
		runs := h2.Runs(t, count)
		if len(runs) != 1 || runs[0].ID != res.ID || runs[0].State != tasks.StateSucceeded {
			t.Fatalf("runs after a restart = %+v, want the one succeeded run %s", runs, res.ID)
		}
		again, err := h2.App().TaskRecord(t.Context(), res.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(again.Result) != string(rec.Result) {
			t.Errorf("result after a restart = %s, was %s", again.Result, rec.Result)
		}
	})
}
