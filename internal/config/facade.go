// file: internal/config/facade.go
// version: 1.1.0
// guid: 4d1f6a0e-7b52-4c39-9e8a-2a6c0b5d7e13
// last-edited: 2026-11-19

package config

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	coreconfig "github.com/falkcorp/mediacore/config"
)

// This file is the Facade's contract (F3-07 e2; 05-config section 1.12.1 item
// 2): the types the settings routes, the Basic gate and telemetry code against
// while the implementation is built. Nothing here has behaviour.
//
// The Facade is the per-App value that sits behind this package's exported
// names (AppConfig, Snapshot, Mutate, Generation, SettingLocks,
// EnvLockedSettings). Today those read a process-wide Config that HEAD's viper
// loader fills. From the flip they read the Facade's projection: HEAD's flat
// Config rebuilt, through the rename table (internal/books/cutover), from core
// config's keys and the secret store, on every core generation change. A write
// is one coreconfig.Store.Apply, so a key's protection, operator lock,
// validation and audit are core's.
//
// Names in this file are HEAD's flat keys (the JSON names of Config's fields,
// "scan_on_startup", "openai_api_key"), the vocabulary /api/v1/config speaks
// and the SPA reads. Core paths ("modules.books.scan.on_startup") never cross
// this interface: the Facade owns the mapping, so a handler never sees one.

// Access is the App's core config store as the Facade reads and writes it.
// *coreconfig.Store satisfies it; the kernel hands the books module one
// (module.Deps.Config), and a test hands a configtest store.
type Access interface {
	// Lookup is the key's effective state: value, source, lock, protection,
	// whether a restart-bound change is pending, and its secrets.Meta for a
	// secret key (never a value).
	Lookup(p coreconfig.Path) (coreconfig.KeyState, bool)
	// Keys is every registered key in path order.
	Keys() iter.Seq2[coreconfig.Path, coreconfig.KeyState]
	// Apply is the one write: authorize, validate, commit rows, staged
	// secrets and the audit record in one batch, publish, bump the generation.
	Apply(ctx context.Context, p coreconfig.Patch, a coreconfig.Actor) (coreconfig.Result, error)
	// Generation moves on every committed Apply and on Attach.
	Generation() uint64
}

// Actor is who an Update acts as; the settings handler fills it from the gin
// context (Subject = HEAD's user ID, Interactive =
// auth.Method.MayChangeCredentials(), Owner = HEAD's owner proof, Can = the
// principal's HEAD atoms mapped through books' atom table).
type Actor = coreconfig.Actor

// KeyClass is a rename-table row's class (cutover.Class), as the settings page
// needs it: how a key is stored decides how it is edited.
type KeyClass string

const (
	// KeyClassKey is a plain core or books key: Value is its effective value.
	KeyClassKey KeyClass = "k"
	// KeyClassSecret is write-only: View carries Secret, never a Value, and
	// Update takes the new plaintext. HEAD's masks and echo detection are gone.
	KeyClassSecret KeyClass = "s"
	// KeyClassInput is an integrations field that stays in the residual
	// settings blob until F7: the Facade reads and writes the blob row with
	// books' frozen codec, so it is editable but not a core key.
	KeyClassInput KeyClass = "i"
	// KeyClassEnv is operator-layer only (env or file): shown, never writable.
	KeyClassEnv KeyClass = "o"
)

// KeyView is one entry of GET /api/v1/config.
type KeyView struct {
	// Name is HEAD's flat key.
	Name string `json:"name"`
	// Class decides how the page edits it.
	Class KeyClass `json:"class"`
	// Value is the effective value as JSON; nil for a secret.
	Value json.RawMessage `json:"value,omitempty"`
	// Secret is set for KeyClassSecret and carries only what the secret store
	// says about the record: set, source, updated_at, fingerprint.
	Secret *SecretMeta `json:"secret,omitempty"`
	// LockedBy says what makes the key read-only: "env ABO_CORE_LOG_LEVEL",
	// "flag --db", "file <name>", "" when it is free. Same reasons as
	// SettingLocks.
	LockedBy string `json:"locked_by,omitempty"`
	// RestartPending is true when a restart-bound change is stored and not yet
	// effective.
	RestartPending bool `json:"restart_pending,omitempty"`
	// Admin is true when the key needs ProtectAdmin or higher, so the page can
	// say why an editor refused it before the request is made.
	Admin bool `json:"admin,omitempty"`
}

