<!-- file: docs/specs/platform-core/05-config.md -->
<!-- version: 1.0.0 -->
<!-- guid: fb637cbc-bbfa-4aea-807c-71f02534b3c9 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Config and secrets (`core/config`, `core/secrets`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §2, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7, G12;
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md) finding 3;
[`raw/03-layering-guard.md`](../../plans/platform-core/raw/03-layering-guard.md) `allowed` rows 1–5;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md). Normative names come from
[03-architecture](03-architecture.md) (R4 layer 2 = `config`, `secrets`, `storage`; R5 no globals;
§3.6 `core.*` / `modules.<id>.*`). Compatibility windows follow
[02-decision-record](02-decision-record.md) §2.5 Q5 (alias for two minor releases, then refusal).

## Decisions at a glance

| # | Decision | Why (short) |
|---|---|---|
| C1 | One `config.Store` per `App`; no package-level config value anywhere in core (R5). | `var AppConfig Config` (`internal/config/config.go:1939`) cannot host two apps and is read racily by design (`config.go:1930-1938`). |
| C2 | **Config owns the mechanism, never a section's type.** Each section struct lives in the package that consumes it and is registered through the manifest as `config.Section[T]`. | This is what breaks all upward imports (§1.9): config imports `aidispatch`, `dedup/unified`, `backup`, `auth`, `database` today only because it owns their types. |
| C3 | Merge order defaults → file → env → flags → DB overrides, with **operator lock**: a key supplied by file, env or flag cannot be overridden from the DB layer and `Apply` refuses it with the lock reason. | Generalizes ABO's per-key `envLockedSettings`/`flagLockedSettings` (`config.go:2241,2353`) to every key; today a DB blob silently beats env for all other keys (`persistence.go:915` then `config.go:2435`). |
| C4 | DB overrides are **per-key rows holding only explicitly set values**, not a whole-struct blob. | The blob freezes every default at save time (`persistence.go:1180-1188` measured 33 keys inheriting stale defaults). |
| C5 | Every key declares `Reload` (live / restart) and `Protection`; both appear in the generated JSON Schema. | ABO has exactly one boot-only key handled ad hoc (`persistence.go:2111`) and a 92-entry protection map (`protected_fields.go`) separate from the schema. |
| C6 | Drop **viper** from core and, by the end of migration, from ABO. Stdlib + `go.yaml.in/yaml/v3` only. | §1.10. |
| C7 | Env names are `<APP>_<PATH>`; no `MEDIACORE_` prefix. ABO's existing names are aliases for two minor releases (OTel standard `OTEL_*` names are permanent aliases). | §1.7. |
| C8 | Every recoverable secret is encrypted at rest by `core/secrets` (AES-256-GCM, keyring with key IDs, AAD = app + name); config fields are `secrets.Ref`, never strings. Verifiers (passwords, API keys, session ids) are hashed by `auth`, never stored in `secrets`. | `raw/10` G12: 4 of at least 10 secret-bearing fields are encrypted, and 3 of those 4 are also written in plaintext to a mirror YAML file (`persistence.go:168-178`). |
| C9 | Secrets are **write-only** over the API: the UI receives `{set, source, updated_at, fingerprint}`, never a mask. | Masks reveal the last 2 characters (`internal/database/settings.go:250-277`) and force echo-detection logic (`update_service.go:353-373`). |

---

## Part 1 — `core/config`

### 1.1 Purpose & scope

`core/config` turns layered sources into **typed, validated, immutable snapshots** per section, persists
UI-made changes, tells subscribers what changed, and describes every key (type, default, reload class,
protection, secret-ness, lock state) for the settings UI. It does not parse command lines, does not
open storage, and does not know any section's fields. Out of scope: secrets storage (Part 2), the HTTP
routes (specified as semantics in §1.3.4, mounted by [05-http-api](05-http-api.md)), the permission check
itself ([05-rbac](05-rbac.md)).

### 1.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Shape | One `type Config struct`, 864 lines, **173 top-level fields** (this writer's count of field lines in the struct), 247 `SetDefault` registrations | `internal/config/config.go:1048-1912`, `registerConfigDefaults` `config.go:2621` |
| Global | `var mu sync.RWMutex`, `var AppConfig Config`, `Snapshot()` copy under RLock, `Mutate(fn)` under Lock, `generation atomic.Uint64` bumped by every Mutate | `config.go:1928,1939,1944,2011,2023,2035` |
| Single-field locked readers | `SupportedExtensionSet`, `AuthorityEvidenceEnabled`, `RetireVersionsApplyEnabled`, `KeptSweepApplyEnabled` | `config.go:1971-2006` |
| Sources, in actual application order | (1) viper defaults + `AutomaticEnv` (no prefix) + 131 explicit `BindEnv` + viper YAML file `<home>/.audiobook-organizer.yaml` or `--config`, decoded by `configFromViper`; (2) 4 flags bound (`dir`, `db`, `db-type`, `playlists`) plus `--log-level` written straight into `AppConfig`; (3) DB `config_blob` **replaces the whole struct**; (4) env-authoritative keys re-applied (auth-surface keys unconditionally, operational keys only when env supplies them); (5) a second YAML file `<db dir>/config.yaml` fills 4 empty fields; (6) `SyncConfigFromEnv` re-applies 6 keys from env if non-empty | `cmd/root.go:603-660`, `cmd/root.go:55-60`, `cmd/root.go:650`, `config.go:2586-2597,3215`, `persistence.go:915,1229`, `config.go:2435`, `persistence.go:42-104`, `cmd/root.go:381`, `persistence.go:2023` |
| Two config files | viper reads `<home>/.audiobook-organizer.yaml`; `SaveConfigToFile` writes `<db dir>/config.yaml` (merged, atomic, 0600) on every DB save — a different file — and `LoadConfigFromFile` reads it back as a fallback | `cmd/root.go:614-623`, `persistence.go:29-38,121-195`, comment `config.go:2290-2292` |
| DB persistence | whole non-secret struct as JSON in setting `config_blob`; 4 secrets as encrypted rows; ten audiobook-specific `migrate*Blob` funcs; boot-only key stripped from blob and pinned | `persistence.go:1932-2016`, `persistence.go:359-824`, `persistence.go:2111,2126-2139` |
| Locks | `envLockedSettings` (env var per key, ~10 keys), `flagLockedSettings` (`database_path` → `--db`), config-file lock for Deluge keys; `SettingLockedBy`/`SettingLocks` feed the UI | `config.go:2241-2289,2353-2425` |
| Protection | 92-entry `configFieldRules` map of JSON path → class (`FieldSignIn`, `FieldExecutable`, `FieldDatabase`, `FieldServerPath`); `UpdateService` refuses protected changes unless `auth.Method.MayChangeCredentials()`; owner trust-root separately | `protected_fields.go:17-34,38-60`, `update_service.go:770-800` |
| Write API | `UpdateService.UpdateConfig(ctx, payload map[string]any)` — unknown-key detection by reflection, mask-echo detection, env-only refusal, owner refusal, protected refusal, `Validate`, persist; dedup score ladder hand-off via `DedupScoreConfigSink` returning "saved but not queued" | `update_service.go:533`, `unknown_keys.go:41`, `update_service.go:353-373`, `update_service.go:40-60` |
| Hot reload | none from files (no watcher, no SIGHUP — `raw/12` §2); reload is write-through `Mutate` + `Generation()` polling, used by exactly one consumer | `internal/ai/ai_metrics.go:130,169` |
| Global call sites | outside `internal/config`, non-test: **676** `config.AppConfig` occurrences in **215** files reading **113** distinct fields (plus 32 whole-struct uses); `config.Snapshot()` 87 in 53 files; `config.Mutate(` 5 in 3 files; `config.SupportedExtensionSet()` 18 in 16 files. Tests: 2,456 `AppConfig` occurrences in 341 files, 151 `Mutate(` in 58 files. Top fields: `RootDir` 132, `Scheduled` 46, `ITunes` 34, `DatabasePath` 31, `Maintenance` 29 | this writer's grep at `748fd787a`; `raw/12` §2 reports 214 files |
| Writes that bypass `Mutate` (races, invisible to `Generation`) | 8 assignments in 5 non-test files | `cmd/root.go:650`, `internal/metafetch/openlibrary.go:78`, `internal/server/handlers/scheduler_admin.go:275,533-535`, `internal/server/handlers/system/handler.go:585-586` |
| DI side door | `func init()` registers config in `serviceregistry`, returning the global | `internal/config/register.go:12,49` |
| Upward imports (layer-1 package reaching layer 2/3) | `database` (`persistence.go:22`, `register.go`, `update_service.go`, `ai_endpoints.go`), `auth` (`update_service.go:781,822`), `backup` (`config.go:24`), `aidispatch` (`ai_endpoints.go:94-179`), `dedup/unified` (`dedup_score_config.go:42-60`, `update_service.go:22,48`); also `audioext`, `tools`, `logger`, `pathutil`, `serviceregistry` | `raw/02` finding 3; `raw/03` `allowed` rows 1–5 (`internal/arch/layering_test.go:291-295`) |
| viper outside config | `cmd/root.go`, `internal/dedup/unified/config.go:222-226` reads viper directly | grep |

