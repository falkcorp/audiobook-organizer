// file: core/module/deps.go
// version: 1.5.0
// guid: 04c2ba0c-0bfa-4d6b-9e91-bc7ba2db6894
// last-edited: 2026-11-19

package module

import (
	"context"
	"errors"
	"iter"
	"log/slog"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Deps are the handles a module may use (06 §6.4.4). Every handle is scoped
// to the module and safe for concurrent use; none is nil except Config and
// AuthCompat, and, until F4-08's boot wiring (B3) builds them, Auth, AuthAdmin
// AuthRefresh and Audit, which the kernel leaves nil. These are the R1 fields and the
// identity flip's (Auth, AuthAdmin, AuthCompat, Audit, Events); the rest
// arrive with their subsystems' R2 flips (06 §6.4.10).
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

	// State is the labelled state filesystem for a boot-time write outside
	// Pebble (a legacy file's deletion in a start hook): every call takes the
	// writer's crash-matrix label, so the kit sees it (state.go). Never nil
	// from the kernel; a path with no state directory or store directory
	// under it is refused (ErrStateConfined), which is every path in
	// app.Describe, where no store is open. A path that is, holds or lies
	// inside what core owns (the store, the keyring, the boot record) is
	// refused too (ErrStateCoreOwned).
	State StateFS

	// SecretsKeyringExported reports whether core.secrets is not raising
	// secrets_keyring_not_exported: every key of the keyring is named by an
	// export (05-config §2.4, §2.9). It reads the export record on every call
	// and fails closed: false when the record cannot be read and when the
	// keyring is not open (app.Describe, after shutdown). It is not the
	// core.secrets health Result, which carries one Reason and so can name
	// secrets_undecryptable while the keyring is also unexported. Never nil
	// from the kernel.
	SecretsKeyringExported func() bool

	// Config is the App's layered config store (05-config): every key of
	// core and of every module, read and written as the caller's Actor, plus
	// the secrets Manifest.AppConfig names. It is nil unless the module
	// declares Manifest.AppConfig and a core config boot ran (not in
	// app.Describe), so it is the one
	// Deps handle a module must nil-check. Apply authorizes per key against the
	// Actor the caller passes, so the handle grants no authority the Actor
	// lacks; it does let the module name any Actor, which is why only a module
	// the App's manifest review admits takes it.
	Config ConfigAccess

	// Events publishes the module's own catalog topics (05-events §5E.3.2:
	// the App's Bus bound to this module, which refuses a topic the module
	// did not declare with Registrar.Topics; subscriptions go through
	// Registrar.Subscribe). Unlike the handles around it, its zero value is
	// usable: a Scope with no bus drops every publish silently, which is what
	// a module gets until the kernel builds the bus (F4-08, B3).
	Events events.Scope

	// Outbox stages durable messages into the module's own batches
	// (05-events §5E.4, 06 §6.4.4): Stage adds one intake row to the batch the
	// caller already writes, and only for a topic this module declared Durable
	// with Registrar.Topics. The zero Writer stages nothing and every Stage
	// through it fails, which is what a module gets on a boot that built no
	// outbox (app.Describe, an app that did not declare the identity flip);
	// a module that stages must check for it.
	Outbox outbox.Writer

	// Auth is the read-only identity view (05-authn §3.1): lookups by ID and
	// username, a user listing and the request principal, no credential,
	// session or user mutation. Nil until the kernel builds the auth service
	// (F4-08, B3).
	Auth auth.Identities

	// AuthAdmin is the actor-checked identity participant (05-authn §3.1):
	// StageUserMerge for multi-owner commits, which joins the caller's batch
	// and never commits, and the account writes and reads of auth.AccountAdmin
	// (invites, set-email, the admin user reads), which each commit on their
	// own. Every method re-checks the ctx principal (core:users.manage,
	// interactive, dominance, the owner rule, core:admin where the method
	// vouches for an address), so holding it grants nothing. Nil until the
	// kernel builds the auth service (F4-08, B3).
	AuthAdmin auth.Admin

	// AuthCompat is the method-bound handle of the module's own
	// Registrar.AuthMethod method (05-authn §3.1, SEC-1; books: books.abs):
	// every call is bound to that method, so the principal it returns names
	// it. It is nil for every module that registered no method, the one
	// handle besides Config a module must nil-check, and nil for all of them
	// until the kernel hands compat methods to auth at P7 (F4-08, B3).
	AuthCompat auth.Compat

	// AuthRefresh is auth.Service.Refresh as a plain func, for a module whose
	// manifest declares Manifest.AuthRefresh and for no other (nil for every
	// module that did not): a request-bound module (a legacy-auth bridge) that
	// must re-derive the principal stage 12 bound after a password reset or a
	// role change. It is a func, not a widened Identities: auth.Identities is
	// fixed at four methods (05-authn §3.1) and the module holds no handle to
	// the rest of the service. Refresh rebuilds a principal from the stored user
	// and at most the scopes it was built with that its stored key still
	// holds; the credential, AuthTime, Assurance, ClientAddr and extension
	// expiry are carried (the expiry is never extended), and a disabled or
	// reset-required user is refused. It trusts nothing the caller can write:
	// a principal auth did not build (a literal), and one whose User, Method,
	// CredentialID, Assurance, AuthTime or ClientAddr differs from what auth
	// recorded when it finished the principal, is refused with
	// auth.ErrInvalidCredentials, so the handle cannot be used to mint a
	// principal for another identity. Pass the request's own principal. Nil
	// until the kernel builds the auth service (F4-08, B3): also nil on a boot
	// without IdentityBoot.
	AuthRefresh RefreshFunc

	// AuthLifecycle is auth.Service.RegisterLifecycle as a plain func, for a
	// module whose manifest declares Manifest.AuthLifecycle and for no other
	// (nil for every module that did not). Call it once, from Register: a
	// second call is refused (auth.ErrLifecycleRegistered). The callbacks
	// are called after the write that caused them has committed, with ids and
	// minimal facts, and cannot veto it; see auth.Lifecycle. Nil until the
	// kernel builds the auth service, and on a boot without IdentityBoot.
	AuthLifecycle LifecycleFunc

	// Audit records audit events, the actor taken from the ctx (05-rbac §3.5:
	// Record, RecordIn, Emit); the action namespace "<ID>." is checked. Nil
	// until the kernel builds the audit logger for modules (F4-08, B3).
	Audit audit.Logger

	// Tasks starts and observes runs by Kind (05-tasks 5T.3.10). It is the
	// module's own client: a call made while serving a request acts as that
	// request's principal, and a call made with no request in its ctx (a hook, a
	// worker, a run body) acts as the module's system actor, which may start the
	// module's own kinds and Manifest.TaskStarts (SEC2-15), cancel what that actor
	// started, and nothing the core:tasks.manage scope guards. It is never nil,
	// but the engine behind it is built at P7 and only when the app runs one: a
	// call before then, or in an app with no task engine, returns
	// ErrTasksNotBound.
	Tasks tasks.Client

	// Health hands out the push Reporter of each component the module declared
	// with Registrar.Health (05-health §5H.3.1): Reporter returns
	// health.ErrNotOwned for any other ID. It is never nil. The registrations are
	// taken at P7, so a Reporter call before then returns ErrHealthNotBound.
	Health health.Reporters
}

