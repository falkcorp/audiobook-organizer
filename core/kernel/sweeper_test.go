// file: core/kernel/sweeper_test.go
// version: 1.0.1
// guid: 4e8b1c37-92d5-4a60-b7f3-0d6c58e21a94
// last-edited: 2026-11-08

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// syncBuffer is a log sink safe for the sweeper's goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestSweeperRunsAtOnceThenHourly (05-storage §5S.4 step 15; 06 §6.5.1
// "After readiness"): start's goroutine runs the first sweep at once and
// then the first callback, then one sweep per interval; a sweep prune holds the lock for is logged at Info and
// keeps the last findings, a failed one at Error and keeps them too; a mode
// finding is logged at Error and served by findings; stop ends the loop and
// no sweep runs after it.
func TestSweeperRunsAtOnceThenHourly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		calls := 0
		results := []error{nil, storage.ErrCheckpointSweepBusy, errors.New("bane cut the cable"), nil}
		logs := &syncBuffer{}
		s := &sweeper{
			sweep: func(context.Context) (storage.SweepReport, error) {
				mu.Lock()
				defer mu.Unlock()
				err := results[min(calls, len(results)-1)]
				calls++
				if err != nil {
					return storage.SweepReport{}, err
				}
				var rep storage.SweepReport
				if calls == 1 {
					rep.Loose, rep.LooseTotal = []string{"/batcave/.mediacore-checkpoints/x"}, 1
				}
				return rep, nil
			},
			log:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			interval: checkpointSweepInterval,
		}
		count := func() int { mu.Lock(); defer mu.Unlock(); return calls }

		var firstAfter int
		s.start(t.Context(), func(context.Context) { firstAfter = count() })
		synctest.Wait()
		if got := count(); got != 1 || firstAfter != 1 {
			t.Fatalf("after start: %d sweeps, first callback after %d; want the first sweep at once, then the callback", got, firstAfter)
		}
		if loose, total := s.findings(); total != 1 || len(loose) != 1 {
			t.Fatalf("findings after the first sweep = %v, %d; want its one mode finding", loose, total)
		}
		if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "storage_checkpoint_mode") {
			t.Fatalf("a mode finding was not logged at Error:\n%s", logs)
		}

		time.Sleep(checkpointSweepInterval - time.Second)
		synctest.Wait()
		if got := count(); got != 1 {
			t.Fatalf("before the period: %d sweeps, want 1", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := count(); got != 2 {
			t.Fatalf("after one period: %d sweeps, want 2", got)
		}
		if !strings.Contains(logs.String(), "level=INFO msg=\"kernel: checkpoint sweep skipped") {
			t.Fatalf("a busy sweep was not logged at Info:\n%s", logs)
		}
		if _, total := s.findings(); total != 1 {
			t.Fatal("a busy sweep dropped the last sweep's findings")
		}
		time.Sleep(checkpointSweepInterval)
		synctest.Wait()
		if !strings.Contains(logs.String(), "bane cut the cable") {
			t.Fatalf("a failed sweep was not logged:\n%s", logs)
		}
		if _, total := s.findings(); total != 1 {
			t.Fatal("a failed sweep dropped the last sweep's findings")
		}
		time.Sleep(checkpointSweepInterval)
		synctest.Wait()
		if loose, total := s.findings(); total != 0 || len(loose) != 0 {
			t.Fatalf("findings after a clean sweep = %v, %d; want none", loose, total)
		}

		if err := s.stop(time.Now().Add(time.Second)); err != nil {
			t.Fatalf("stop = %v", err)
		}
		before := count()
		time.Sleep(3 * checkpointSweepInterval)
		synctest.Wait()
		if got := count(); got != before {
			t.Fatalf("%d sweeps after stop", got-before)
		}
	})
}

// TestSweeperStop: a sweeper that never started stops at once; a stop
// during a slow first sweep cancels it, returns once the sweep sees the
// cancellation, and skips the first callback; and one whose sweep outlives
// the shutdown budget is abandoned with context.DeadlineExceeded rather
// than holding the stop order.
func TestSweeperStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		quiet := slog.New(slog.DiscardHandler)
		if err := (&sweeper{log: quiet}).stop(time.Now()); err != nil {
			t.Fatalf("stop before start = %v", err)
		}
		slowFirst := &sweeper{
			sweep: func(ctx context.Context) (storage.SweepReport, error) {
				<-ctx.Done() // storage checks ctx between checkpoints
				return storage.SweepReport{}, ctx.Err()
			},
			log:      quiet,
			interval: checkpointSweepInterval,
		}
		called := false
		slowFirst.start(t.Context(), func(context.Context) { called = true })
		synctest.Wait()
		if err := slowFirst.stop(time.Now().Add(time.Minute)); err != nil {
			t.Fatalf("stop during a slow first sweep = %v", err)
		}
		if called {
			t.Fatal("the first callback ran after a stop cancelled the first sweep")
		}

		release := make(chan struct{})
		var n int
		var mu sync.Mutex
		s := &sweeper{
			sweep: func(context.Context) (storage.SweepReport, error) {
				mu.Lock()
				n++
				first := n == 1
				mu.Unlock()
				if !first {
					<-release // ignores ctx: a deletion once begun finishes
				}
				return storage.SweepReport{}, nil
			},
			log:      quiet,
			interval: checkpointSweepInterval,
		}
		s.start(t.Context(), nil)
		time.Sleep(checkpointSweepInterval)
		synctest.Wait()
		if err := s.stop(time.Now().Add(time.Minute)); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stop with a sweep past the budget = %v, want context.DeadlineExceeded", err)
		}
		close(release)
	})
}

