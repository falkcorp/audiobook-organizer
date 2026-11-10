// file: internal/server/afterlisten_gate_test.go
// version: 1.2.0
// guid: 5c0e9f1a-8d3b-4e47-9a62-3b7d1f4c2a90
// last-edited: 2026-11-10

//go:build !windows

package server

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/watcher"
)

// TestStopHook_WaitsForARunningAfterListen reproduces the race the -race
// shard of PR #4551 reported: StopHook's wait on life.backgroundWG ran while
// afterListen was still adding to it, and a caller that touched what
// afterListen reads once the stop returned raced that read. afterListen is
// simulated by a goroutine that holds the gate, writes a plain variable and
// adds to backgroundWG, in that order, after the stop has begun. StopHook
// must not return before it finished: the variable is read only after
// Stopped closes, so `go test -race` reports a data race here if StopHook
// returned early, and the assertions fail either way.
func TestStopHook_WaitsForARunningAfterListen(t *testing.T) {
	s := &Server{stopped: make(chan struct{})}
	finish, res := s.life.afterGate.begin()
	require.Equal(t, beginOK, res)

	var written int // plain on purpose: ordered only by StopHook's wait
	release := make(chan struct{})
	go func() {
		defer finish()
		<-release
		written = 1
		s.life.backgroundWG.Go(func() {})
	}()

	stopDone := make(chan error, 1)
	go func() { stopDone <- s.StopHook(context.Background()) }()

	select {
	case err := <-stopDone:
		t.Fatalf("StopHook returned (%v) while afterListen was still running", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("StopHook did not return once afterListen finished")
	}
	<-s.Stopped()
	require.Equal(t, 1, written, "StopHook returned before afterListen finished")
}

// TestStopHook_BudgetEndingDuringAfterListenIsReported: a post-listen start
// that outlives its share of the shutdown budget does not hold StopHook past
// it, the stop reports it as abandoned, and what afterListen writes after the
// stop (life.watchers, backgroundWG, bgWG) is not read or waited on by
// StopHook while it writes: the late writes below happen after StopHook
// returned, so `go test -race` reports a data race if a step read them
// inline.
func TestStopHook_BudgetEndingDuringAfterListenIsReported(t *testing.T) {
	const budget = 300 * time.Millisecond
	s := &Server{stopped: make(chan struct{})}
	finish, res := s.life.afterGate.begin()
	require.Equal(t, beginOK, res)

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	go func() {
		defer finish()
		// Released by its own timer, after the budget: a channel the test
		// closed, or the budget's context (whose lock StopHook also takes),
		// would order these writes after StopHook's reads and hide exactly
		// the race under test.
		time.Sleep(budget + 100*time.Millisecond)
		s.life.watchers = watcher.NewSupervisor(
			func() ([]string, error) { return nil, nil }, func(string) {}, time.Second, 0)
		s.life.backgroundWG.Go(func() {})
		s.bgWG.Go("late-backfill", func() {})
	}()

	start := time.Now()
	abandoned := s.stopSteps(ctx)
	require.Less(t, time.Since(start), budget+time.Second)
	require.Contains(t, abandoned, "post-listen start")

	select {
	case <-s.life.afterGate.done():
	case <-time.After(10 * time.Second):
		t.Fatal("the late afterListen never finished")
	}
}

// TestAfterListenSealShare (review MAJOR 1): the seal's wait is its own share
// of the budget, never all of it: at most afterListenSealWait, and always
// leaving afterListenSealReserve for the steps after it, which is not below
// the ops registry's reserve (8 s), the largest in StopHook's ladder.
func TestAfterListenSealShare(t *testing.T) {
	require.GreaterOrEqual(t, afterListenSealReserve, 8*time.Second)
	for _, tc := range []struct {
		name   string
		budget time.Duration // 0: no deadline
		want   time.Duration
	}{
		{"no deadline", 0, afterListenSealWait},
		{"plenty of budget", time.Minute, afterListenSealWait},
		{"tight budget", 14 * time.Second, 4 * time.Second},
		{"inside the reserve", 5 * time.Second, 0},
		{"spent", time.Millisecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.budget > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.budget)
				defer cancel()
			}
			got := afterListenSealShare(ctx)
			require.LessOrEqual(t, got, tc.want)
			require.Greater(t, got+time.Second, tc.want, "share %s, want about %s", got, tc.want)
			if tc.budget >= afterListenSealReserve {
				require.LessOrEqual(t, got+afterListenSealReserve, tc.budget)
			}
		})
	}
}

// TestAfterListen_ReturnsAtTheFirstBoundaryOnceSealed (review MAJOR 1a): a
// stop that sealed the gate does not wait for the rest of a start it is about
// to undo. On this bare Server the steps after the first boundary would
// dereference nil state, so a return there is the proof.
func TestAfterListen_ReturnsAtTheFirstBoundaryOnceSealed(t *testing.T) {
	s := &Server{}
	require.True(t, s.life.afterGate.seal(context.Background()))
	s.afterListen()
}