// RefreshFunc is Deps.AuthRefresh: auth.Service.Refresh. It has the underlying
// type of a legacy-auth bridge's own refresh hook, so a bridge takes the handle
// by plain assignment.
type RefreshFunc func(ctx context.Context, p *auth.Principal) (*auth.Principal, error)

// LifecycleFunc is Deps.AuthLifecycle: auth.Service.RegisterLifecycle.
type LifecycleFunc func(l auth.Lifecycle) error

// ConfigAccess is Deps.Config: *config.Store's reads, one write, its
// generation and the secrets the module's declaration admits. The store is
// attached before any module's Register runs (P4d), so Apply works from
// Register on. It is three embedded interfaces, not ten methods: each is a
// handle a caller can take alone, and the interface-width ratchet counts the
// declared entries.
type ConfigAccess interface {
	ConfigReads
	ConfigWrite
	ConfigSecrets
}

// ConfigReads is the read side of ConfigAccess.
type ConfigReads interface {
	// Lookup is a key's effective state (value, source, lock, protection,
	// pending restart, secret metadata); false for an unregistered path.
	Lookup(p config.Path) (config.KeyState, bool)
	// Keys is every registered key in path order.
	Keys() iter.Seq2[config.Path, config.KeyState]
	// Generation moves on every commit and on Attach.
	Generation() uint64
	// OnCommit registers fn to run after every committed Apply with the new
	// generation (config.Store.OnCommit): the hook an App-wide projection
	// rebuilds on, whoever wrote.
	OnCommit(fn func(gen uint64))
	// PassthroughValues are the operator-layer values of passthrough paths
	// (integrations.<kind>.<instance>.<field>), raw and sorted by path.
	PassthroughValues() iter.Seq2[config.Path, config.OperatorValue]
}

// ConfigWrite is the one write of ConfigAccess.
type ConfigWrite interface {
	// Apply validates and commits a patch in one batch (05-config 1.4 step 8).
	Apply(ctx context.Context, p config.Patch, a config.Actor) (config.Result, error)
}

// ConfigSecrets is the secret side of ConfigAccess.
type ConfigSecrets interface {
	// Secret, PutSecret and DeleteSecret reach the secret store by name, for
	// names Manifest.AppConfig.Secrets admits; any other name is refused with
	// secrets.ErrOutOfScope. A name that is a registered config key is
	// readable (the key's Lookup carries only metadata) but not writable here:
	// PutSecret and DeleteSecret refuse it, because Apply writes it and so
	// keeps its audit, protection and lock.
	Secret(ctx context.Context, name secrets.Name) (secrets.Value, secrets.Meta, error)
	PutSecret(ctx context.Context, name secrets.Name, v secrets.Value, by string) (secrets.Meta, error)
	DeleteSecret(ctx context.Context, name secrets.Name, by string) error
	// ListSecrets is the Meta (never a value) of every set secret whose name
	// starts with prefix and that Manifest.AppConfig.Secrets admits, sorted by
	// name. A name the gate refuses is left out, not reported, so the list can
	// never reveal that a record exists outside the declaration. It lets a module
	// find the secrets of an entry it cannot enumerate by name (the credentials of
	// a metadata source).
	ListSecrets(ctx context.Context, prefix string) ([]secrets.Meta, error)
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

// ErrTasksNotBound is what Deps.Tasks returns while the kernel has no task
// engine for the module: before P7 builds it, or in an app that runs none.
var ErrTasksNotBound = errors.New("module: Deps.Tasks is not bound: the task engine is built at P7 and only when the app runs one")

// ErrHealthNotBound is what Deps.Health.Reporter returns before the kernel has
// taken the module's Registrar.Health declarations at P7.
var ErrHealthNotBound = errors.New("module: Deps.Health is not bound: the module's components are registered at P7")