### 1.3 Go API

Package `github.com/falkcorp/mediacore/config`. Imports: stdlib, `go.yaml.in/yaml/v3`,
`github.com/falkcorp/mediacore/secrets` (for `secrets.Ref` and `secrets.Resolver` only). It does **not**
import `storage`, `log`, `auth` or any module (§1.9).

#### 1.3.1 Declaring a section (module side)

```go
package config

// Path is a dotted key path: "core.log.level", "modules.books.scan.interval".
// Grammar: seg = [a-z][a-z0-9_]{0,47}; path = seg ("." seg)*; first seg is "core" or "modules".
type Path string

func (p Path) Validate() error
func (p Path) Parent() Path
func (p Path) Join(seg string) Path

// Reload says when a change takes effect.
type Reload uint8

const (
	ReloadLive    Reload = iota + 1 // published to Get() and OnChange on Apply
	ReloadRestart                   // persisted on Apply; Get() keeps the boot value until restart
)

// Protection says who may change a key through Apply (the DB layer).
type Protection uint8

const (
	ProtectNone        Protection = iota + 1 // the section's write permission suffices
	ProtectInteractive                       // also an interactive, credential-changing session (05-authn)
	ProtectOwner                             // also the owner proof (05-authn)
	ProtectOperator                          // never writable via Apply: file / env / flag only
)

// Duration is a time.Duration that (un)marshals as a Go duration string ("90s", "24h").
type Duration time.Duration

func (d Duration) Std() time.Duration
func (d Duration) MarshalJSON() ([]byte, error)
func (d *Duration) UnmarshalJSON(b []byte) error

// Validator is implemented by section types that need cross-field checks.
// It runs on every candidate snapshot before commit; return errors.Join of field errors
// wrapped with FieldError so the UI can attach them to keys.
type Validator interface{ Validate() error }

type FieldError struct {
	Path Path
	Err  error
}

func (e *FieldError) Error() string
func (e *FieldError) Unwrap() error

// Section is a declared, unbound section. Define never fails; Register validates.
type Section[T any] struct{ /* unexported: path, defaults, opts */ }

func Define[T any](path Path, defaults T, opts ...SectionOption) Section[T]

type SectionOption func(*sectionSpec)

func Permissions(read, write string) SectionOption        // rbac strings; default derived from path (§1.3.4)
func Docs(d map[Path]string) SectionOption                // key -> description for schema/UI
func LegacyKeys(m map[string]Path) SectionOption          // ABO flat key -> path (compat window)
func EnvAliases(m map[string]Path) SectionOption          // extra env names -> path (compat window)
func PermanentEnvAliases(m map[string]Path) SectionOption // ecosystem names (OTEL_*) — never deprecated
func Deprecated(p Path, replacement Path, removeIn string) SectionOption
```

Field metadata is struct tags on `T` (reflected once at `Register`):

| Tag | Meaning | Example |
|---|---|---|
| `json:"name"` | key segment; decoding uses `encoding/json`, so there is **one** decoder (today: dual `json` + `mapstructure` tags, e.g. `config.go:1636,1773`) | `json:"interval"` |
| `reload:"restart"` | `ReloadRestart`; default live | `reload:"restart"` |
| `protect:"interactive\|owner\|operator"` | `Protection`; default none | `protect:"operator"` |
| `schema:"…"` | JSON-Schema keywords: `minimum=`, `maximum=`, `enum=a\|b`, `format=duration\|uri\|cidr\|path\|email`, `pattern=` | `schema:"minimum=1,maximum=64"` |
| `sensitive:"true"` | not a secret, but redacted in diagnostics bundles and logs (emails, allow-lists) | owner e-mail |
| field type `secrets.Ref` | secret; schema `writeOnly: true`; value lives in `secrets` (Part 2) | `APIKey secrets.Ref \`json:"api_key"\`` |

Example (books module, ABO repo):

```go
type ScanSettings struct {
	Interval      config.Duration `json:"interval" schema:"format=duration"`
	Concurrency   int             `json:"concurrency" schema:"minimum=1,maximum=64"`
	OnStartup     bool            `json:"on_startup"`
	SortIndexes   []string        `json:"sort_indexes" reload:"restart"`
	HardcoverKey  secrets.Ref     `json:"hardcover_api_token" protect:"interactive"`
}

func (s ScanSettings) Validate() error { /* cross-field checks */ return nil }

var Scan = config.Define("modules.books.scan", ScanSettings{Interval: config.Duration(6 * time.Hour), Concurrency: 2},
	config.LegacyKeys(map[string]config.Path{"concurrent_scans": "modules.books.scan.concurrency"}),
	config.EnvAliases(map[string]config.Path{"CONCURRENT_SCANS": "modules.books.scan.concurrency"}))
```

#### 1.3.2 Binding and reading (module side)

```go
// Registry is handed to modules as module.Registrar.Config(). It has unexported methods,
// so only the kernel can implement it. Registration is legal only before Store.Load.
type Registry interface {
	register(spec *sectionSpec) (*binding, error)
}

// Register binds a section. Generic free function because interface methods cannot have type parameters.
func Register[T any](r Registry, s Section[T]) (*Handle[T], error)

// Handle is the live, typed view of one section. Safe for concurrent use. Never nil after Register.
type Handle[T any] struct{ /* atomic.Pointer[snapshot[T]] */ }

func (h *Handle[T]) Path() Path
func (h *Handle[T]) Get() *T          // current effective snapshot; READ-ONLY; one atomic load
func (h *Handle[T]) Pending() *T      // stored value incl. restart-required changes; == Get() when none pending
func (h *Handle[T]) Generation() uint64

// OnChange runs fn on a dedicated goroutine after each commit that changed this section's
// effective value. Deliveries coalesce: a slow fn sees only the latest snapshot. ctx is cancelled at Store.Close.
func (h *Handle[T]) OnChange(fn func(ctx context.Context, prev, next *T)) (stop func())

// AfterCommit runs fn synchronously inside Apply, after the durable commit, in registration order.
// It cannot veto (validation is Validate's job); a returned error is reported to the caller as
// *PostCommitError while the change stays committed. This preserves ABO's "saved but rescore not
// queued" contract (update_service.go:30-48).
func (h *Handle[T]) AfterCommit(fn func(ctx context.Context, prev, next *T) error) (stop func())
```

`Get` returns a pointer to an immutable snapshot built by deep-decoding (JSON round trip) at publish time,
so no two snapshots share slices or maps (ABO needed `config_deepcopy.go` for this). Mutating it is a bug;
`coretest` fingerprints every published snapshot and fails the test if one changed (§1.8).

#### 1.3.3 The store (kernel side)