// maintenanceRunReads is a store that counts the read scheduler Start makes
// first, synchronously (loadLastMaintenanceRun), so a test can tell whether
// Start ran. Anything else it is asked falls through to the nil embedded Store
// and panics, which is also a failure.
type maintenanceRunReads struct {
	database.Store
	reads atomic.Int32
}

func (m *maintenanceRunReads) GetSetting(key string) (*database.Setting, error) {
	if key == "maintenance_window_last_run" {
		m.reads.Add(1)
		return nil, nil
	}
	return m.Store.GetSetting(key)
}

// TestStartScheduler_StopBeganBeforeStartDoesNotStartTheScheduler: scheduler
// Start launches the RunOnStart tasks on goroutines nothing tracks, so a stop
// that sealed the gate after afterListen's check before the scheduler was
// built must not have them start. Here the stop has begun by the time the ops
// schedules are registered, the point between that and Start where the seal
// can land in production. Start's first act is to read the last maintenance
// run from the store, so a read means Start ran.
func TestStartScheduler_StopBeganBeforeStartDoesNotStartTheScheduler(t *testing.T) {
	store := &maintenanceRunReads{}
	s := &Server{
		store:      store,
		opRegistry: opsregistry.New(nil, slog.New(slog.DiscardHandler), 1, nil),
	}
	var sealed atomic.Bool
	sealed.Store(true) // the stop began before the check
	var checks atomic.Int32
	stopping := func() bool {
		checks.Add(1)
		return sealed.Load()
	}

	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	started := s.startScheduler(stopping, shutdown, &wg)
	close(shutdown)
	wg.Wait()

	require.False(t, started, "the start ends at this boundary")
	require.Equal(t, int32(1), checks.Load(), "the stop is checked once, before Start")
	require.NotNil(t, s.scheduler.Load(), "the scheduler is stored before the check, for the stop to find")
	require.Zero(t, store.reads.Load(), "Start must not run: it reads the last maintenance run first")
}

func TestAfterListenGate(t *testing.T) {
	t.Run("seal with nothing begun is immediate and refuses a later begin", func(t *testing.T) {
		var g afterListenGate
		require.True(t, g.seal(context.Background()))
		_, res := g.begin()
		require.Equal(t, beginSealed, res, "begin after seal must refuse")
	})
	t.Run("begin is single-shot", func(t *testing.T) {
		var g afterListenGate
		finish, res := g.begin()
		require.Equal(t, beginOK, res)
		defer finish()
		_, res = g.begin()
		require.Equal(t, beginAlready, res)
	})
	t.Run("seal waits for a running start", func(t *testing.T) {
		var g afterListenGate
		finish, res := g.begin()
		require.Equal(t, beginOK, res)
		sealed := make(chan bool, 1)
		go func() { sealed <- g.seal(context.Background()) }()
		select {
		case <-sealed:
			t.Fatal("seal returned while the start was running")
		case <-time.After(100 * time.Millisecond):
		}
		finish()
		select {
		case done := <-sealed:
			require.True(t, done)
		case <-time.After(10 * time.Second):
			t.Fatal("seal did not return after finish")
		}
	})
	t.Run("seal reports a start still running when ctx ends", func(t *testing.T) {
		var g afterListenGate
		finish, res := g.begin()
		require.Equal(t, beginOK, res)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		require.False(t, g.seal(ctx))
		select {
		case <-g.done():
			t.Fatal("done closed while the start was running")
		default:
		}
		finish()
		<-g.done()
	})
	t.Run("seal closes stopCh, and done is closed when nothing began", func(t *testing.T) {
		var g afterListenGate
		select {
		case <-g.stopCh():
			t.Fatal("stopCh closed before any seal")
		default:
		}
		require.True(t, g.seal(context.Background()))
		<-g.stopCh()
		<-g.done()
	})
}

// TestR1Listener_StopBeforeServeNeverRunsAfterListen is the other half of the
// race: the kernel runs Serve on its own goroutine and may stop before it
// ran. Shutdown seals the gate, so a Serve that starts afterwards must not
// run afterListen (on this bare Server it would dereference nil state, and
// afterDone stays open as the proof it did not run).
func TestR1Listener_StopBeforeServeNeverRunsAfterListen(t *testing.T) {
	s := listenTestServer()
	ln, err := s.listenR1(listenTestConfig("0"), listenOptionsFor(listenTestConfig("0")), s.router)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, ln.Shutdown(ctx))

	require.NoError(t, ln.Serve())
	select {
	case <-ln.afterDone:
		t.Fatal("afterListen ran after the stop had begun")
	case <-time.After(100 * time.Millisecond):
	}
}
