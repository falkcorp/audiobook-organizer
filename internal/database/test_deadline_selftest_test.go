// file: internal/database/test_deadline_selftest_test.go
// version: 1.0.1
// guid: a3232db7-10d7-425f-865c-cb905b10b5c3
// last-edited: 2026-10-04

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	deadlineChildReturned       = "DEADLINE-CHILD: wait returned after the worker finished"
	deadlineChildTempDirPrefix  = "DEADLINE-CHILD: tempdir "
)

// TestAwaitOrFatalDeadline_Child is the body run in a child process by
// TestAwaitOrFatalDeadline_TimedOutWaitNeverClosesStoreUnderWorkers; it skips
// in a normal run. It opens a real Pebble store (closed by t.Cleanup), starts a
// worker that keeps WRITING to it past a forced 100ms wait bound, and waits on
// the worker with waitGroupOrFatal. Writes, not reads: reads can be served from
// the memdb and never touch Pebble, so they would not expose a closed store.
//
//   - mode "slow": floored bound; the worker writes for 1.5s and returns,
//     inside the grace. The wait must RETURN and the test pass: the bound was
//     starved by the package deadline and the work finished.
//   - mode "slow-full": the same worker under a FULL-regime bound (forced to
//     100ms). A full bound that expires is a deadlock signal, so the test must
//     still fail once the worker finishes.
//   - mode "stuck": floored bound; the worker writes forever; the grace is
//     500ms. The binary must exit.
func TestAwaitOrFatalDeadline_Child(t *testing.T) {
	mode := os.Getenv(deadlineChildEnv)
	if mode == "" {
		t.Skip("child process body for TestAwaitOrFatalDeadline_TimedOutWaitNeverClosesStoreUnderWorkers")
	}
	runFor, grace, regime := 1500*time.Millisecond, time.Minute, waitFloored
	switch mode {
	case "stuck":
		runFor, grace = 0, 500*time.Millisecond
	case "slow-full":
		regime = waitFull
	}

	s := newReviewTestStore(t)
	// The parent removes this directory itself: the "stuck" child exits the
	// binary before any t.Cleanup runs, so t.TempDir's own removal never
	// happens. The parent points TMPDIR at a directory it owns and checks
	// that this path was inside it and is gone afterwards.
	fmt.Fprintf(os.Stderr, "%s%s\n", deadlineChildTempDirPrefix, filepath.Dir(t.TempDir()))
	it, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", "dk-deadline-child", "/f", "s", `{}`))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// A floored bound, as in the make-ci failure: the package deadline had all
	// but run out, so the wait got the 100ms floor.
	testWaitBudgetOverride = &waitBudget{
		bound: testWaitMinBound, grace: grace, regime: regime,
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
	if mode != "slow" {
		t.Error("waitGroupOrFatal returned normally although the worker outlived the 100ms bound")
		return
	}
	fmt.Fprintln(os.Stderr, deadlineChildReturned)
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
	for _, mode := range []string{"slow", "slow-full", "stuck"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0],
				"-test.run=^TestAwaitOrFatalDeadline_Child$", "-test.count=1", "-test.timeout=2m", "-test.v")
			// The child's t.TempDir (which holds its on-disk Pebble store) goes
			// under a directory this test owns. A child that exits the binary
			// skips its own cleanup, so without this every "stuck" run leaked
			// its whole temp dir, store included.
			childTmp := t.TempDir()
			cmd.Env = append(os.Environ(), deadlineChildEnv+"="+mode, "TMPDIR="+childTmp)
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			assertChildTempDirRemoved(t, out, childTmp)

			var exitErr *exec.ExitError
			if mode == "slow" {
				if err != nil {
					t.Fatalf("slow child (deadline-floored bound, work finished in the grace) must pass, got err=%v\n%s", err, out)
				}
			} else if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
				t.Fatalf("child must fail with a non-zero exit, got err=%v\n%s", err, out)
			}
			for _, bad := range []string{"pebble: closed", deadlineChildWorkerErr, "panic:", "after Test"} {
				if strings.Contains(out, bad) {
					t.Errorf("child output contains %q: the store was torn down under the live worker\n%s", bad, out)
				}
			}
			mustContain := func(wants ...string) {
				t.Helper()
				for _, want := range wants {
					if !strings.Contains(out, want) {
						t.Errorf("%s child output is missing %q\n%s", mode, want, out)
					}
				}
			}
			mustNotContain := func(why string, bads ...string) {
				t.Helper()
				for _, bad := range bads {
					if strings.Contains(out, bad) {
						t.Errorf("%s child output contains %q: %s\n%s", mode, bad, why, out)
					}
				}
			}
			switch mode {
			case "slow":
				mustContain(
					"PACKAGE -timeout NEARLY EXHAUSTED, not evidence of a deadlock",
					"cut by the package deadline",
					deadlineChildWorkerFinished,
					"finished work is not a deadlock, so the wait passes",
					deadlineChildReturned,
					"--- PASS: TestAwaitOrFatalDeadline_Child",
				)
				mustNotContain("finished work under a deadline-shortened bound must pass",
					"EXITING THE TEST BINARY", "--- FAIL", "returned normally", "--- all goroutines")
			case "slow-full":
				mustContain(
					"waitGroupOrFatal: still waiting for "+deadlineChildWhat,
					"which is a deadlock or a lost signal, not slowness",
					"--- all goroutines at wait timeout (TestAwaitOrFatalDeadline_Child) ---",
					deadlineChildWorkerFinished,
					"after the wait bound expired; failing only now",
					"--- FAIL: TestAwaitOrFatalDeadline_Child",
				)
				mustNotContain("a full-bound timeout must still fail, by FailNow, after the worker exits",
					"EXITING THE TEST BINARY", "returned normally", deadlineChildReturned)
			case "stuck":
				mustContain(
					"PACKAGE -timeout NEARLY EXHAUSTED, not evidence of a deadlock",
					"--- all goroutines at grace expiry (TestAwaitOrFatalDeadline_Child) ---",
					"EXITING THE TEST BINARY",
				)
				mustNotContain("the stuck worker never finishes", deadlineChildWorkerFinished, deadlineChildReturned)
			}
		})
	}
}

// assertChildTempDirRemoved finds the temp dir the deadline child reported,
// checks it was created under childTmp (the TMPDIR the parent gave it, not the
// system temp dir), removes childTmp's contents, and asserts the child's dir is
// gone, so a killed child cannot leave its store behind.
func assertChildTempDirRemoved(t *testing.T, out, childTmp string) {
	t.Helper()
	var childDir string
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, deadlineChildTempDirPrefix); ok {
			childDir = strings.TrimSpace(rest)
		}
	}
	if childDir == "" {
		t.Fatalf("child did not report its temp dir\n%s", out)
	}
	rel, err := filepath.Rel(childTmp, childDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("child temp dir %s is outside the parent-owned %s: a killed child would leak it", childDir, childTmp)
	}
	entries, err := os.ReadDir(childTmp)
	if err != nil {
		t.Fatalf("read %s: %v", childTmp, err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(childTmp, e.Name())); err != nil {
			t.Fatalf("remove child temp entry %s: %v", e.Name(), err)
		}
	}
	if _, err := os.Stat(childDir); !os.IsNotExist(err) {
		t.Errorf("child temp dir %s still exists after cleanup (stat err=%v)", childDir, err)
	}
}
