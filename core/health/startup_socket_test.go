// file: core/health/startup_socket_test.go
// version: 1.0.0
// guid: 3f7a1c95-d204-4e68-9b1a-6c0e8d2f4a73
// last-edited: 2026-11-13

//go:build !windows

package health

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/lifecycle"
)

// notifySocket binds a unix datagram socket and points NOTIFY_SOCKET at it for
// the test, so the real SystemdNotifier writes to it. It lives under
// os.MkdirTemp rather than t.TempDir: a socket path is limited to 104 bytes on
// darwin and t.TempDir's passes that.
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

// TestStartup_ExtendPerStepNeverTimer drives the real core startup table with
// the real SystemdNotifier against a fake notify socket: each step that asks for
// time (a step that makes real progress calls ExtendStart before it works)
// produces exactly one EXTEND_TIMEOUT_USEC=<StartupStepBudget> datagram, in step
// order, a step that does not ask produces none, a failing Degraded step
// produces none, and nothing arrives between steps (no timer). That the timer
// stays silent for a long idle stretch is TestStartup_ExtendNeverOnATimer, in
// fake time; a socket test cannot wait an hour.
func TestStartup_ExtendPerStepNeverTimer(t *testing.T) {
	sock := notifySocket(t)
	sup := NewSupervisor(nil, SystemdNotifier())
	steps := CoreStartupSteps()
	su, err := NewStartup(nil, nil, steps...)
	if err != nil {
		t.Fatal(err)
	}
	want := "EXTEND_TIMEOUT_USEC=" + strconv.FormatInt(StartupStepBudget.Microseconds(), 10)
	if StartupStepBudget != 10*time.Minute || want != "EXTEND_TIMEOUT_USEC=600000000" {
		t.Fatalf("StartupStepBudget = %s (%s), want HEAD's 10m", StartupStepBudget, want)
	}

	extending, ran := 0, 0
	for i, st := range steps {
		asks := i%2 == 0 // every other step makes real progress and asks for time
		if err := su.Run(context.Background(), st.Name, func(context.Context) error {
			if asks {
				sup.ExtendStart(st.Name)
			}
			return nil
		}); err != nil {
			t.Fatalf("step %s: %v", st.Name, err)
		}
		ran++
		if asks {
			extending++
			if got := nextDatagram(t, sock, 5*time.Second); got != want {
				t.Fatalf("after step %s: datagram %q, want %q", st.Name, got, want)
			}
		}
		if got := nextDatagram(t, sock, 20*time.Millisecond); got != "" {
			t.Fatalf("after step %s: unrequested datagram %q (a step that did not ask, or a timer)", st.Name, got)
		}
	}
	if ran != len(steps) || extending == 0 || extending == ran {
		t.Fatalf("ran %d of %d steps, %d extending: the table must mix asking and silent steps", ran, len(steps), extending)
	}

	// A Degraded step that fails is logged and carried on; it sends nothing itself.
	if err := su.Add(StartupStep{Name: "searchindex", Class: Degraded, Token: "search_index", Reason: "search falls back"}); err != nil {
		t.Fatal(err)
	}
	if err := su.Run(context.Background(), "searchindex", func(context.Context) error { return errors.New("the bat signal is out") }); err != nil {
		t.Fatalf("a failing degraded step returned %v, want nil", err)
	}
	if got := nextDatagram(t, sock, 20*time.Millisecond); got != "" {
		t.Errorf("a failing degraded step sent %q", got)
	}
}
