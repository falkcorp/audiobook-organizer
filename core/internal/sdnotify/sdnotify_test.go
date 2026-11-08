// file: core/internal/sdnotify/sdnotify_test.go
// version: 2.2.0
// guid: 9009cd80-44b0-462e-a2ad-d3e325def504
// last-edited: 2026-11-08

//go:build !windows

package sdnotify

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/lifecycle"
)

// noErr fails the test when err is not nil. core's tests use the standard
// library only (03 R1(b)).
func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// want fails the test unless got is exactly want.
func want(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// fakeNotifySocket binds a unix datagram socket, points NOTIFY_SOCKET at it
// for the test and returns the listener. It lives under os.MkdirTemp rather
// than t.TempDir: a unix socket path is limited to 104 bytes on darwin, and
// t.TempDir's path (which embeds the test name) passes that.
func fakeNotifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	noErr(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	noErr(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv(lifecycle.NotifySocketEnv, path)
	return conn
}

// readNotify returns the next datagram on conn, or "" after the timeout.
func readNotify(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	noErr(t, conn.SetReadDeadline(time.Now().Add(timeout)))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ""
		}
		noErr(t, err)
	}
	return string(buf[:n])
}

func TestReadySendsReadyDatagram(t *testing.T) {
	conn := fakeNotifySocket(t)
	noErr(t, Ready())
	want(t, readNotify(t, conn, 5*time.Second), "READY=1")
}

func TestStoppingAndStatusDatagrams(t *testing.T) {
	conn := fakeNotifySocket(t)
	noErr(t, Stopping())
	want(t, readNotify(t, conn, 5*time.Second), "STOPPING=1")
	// A newline would start a second assignment; it must not reach the socket.
	noErr(t, Status("Serving\nREADY=1"))
	want(t, readNotify(t, conn, 5*time.Second), "STATUS=Serving READY=1")
}

func TestExtendTimeoutDatagram(t *testing.T) {
	conn := fakeNotifySocket(t)
	noErr(t, ExtendTimeout(StartupStepBudget))
	want(t, readNotify(t, conn, 5*time.Second), "EXTEND_TIMEOUT_USEC=600000000")
	// A non-positive duration asks for nothing, so nothing is sent.
	noErr(t, ExtendTimeout(0))
	want(t, readNotify(t, conn, 200*time.Millisecond), "")
}

func TestUnsetIsNoOp(t *testing.T) {
	t.Setenv(lifecycle.NotifySocketEnv, "")
	noErr(t, os.Unsetenv(lifecycle.NotifySocketEnv))
	noErr(t, Ready())
	noErr(t, Stopping())
	noErr(t, Status("x"))
	noErr(t, ExtendTimeout(time.Minute))
}

func TestMissingSocketErrorOmitsPath(t *testing.T) {
	dir, err := os.MkdirTemp("", "sdn")
	noErr(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "absent.sock")
	t.Setenv(lifecycle.NotifySocketEnv, path)
	err = Ready()
	if err == nil {
		t.Fatal("Ready to an absent socket returned no error")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("the notify error must not carry the socket path: %v", err)
	}
}

func TestSocketAddr(t *testing.T) {
	addr, err := socketAddr("@aorg/notify")
	noErr(t, err)
	want(t, addr.Name, "\x00aorg/notify") // a leading @ names an abstract socket, whose first byte is NUL
	want(t, addr.Net, "unixgram")

	addr, err = socketAddr("/run/systemd/notify")
	noErr(t, err)
	want(t, addr.Name, "/run/systemd/notify")

	for _, bad := range []string{"relative/notify", "vsock:2:1234"} {
		if _, err = socketAddr(bad); !errors.Is(err, errSocketUnsupported) {
			t.Errorf("socketAddr(%q) error = %v, want errSocketUnsupported", bad, err)
		}
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
	noErr(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv(lifecycle.NotifySocketEnv, "@"+name)
	noErr(t, Ready())
	want(t, readNotify(t, conn, 5*time.Second), "READY=1")
}
