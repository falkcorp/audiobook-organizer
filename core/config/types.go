// file: core/config/types.go
// version: 1.1.0
// guid: e2e5754c-03e1-42ea-83cb-5ae606cfee3a
// last-edited: 2026-11-10

package config

import (
	"context"
	"encoding/json"
	"iter"
	"log/slog"
	"os"
	"time"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Options configures a Store (05-config §1.3.3). The kernel builds it from
// app.Options.
type Options struct {
	AppID     string   // app.Options.AppID, e.g. "abo"; schema $id
	EnvPrefix string   // app.Options.EnvPrefix, default strings.ToUpper(AppID) ("ABO"); [A-Z][A-Z0-9]{1,15}
	Environ   []string // os.Environ() captured once by app.New; config never calls os.Getenv
	FilePath  string   // operator YAML file; "" = none. Read-only: core never writes it.
	// Flags holds ONLY the flags the operator explicitly set (FromFlagSet or
	// the app's adapter).
	Flags map[Path]string
	// BootRecordRoot is called by Boot with core.paths.state_dir as
	// ResolveStateDir resolved it (env, flags, compiled default, HEAD's
	// fallback; never the file or the record it locates, §1.7); the kernel
	// opens and returns the os.Root that holds config.boot.json (§1.3.5). The
	// Store closes the root at Close. nil = no boot record (an app without a
	// state directory): boot keys then keep their operator or default value
	// and Apply refuses them.
	BootRecordRoot func(stateDir string) (*os.Root, error)
	// Passthrough reports the operator-layer paths config reads raw and does
	// not own: the integrations.<kind>.<instance>.<field> paths of
	// 05-metadata M1 (C11).
	Passthrough func(Path) bool
	// EnvBindings are the app's ecosystem env names for passthrough paths
	// (ABO: OPENAI_API_KEY and OPENAI_BASE_URL), from
	// app.Options.ConfigEnvBindings. Read by the env layer under the
	// canonical-beats-binding rule (§1.3.5); a name listed twice fails
	// NewStore, one equal to a canonical name fails Register (ErrDuplicate).
	EnvBindings []EnvName
	// Retired is the app's list of retired env names and file keys (§1.3.6),
	// from app.Options.RetiredConfigNames. Boot and Load act on it
	// (retired.go); RetiredSet lists what is set.
	Retired []RetiredName
	Logger  *slog.Logger
	Metrics *metrics.Scope   // core scope "mediacore.config" (05-metrics); or Store.Instrument later
	Secrets secrets.Resolver // may be nil until secrets opens; Refs resolve after SetSecrets
	Now     func() time.Time
	// OwnerSet reports whether an owner identity is set and an owner verifier
	// is configured (name coined by F3-01: 05-config §1.3.7 checks
	// ProtectOwner with Actor.Owner only then, and neither Options nor Actor
	// carried the fact). nil = no owner set: ProtectOwner is checked as
	// ProtectAdmin.
	OwnerSet func() bool
}

// EnvName is one ecosystem env name bound to a path.
type EnvName struct {
	Name string
	Path Path
}

// RetiredName is one env name or top-level operator-file key an app read
// before core (§1.3.6).
type RetiredName struct {
	Name      string // the env name, or the flat top-level file key (its nested keys included)
	File      bool   // true: an operator-file key; false: an env name
	Canonical Path   // the key it became; "" when the key was dropped (its use only warns)
	// Refuse is read only when Canonical is a passthrough path: config cannot
	// see an instance field's class, so the app states it.
	Refuse  bool
	Message string // optional reason (ABO: HEAD's removed-key messages)
}

// RetiredUse is one retired name found set (§1.3.6).
type RetiredUse struct {
	Name         string
	File         bool
	Canonical    Path   // "" for a dropped key
	CanonicalEnv string // the canonical env name, "" for a dropped key
	Refused      bool   // a protected or secret key: this boot was refused
}

// BootView exposes the resolved ReloadBoot and ProtectOperator keys of core
// sections (module enablement, core.paths.*, core.storage.path) before
// module sections exist and before storage opens.
type BootView interface {
	Bool(p Path) (v bool, src Source, ok bool)
	Raw(p Path) (json.RawMessage, Source, bool)
}

// OperatorValue is one raw operator-layer value with its origin, so
// integrations can show a lock reason.
type OperatorValue struct {
	Value json.RawMessage
	Src   Source // SourceFile, SourceEnv or SourceFlag
	By    string // "file <name>", "env ABO_…", "env OPENAI_API_KEY", "flag <path>"
}

// OverrideStore is the store interface of the DB layer (R4-1). Core ships its
// one implementation, core/internal/configstore over keyspace c/config/; no
// app supplies one. Paths and values are canonical.
// configtest.OverrideStoreSuite is its contract (§1.11).
type OverrideStore interface {
	// Load returns every stored DB-layer value. Read-only: it never writes,
	// renders or repairs.
	Load(ctx context.Context) iter.Seq2[Override, error]
	// Stage joins b with the writes for set and unset: only those keys
	// change. A read-modify-write reads with GetForUpdate on b, so a
	// concurrent writer fails the commit with storage.ErrConflict and the
	// Committer retries.
	Stage(ctx context.Context, b *storage.Batch, set []Override, unset []Path) error
}

// Importer is config's write side for a one-way cutover step (03 §3.3
// "importers"; §1.12.2). It is valid after Load.
type Importer interface {
	// Stage validates v against p's registered schema and stages p's
	// c/config/ row into b. A value equal to p's default stages nothing (C4).
	// It refuses with ErrUnknownKey a path that is not registered or is a
	// passthrough path, with ErrInvalid a secrets.Ref key, a ProtectOperator
	// key and a ReloadBoot key, and returns *FieldError for an invalid value.
	Stage(b *storage.Batch, p Path, v json.RawMessage, by string) error
	// Default returns p's registered default.
	Default(p Path) (json.RawMessage, error)
}

// SecretStager stages one secret write into the caller's batch:
// secrets.(*Store).Stage (name coined by F3-OIDC-SECRET). The Importer
// deletes through it the secrets of the list entries a list it replaces
// drops or rebinds (Store.SetImportSecrets).
type SecretStager interface {
	Stage(ctx context.Context, b *storage.Batch, w secrets.Write) error
}

// Committer is implemented by the kernel (core/internal/configstore). Commit
// opens ONE storage batch and stages into it OverrideStore.Stage for c.Set
// and c.Unset, the secret writes of c.Secrets and the audit record
// core.settings.update, and retries with storage.RetryOnConflict, each
// attempt a fresh batch. config never imports audit (L4).
type Committer interface {
	Commit(ctx context.Context, c Commit) error
}

// Override is one stored DB-layer value.
type Override struct {
	Path  Path
	Value json.RawMessage
	Gen   uint64
	At    time.Time
	By    string // audit subject
}

// Commit is one Apply's durable change.
type Commit struct {
	Set     []Override
	Unset   []Path
	Secrets []secrets.Write // staged into the same batch via the secrets store
	Gen     uint64
	// Boot names the boot-record keys this Apply changed (field coined by
	// F3-01). They are written to <state_dir>/config.boot.json before Commit,
	// never staged, and listed here only so the audit record names them
	// (05-rbac §7.1: section and changed key names).
	Boot []Path
	// By is the subject of the Actor whose Apply produced the commit
	// (Actor.Subject; field coined by F3-01). The committer names it as the
	// audit actor, so a commit that only unsets keys or only changes boot keys
	// still carries its writer. Every Override.By and secrets.Write.By of the
	// commit equals it.
	By string
}

// Patch is the API write unit. Set values are JSON; for a secret key the JSON
// string is the secret value and listing it in Unset deletes it.
type Patch struct {
	Set   map[Path]json.RawMessage
	Unset []Path
}

// Actor is filled by the HTTP layer (05-http-api) from the request's auth
// context, so config never imports auth.
type Actor struct {
	Subject     string
	Interactive bool                   // auth.Method.MayChangeCredentials()
	Owner       bool                   // owner proof verified
	Can         func(perm string) bool // rbac check; ProtectAdmin asks Can("core:admin")
}

// Result is Apply's outcome.
type Result struct {
	Generation     uint64
	Applied        []Path
	RestartPending []Path
	PostCommit     error // *PostCommitError, joined; non-nil means "committed, but a hook failed"
}

// Source is the layer a key's effective value comes from.
type Source uint8

const (
	SourceDefault Source = iota + 1
	SourceFile
	SourceEnv
	SourceFlag
	SourceBootRecord
	SourceOverride
)

// String names a Source.
func (s Source) String() string {
	switch s {
	case SourceDefault:
		return "default"
	case SourceFile:
		return "file"
	case SourceEnv:
		return "env"
	case SourceFlag:
		return "flag"
	case SourceBootRecord:
		return "boot_record"
	case SourceOverride:
		return "override"
	}
	return "unknown"
}

// operator reports whether s is an operator layer (file, env or flag).
func (s Source) operator() bool { return s == SourceFile || s == SourceEnv || s == SourceFlag }

// KeyState describes one key for the settings UI (§1.3.4).
type KeyState struct {
	Path        Path
	Section     Path
	Schema      json.RawMessage // this key's sub-schema
	Effective   json.RawMessage // nil for secrets
	Source      Source
	LockedBy    string // "env ABO_CORE_LOG_LEVEL", "flag core.storage.path", "file <name>", "" when free
	Reload      Reload
	Protection  Protection
	FailClosed  bool          // the key is FailClosed (§1.3.5)
	Pending     bool          // a restart-required change is stored but not effective
	Quarantined error         // stored override ignored at boot (§1.4)
	Secret      *secrets.Meta // non-nil for secret keys
}

// AttachReport is what Attach found in the DB layer.
type AttachReport struct {
	Applied     int
	Unknown     []Path          // retained rows for keys this binary does not declare
	Quarantined map[Path]error  // rows ignored because their section failed to decode/validate with them
	Ignored     map[Path]string // stored values shadowed by an operator lock (rows, and boot-record values) -> lock reason
	FailClosed  map[Path]string // FailClosed keys whose stored value wins -> the operator source it beats (§1.3.5)
}