```go
type Options struct {
	AppID     string            // "abo"; schema $id and AAD context
	EnvPrefix string            // "ABO"; [A-Z][A-Z0-9]{1,15}
	Environ   []string          // os.Environ() captured once by app.New; config never calls os.Getenv
	FilePath  string            // operator YAML file; "" = none. Read-only: core never writes it.
	Flags     map[Path]string   // ONLY flags the operator explicitly set (FromFlagSet / app adapter)
	Logger    *slog.Logger
	Secrets   secrets.Resolver  // may be nil until secrets opens; Refs resolve after SetSecrets
	Now       func() time.Time
}

func NewStore(o Options) (*Store, error)
func (s *Store) Registry() Registry
func (s *Store) Load(ctx context.Context) error                        // seals Registry; defaults+file+env+flags
func (s *Store) SetSecrets(r secrets.Resolver)
func (s *Store) AttachOverrides(ctx context.Context, o OverrideStore) (AttachReport, error)
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error)
func (s *Store) Lookup(p Path) (KeyState, bool)
func (s *Store) Keys() iter.Seq2[Path, KeyState]                         // sorted; secret values never included
func (s *Store) Schema(include func(section Path) bool) (json.RawMessage, error)
func (s *Store) Generation() uint64
func (s *Store) Close(ctx context.Context) error

// FromFlagSet converts a stdlib FlagSet to Options.Flags using Visit (explicitly-set flags only).
// Apps on cobra/pflag write the same 10-line adapter over pflag.FlagSet.Visit; core does not import pflag.
func FromFlagSet(fs *flag.FlagSet, bind map[string]Path) map[Path]string

// OverrideStore persists the DB layer. Implemented by kernel (core/internal/configstore) over the
// storage keyspace c/config/; config itself never imports storage (§1.9).
type OverrideStore interface {
	Load(ctx context.Context) iter.Seq2[Override, error]
	// Commit writes set/unset rows and the staged secret writes in ONE storage batch.
	Commit(ctx context.Context, c Commit) error
}

type Override struct {
	Path  Path
	Value json.RawMessage
	Gen   uint64
	At    time.Time
	By    string // audit subject
}

type Commit struct {
	Set     []Override
	Unset   []Path
	Secrets []secrets.Write // staged into the same batch via secrets.(*Store).Stage
	Gen     uint64
}

// Patch is the API write unit. Set values are JSON; for a secret key the JSON string is the
// secret value and JSON null in Unset semantics deletes it.
type Patch struct {
	Set   map[Path]json.RawMessage
	Unset []Path
}

// Actor is filled by the HTTP layer (05-http-api) from the request's auth context, so config never imports auth.
type Actor struct {
	Subject     string
	Interactive bool                  // auth.Method.MayChangeCredentials()
	Owner       bool                  // owner proof verified
	Can         func(perm string) bool // rbac check for section permissions
}

type Result struct {
	Generation     uint64
	Applied        []Path
	RestartPending []Path
	PostCommit     error // *PostCommitError, joined; non-nil means "committed, but a hook failed"
}

type Source uint8

const (
	SourceDefault Source = iota + 1
	SourceFile
	SourceEnv
	SourceFlag
	SourceOverride
)

type KeyState struct {
	Path        Path
	Section     Path
	Schema      json.RawMessage // this key's sub-schema
	Effective   json.RawMessage // nil for secrets
	Source      Source
	LockedBy    string // "env ABO_CORE_LOG_LEVEL", "flag --db", "file <path>", "" when free
	Reload      Reload
	Protection  Protection
	Pending     bool          // a restart-required change is stored but not effective
	Quarantined error         // stored override ignored at boot (§1.4)
	Secret      *secrets.Meta // non-nil for secret keys
	Alias       []string      // legacy names that address this key
}

type AttachReport struct {
	Applied     int
	Unknown     []Path                // retained rows for keys this binary does not declare (downgrade-safe)
	Quarantined map[Path]error        // rows ignored because their section failed to decode/validate with them
	Ignored     map[Path]string       // rows shadowed by an operator lock -> lock reason
}
```

Sentinel and typed errors:

```go
var (
	ErrUnknownKey  = errors.New("config: unknown key")
	ErrLocked      = errors.New("config: key is set by the operator")      // wrapped in *LockedError
	ErrProtected   = errors.New("config: key needs a stronger credential")  // wrapped in *ProtectedError
	ErrForbidden   = errors.New("config: missing section permission")
	ErrInvalid     = errors.New("config: invalid value")                  // errors.Join of *FieldError
	ErrSealed      = errors.New("config: registry sealed after Load")
	ErrDuplicate   = errors.New("config: duplicate path or env name")
)

type LockedError struct{ Path Path; By string }
type ProtectedError struct{ Path Path; Need Protection }
type PostCommitError struct{ Section Path; Err error }
```

#### 1.3.4 Settings API semantics (routes owned by 05-http-api)

| Route | Permission | Semantics |
|---|---|---|
| `GET /api/core/v1/settings/schema` | any section read permission | JSON Schema 2020-12 document, one property per section the caller may read. Vendor keywords: `x-mediacore-reload` (`live`\|`restart`), `x-mediacore-protect`, `x-mediacore-secret`, `x-mediacore-aliases`; standard `writeOnly: true` on secrets, `deprecated: true` on deprecated paths. |
| `GET /api/core/v1/settings` | per section read | `{path: KeyState}` for readable keys. Secrets appear as `Secret` meta only. |
| `PATCH /api/core/v1/settings` | per section write + protection | Body `{"set": {path: value}, "unset": [path]}` → `Store.Apply`. 400 unknown key, 409 locked (`locked_by` in body), 403 forbidden/protected, 422 invalid (field errors), 500 storage. 200 with `restart_pending` and `post_commit_error` when present. |

Default section permissions: `core.*` → read and write `core:settings.manage`; `modules.<id>.*` → read and
write `<id>:settings.manage` (registered by the module in its permission list; [05-rbac](05-rbac.md)).
ABO's `settings.manage` atom maps to both during the compat window ([09-abo-on-core](09-abo-on-core.md)).

#### 1.3.5 Precedence and locks (normative)

Merge order, lowest to highest: **defaults → file → env → flags → DB overrides**. One rule constrains the
last layer: if any operator layer (file, env, flag) supplies a key, that key is **locked**: its DB row is
ignored at load (listed in `AttachReport.Ignored`, kept in storage, not deleted) and `Apply` refuses it with
`*LockedError{By}`. Keys with `ProtectOperator` are never read from or written to the DB layer at all.
Consequently the DB layer only ever overrides defaults, which is what an operator expects: what they put
in the file, environment or command line is what runs, and the UI says so.

Within operator layers flags beat env beat file. For one key, the canonical env name beats an alias; if
both are set with different values the canonical wins and a warning plus health token
`config_alias_conflict` is raised (§1.5).

#### 1.3.6 Environment naming

Canonical name = `<EnvPrefix>_` + upper(path) with `.` → `_`, with the leading `MODULES_` dropped for
module paths (unambiguous because `core` is a reserved module ID, 03 §3.6):
`core.log.level` → `ABO_CORE_LOG_LEVEL`; `modules.books.scan.interval` → `ABO_BOOKS_SCAN_INTERVAL`.
Any key may instead be given as `<NAME>_FILE=<path>` (contents read once at `Load`, one trailing newline
trimmed) — the supported way to hand secrets in from systemd `LoadCredential=` or container secrets.
Object and array keys take JSON (`ABO_CORE_LOG_LEVELS='{"books":"debug"}'`). `Load` fails with
`ErrDuplicate` if two paths map to the same canonical name or alias. Unknown `<EnvPrefix>_*` variables
produce one warning each (fail-open, ABO's rule that a stale key must not stop startup, `config.go:2587-2590`).

No `MEDIACORE_` variable exists: the operator of an app should not need to know the library's name, and
per-app prefixes keep two apps' variables apart on one host or — lowest priority — in one process.

### 1.4 Lifecycle

Inside `app.New` (03 §3.7), in this order:

1. `config.NewStore(opts)`; the kernel registers core sections, then every enabled module's sections
   (from manifests), then `Store.Load`. Registration after `Load` → `ErrSealed`, boot fails.
2. `Load` builds defaults, reads the file (parse error = boot error), env, flags; validates each section with
   operator layers only. A section that fails here fails boot (the operator must fix their input).
3. The kernel builds logging and metrics from `core.log` / `core.metrics` (operator layers only).
4. Storage opens using `core.storage.path` (`ProtectOperator`: needed before the DB layer exists).
5. `secrets.Open`, then `Store.SetSecrets`.
6. `Store.AttachOverrides`: rows are decoded per section; if a section fails decode or `Validate` with its
   overrides but passes without them, **all of that section's overrides are quarantined** (kept, ignored,
   reported, health token `config_overrides_quarantined`) and boot continues. Rationale: an operator cannot
   repair the DB without booting; ABO's equivalent path is fatal (`cmd/root.go:385-391`).
