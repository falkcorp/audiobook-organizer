// file: internal/server/afterlisten_gate_test.go
// version: 1.0.0
// guid: 5c0e9f1a-8d3b-4e47-9a62-3b7d1f4c2a90
// last-edited: 2026-11-10

//go:build !windows

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	finish, ok := s.life.afterGate.begin()
	require.True(t, ok)

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
// that outlives the shutdown budget does not hold StopHook past it, and the
// stop reports it instead of waiting on backgroundWG alongside its Adds.
func TestStopHook_BudgetEndingDuringAfterListenIsReported(t *testing.T) {
	const budget = 300 * time.Millisecond
	s := &Server{stopped: make(chan struct{})}
	finish, ok := s.life.afterGate.begin()
	require.True(t, ok)
	t.Cleanup(finish)

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	require.NoError(t, s.StopHook(ctx))
	require.Less(t, time.Since(start), budget+time.Second)
	select {
	case <-s.Stopped():
	default:
		t.Fatal("StopHook returned without closing Stopped")
	}
}

func TestAfterListenGate(t *testing.T) {
	t.Run("seal with nothing begun is immediate and refuses a later begin", func(t *testing.T) {
		var g afterListenGate
		require.True(t, g.seal(context.Background()))
		_, ok := g.begin()
		require.False(t, ok, "begin after seal must refuse")
	})
	t.Run("begin is single-shot", func(t *testing.T) {
		var g afterListenGate
		finish, ok := g.begin()
		require.True(t, ok)
		defer finish()
		_, ok = g.begin()
		require.False(t, ok)
	})
	t.Run("seal waits for a running start", func(t *testing.T) {
		var g afterListenGate
		finish, ok := g.begin()
		require.True(t, ok)
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
		finish, ok := g.begin()
		require.True(t, ok)
		defer finish()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		require.False(t, g.seal(ctx))
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
