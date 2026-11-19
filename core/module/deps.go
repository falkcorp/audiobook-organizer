// file: core/module/deps.go
// version: 1.4.0
// guid: 04c2ba0c-0bfa-4d6b-9e91-bc7ba2db6894
// last-edited: 2026-11-19

package module

import (
	"context"
	"errors"
	"iter"
	"log/slog"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Deps are the handles a module may use (06 §6.4.4). Every handle is scoped
// to the module and safe for concurrent use; none is nil except Config. These
// are the R1 fields; the rest arrive with their subsystems' R2 flips (06
// §6.4.10).
type Deps struct {
	Module ID

	Log     *slog.Logger   // child of the app logger with attr module=<ID> (05-logging): log.Logging.Module(id)
	Metrics *metrics.Scope // module series under app.Options.MetricNamespace on the App's registry, never the global one (05-metrics, 03 §3.6)

	// ExtendStart is health.Supervisor.ExtendStart as a plain func: call it
	// right before a startup step that makes real progress (a store open, a
	// migration step, a search-index open); never from a timer; a no-op
	// without NOTIFY_SOCKET (05-health §5H.3.4). It also extends the start
	// window of the calling module's running hook Start, so a step that makes
	// progress is not abandoned at core.lifecycle.start_timeout.
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

	// Secrets is the module's secret scope: names under "modules.<ID>.",
	// values never logged (06 §6.4.4, 05-config Part 2). It is the zero
	// secrets.Scope, which refuses every name (secrets.ErrOutOfScope),
	// where no secret store is open: app.Describe, and a boot without core
	// config.
	Secrets secrets.Scope

	// Config is the App's layered config store (05-config): every key of
	// core and of every module, read and written as the caller's Actor, plus
	// the secrets Manifest.AppConfig names. It is nil unless the module
	// declares Manifest.AppConfig and a core config boot ran (not in
	// app.Describe, nor without kernel.Config.CoreConfigBoot), so it is the one
	// Deps handle a module must nil-check. Apply authorizes per key against the
	// Actor the caller passes, so the handle grants no authority the Actor
	// lacks; it does let the module name any Actor, which is why only a module
	// the App's manifest review admits takes it.
	Config ConfigAccess
}

// ConfigAccess is Deps.Config: *config.Store's reads, one write and its
// generation. The store is attached before any module's Register runs (P4d),
// so Apply works from Register on.
type ConfigAccess interface {
	// Lookup is a key's effective state (value, source, lock, protection,
	// pending restart, secret metadata); false for an unregistered path.
	Lookup(p config.Path) (config.KeyState, bool)
	// Keys is every registered key in path order.
	Keys() iter.Seq2[config.Path, config.KeyState]
	// Apply validates and commits a patch in one batch (05-config 1.4 step 8).
	Apply(ctx context.Context, p config.Patch, a config.Actor) (config.Result, error)
	// Generation moves on every commit and on Attach.
	Generation() uint64
	// OnCommit registers fn to run after every committed Apply with the new
	// generation (config.Store.OnCommit): the hook an App-wide projection
	// rebuilds on, whoever wrote.
	OnCommit(fn func(gen uint64))
	// PassthroughValues are the operator-layer values of passthrough paths
	// (integrations.<kind>.<instance>.<field>), raw and sorted by path.
	PassthroughValues() iter.Seq2[config.Path, config.OperatorValue]
	// Secret, PutSecret and DeleteSecret reach the secret store by name, for
	// names Manifest.AppConfig.Secrets admits; any other name is refused with
	// secrets.ErrOutOfScope. A name that is a registered config key is
	// readable (the key's Lookup carries only metadata) but not writable here:
	// PutSecret and DeleteSecret refuse it, because Apply writes it and so
	// keeps its audit, protection and lock.
	Secret(ctx context.Context, name secrets.Name) (secrets.Value, secrets.Meta, error)
	PutSecret(ctx context.Context, name secrets.Name, v secrets.Value, by string) (secrets.Meta, error)
	DeleteSecret(ctx context.Context, name secrets.Name, by string) error
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
