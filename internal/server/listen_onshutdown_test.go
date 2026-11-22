// file: internal/server/listen_onshutdown_test.go
// version: 0.1.0
// guid: 5d0c8e37-1fa4-4b92-8c63-e17a9b2d4f05
// last-edited: 2026-11-22

//go:build !windows

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestListenR1_RunsOnShutdownWhenTheDrainBegins: the kernel hands the listener
// the realtime hub's Close in ListenOptions.OnShutdown (F6-03), and ABO's own
// listener must run it as soon as Shutdown begins, so the open event streams
// end with closed{shutdown} before the drain waits on them. A listener that
// dropped the field would hold every /api/core/v1/events stream open for the
// whole drain budget.
func TestListenR1_RunsOnShutdownWhenTheDrainBegins(t *testing.T) {
	s := listenTestServer()
	cfg := listenTestConfig("0")
	o := listenOptionsFor(cfg)
	ran := make(chan struct{}, 1)
	o.OnShutdown = append(o.OnShutdown, func() { ran <- struct{}{} })

	ln, err := s.listenR1(cfg, o, s.router)
	require.NoError(t, err)
	go func() { _ = ln.serveListeners() }()
	t.Cleanup(func() { _ = s.httpServer.Close() })

	select {
	case <-ran:
		t.Fatal("OnShutdown ran before the shutdown began")
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.httpServer.Shutdown(ctx))
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("OnShutdown did not run when the HTTP server began to shut down")
	}
}
