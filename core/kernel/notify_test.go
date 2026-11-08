// file: core/kernel/notify_test.go
// version: 1.0.0
// guid: 15d28130-1cd3-469d-8990-083e81f9f9b2
// last-edited: 2026-11-08

//go:build !windows

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/lifecycle"
)

// The STATUS= lines are HEAD's, byte for byte: the server sent them until
// R1-16 moved the sends into the supervisor.
const (
	headServingStatus  = "Serving; readiness is reported by the health endpoint"
	headStoppingStatus = "Shutting down"
)

func TestSupervisor_NotifiesReadyAndStopping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		k := mustNew(t, supervised(t, rec, hooked("alfred", rec)))
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()

		// Start step 8: READY=1 after the listener is up and the hooks have
		// started, then the serving STATUS=, in HEAD's order.
		order := []string{"start:alfred.cape", "listen:localhost:0", "ready", "status:" + headServingStatus}
		for i := 1; i < len(order); i++ {
			if a, b := rec.index(order[i-1]), rec.index(order[i]); a < 0 || b < 0 || a >= b {
				t.Fatalf("%s (%d) is not before %s (%d): %v", order[i-1], a, order[i], b, rec.list())
			}
		}
		if n := len(rec.matching("ready")); n != 1 {
			t.Fatalf("ready sent %d times, want once: %v", n, rec.list())
		}
		if rec.index("stopping") >= 0 {
			t.Fatalf("STOPPING sent before shutdown began: %v", rec.list())
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		// The first stop step: STOPPING=1 and the shutting-down STATUS= before
		// the listener drains and before any Stop hook.
		order = []string{"stopping", "status:" + headStoppingStatus, "listener:shutdown", "stop:alfred.cape"}
		for i := 1; i < len(order); i++ {
			if a, b := rec.index(order[i-1]), rec.index(order[i]); a < 0 || b < 0 || a >= b {
				t.Fatalf("%s (%d) is not before %s (%d): %v", order[i-1], a, order[i], b, rec.list())
			}
		}
		if n := len(rec.matching("stopping")); n != 1 {
			t.Fatalf("stopping sent %d times, want once: %v", n, rec.list())
		}
	})
}

// A notifier that fails is logged and never stops the app (HEAD's behaviour:
// under Type=simple there is no socket at all).
type failingNotifier struct{ calls *atomic.Int32 }

func (n failingNotifier) Ready() error        { n.calls.Add(1); return errors.New("no socket") }
func (n failingNotifier) Status(string) error { n.calls.Add(1); return errors.New("no socket") }
func (n failingNotifier) Stopping() error     { n.calls.Add(1); return errors.New("no socket") }

func TestSupervisor_NotifierErrorsAreNotFatal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		c := supervised(t, rec, hooked("alfred", rec))
		var calls atomic.Int32
		c.Notifier = failingNotifier{&calls}
		k := mustNew(t, c)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		if rec.index("listen:localhost:0") < 0 || rec.index("worker:alfred") < 0 {
			t.Fatalf("the app did not start past a failing notifier: %v", rec.list())
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil: a notify error must not fail the app", err)
		}
		// starting, ready, serving, stopping, shutting down.
		if got := calls.Load(); got != 5 {
			t.Fatalf("notifier calls = %d, want 5", got)
		}
	})
}

// A listener that cannot open means the app never became ready: no READY=1
// and no serving STATUS= (HEAD sent them only after the bind succeeded).
func TestSupervisor_NoReadyWhenListenerFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		c := supervised(t, rec, hooked("alfred", rec))
		c.Listen = func(httpapi.ListenOptions, http.Handler, *slog.Logger) (Server, error) {
			return nil, errors.New("address already in use")
		}
		k := mustNew(t, c)
		err := k.Run(t.Context())
		if err == nil || !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("Run = %v, want the listener error", err)
		}
		for _, never := range []string{"ready", "status:" + headServingStatus} {
			if rec.index(never) >= 0 {
				t.Errorf("%s was sent although the listener failed: %v", never, rec.list())
			}
		}
	})
}

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

// nextDatagram returns the next datagram, failing the test after the timeout.
func nextDatagram(t *testing.T, conn *net.UnixConn, timeout time.Duration) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no datagram within %s: %v", timeout, err)
	}
	return string(buf[:n])
}

// TestSupervisor_DefaultNotifierSpeaksSystemd runs the real kernel with the
// default notifier over a fake NOTIFY_SOCKET and the real listener, the
// property the server's own tests held until R1-16: the first state datagram
// after the starting status is READY=1, the port already accepts connections
// when it arrives, and the stop sends STOPPING=1 before the listener drains.
// It uses real sockets, so it cannot run in a synctest bubble.
func TestSupervisor_DefaultNotifierSpeaksSystemd(t *testing.T) {
	sock := notifySocket(t)
	rec := &recorder{}
	var addr atomic.Value
	c := supervised(t, rec, hooked("alfred", rec))
	c.Notifier = nil // the default: systemd's notify protocol
	c.R1Values = func() R1Values { return R1Values{Listen: "127.0.0.1:0"} }
	c.Listen = func(o httpapi.ListenOptions, h http.Handler, l *slog.Logger) (Server, error) {
		s, err := listenHTTP(o, h, l)
		if err == nil {
			addr.Store(s.Addr().String())
		}
		return s, err
	}
	k := mustNew(t, c)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()

	var got []string
	next := func() string {
		msg := nextDatagram(t, sock, 10*time.Second)
		got = append(got, msg)
		return msg
	}
	if msg := next(); msg != "STATUS=starting" {
		t.Fatalf("first datagram = %q, want STATUS=starting (got %q)", msg, got)
	}
	if msg := next(); msg != "READY=1" {
		t.Fatalf("second datagram = %q, want READY=1 (got %q)", msg, got)
	}
	a, _ := addr.Load().(string)
	if a == "" {
		t.Fatal("READY=1 was sent before the listener was opened")
	}
	conn, err := net.DialTimeout("tcp", a, 2*time.Second)
	if err != nil {
		t.Fatalf("READY=1 was sent but the port does not accept connections: %v", err)
	}
	_ = conn.Close()
	if msg := next(); msg != "STATUS="+headServingStatus {
		t.Fatalf("third datagram = %q, want the serving status (got %q)", msg, got)
	}

	cancel()
	if msg := next(); msg != "STOPPING=1" {
		t.Fatalf("stop datagram = %q, want STOPPING=1 (got %q)", msg, got)
	}
	if msg := next(); msg != "STATUS="+headStoppingStatus {
		t.Fatalf("stop status datagram = %q (got %q)", msg, got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// Without NOTIFY_SOCKET the default notifier sends nothing and fails nothing.
func TestSystemdNotifier_WithoutSocketIsNoOp(t *testing.T) {
	t.Setenv(lifecycle.NotifySocketEnv, "")
	if err := os.Unsetenv(lifecycle.NotifySocketEnv); err != nil {
		t.Fatal(err)
	}
	var n ReadinessNotifier = systemdNotifier{}
	for name, err := range map[string]error{"ready": n.Ready(), "status": n.Status("x"), "stopping": n.Stopping()} {
		if err != nil {
			t.Errorf("%s = %v, want nil", name, err)
		}
	}
}