// SecretMeta is secrets.Meta without the fields a response must not carry (the
// key ID and the error text). A record that cannot be decrypted reports
// Unreadable, which the page shows as "must be re-entered".
type SecretMeta struct {
	Set         bool      `json:"set"`
	Source      string    `json:"source,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Unreadable  bool      `json:"unreadable,omitempty"`
}

// View is the whole of GET /api/v1/config: every key, ordered by Name, at one
// generation. Keys is never nil.
type View struct {
	Generation uint64    `json:"generation"`
	Keys       []KeyView `json:"keys"`
}

// Update is the body of PUT /api/v1/config as the Facade takes it: HEAD's flat
// keys to their new JSON values. A secret key's value is the plaintext to
// store; an empty JSON string clears it (Unset names it explicitly). Keys in
// both Set and Unset are refused. The Facade turns the whole Update into one
// coreconfig.Patch, so it commits all or none.
type Update struct {
	Set   map[string]json.RawMessage
	Unset []string
}

// UpdateResult is what an applied Update did, in HEAD's key names.
type UpdateResult struct {
	Generation     uint64
	Applied        []string
	RestartPending []string
	// PostCommit is non-nil when the write committed and an after-commit hook
	// failed (a *coreconfig.PostCommitError): report it, do not roll back. It
	// joins core's own hook failures with those of the UpdateHooks.
	PostCommit error
	// Ops is what the UpdateHooks started, response key to operation id:
	// "dedup_rescore_op_id" when a dedup weight change queued a re-band. A
	// handler copies each pair into the response body under the same key.
	Ops map[string]string
}

// UpdateHook runs once after an Update commits and the projection has been
// rebuilt, with the projection before and after. It starts whatever the change
// implies (the dedup re-band when the score ladder moved) and returns the
// operations it started by response key. A hook that errs does not undo the
// write: the error lands in UpdateResult.PostCommit. Mutate does not run hooks;
// only the API write does, as the update service's sinks did.
type UpdateHook func(ctx context.Context, before, after Config) (ops map[string]string, err error)

// Facade is the per-App configuration value. The exported package names
// delegate to the installed one. Every method is safe for concurrent use.
//
// Update's errors are core's, so a handler maps them with errors.Is and
// errors.As: coreconfig.ErrUnknownKey (a key the rename table does not know,
// reported by its HEAD name), ErrForbidden, ErrProtected (*ProtectedError),
// ErrLocked (*LockedError), ErrInvalid (a join of *FieldError, with HEAD
// names), and ErrKeyReadOnly for a KeyClassEnv key.
type Facade interface {
	// Snapshot is the projection as a value copy: what Snapshot() returns.
	Snapshot() Config
	// Mutate applies fn to a copy of the projection and turns the difference
	// into one Update through the rename table. It is what Mutate(fn) calls,
	// and it keeps that function's contract (callers hold no error today):
	// a refused write is returned and leaves the projection unchanged.
	Mutate(fn func(*Config)) error
	// Generation moves whenever the projection was rebuilt or Mutate ran.
	Generation() uint64
	// Locks is SettingLocks: every locked setting to what locks it.
	Locks() map[string]string
	// EnvLocked is EnvLockedSettings: the locked settings' names, sorted.
	EnvLocked() []string
	// View is GET /api/v1/config's content.
	View() View
	// Update is PUT /api/v1/config's write.
	Update(ctx context.Context, a Actor, u Update) (UpdateResult, error)
}

// ErrKeyReadOnly is Update's refusal of a KeyClassEnv key: it is set by the
// operator layers and nothing is stored for it.
var ErrKeyReadOnly = errKeyReadOnly{}

type errKeyReadOnly struct{}

func (errKeyReadOnly) Error() string {
	return "config: key is set by the operator layers and is not writable"
}

// *coreconfig.Store is the production Access.
var _ Access = (*coreconfig.Store)(nil)