7. Runtime: `Apply` = authorize → decode candidate per touched section → `Validate` → `OverrideStore.Commit`
   (overrides + staged secrets, one batch) → publish snapshots for live keys (atomic swap) → bump generation
   → `AfterCommit` hooks → wake `OnChange` subscribers.
8. `Close` cancels subscriber contexts and waits up to the drain timeout.

Restart-required keys: `Apply` commits them and returns them in `RestartPending`; `Get` keeps the boot value;
`Pending` shows the stored value; `KeyState.Pending` drives a "restart required" badge in the shell.

### 1.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Config file exists but unreadable or unparsable | fail-closed (boot error) | operator intent unknown; ABO ignores viper read errors (`cmd/root.go:627`) — behaviour change, release-noted |
| Unknown key in file / unknown prefixed env var | fail-open, warn once | stale keys must not stop startup (`config.go:2587-2590`) |
| Legacy key / env alias used | fail-open, applied, deprecation warning once + metric | compat window (Q5) |
| Canonical and alias both set, different | fail-open, canonical wins, warn, health `config_alias_conflict` | an obvious mistake, but not worth an outage |
| Operator value fails decode or `Validate` | fail-closed | operator must fix input |
| `<NAME>_FILE` unreadable | fail-closed | a missing credential file is a deployment error |
| Override rows unreadable (I/O) | fail-closed | running on defaults would let the next save overwrite stored config (`cmd/root.go:360-364`, comment at `:361`) |
| Override row fails decode/validate | fail-open per section: quarantine (§1.4 step 6) | DB cannot be fixed without booting |
| Override row for unknown key | fail-open: retained, ignored, reported | downgrade safety (gate G5, 02 §2.4) |
| `Apply` unknown key | refuse `ErrUnknownKey`, nothing persisted | typos must not silently no-op |
| `Apply` locked key | refuse `*LockedError` | the value would be discarded at next boot (`config.go:2224-2229` rationale) |
| `Apply` missing permission / protection | refuse `ErrForbidden` / `*ProtectedError` | fail-closed security |
| `Apply` validation failure | refuse `ErrInvalid` with `*FieldError`s | nothing partially applied |
| `OverrideStore.Commit` fails | fail-closed: nothing published, generation unchanged | durable-before-visible |
| `AfterCommit` hook error | committed; `Result.PostCommit` set | hooks cannot veto; caller must know (ABO contract `update_service.go:30-48`) |
| `OnChange` fn panics | fail-open: recovered, logged, metric; others continue | one subscriber must not break config |
| `Ref.Resolve` on unset secret | `secrets.ErrNotSet` to the consumer | consumer decides (e.g. provider disabled) |
| Duplicate path / env-name collision / invalid path | fail-closed at registration | programming error |

### 1.6 Concurrency contract

- `Handle.Get`, `Generation`, `Lookup`, `Keys` are lock-free (one `atomic.Pointer` load per section; `Keys`
  iterates an immutable published index).
- `Apply` calls are serialized by one store mutex held from candidate build through publish, replacing ABO's
  `configBlobSaveMu` (`persistence.go:1897`) and `mu`. Publish happens after `Commit` returns.
- `OnChange`: one goroutine per subscription with a one-slot coalescing mailbox; never blocks `Apply`.
  Subscription count is bounded by code (registered at init). `stop()` is idempotent and waits for an
  in-flight call.
- `AfterCommit`: runs on the `Apply` goroutine with `Apply`'s `ctx`; total hook time is bounded by the
  request deadline. Hooks must enqueue work (`tasks`) rather than do it.
- No goroutine is started before `Load`; `Close` cancels and joins all (verified with `testing/synctest`).

### 1.7 Config keys (core-owned sections)

This file owns only `core.config.*` and `core.paths.*`; other `core.*` sections are owned and tabled by their
specs ([05-logging](05-logging.md), [05-metrics](05-metrics.md), [05-storage](05-storage.md),
[05-http-api](05-http-api.md), [05-authn](05-authn.md), [05-backup](05-backup.md), [05-updater](05-updater.md),
[05-tasks](05-tasks.md), [05-ai](05-ai.md), [05-download](05-download.md), [05-notify](05-notify.md)).

| Key | Type | Default | Hot reload? |
|---|---|---|---|
| `core.paths.state_dir` | path | app-defined (ABO: its compiled default, `internal/config/state_dir.go:54`) | no (`ProtectOperator`) |
| `core.paths.data_dir` | path | `<state_dir>/data` | no (`ProtectOperator`) |
| `core.config.file` | path | `""` (also `--config`, `<APP>_CONFIG`) | no (`ProtectOperator`) |
| `core.config.legacy_aliases` | bool | `true` for two minor releases after cut-over, then removed | no |
| `core.config.subscriber_drain_timeout` | duration | `5s` | no |

### 1.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_config_applies_total` | counter | `outcome` ∈ applied, unknown_key, locked, forbidden, protected, invalid, storage_error |
| `mediacore_config_generation` | gauge | — |
| `mediacore_config_restart_pending_keys` | gauge | — |
| `mediacore_config_quarantined_overrides` | gauge | — |
| `mediacore_config_legacy_uses_total` | counter | `kind` ∈ env, file, api (the key is logged, not labelled) |
| `mediacore_config_post_commit_failures_total` | counter | `section` (closed set: registered sections) |
| `mediacore_config_subscriber_panics_total` | counter | `section` |

### 1.9 Breaking config's upward imports

| Import today | What it is used for | Where it goes |
|---|---|---|
| `internal/database` (`SettingsStore`, `Setting`, `DecryptValue`, `MaskSecret`, `IsEchoedMask`, `LooksLikeMask`) | blob/row persistence, secret decrypt, masks (`persistence.go:883-1260`, `update_service.go:91-373`) | persistence → `OverrideStore` interface implemented by kernel over `storage`; decrypt → `secrets`; masks → deleted (write-only, C9); the ABO compat handler keeps mask echo logic for the window only |
| `internal/auth` (`MethodFromContext().MayChangeCredentials`, `AccessEmailFromContext`) | protected-field and owner checks (`update_service.go:781,822`) | `config.Actor` filled by the HTTP layer; protection is per-key metadata |
| `internal/backup` (`LevelDefault`, `ResolveMaxTotalBytes`) | backup defaults documented in config (`config.go:1086-1107`) | `core/backup` defines and registers `core.backup` itself (backup at layer 3 imports config at layer 2 — legal) |
| `internal/aidispatch` (`Endpoint`, `Protocol`, `ParseCapability`) | AI endpoint conversion/validation (`ai_endpoints.go:94-179`) | `core/integrations/ai` owns `core.ai` section type and its `Validate` |
| `internal/dedup/unified` (`ScoreConfig`, `LoadScoreConfig`) + `DedupScoreConfigSink` | dedup score ladder (`dedup_score_config.go:42-60`, `update_service.go:48`) | books module owns `modules.books.dedup` type; the sink becomes an `AfterCommit` hook on that handle |
| `internal/audioext`, `internal/tools`, `internal/pathutil` | extension set, tool modes, iTunes paths (`config.go:1971,3717`, `itunes_libraries.go`) | books module section types |
| `internal/logger` | logging (`update_service.go`, `chapter_consolidation.go`) | `*slog.Logger` in `Options` |
| `internal/serviceregistry` + `init()` | DI side door (`register.go:12,49`) | deleted; kernel container (06) |

