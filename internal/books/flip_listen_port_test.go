// file: internal/books/flip_listen_port_test.go
// version: 0.1.0
// guid: b17702ee-1c11-4dfa-8f85-22eb9e667236
// last-edited: 2026-11-22

package books

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFlipBoot_RunsWhileTheDefaultPortIsTaken pins that a running test App
// never binds ABO's compiled listen default (localhost:8484). A flipped boot
// hands the App its own Options.Env, which replaces the process environment
// composedOptions puts on a free loopback port; before withLoopbackListen those
// Apps bound 8484, and two runs on one host (parallel packages, two worktrees,
// two CI pipelines on a shared agent) failed with "address already in use".
// Here the port is held for the whole test, the way another run holds it.
func TestFlipBoot_RunsWhileTheDefaultPortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:8484")
	if err != nil {
		// Something else holds it already, which is the condition under test.
		require.True(t, errors.Is(err, syscall.EADDRINUSE), "holding 127.0.0.1:8484: %v", err)
	} else {
		t.Cleanup(func() { _ = ln.Close() })
	}

	// Two flipped boots, the second while the first one's store is still held
	// by the test: neither may reach for the taken port.
	first := flippedOfflineABO(t, &[]string{})
	second := flippedOfflineABO(t, &[]string{})
	require.NotEqual(t, first.path, second.path)
}

// TestWithLoopbackListen pins the helper's three cases.
func TestWithLoopbackListen(t *testing.T) {
	require.Nil(t, withLoopbackListen(nil), "a nil env falls back to the process environment, which carries the value")
	require.Equal(t, []string{"A=1", loopbackListenEnv}, withLoopbackListen([]string{"A=1"}))
	own := []string{"ABO_CORE_HTTP_LISTEN=192.0.2.8:9000"}
	require.Equal(t, own, withLoopbackListen(own), "a test that names a listen address keeps it")
}
