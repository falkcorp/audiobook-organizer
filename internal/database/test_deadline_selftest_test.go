// file: internal/database/test_deadline_selftest_test.go
// version: 1.0.0
// guid: a3232db7-10d7-425f-865c-cb905b10b5c3
// last-edited: 2026-09-26

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for the bounded-wait helpers in test_deadline_test.go. Every name here
// contains "Deadline" so `-run Deadline` exercises the helpers themselves.

// TestComputeWaitBudgetDeadline_Regimes pins the bound/grace arithmetic and,
// above all, that only a full-length bound calls its timeout a deadlock: a
// bound cut short by the package -timeout must say the package ran out of time.
func TestComputeWaitBudgetDeadline_Regimes(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		left        time.Duration
		hasDeadline bool
		wantBound   time.Duration
		wantGrace   time.Duration
		wantRegime  waitRegime
	}{
		{"no deadline", 0, false, testWaitDefault, testWaitDefault, waitFull},
		{"deadline far away", 10 * time.Minute, true, testWaitDefault, testWaitDefault, waitFull},
		{"deadline cuts the bound", 20 * time.Second, true, 15 * time.Second, 4 * time.Second, waitShortened},
		{"deadline inside cleanup margin", 5*time.Second + 50*time.Millisecond, true,
			testWaitMinBound, 5*time.Second + 50*time.Millisecond - testWaitMinBound - testWaitExitMargin, waitFloored},
		{"deadline nearly here", 500 * time.Millisecond, true, testWaitMinBound, 0, waitFloored},
		{"deadline passed", -time.Second, true, testWaitMinBound, 0, waitFloored},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := computeWaitBudget(now.Add(tc.left), tc.hasDeadline, now)
			if b.bound != tc.wantBound || b.grace != tc.wantGrace || b.regime != tc.wantRegime {
				t.Fatalf("got bound=%v grace=%v regime=%d, want bound=%v grace=%v regime=%d",
					b.bound, b.grace, b.regime, tc.wantBound, tc.wantGrace, tc.wantRegime)
			}
			diag := b.diagnosis()
			calledDeadlock := strings.Contains(diag, "deadlock or a lost signal")
			calledExhausted := strings.Contains(diag, "NEARLY EXHAUSTED")
			if tc.wantRegime == waitFull && (!calledDeadlock || calledExhausted) {
				t.Errorf("full-length bound must report a deadlock/lost signal, got %q", diag)
			}
			if tc.wantRegime != waitFull && (calledDeadlock || !calledExhausted) {
				t.Errorf("deadline-cut bound must report the package -timeout nearly exhausted, got %q", diag)
			}
		})
	}
}

const (
	deadlineChildEnv            = "AUDIOBOOK_TEST_DEADLINE_CHILD"
	deadlineChildWorkerFinished = "DEADLINE-CHILD: worker finished its store writes"
	deadlineChildWorkerErr      = "DEADLINE-CHILD: worker store write failed"
	deadlineChildWhat           = "the deadline child's store-writing worker"
)