Enforced by R4 (`core/internal/archtest_layers_test.go`) and a targeted test
`TestConfigImportsOnlyStdlibYAMLAndSecrets` (§1.11).

### 1.10 viper: drop it

| Criterion | viper v1.21.0 (current, `raw/15`) | koanf | stdlib + yaml (chosen) |
|---|---|---|---|
| Instance-only (R5) | no: ABO uses the process-wide instance `viper.GetViper()` (`config.go:2590`) and package funcs (`dedup/unified/config.go:222`) | yes | yes |
| Per-key source/lock tracking | no — ABO bolted on `MarkFlagExplicit`, `envLockedSettings`, `flagLockedSettings`, `configFileSupplied` (`config.go:2203,2241,2353`) | no | yes, by construction (`KeyState.Source/LockedBy`) |
| Semantics traps | `IsSet` is true for every key with a `SetDefault`; ABO's own comment documents a past bug from believing otherwise (`config.go:2058-2063`); case-insensitive keys; `AutomaticEnv` with no prefix makes every key env-settable by bare name | fewer | none: one decoder (`encoding/json`), exact-case paths |
| Decoders | mapstructure + json tags must agree (dual tags throughout `config.go:1590-1700`) | mapstructure-like | `encoding/json` only |
| Direct deps pulled | 9 (fsnotify, mapstructure, go-toml, locafero, afero, cast, pflag, gotenv, yaml) per `go mod graph` | ~3 | 1 (`go.yaml.in/yaml/v3`) |
| What we would still have to write | schema, locks, reload classes, protection, overrides, secrets refs | same | same, plus ~150 LOC of file/env parsing |

