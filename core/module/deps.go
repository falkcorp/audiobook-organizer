// file: core/module/deps.go
// version: 1.0.0
// guid: 04c2ba0c-0bfa-4d6b-9e91-bc7ba2db6894
// last-edited: 2026-11-07

package module

import (
	"errors"
	"log/slog"

	"github.com/falkcorp/mediacore/metrics"
)

// Deps are the handles a module may use (06 §6.4.4). Every handle is scoped
// to the module and safe for concurrent use; none is nil. These are the R1
// fields; Storage arrives with storage (R1-20) and the rest with their
// subsystems' R2 flips (06 §6.4.10).
type Deps struct {
	Module ID

	Log     *slog.Logger   // child of the app logger with attr module=<ID> (05-logging): log.Logging.Module(id)
	Metrics *metrics.Scope // module series under app.Options.MetricNamespace on the App's registry, never the global one (05-metrics, 03 §3.6)

	// ExtendStart is health.Supervisor.ExtendStart as a plain func: call it
	// right before a startup step that makes real progress (a store open, a
	// migration step, a search-index open); never from a timer; a no-op
	// without NOTIFY_SOCKET (05-health §5H.3.4). Until R1-16 moves sdnotify
	// into core it is a no-op everywhere.
	ExtendStart func(step string)
}

// ErrStopped is returned by any handle used after the app stopped.
var ErrStopped = errors.New("module: app stopped")
