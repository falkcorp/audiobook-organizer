// file: core/module/deps.go
// version: 1.0.3
// guid: 04c2ba0c-0bfa-4d6b-9e91-bc7ba2db6894
// last-edited: 2026-11-07

package module

import (
	"errors"
	"log/slog"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
)

// Deps are the handles a module may use (06 §6.4.4). Every handle is scoped
// to the module and safe for concurrent use; none is nil. These are the R1
// fields; the rest arrive with their subsystems' R2 flips (06 §6.4.10).
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

	// RequestRestart is kernel.Lifecycle.RequestRestart as a plain func (06
	// §6.5.2 "Restart request", OPS8-12): the app stops in the normal order
	// within core.shutdown.budget and exits core.lifecycle.restart_exit_code
	// (75), so the service manager starts it again. The first reason wins;
	// later calls do nothing. Call it after staging work the next boot
	// applies (a restore, a reset), never to recover from an error.
	//
	// A call before the app is ready (start step 8: from Register, a hook's
	// Start, or a worker before readiness) is logged at ERROR and ignored:
	// honouring it would restart the app in a loop, every boot asking again.
	RequestRestart func(reason string)

	// Storage is the module's storage: its keyspace m/<ID>/ and its legacy
	// range (05-storage §5S.3.4). Reads and writes outside them are refused
	// (R8). Both are zero values, whose every method fails, when the module
	// declared no keyspace or no claims.
	Storage StorageHandles
}

// StorageHandles is what Deps.Storage carries (06 §6.4.4). It lives in
// module, not in storage, because its later members name storage/index,
// which imports storage (05-storage §5S.3.4, COR-2). Indexes, Aux and
// Derived arrive with the full-text index, auxiliary DBs and derived indexes
// (05-storage §5S.3.8-§5S.3.10). No core subsystem and no module ever
// receives another owner's rows (R3, R8).
type StorageHandles struct {
	Keyspace storage.Keyspace    // m/<ID>/; the zero value unless the manifest set Storage.Keyspace
	Legacy   storage.LegacyRange // the zero value unless the manifest declared claims
}

// ErrStopped is returned by any handle used after the app stopped.
var ErrStopped = errors.New("module: app stopped")
