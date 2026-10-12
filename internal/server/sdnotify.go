// file: internal/server/sdnotify.go
// version: 1.0.0
// guid: 96427de2-bc6f-4a85-9a35-774bdfed9ee7
// last-edited: 2026-10-12

package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

// The systemd notify protocol (sd_notify(3)), stdlib only. Under a unit with
// Type=notify, systemd passes the path of a unix datagram socket in
// NOTIFY_SOCKET and treats the service as started only when the process sends
// "READY=1" to it. The protocol is a single datagram of newline-separated
// KEY=VALUE lines, so it does not justify a dependency (coreos/go-systemd).
//
// Every helper is a no-op returning nil when NOTIFY_SOCKET is unset, which is
// the case under the old Type=simple unit, in tests and on a developer
// machine: the binary behaves exactly as before there.
//
// Nothing sent here may carry a path, version, count or credential: STATUS=
// text is shown to anyone who can run `systemctl status`.

// notifySocketEnv names the variable systemd sets for a Type=notify service.
const notifySocketEnv = "NOTIFY_SOCKET"

// errNotifySocketUnsupported is returned for a NOTIFY_SOCKET that is neither
// an absolute path nor an abstract socket (for example systemd's vsock form,
// which a service on the host never receives).
var errNotifySocketUnsupported = errors.New("sdnotify: unsupported NOTIFY_SOCKET form")

// notifySocketAddr turns a NOTIFY_SOCKET value into the datagram address to
// write to. A leading '@' names a Linux abstract-namespace socket, whose real
// first byte is NUL; the translation is done here rather than left to the
// runtime so the address that is dialed is the one the protocol defines.
func notifySocketAddr(env string) (*net.UnixAddr, error) {
	switch {
	case strings.HasPrefix(env, "@"):
		return &net.UnixAddr{Name: "\x00" + env[1:], Net: "unixgram"}, nil
	case strings.HasPrefix(env, "/"):
		return &net.UnixAddr{Name: env, Net: "unixgram"}, nil
	default:
		return nil, errNotifySocketUnsupported
	}
}

// notify sends one state datagram to the socket named by NOTIFY_SOCKET. It
// returns nil without doing anything when the variable is unset. The error
// text never includes the socket path.
func notify(state string) error {
	env := os.Getenv(notifySocketEnv)
	if env == "" {
		return nil
	}
	addr, err := notifySocketAddr(env)
	if err != nil {
		return err
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return fmt.Errorf("sdnotify: dial notify socket: %w", unwrapOpError(err))
	}
	defer conn.Close()
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

// NotifyReady tells systemd the service is up ("READY=1"). Start sends it
// once the last Fatal startup step (the listener bind) has succeeded, not
// after the in-memory warmup: /health reports `ready` for that.
func NotifyReady() error { return notify("READY=1") }

// NotifyStopping tells systemd a shutdown has begun ("STOPPING=1").
func NotifyStopping() error { return notify("STOPPING=1") }

// NotifyStatus sets the free-text line `systemctl status` shows ("STATUS=").
// Callers pass fixed strings only. Newlines would start a new protocol
// assignment, so they are replaced with spaces.
func NotifyStatus(msg string) error {
	return notify("STATUS=" + strings.NewReplacer("\n", " ", "\r", " ").Replace(msg))
}
