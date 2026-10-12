// file: internal/sdnotify/sdnotify_test.go
// version: 2.1.0
// guid: 9009cd80-44b0-462e-a2ad-d3e325def504
// last-edited: 2026-10-12

//go:build !windows

package sdnotify

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeNotifySocket binds a unix datagram socket, points NOTIFY_SOCKET at it
// for the test and returns the listener. It lives under os.MkdirTemp rather
// than t.TempDir: a unix socket path is limited to 104 bytes on darwin, and
// t.TempDir's path (which embeds the test name) passes that.
func fakeNotifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv(SocketEnv, path)
	return conn
}

// readNotify returns the next datagram on conn, or "" after the timeout.
func readNotify(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	buf := make([]byte, 4096)
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

func TestReadySendsReadyDatagram(t *testing.T) {
	conn := fakeNotifySocket(t)
	require.NoError(t, Ready())
	require.Equal(t, "READY=1", readNotify(t, conn, 5*time.Second))
}

func TestStoppingAndStatusDatagrams(t *testing.T) {
	conn := fakeNotifySocket(t)
	require.NoError(t, Stopping())
	require.Equal(t, "STOPPING=1", readNotify(t, conn, 5*time.Second))
	// A newline would start a second assignment; it must not reach the socket.
	require.NoError(t, Status("Serving\nREADY=1"))
	require.Equal(t, "STATUS=Serving READY=1", readNotify(t, conn, 5*time.Second))
}

func TestExtendTimeoutDatagram(t *testing.T) {
	conn := fakeNotifySocket(t)
	require.NoError(t, ExtendTimeout(StartupStepBudget))
	require.Equal(t, "EXTEND_TIMEOUT_USEC=600000000", readNotify(t, conn, 5*time.Second))
	// A non-positive duration asks for nothing, so nothing is sent.
	require.NoError(t, ExtendTimeout(0))
	require.Empty(t, readNotify(t, conn, 200*time.Millisecond))
}

func TestUnsetIsNoOp(t *testing.T) {
	t.Setenv(SocketEnv, "")
	require.NoError(t, os.Unsetenv(SocketEnv))
	require.NoError(t, Ready())
	require.NoError(t, Stopping())
	require.NoError(t, Status("x"))
	require.NoError(t, ExtendTimeout(time.Minute))
}

func TestMissingSocketErrorOmitsPath(t *testing.T) {
	dir, err := os.MkdirTemp("", "sdn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "absent.sock")
	t.Setenv(SocketEnv, path)
	err = Ready()
	require.Error(t, err)
	require.NotContains(t, err.Error(), dir, "the notify error must not carry the socket path")
}

func TestSocketAddr(t *testing.T) {
	addr, err := socketAddr("@aorg/notify")
	require.NoError(t, err)
	require.Equal(t, "\x00aorg/notify", addr.Name, "a leading @ names an abstract socket, whose first byte is NUL")
	require.Equal(t, "unixgram", addr.Net)

	addr, err = socketAddr("/run/systemd/notify")
	require.NoError(t, err)
	require.Equal(t, "/run/systemd/notify", addr.Name)

	for _, bad := range []string{"relative/notify", "vsock:2:1234"} {
		_, err = socketAddr(bad)
		require.ErrorIs(t, err, errSocketUnsupported, bad)
	}
}

// TestAbstractSocketRoundTrip sends to a real abstract-namespace socket,
// the form systemd uses when NOTIFY_SOCKET starts with '@'. Abstract sockets
// exist only on Linux.
func TestAbstractSocketRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abstract unix sockets are Linux-only")
	}
	name := fmt.Sprintf("aorg-sdnotify-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: "\x00" + name, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv(SocketEnv, "@"+name)
	require.NoError(t, Ready())
	require.Equal(t, "READY=1", readNotify(t, conn, 5*time.Second))
}