// TestAwaitOrFatalDeadline_Child is the body run in a child process by
// TestAwaitOrFatalDeadline_TimedOutWaitNeverClosesStoreUnderWorkers; it skips
// in a normal run. It opens a real Pebble store (closed by t.Cleanup), starts a
// worker that keeps WRITING to it past a forced 100ms wait bound, and waits on
// the worker with waitGroupOrFatal. Writes, not reads: reads can be served from
// the memdb and never touch Pebble, so they would not expose a closed store.
//
//   - mode "slow": the worker writes for 1.5s and returns, inside the grace.
//   - mode "stuck": the worker writes forever; the grace is 500ms.
func TestAwaitOrFatalDeadline_Child(t *testing.T) {
	mode := os.Getenv(deadlineChildEnv)
	if mode == "" {
		t.Skip("child process body for TestAwaitOrFatalDeadline_TimedOutWaitNeverClosesStoreUnderWorkers")
	}
	runFor, grace := 1500*time.Millisecond, time.Minute
	if mode == "stuck" {
		runFor, grace = 0, 500*time.Millisecond
	}

	s := newReviewTestStore(t)
	it, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", "dk-deadline-child", "/f", "s", `{}`))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// A floored bound, as in the make-ci failure: the package deadline had all
	// but run out, so the wait got the 100ms floor.
	testWaitBudgetOverride = &waitBudget{
		bound: testWaitMinBound, grace: grace, regime: waitFloored,
		hasDeadline: true, left: 3 * time.Second,
	}
	t.Cleanup(func() { testWaitBudgetOverride = nil })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		statuses := []string{ReviewStatusApproved, ReviewStatusRejected}
		start := time.Now()
		for i := 0; runFor == 0 || time.Since(start) < runFor; i++ {
			if _, err := s.SetReviewItemDecision(it.ID, statuses[i%2], ""); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", deadlineChildWorkerErr, err)
				return
			}
		}
		fmt.Fprintln(os.Stderr, deadlineChildWorkerFinished)
	}()

	waitGroupOrFatal(t, &wg, deadlineChildWhat)
	t.Error("waitGroupOrFatal returned normally although the worker outlived the 100ms bound")
}

// 🔴 A TIMED-OUT WAIT MUST NEVER LET CLEANUP CLOSE THE STORE UNDER LIVE WORKERS.
//
// Before the fix, awaitOrFatal called t.Fatalf the moment its bound expired;
// the test's cleanup then closed the Pebble store while the workers were still
// writing, and they died with "panic: pebble: closed", killing the test binary
// and hiding the real message. Verified by restoring the old awaitOrFatal: the
// "slow" child then panics with pebble: closed.
func TestAwaitOrFatalDeadline_TimedOutWaitNeverClosesStoreUnderWorkers(t *testing.T) {
	if os.Getenv(deadlineChildEnv) != "" {
		t.Skip("running as a deadline child")
	}
	for _, mode := range []string{"slow", "stuck"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0],
				"-test.run=^TestAwaitOrFatalDeadline_Child$", "-test.count=1", "-test.timeout=2m")
			cmd.Env = append(os.Environ(), deadlineChildEnv+"="+mode)
			raw, err := cmd.CombinedOutput()
			out := string(raw)

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
				t.Fatalf("child must fail with a non-zero exit, got err=%v\n%s", err, out)
			}
			for _, bad := range []string{"pebble: closed", deadlineChildWorkerErr, "panic:", "after Test"} {
				if strings.Contains(out, bad) {
					t.Errorf("child output contains %q: the store was torn down under the live worker\n%s", bad, out)
				}
			}
			for _, want := range []string{
				"waitGroupOrFatal: still waiting for " + deadlineChildWhat,
				"PACKAGE -timeout NEARLY EXHAUSTED, not evidence of a deadlock",
				"--- all goroutines at wait timeout (TestAwaitOrFatalDeadline_Child) ---",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("child output is missing %q\n%s", want, out)
				}
			}
			switch mode {
			case "slow":
				for _, want := range []string{
					deadlineChildWorkerFinished,
					"finished", "after the wait bound expired; failing only now",
					"--- FAIL: TestAwaitOrFatalDeadline_Child",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("slow child output is missing %q\n%s", want, out)
					}
				}
				if strings.Contains(out, "EXITING THE TEST BINARY") {
					t.Errorf("slow child's worker finished inside the grace; it must not have exited the binary\n%s", out)
				}
				if strings.Contains(out, "returned normally") {
					t.Errorf("waitGroupOrFatal returned instead of failing the test\n%s", out)
				}
			case "stuck":
				if !strings.Contains(out, "EXITING THE TEST BINARY") {
					t.Errorf("stuck child must exit the binary after the grace, with the reason\n%s", out)
				}
				if strings.Contains(out, deadlineChildWorkerFinished) {
					t.Errorf("stuck child's worker cannot have finished\n%s", out)
				}
			}
		})
	}
}
