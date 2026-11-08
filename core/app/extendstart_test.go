// file: core/app/extendstart_test.go
// version: 1.0.0
// guid: 0d057159-dd11-4c4f-a871-b20ed3ff5611
// last-edited: 2026-11-08

//go:build !windows

package app

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/lifecycle"
)

// notifySocket binds a unix datagram socket and points NOTIFY_SOCKET at it
// for the test. It lives under os.MkdirTemp rather than t.TempDir: a socket
// path is limited to 104 bytes on darwin and t.TempDir's passes that.
func notifySocket(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv(lifecycle.NotifySocketEnv, path)
	return conn
}

// nextDatagram returns the next datagram, or "" once nothing arrives within
// the timeout.
func nextDatagram(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ""
		}
		t.Fatal(err)
	}
	return string(buf[:n])
}

// TestExtendStart_ExtendsTimeout: the stateless ExtendStart, which app code
// that runs before New calls (the store's open and migration steps), asks the
// service manager for StartupStepBudget (10 minutes) more start time.
func TestExtendStart_ExtendsTimeout(t *testing.T) {
	sock := notifySocket(t)
	ExtendStart("store open")
	if got := nextDatagram(t, sock, 5*time.Second); got != "EXTEND_TIMEOUT_USEC=600000000" {
		t.Fatalf("datagram = %q, want EXTEND_TIMEOUT_USEC=600000000", got)
	}
	// One request per call: a second step asks again, and nothing else is sent.
	ExtendStart("migration 7")
	if got := nextDatagram(t, sock, 5*time.Second); got != "EXTEND_TIMEOUT_USEC=600000000" {
		t.Fatalf("second datagram = %q", got)
	}
	if got := nextDatagram(t, sock, 200*time.Millisecond); got != "" {
		t.Fatalf("extra datagram %q", got)
	}
}

// Without NOTIFY_SOCKET (Type=simple, tests, a developer machine) nothing is
// sent and nothing fails.
func TestExtendStart_WithoutSocketIsNoOp(t *testing.T) {
	t.Setenv(lifecycle.NotifySocketEnv, "")
	if err := os.Unsetenv(lifecycle.NotifySocketEnv); err != nil {
		t.Fatal(err)
	}
	ExtendStart("store open")
}

// A socket nobody listens on is a failed write: logged, never fatal, never a
// panic (HEAD's behaviour for a start-timeout extension).
func TestExtendStart_WriteFailureIsNotFatal(t *testing.T) {
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(lifecycle.NotifySocketEnv, filepath.Join(dir, "absent.sock"))
	ExtendStart("store open")
}

// TestFastExit: --print-capabilities is answered with exactly the token list
// the deploy and rollback recipes grep for; every other argument vector is
// left to the host.
func TestFastExit(t *testing.T) {
	var out bytes.Buffer
	handled, err := FastExit([]string{"audiobook-organizer", "--print-capabilities"}, &out)
	if err != nil || !handled {
		t.Fatalf("FastExit = %v, %v, want handled", handled, err)
	}
	if out.String() != "sd_notify\n" {
		t.Fatalf("stdout = %q, want %q (byte-identical to HEAD)", out.String(), "sd_notify\n")
	}

	for _, args := range [][]string{
		nil,
		{"audiobook-organizer"},
		{"audiobook-organizer", "serve"},
		{"audiobook-organizer", "--print-capabilities", "extra"},
		{"audiobook-organizer", "serve", "--print-capabilities"},
		{"audiobook-organizer", "--print-storage-format"},
	} {
		out.Reset()
		handled, err := FastExit(args, &out)
		if handled || err != nil || out.Len() != 0 {
			t.Errorf("FastExit(%q) = %v, %v with stdout %q, want not handled and silent", args, handled, err, out.String())
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

// A stdout that cannot be written is reported, so the host exits non-zero
// rather than letting a recipe read an empty capability list as "none".
func TestFastExit_WriteErrorIsReturned(t *testing.T) {
	handled, err := FastExit([]string{"x", "--print-capabilities"}, failingWriter{})
	if !handled || err == nil || !strings.Contains(err.Error(), "closed pipe") {
		t.Fatalf("FastExit = %v, %v, want handled with the write error", handled, err)
	}
}