YAML library: `go.yaml.in/yaml/v3` **v3.0.6** (latest stable on the module proxy, checked 2026-11-04;
`go.yaml.in/yaml/v4` is still `v4.0.0-rc.9`). It is the maintained continuation of `gopkg.in/yaml.v3`
(v3.0.1, direct dep today, `go.mod`). ABO keeps cobra/pflag in its own `main` (CLI is the app's business).

JSON Schema: generated by `core/internal/jsonschema`, the **same reflector** `internal/openapi` uses for
OpenAPI 3.1 ([05-http-api](05-http-api.md)) so `config.Duration`, `secrets.Ref`, enums and formats render
identically in both documents. No third-party reflector: the vendor keywords and shared type rules would
need extension hooks anyway, and one in-house reflector (~400 LOC) is cheaper than two divergent ones.

### 1.11 Tests (core must have)

| Test | Asserts |
|---|---|
| `TestPrecedence_Matrix` | table over every (default, file, env, flag, override) combination: effective value, `Source`, `LockedBy` |
| `TestOverride_IgnoredWhenOperatorLocked` | DB row kept, not applied, reported in `AttachReport.Ignored` |
| `TestApply_RefusesLocked` / `_Protected` / `_Forbidden` / `_UnknownKey` / `_Invalid` | error type, nothing persisted, generation unchanged |
| `TestApply_RestartKeyNotPublished` | `Get` unchanged, `Pending` changed, `RestartPending` returned |
| `TestApply_CommitFailureNothingPublished` | fake `OverrideStore` error |
| `TestApply_SecretAndOverrideOneBatch` | `Commit.Secrets` and `Commit.Set` arrive together |
| `TestAttach_QuarantineInvalidSection` | boot continues, section on operator view, health token set |
| `TestOnChange_CoalescesUnderSynctest` | `testing/synctest`: 100 applies, slow subscriber sees ≤2 calls, last = latest |
| `TestAfterCommit_ErrorReportedNotRolledBack` | `Result.PostCommit` non-nil, value committed |
| `TestEnvName_Derivation` / `TestEnvName_CollisionRejected` | naming rule; `ErrDuplicate` |
| `TestEnvFileSuffix` | `_FILE` read once, newline trimmed, unreadable = boot error |
| `TestSchema_Golden` | schema of a fixture section equals `testdata/schema.golden.json` |
| `TestSchema_SecretIsWriteOnly` / `TestKeys_NeverContainSecretValues` | secret never in `Effective` or schema defaults |
| `TestSnapshot_ImmutableUnderRace` | `-race`; `coretest` fingerprint check |
| `TestRegistry_SealedAfterLoad` | `ErrSealed` |
| `TestConfigImportsOnlyStdlibYAMLAndSecrets` | `go list -deps` allow-list for `core/config` |
| `FuzzEnvDecode`, `FuzzPatchDecode` | no panic, typed errors only |

### 1.12 Migration from ABO

**Strangler, four stages** (gated by the plan; each stage keeps `make ci` green):

1. **Fix first (stage 0, in ABO, no core needed):** convert the 8 bypassing writes (§1.2) to `Mutate`, and add
   `TestNoDirectAppConfigWrites` (AST: no assignment to `config.AppConfig` outside `internal/config`). Without
   this, stage 1's projection would silently drop those writes.
2. **Stage 1 — façade.** ABO's `internal/config` keeps its exported surface but becomes a façade over the core
   store: `AppConfig` is a **projection** rebuilt under the façade's `mu` on every core generation change
   (one `OnChange` per section, assembled by the books module's `legacyconfig.Projector`); `Mutate(fn)` diffs
   the before/after `Config` and turns it into one `Store.Apply` through the key map below; `Snapshot`,
   `Generation`, `SettingLocks`, `EnvLockedSettings` read core `KeyState`. All 215 files and 341 test files
   compile unchanged; tests use a `coretest` store behind the façade. viper is removed from the boot path
   here (ABO's `configFromViper` is replaced by core `Load` + legacy aliases).
3. **Stage 2 — accessor.** The books module exposes `booksettings.Accessor` (ABO repo, `internal/books/settings`),
   constructed in the module's init from the core handles it registered plus the core handles it reads
   (`core.storage`, `core.auth`, …). Packages migrate from `config.AppConfig.X` to an injected `*Accessor`
   in the order of the per-package file counts: `internal/server` 71 files, `internal/plugins` 33,
   `internal/metafetch` 15, `internal/maintenance` 11, `internal/scanner` 9, `cmd` 9, `internal/audiobooks` 8,
   then the 28 packages with ≤6 files each. A ratchet test (`TestAppConfigRatchet`) holds a per-package
   ceiling that may only go down, like the repo's existing ratchets (`internal/logger/slog_guard_ratchet_test.go`).
4. **Stage 3 — delete.** When the ratchet reaches zero the façade, `AppConfig`, `Mutate`, `Snapshot`,
   viper and `config_blob` writing are deleted.

**Data migration** (one backed-up, idempotent boot migration owned by the books module):
`setting:config_blob` (`persistence.go:884`) → per-key override rows, importing **only values that differ
from the current shipped default** (so future default changes propagate; values a user set equal to the
default become "default" — release-noted); the four encrypted rows → `secrets` (Part 2 §2.10); plaintext
secret fields found in the blob → `secrets`, then blanked in the retained blob copy; `<db dir>/config.yaml`
values are imported the same way **once**, then the file is no longer read or written (operator removes it;
the boot log says so). The original `config_blob` row is kept untouched for one release so the previous
binary still opens the DB (gate G5).

**Key map (family level; the per-key golden `core/config/testdata/abo_keys.golden` is generated by walking
ABO's `Config` and is owned by [09-abo-on-core](09-abo-on-core.md)).** Of the 173 top-level keys (nested
sub-keys move with their parent), 64 match platform families and 109 are audiobook; 6 of the 64
(Cloudflare guest provisioning) stay app-side per `raw/10` G11, giving **58 → `core.*`, 115 → `modules.books.*`**.

| ABO flat key family | Count | New path | Owner spec |
|---|---|---|---|
| `database_path`, `database_type` | 2 | `core.storage.path`, (`database_type` retired: Pebble only, `config.go:1053`, `config.go:3837`) | 05-storage |
| `api_rate_limit_per_minute`, `json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`, `public_base_url`, `tunnel_connector_addrs`, `allowed_peer_cidrs`, `login_saturated_*` | 9 | `core.http.*` | 05-http-api |
| `log_level`, `log_retention_days` | 2 | `core.log.level`, `core.log.journal.retention_days` | 05-logging |
| `otel_*` (4), `telemetry_environment` | 5 | `core.metrics.*`, `core.tracing.*` | 05-metrics |
| `enable_auth`, `basic_auth_*`, `oauth_*`, `cf_access_*`, `owner_email`, `bootstrap_key_ttl*`, `write_startup_readonly_key` | 18 | `core.auth.*` | 05-authn |
| `cf_account_id`, `cf_guests_policy_id`, `cf_policy_api_token`, `cf_guest_service_tokens`, `cf_service_auth_policy_id`, `invite_email_service_token` | 6 | `modules.books.cfguests.*` | app-side (G11) |
| `smtp_*` | 5 | `core.notify.mail.*` | 05-notify |
| `backup_dir`, `backup_max_total_bytes`, `backup_compression[_level]`, `backup_retention_days` | 5 | `core.backup.*` | 05-backup |
| `auto_update` | 1 | `core.updater.*` | 05-updater |
| `operation_timeout_minutes`, `operation_log_retention_days`, `maintenance`, `scheduled` | 4 | `core.tasks.*`; each `scheduled.<task>` entry moves to its task kind's namespace | 05-tasks |
| `openai_api_key`, `openai_base_url`, `ai_endpoints`, `ai_endpoints_routing`, `ai_backend`, `ai_journal_retention_days` | 6 | `core.ai.*` | 05-ai |
| `download_client` | 1 | `core.download.clients` | 05-download |
| everything else (scan, organize, naming, metadata, dedup, whisper, fingerprint, iTunes, ABS, Deluge web, activity log, catalog, search, embedding, tools, plugins, …) | 109 | `modules.books.<family>.*` | 09 |

Env compatibility: every ABO env name is registered as an alias of its new path for the window — the 131
explicit `BindEnv` names (e.g. `ACOUSTID_API_KEY`, `FP_LENGTH_SEC`, `ABS_AUTH_PROBE`, `config.go:2702-2747`),
the bare upper-case form of every flat key that viper's prefix-less `AutomaticEnv` made settable
(`cmd/root.go:625`), and the `ABK_*` operational names (`ABK_STATE_DIR` `state_dir.go:25`,
`ABK_ALLOW_ENCRYPTION_KEY_REGEN` `cmd/root.go:733`). `OTEL_EXPORTER_OTLP_ENDPOINT` and
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` (`config.go:2746-2747`) are permanent aliases. Flags: ABO's cobra
adapter maps `--dir`, `--db`, `--db-type`, `--playlists`, `--log-level` to paths.

### 1.13 Open items

1. Whether `encoding/json/v2` is GA in go1.27.3; if so, decode sections with it (strict unknown-field
   handling and `omitzero` are better fits). Decide in wave 0 with the toolchain spec ([13-toolchain](13-toolchain.md)).
2. Exact per-key reload class for ABO keys: default every books key to `ReloadLive` only where the consumer
   reads the handle per use; keys captured at construction (e.g. worker counts) are `ReloadRestart` until the
   consumer subscribes. The per-key audit is a 09 task.
3. Whether the shell needs `x-mediacore-ui` hints (grouping, ordering, widgets) in the schema or a separate
   UI contribution ([07-ui-shell](07-ui-shell.md)).

---

## Part 2 — `core/secrets`

### 2.1 Purpose & scope

Encrypted-at-rest storage for **recoverable** credentials the app must present to someone else (provider API
keys, OAuth client secrets, download-client passwords, SMTP password, signing keys), key management
(keyring, rotation, loss handling), the `Value` type that cannot be printed, and `Ref` for config fields.
Not in scope: **verifiers** — passwords, API keys, session ids, bootstrap/temp-login tokens — which `auth`
stores as hashes and never needs to recover ([05-authn](05-authn.md); session ids stored raw today,
`internal/database/pebble_store_auth.go:1656-1663`, is fixed there).

### 2.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Cipher | AES-256-GCM, random nonce, base64; **no key id, no AAD** (a row's ciphertext can be copied to another key's row and decrypts) | `internal/database/settings.go:189-235` |
| Key | 32 raw bytes in `.encryption_key`, 0600, in the secure state dir (`ABK_STATE_DIR`, compiled default), legacy fallback `<root_dir>/.appdata/`; package-global `encryptionKey` | `settings.go:42-52,110-175`, `internal/config/state_dir.go:25,54,133` |
| Encrypted fields | exactly 4: `openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`, as `IsSecret` rows; zeroed in the blob | `persistence.go:1955-1958,1989-1994` |
| **Same 4 also in plaintext on disk** | `SaveConfigToFile` writes `openai_api_key`, `google_books_api_key`, `hardcover_api_token` in clear to `<db dir>/config.yaml` ("file permissions protect them") on every save | `persistence.go:107-108,168-178` |
| Key loss | `guardAgainstKeyRegeneration` refuses boot if no key and encrypted rows exist (override `ABK_ALLOW_ENCRYPTION_KEY_REGEN`); if a key does regenerate, `LoadConfigFromDatabase` re-encrypts what the YAML mirror can recover and **deletes** every other secret row | `cmd/root.go:726-783`, `persistence.go:1191-1260` |
| Rotation | none | `raw/10` §7 |
| API exposure | `MaskSecrets` returns `********` + last 2 chars for secrets ≥16 runes; PUT restores echoed masks | `settings.go:250-277`, `update_service.go:88-150,353-373` |
| Log redaction | query-string token masking in access log/recovery; invite path redaction; `cfservicetoken.Created.LogValue` | `internal/server/server.go:695-704`, `internal/logging/redact_path.go:107`, `internal/cfservicetoken/client.go:191` |

**Secret-bearing fields not encrypted at rest** (verified: the blob builder zeroes only the 4 above,
`persistence.go:1955-1958`, and `marshalConfigBlob` strips only boot-only keys, `persistence.go:2126-2139`):

| Field (JSON key) | Anchor | At rest today | New name (secret) |
|---|---|---|---|
| `acoustid_api_key` | `config.go:1141` | plaintext `config_blob` (masked in API, `update_service.go:94`) | `modules.books.acoustid.api_key` |
| `oauth_github_client_secret` | `config.go:1588` | plaintext `config_blob` (value re-applied from env each boot, still written) | `core.auth.oauth.github.client_secret` |
| `oauth_google_client_secret` | `config.go:1590` | plaintext `config_blob` | `core.auth.oauth.google.client_secret` |
| `deluge_web_password` | `config.go:1777` | plaintext `config_blob` (carried forward when env-locked, `persistence.go:1965-1971`) | `modules.books.deluge.web_password` |
| `download_client.torrent.deluge.password` | `config.go:240` | plaintext `config_blob` | `core.download.clients[<id>].password` |
| `metadata_sources[].credentials` (map) | `config.go:154` | plaintext `config_blob` | `modules.books.metadata.sources.<id>.credentials.<k>` (one secret per entry) |
| `plugins[].settings` (free-form map) | `config.go:244-247` | plaintext `config_blob`; may hold credentials (unknowable) | retired with `internal/plugin` (02 N4); any surviving entry declared per adapter |
| `abs_jwt_secret` | `config.go:1636` (`json:"-"`) | env / viper file only | `modules.books.abs.jwt_secret` |
| `smtp_password` | `config.go:1678` (`json:"-"`) | env / viper file only | `core.notify.mail.password` |
| `cf_policy_api_token` | `config.go:1688` (`json:"-"`) | env / viper file only | `modules.books.cfguests.policy_api_token` |
| userinfo in URLs (`openai_base_url`, `whisper_remote_url`, `ai_endpoints[].url`, `embedding.base_url`) | `config.go:1222,1319`, `ai_endpoints.go:40` | plaintext if an operator embeds `user:pass@` | `schema:"format=uri"` validation **rejects** userinfo; credentials go in a sibling `secrets.Ref` |

### 2.3 Go API

Package `github.com/falkcorp/mediacore/secrets`. Imports stdlib (`crypto/aes`, `crypto/cipher`,
`crypto/hkdf`, `crypto/hmac`, `crypto/sha256`, `crypto/rand`, `os`), `github.com/falkcorp/mediacore/storage`.

```go
package secrets

// Name uses config.Path's grammar; by default a secret's name is its config path.
type Name string

// Value holds secret bytes. Every formatting path is redacted; only Reveal/Bytes expose it.
type Value struct{ b []byte }

func NewValue(s string) Value
func (v Value) Reveal() string
func (v Value) Bytes() []byte                     // a copy
func (v Value) IsZero() bool
func (Value) String() string                      // "[REDACTED]"
func (Value) GoString() string                    // "secrets.Value([REDACTED])"
func (Value) Format(f fmt.State, verb rune)       // every verb prints [REDACTED]
func (Value) LogValue() slog.Value                // slog.StringValue("[REDACTED]")
func (Value) MarshalJSON() ([]byte, error)        // returns ErrNotSerializable: accidental API exposure fails loudly
func (Value) MarshalText() ([]byte, error)        // same

// Ref is a config field type. config's decoder fills name (default: the field's path) and binds the resolver.
type Ref struct {
	name Name
	r    Resolver
}

func RefTo(n Name) Ref
func (r Ref) Name() Name
func (r Ref) Resolve(ctx context.Context) (Value, error) // ErrNotSet, ErrKeyUnavailable, ErrUnbound
func (r Ref) MarshalJSON() ([]byte, error)              // {"secret": "<name>"} — never the value
func (r *Ref) UnmarshalJSON(b []byte) error

type Resolver interface {
	Get(ctx context.Context, n Name) (Value, Meta, error)
	Meta(ctx context.Context, n Name) (Meta, error)
}

type Source uint8

const (
	SourceStored Source = iota + 1 // encrypted in storage
	SourceEnv                      // operator env (incl. <NAME>_FILE); in memory only, never persisted
	SourceFile                     // operator config file; in memory only
)

type Meta struct {
	Name        Name
	Set         bool
	Source      Source
	KeyID       string    // stored only
	UpdatedAt   time.Time
	UpdatedBy   string
	Fingerprint string    // first 8 hex of HMAC-SHA256(fingerprint subkey, value); stable per value, not reversible
	Err         error     // ErrKeyUnavailable / ErrCorrupt when the row cannot be decrypted
}

type Write struct { // staged by config.Store.Apply into OverrideStore.Commit
	Name   Name
	Value  Value // zero Value + Delete=true deletes
	Delete bool
	By     string
}

type Options struct {
	StateDir      *os.Root          // opened by the kernel on core.paths.state_dir
	KeyFile       string            // relative to StateDir; default "secrets.keyring"
	Keyspace      storage.Keyspace  // c/secrets/
	AppID         string            // bound into AAD
	AllowKeyRegen bool              // core.secrets.allow_key_regen (operator only)
	Legacy        *LegacyImport     // ABO .encryption_key import (§2.10); nil otherwise
	Logger        *slog.Logger
	Now           func() time.Time
}

type LegacyImport struct {
	Dirs []*os.Root // where a raw 32-byte ".encryption_key" may live (state dir, legacy dir)
}

func Open(ctx context.Context, o Options) (*Store, error)

func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
func (s *Store) Stage(b storage.Batch, w Write) error           // same-batch write used by configstore
func (s *Store) Ephemeral(n Name, v Value, src Source)           // operator-supplied value; locks the key
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]  // never values
func (s *Store) Rotate(ctx context.Context, by string) (RotateReport, error)
func (s *Store) PruneKeys(ctx context.Context, inUse func(keyID string) bool) (pruned []string, err error)
func (s *Store) KeyIDsInUse(ctx context.Context) ([]string, error) // recorded in backup manifests
func (s *Store) Close() error

type RotateReport struct {
	NewKeyID     string
	Reencrypted  int
	Undecryptable []Name // left untouched under their old key id
}

var (
	ErrNotSet          = errors.New("secrets: not set")
	ErrKeyUnavailable  = errors.New("secrets: encryption key for this value is not in the keyring")
	ErrCorrupt         = errors.New("secrets: ciphertext failed authentication")
	ErrUnbound         = errors.New("secrets: Ref used before the store was attached")
	ErrNotSerializable = errors.New("secrets: Value must not be serialized")
	ErrKeyfileInsecure = errors.New("secrets: keyring file permissions are too open")
	ErrKeyMissing      = errors.New("secrets: no keyring and encrypted values exist")
)
```

**Formats (normative).**

- Keyring file `<state_dir>/secrets.keyring`, 0600, directory 0700, written atomically via `os.Root`
  (temp + fsync + rename):
  `{"format":"mediacore-keyring/1","keys":[{"id":"k3","created":"…","status":"active|retired","material":"<base64 32 bytes>"}]}`.
  Exactly one `active`.
- Per-purpose subkeys via `crypto/hkdf` (SHA-256) from the active master: info
  `"mediacore/secrets/aes-256-gcm/v1"` (data key), `"mediacore/secrets/fingerprint/v1"` (HMAC key). Other
  specs may derive their own subkeys with distinct `info` strings through
  `(*Store).DeriveKey(info string) ([]byte, error)` (e.g. OAuth state codec, ABS refresh derivation) instead of
  separate secrets.
- Stored record at key `c/secrets/v/<name>`: JSON `{"v":1,"kid":"k3","nonce":"…","ct":"…","at":"…","by":"…"}`;
  AAD = `"mediacore/secrets/v1\x00" + AppID + "\x00" + name`, so a ciphertext moved to another name or app
  fails with `ErrCorrupt`.

### 2.4 Lifecycle

1. After storage opens (§1.4 step 5): `Open` resolves the keyring:
   - keyring present → load; mode looser than 0600 → try `chmod 0600`; if that fails → `ErrKeyfileInsecure`
     (fail-closed);
   - keyring absent, no stored records → generate `k1`, write keyring (first run);
   - keyring absent, stored records exist → `ErrKeyMissing` (boot refused, message names the file and the
     override). With `core.secrets.allow_key_regen=true` a new key is generated **and existing records are
     kept** under their old key id (`Meta.Err = ErrKeyUnavailable`); putting the old keyring back later
     recovers them. Nothing is ever deleted automatically (contrast `persistence.go:1232-1260`).
   - `Legacy` set and an ABO `.encryption_key` found → imported as key id `k0-legacy` (`status: retired`), then a
     new active key is generated and all legacy rows are re-encrypted with AAD in one batch (§2.10).
2. `config.Load` registered `Ephemeral` values for secret keys supplied by env/file.
3. Runtime: `Get` decrypts on demand (no plaintext cache beyond the call; callers hold `Value` briefly).
   `Put`/`Stage`/`Delete` encrypt with the active key.
4. `Rotate` (task `core.secrets.rotate`, permission `core:system.manage` with `ProtectInteractive`, audit-logged): generate `k(n+1)` → write
   keyring with new active + old retired → re-encrypt every decryptable record in **one** storage batch →
   report. A crash after the keyring write is harmless (records name their kid); the next boot finishes any
   stragglers in the background.
5. `PruneKeys` removes retired keys no record and no retained backup references (`inUse` is wired by the
   kernel from [05-backup](05-backup.md) manifests, which record `KeyIDsInUse` at backup time).

**Backups and loss.** The keyring lives in the state dir, not the data dir. Today no backup carries the key
(`internal/backup` archives the DB directory, `raw/12` §6; `grep` finds no key handling in `internal/backup`),
so a lost key file loses the secrets. Aligned with [05-backup](05-backup.md) B1/B3: the keyring is included
**only inside an encrypted archive** (age recipients) and never in an unencrypted one; the manifest records
`KeyIDsInUse` (all key ids referenced by stored records, not only the active one). Consequences: a stolen
encrypted backup yields nothing without a recipient identity; a lost keyring is recoverable from any encrypted
backup; with only unencrypted backups, a lost keyring loses only the secrets (data restores; each affected
secret shows "must be re-entered"). For operators without encrypted backups:
`<app> secrets export-keyring --out <file>` (CLI, filesystem access required, refuses to write a file looser
than 0600). Restore reports key ids missing from the current keyring before it starts.

### 2.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Keyring unreadable (exists) | fail-closed (boot) | never overwrite a key we failed to read (ABO's lesson, `settings.go:83-98`) |
| Keyring too permissive and not fixable | fail-closed | secret material exposure |
| No keyring, encrypted records exist | fail-closed unless `allow_key_regen`; with override, records kept, not deleted | recovery must stay possible |
| Record under unknown key id | fail-open per secret: `ErrKeyUnavailable`, health `secrets_undecryptable`, UI "re-enter" | the app keeps working; the integration using it reports itself unconfigured |
| AAD/auth failure | fail-open per secret: `ErrCorrupt`, same reporting | tampering or swap; never "repaired" automatically |
| `Put` storage error | fail-closed: error to caller, nothing changed | |
| `Ref.Resolve` with no value | `ErrNotSet` | consumer decides |
| `Value` JSON/text marshal | error `ErrNotSerializable` | accidental exposure fails the response instead of leaking |
| Rotation hits an undecryptable record | fail-open: left under old kid, listed in report, old key not pruned | partial rotation is safe |

### 2.6 Concurrency contract

`Store` is safe for concurrent use. Reads take a read lock on the in-memory keyring only; storage reads are
lock-free snapshots. `Put`/`Delete`/`Stage` serialize per name via a keyed mutex (the existing
`internal/keyedmutex` moves into core per 03 §3.2.1 E2). `Rotate` takes the store write lock for the
keyring swap and runs the re-encryption batch under it; secret counts are small (tens to low hundreds), so
one batch is bounded. No background goroutine except the post-boot straggler re-encryption, which honours
`ctx` and `Close`.

### 2.7 Config keys

| Key | Type | Default | Hot reload? |
|---|---|---|---|
| `core.secrets.key_file` | path (relative to state dir) | `secrets.keyring` | no (`ProtectOperator`) |
| `core.secrets.allow_key_regen` | bool | `false` (ABO alias `ABK_ALLOW_ENCRYPTION_KEY_REGEN`) | no (`ProtectOperator`) |
| `core.secrets.rotate.schedule` | duration | `0` (manual only) | yes |

### 2.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_secrets_operations_total` | counter | `op` ∈ get, put, delete, rotate; `outcome` ∈ ok, not_set, key_unavailable, corrupt, error |
| `mediacore_secrets_undecryptable` | gauge | — |
| `mediacore_secrets_keyring_keys` | gauge | `status` ∈ active, retired |
| `mediacore_secrets_active_key_age_seconds` | gauge | — |

### 2.9 Tests

| Test | Asserts |
|---|---|
| `TestValue_NeverFormats` | `%v %s %q %x %#v %+v`, `slog` text and JSON handlers, `errors.New(fmt.Sprint(v))` all print `[REDACTED]`; `json.Marshal` errors |
| `TestAAD_SwapBetweenNamesFails` | copy record A to B → `ErrCorrupt` |
| `TestRotate_ReencryptsAtomically` / `TestRotate_CrashAfterKeyringWrite` | one batch; restart finishes stragglers |
| `TestOpen_MissingKeyringWithRecordsRefuses` / `TestOpen_RegenKeepsOrphans` | `ErrKeyMissing`; override keeps rows; old keyring restores them |
| `TestOpen_KeyringPermissions` | 0644 fixed to 0600 or refused |
| `TestLegacyImport_ABOEncryptionKey` | raw 32-byte key + 4 legacy rows → readable, re-encrypted with AAD, `k0-legacy` retired |
| `TestEphemeral_NeverPersisted` | env secret never reaches storage |
| `TestFingerprint_StableNotReversible` | same value same fingerprint; differs per keyring |
| `TestPruneKeys_RespectsBackupInUse` | key referenced by a manifest is kept |

### 2.10 Migration from ABO

1. Boot migration (books module, backed up, idempotent): import `.encryption_key` (state dir, then legacy dir —
   the same search order as `database.InitEncryption`, `settings.go:110-140`) as `k0-legacy`; read the four
   `IsSecret` rows (`setting:<key>`, `settings.go:299`) with the legacy cipher, write them under their new
   names (§2.2 table) with the new format; read the plaintext secret fields from `config_blob` and import them
   likewise; blank them in the retained blob copy (the previous release can still read the blob; it will see
   empty secrets — documented downgrade cost).
2. `<db dir>/config.yaml`: secrets in it are imported only if the store lacks them; core never writes the file
   again (§1.12). The boot log tells the operator to delete it because it holds plaintext secrets.
3. Replace `database.EncryptValue/DecryptValue/InitEncryption/MaskSecret/IsEchoedMask/LooksLikeMask`
   (`settings.go:110,189,214,271,285,293`) and `cmd/root.go` `guardAgainstKeyRegeneration` (`cmd/root.go:735`) with
   `secrets.Open`. ABO's `/api/v1/config` compat handler returns masks during the window by composing
   `Meta` (`set`) with the old mask shape; the core settings API never does.
4. ABS JWT secret, SMTP password, Cloudflare policy token: env stays authoritative (aliases); a value
   entered in the UI is stored encrypted for the first time.

### 2.11 Open items

1. Whether `Rotate` should also be offered over HTTP (owner-only, interactive) or CLI-only. Recommendation: task
   + HTTP trigger with `ProtectOwner`, since the shell already shows tasks.
2. Hardware/OS keystores (TPM, macOS Keychain) as keyring backends: out of scope; the keyring file interface
   leaves room (`Options.StateDir` could become a `KeyringBackend`).
3. Cross-spec alignment: [05-authn](05-authn.md) writes its dependency as `secrets.Store` (value); this file
   defines `*secrets.Store` (it holds a keyring and locks) — authn should take `*secrets.Store` or the
   `secrets.Resolver` interface. [05-backup](05-backup.md)'s manifest field `SecretsKeyID` (one fingerprint)
   should become the set returned by `KeyIDsInUse`, because records may reference retired keys until
   `PruneKeys` runs.

---

## Objections to 03

1. **R4 layer 2 needs an internal order.** 03 puts `config`, `secrets`, `storage` in one layer, so a future
   `storage → config` import would be legal yet would force this design apart. Proposed amendment: within
   layer 2, `storage` < `secrets` < `config` (config imports secrets for `Ref`; secrets imports storage; storage
   imports neither), encoded as sub-layers in the core `LayerSpec`.
2. **§3.3 `config` row wording.** "defaults → file → env → flags → DB overrides" should add "operator-supplied
   keys are locked against DB overrides" (§1.3.5). Without it, the order reads as "the UI beats the operator",
   which is ABO's current accidental behaviour for most keys and the opposite of its explicit intent for the
   keys it has thought about (`config.go:2037-2066`, the doc comment of `applyEnvAuthoritativeConfig`,
   detached from the function at `config.go:2435`).

## Out-of-scope defects found

1. Eight writes to `config.AppConfig` bypass `Mutate` (§1.2 anchors): unsynchronized, and invisible to
   `config.Generation()` consumers (`internal/ai/ai_metrics.go:169`). Fixed in migration stage 0 because the
   façade depends on it.
2. Three encrypted secrets are also persisted in plaintext to `<db dir>/config.yaml` on every settings save
   (`persistence.go:168-178`), which defeats their encryption. Fixed by migration §2.10 step 2.
3. Secret ciphertext has no associated data (`settings.go:189-208`): rows are swappable between keys.
4. `LoadConfigFromDatabase` deletes secret rows it cannot decrypt or recover (`persistence.go:1253-1260`, `DeleteSetting` at `:1258`),
   turning a temporarily missing key file into permanent loss when the guard is overridden.