// TestRunSweepsCheckpointsAfterReadiness (05-storage §5S.10 "checkpoint
// auto-deleted after 7 days", the kernel's half): a completed checkpoint
// past its delete_after is still there at READY=1 and gone, record, copy
// and pointer, once Run's start sequence is done; Kernel.CheckpointHealth
// then lists no checkpoint.
func TestRunSweepsCheckpointsAfterReadiness(t *testing.T) {
	path := tempStoragePath(t)
	v1 := []storage.Migration{seedStep(1, "cave")}
	v2 := append(slices.Clone(v1), seedStep(2, "cowl"))
	c := testConfig(t, storageMod("alfred", nil, v1...))
	c.StoragePath = path
	k := mustNew(t, c)
	if err := k.closeCore(t.Context(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatal(err)
	}
	c.Modules = []module.Module{storageMod("alfred", nil, v2...)}
	k = mustNew(t, c)
	if err := k.closeCore(t.Context(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path + ".migration-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	var ptr struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &ptr); err != nil || ptr.Name == "" {
		t.Fatalf("the upgrade's pointer %q: %v", raw, err)
	}
	cps := filepath.Join(filepath.Dir(path), ".mediacore-checkpoints")
	record := filepath.Join(cps, ptr.Name+".json")
	backdate(t, record, 8*24*time.Hour)

	rec := &recorder{}
	var atReady error
	ready := make(chan struct{})
	sc := supervised(t, rec, storageMod("alfred", nil, v2...))
	sc.Notifier = readyProbe{notifier: notifier{rec}, probe: func() { _, atReady = os.Stat(record) }, ready: ready}
	sc.StoragePath = path
	k = mustNew(t, sc)
	ctx, cancel := context.WithCancel(t.Context())
	done := run(ctx, k)
	<-ready
	if atReady != nil {
		t.Fatalf("the checkpoint was gone at READY=1 (%v): the sweep runs after readiness", atReady)
	}
	// The first sweep runs on the sweeper's goroutine; CheckpointHealth
	// reads the directory as it is now, so poll briefly for its end.
	deadline := time.Now().Add(30 * time.Second)
	for {
		h, err := k.CheckpointHealth()
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Checkpoints) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("CheckpointHealth still lists %v after the first sweep", h.Checkpoints)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	for _, p := range []string{record, filepath.Join(cps, ptr.Name), path + ".migration-checkpoint"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the first sweep (%v)", filepath.Base(p), err)
		}
	}
}

// TestRunStopsDuringASlowFirstSweep: the first sweep runs on the sweeper's
// goroutine, so Run's select is live while it runs. A stop (here Run's
// context ending, as on SIGTERM) is acted on at once: the stop order
// cancels the sweep and Run returns, without logging the start-complete
// record the sweep never reached.
func TestRunStopsDuringASlowFirstSweep(t *testing.T) {
	rec := &recorder{}
	logs := &syncBuffer{}
	sc := supervised(t, rec, storageMod("alfred", nil, seedStep(1, "cave")))
	sc.StoragePath = tempStoragePath(t)
	sc.Logger = slog.New(slog.NewTextHandler(logs, nil))
	k := mustNew(t, sc)
	if k.store == nil || k.store.sweeper == nil {
		t.Fatal("no checkpoint sweeper on a kernel with a store")
	}
	sweeping := make(chan struct{})
	k.store.sweeper.sweep = func(ctx context.Context) (storage.SweepReport, error) {
		close(sweeping)
		<-ctx.Done() // a large copy whose deletion storage stops between checkpoints
		return storage.SweepReport{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := run(ctx, k)
	select {
	case <-sweeping:
	case <-time.After(30 * time.Second):
		t.Fatal("the first sweep never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop while the first sweep was running: the sweep holds Run's select")
	}
	if strings.Contains(logs.String(), startCompleteMessage) {
		t.Fatalf("%q was logged although the stop cancelled the first sweep:\n%s", startCompleteMessage, logs)
	}
}

// backdate moves a completed record's completed_at and delete_after back by
// d, as if it had completed d ago.
func backdate(t *testing.T, record string, d time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec["status"] != "completed" {
		t.Fatalf("the record is %v, want completed", rec["status"])
	}
	for _, f := range []string{"completed_at", "delete_after"} {
		at, err := time.Parse(time.RFC3339Nano, rec[f].(string))
		if err != nil {
			t.Fatal(err)
		}
		rec[f] = at.Add(-d).Format(time.RFC3339Nano)
	}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
