// file: core/lifecycle/notify.go
// version: 1.0.0
// guid: 2a7b261e-1f03-4919-bf00-a3d52939571f
// last-edited: 2026-11-08

package lifecycle

// NotifySocketEnv names the environment variable systemd sets, to the path of
// a unix datagram socket, for a Type=notify service (sd_notify(3)). Only the
// main process may notify (NotifyAccess=main), and systemd logs a warning for
// every datagram a child sends, so code that starts child processes withholds
// it from their environment. The runtime's own notifier reads the same name
// (core/internal/sdnotify), so the two cannot drift apart.
const NotifySocketEnv = "NOTIFY_SOCKET"
