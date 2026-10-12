// file: internal/database/startup_notify_test.go
// version: 1.0.0
// guid: df9cf9a0-3bbf-446b-8d35-f26e8d704219
// last-edited: 2026-10-12

//go:build !windows

package database

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startupNotifySocket binds a unix datagram socket under os.MkdirTemp (a
// t.TempDir path can pass darwin's 104-byte socket path limit) and points
// NOTIFY_SOCKET at it for the test.
func startupNotifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	return conn
}

// nextStartupNotify returns the next datagram, or "" once nothing arrives
// within the timeout.
func nextStartupNotify(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ""
		}
		require.NoError(t, err)
	}
	return string(buf[:n])
}

const startupExtendDatagram = "EXTEND_TIMEOUT_USEC=600000000"

// TestRunMigrationsExtendsStartTimeoutBeforeEachUp: every migration that is
// actually applied asks systemd for more start time BEFORE its Up runs, so a
// long run of migrations under Type=notify is not killed at TimeoutStartSec.
// A migration only advanced past (already recorded) does no work and asks for
// nothing.
func TestRunMigrationsExtendsStartTimeoutBeforeEachUp(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	sock := startupNotifySocket(t)

	var seenInUp []string
	up := func(migrationStore) error {
		// The extension must already be on the socket when Up starts.
		seenInUp = append(seenInUp, nextStartupNotify(t, sock, time.Second))
		return nil
	}
	recorded := Migration{Version: 9101, Description: "startup-notify recorded", Up: up}
	swapMigrationRegistry(t, []Migration{
		recorded,
		{Version: 9102, Description: "startup-notify probe a", Up: up},
		{Version: 9103, Description: "startup-notify probe b", Up: up},
	})
	require.NoError(t, setVersion(store, 9100))
	require.NoError(t, recordMigration(store, recorded))

	require.NoError(t, RunMigrations(store))
	require.Equal(t, []string{startupExtendDatagram, startupExtendDatagram}, seenInUp,
		"one extension per applied migration, sent before its Up; none for the recorded one")
	require.Empty(t, nextStartupNotify(t, sock, 200*time.Millisecond), "no extra datagrams")
}

// TestInitializeStoreExtendsStartTimeoutBeforeOpen: opening the store (which
// can replay a large WAL) is preceded by an extension.
func TestInitializeStoreExtendsStartTimeoutBeforeOpen(t *testing.T) {
	sock := startupNotifySocket(t)
	swapMigrationRegistry(t, nil)
	store, err := InitializeStore("pebble", filepath.Join(t.TempDir(), "s.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() {
		SetGlobalStore(nil)
		_ = store.Close()
	})
	require.Equal(t, startupExtendDatagram, nextStartupNotify(t, sock, 5*time.Second))
}

// TestExtendStartTimeoutWithoutSocketIsNoOp: without NOTIFY_SOCKET (Type=simple,
// tests, a developer machine) nothing is sent and nothing fails.
func TestExtendStartTimeoutWithoutSocketIsNoOp(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	require.NoError(t, os.Unsetenv("NOTIFY_SOCKET"))
	extendStartTimeout()
}
