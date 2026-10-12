// file: internal/sdnotify/sdnotify.go
// version: 2.0.0
// guid: 96427de2-bc6f-4a85-9a35-774bdfed9ee7
// last-edited: 2026-10-12

// Package sdnotify implements the systemd notify protocol (sd_notify(3)) with
// the standard library only.
//
// Under a unit with Type=notify, systemd passes the path of a unix datagram
// socket in NOTIFY_SOCKET and treats the service as started only when the
// process sends "READY=1" to it. The protocol is a single datagram of
// newline-separated KEY=VALUE lines, so it does not justify a dependency
// (coreos/go-systemd).
//
// Every function is a no-op returning nil when NOTIFY_SOCKET is unset, which is
// the case under the old Type=simple unit, in tests and on a developer machine:
// the binary behaves exactly as before there.
//
// It is a leaf package (no module imports) so the store and search packages,
// which run long startup steps, can extend the start timeout without
// importing the server.
//
// Nothing sent here may carry a path, version, count or credential: STATUS=
// text is shown to anyone who can run `systemctl status`.
package sdnotify

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Capability is the token `audiobook-organizer --print-capabilities` prints
// when the binary sends READY=1. `make rollback` and the deploy recipes ask a
// binary for it before restarting under the Type=notify unit: a binary that
// does not print it (any build older than this package) never reports ready,
// so it must run as Type=simple or systemd kills it at TimeoutStartSec.
const Capability = "sd_notify"

// StartupStepBudget is how much start-timeout one startup step that makes real
// progress (opening the store, applying one migration, opening the search
// index) asks systemd for, through ExtendTimeout. Each such step extends the
// deadline to at least this far from when it begins, so a long migration run
// is never killed part-way, while a single step that hangs is still killed
// once its own budget runs out. There is deliberately no ticker: extending on
// a timer would keep a hung startup alive forever.
const StartupStepBudget = 10 * time.Minute

// writeTimeout bounds the datagram write. systemd reads its notify socket
// promptly; this only keeps a wedged receiver from stalling startup.
const writeTimeout = time.Second

// socketEnv names the variable systemd sets for a Type=notify service.
const socketEnv = "NOTIFY_SOCKET"

// errSocketUnsupported is returned for a NOTIFY_SOCKET that is neither an
// absolute path nor an abstract socket (for example systemd's vsock form,
// which a service on the host never receives).
var errSocketUnsupported = errors.New("sdnotify: unsupported NOTIFY_SOCKET form")

// socketAddr turns a NOTIFY_SOCKET value into the datagram address to write
// to. A leading '@' names a Linux abstract-namespace socket, whose real first
// byte is NUL; the translation is done here rather than left to the runtime so
// the address that is dialed is the one the protocol defines.
func socketAddr(env string) (*net.UnixAddr, error) {
	switch {
	case strings.HasPrefix(env, "@"):
		return &net.UnixAddr{Name: "\x00" + env[1:], Net: "unixgram"}, nil
	case strings.HasPrefix(env, "/"):
		return &net.UnixAddr{Name: env, Net: "unixgram"}, nil
	default:
		return nil, errSocketUnsupported
	}
}

// notify sends one state datagram to the socket named by NOTIFY_SOCKET. It
// returns nil without doing anything when the variable is unset. The error
// text never includes the socket path.
func notify(state string) (err error) {
	env := os.Getenv(socketEnv)
	if env == "" {
		return nil
	}
	addr, err := socketAddr(env)
	if err != nil {
		return err
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return fmt.Errorf("sdnotify: dial notify socket: %w", unwrapOpError(err))
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("sdnotify: close notify socket: %w", unwrapOpError(cerr))
		}
	}()
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("sdnotify: set write deadline: %w", unwrapOpError(err))
	}
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("sdnotify: write notify socket: %w", unwrapOpError(err))
	}
	return nil
}

// unwrapOpError drops a *net.OpError's address so the socket path does not
// reach a log line, keeping only the underlying cause (ENOENT, ECONNREFUSED).
func unwrapOpError(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err
	}
	return err
}

// Ready tells systemd the service is up ("READY=1"). The server sends it once
// the last Fatal startup step (the listener bind) has succeeded, not after the
// in-memory warmup: /health reports `ready` for that.
func Ready() error { return notify("READY=1") }

// Stopping tells systemd a shutdown has begun ("STOPPING=1").
func Stopping() error { return notify("STOPPING=1") }

// Status sets the free-text line `systemctl status` shows ("STATUS=").
// Callers pass fixed strings only. Newlines would start a new protocol
// assignment, so they are replaced with spaces.
func Status(msg string) error {
	return notify("STATUS=" + strings.NewReplacer("\n", " ", "\r", " ").Replace(msg))
}

// ExtendTimeout asks systemd to push the current (start or stop) timeout to at
// least d from now ("EXTEND_TIMEOUT_USEC="). It never shortens a timeout.
// Call it when a step makes real progress, never from a timer; see
// StartupStepBudget.
func ExtendTimeout(d time.Duration) error {
	if d <= 0 {
		return nil
	}
	return notify("EXTEND_TIMEOUT_USEC=" + strconv.FormatInt(d.Microseconds(), 10))
}
