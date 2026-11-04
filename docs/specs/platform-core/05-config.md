<!-- file: docs/specs/platform-core/05-config.md -->
<!-- version: 1.1.0 -->
<!-- guid: fb637cbc-bbfa-4aea-807c-71f02534b3c9 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Config and secrets (`core/config`, `core/secrets`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §2, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7, G12;
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md) finding 3;
[`raw/03-layering-guard.md`](../../plans/platform-core/raw/03-layering-guard.md) `allowed` rows 1–5;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md). Normative names come from
[03-architecture](03-architecture.md) v1.3.0: R4 layers **L1** `storage` → **L2** `secrets` → **L3** `config`
(`metrics` and `log` are L0; `compat` and `integrations` are L4, above config); R5 (no globals) binds **core**
only; §3.6 `core.*` / `modules.<id>.*`. The compatibility window is defined **once**, in
[09-abo-on-core](09-abo-on-core.md) §9.2.1, and implemented once in `compat`; config aliases reference it and
carry no window logic of their own.

## Decisions at a glance

| # | Decision | Why (short) |
|---|---|---|
| C1 | One `config.Store` per `App`; no package-level config value anywhere in **core** (R5). ABO keeps `config.AppConfig` as a books-module façade over core handles (§1.12.1). | `var AppConfig Config` (`internal/config/config.go:1939`) cannot host two apps and is read racily by design (`config.go:1930-1938`). R5 governs core only (03 §3.5), so ABO's façade is allowed. |
| C2 | **Config owns the mechanism, never a section's type.** Each section struct lives in the package that consumes it and is registered through the manifest as `config.Section[T]`. | This is what breaks all upward imports (§1.9): config imports `aidispatch`, `dedup/unified`, `backup`, `auth`, `database` today only because it owns their types. |
| C3 | Merge order defaults → file → env → flags → DB overrides, with **operator lock**: a key supplied by file, env or flag cannot be overridden from the DB layer and `Apply` refuses it with the lock reason. | Generalizes ABO's per-key `envLockedSettings`/`flagLockedSettings` (`config.go:2241,2353`) to every key; today a DB blob silently beats env for all other keys (`persistence.go:915` then `config.go:2435`). |
| C4 | DB overrides are **per-key rows in the new keyspace `c/config/`** holding only explicitly set values. Config never reads, writes or adopts the books families `setting:`, `preference:`, `pref:`. | The blob freezes every default at save time (`persistence.go:1180-1188` measured 33 keys inheriting stale defaults). `setting:` is a general runtime-marker store, not config (DATA-1). |
| C5 | Every key declares `Reload` (live / restart / boot) and `Protection` (none / interactive / admin / owner / operator); both appear in the generated JSON Schema. | ABO has exactly one boot-only key handled ad hoc (`persistence.go:2111`) and a 92-entry protection map (`protected_fields.go`) separate from the schema. Roles-as-data can hand `core:settings.manage` to non-admins, so sign-in, proxy and updater keys need an admin level (§1.3.7). |
| C6 | Drop **viper** from core and, by the end of migration, from ABO. Stdlib + `go.yaml.in/yaml/v3` + `github.com/google/jsonschema-go` only. | §1.10. |
| C7 | Env names are `<APP>_<PATH>`; no `MEDIACORE_` prefix. ABO's existing names are aliases for the single window of 09 §9.2.1. **Permanent** aliases: ecosystem names (`OTEL_*`, `OPENAI_*`) and every name ABO reads today that a shipped deploy file sets (e.g. `DATABASE_PATH`). | §1.3.6, §1.12.4. Dropping `DATABASE_PATH` after the window would open a new, empty database (OPS-5). |
| C8 | Every recoverable secret is encrypted at rest by `core/secrets` with **one keyring design** (AES-256-GCM, key IDs in every record, AAD = app + name, a separate derivation key); config fields are `secrets.Ref`, never strings. Rotation and prune tooling are **deferred**; the format already carries key IDs so it never changes. Verifiers (passwords, API keys, session ids) are hashed by `auth`, never stored in `secrets`. | `raw/10` G12: 4 of at least 10 secret-bearing fields are encrypted, and 3 of those 4 are also written in plaintext to a mirror YAML file (`persistence.go:168-178`). Rotation does not exist today (`raw/10` §7). |
| C9 | Secrets are **write-only** over the API: the UI receives `{set, source, updated_at, fingerprint}`, never a mask. | Masks reveal the last 2 characters (`internal/database/settings.go:250-277`) and force echo-detection logic (`update_service.go:353-373`). |
| C10 | **One JSON-Schema reflector**: `github.com/google/jsonschema-go` v0.4.3, pinned exactly, with a config-owned override table; [05-http-api](05-http-api.md) reuses the same table for OpenAPI. | §1.10.2; verified 2026-11-04. |
| C11 | **Config holds no integration instances.** Instance records (kind, instance, settings, `secrets.Ref`s) live in the `integrations` store ([05-metadata](05-metadata.md) M1, keyspace `c/integrations/instances/`). Config only reads operator-file instance blocks raw and hands them over (§1.3.3 `Passthrough`). | One store for one fact (SIMP-4): the Deluge password had four destinations across the specs. |
| C12 | This file owns the **config and secrets migration mechanism** (§1.12.2); 09 S3/C3 sequence it and reference it. | Specified twice with contradictory semantics before (SIMP-15, OPS-9). |

---

## Part 1 — `core/config`

### 1.1 Purpose & scope

`core/config` turns layered sources into **typed, validated, immutable snapshots** per section, persists
UI-made changes, tells subscribers what changed, and describes every key (type, default, reload class,
protection, secret-ness, lock state) for the settings UI. It does not parse command lines, does not
open storage, and does not know any section's fields. Out of scope: secrets storage (Part 2), integration
instance records ([05-metadata](05-metadata.md) M1), the HTTP routes (specified as semantics in §1.3.4,
mounted by [05-http-api](05-http-api.md)), the permission check itself ([05-rbac](05-rbac.md)), the
compatibility ledger and its counter ([09](09-abo-on-core.md) §9.2.9, package `compat`).

### 1.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Shape | One `type Config struct`, 864 lines, **173 top-level fields** (this writer's count of field lines in the struct), 247 `SetDefault` registrations | `internal/config/config.go:1048-1912`, `registerConfigDefaults` `config.go:2621` |
| Global | `var mu sync.RWMutex`, `var AppConfig Config`, `Snapshot()` copy under RLock, `Mutate(fn)` under Lock, `generation atomic.Uint64` bumped by every Mutate | `config.go:1928,1939,1944,2011,2023,2035` |
| Single-field locked readers | `SupportedExtensionSet`, `AuthorityEvidenceEnabled`, `RetireVersionsApplyEnabled`, `KeptSweepApplyEnabled` | `config.go:1971-2006` |
| Sources, in actual application order | (1) viper defaults + `AutomaticEnv` (no prefix) + 131 explicit `BindEnv` + viper YAML file `<home>/.audiobook-organizer.yaml` or `--config`, decoded by `configFromViper`; (2) 4 flags bound (`dir`, `db`, `db-type`, `playlists`) plus `--log-level` written straight into `AppConfig`; (3) DB `config_blob` **replaces the whole struct**; (4) env-authoritative keys re-applied (auth-surface keys unconditionally, operational keys only when env supplies them); (5) a second YAML file `<db dir>/config.yaml` fills 4 empty fields; (6) `SyncConfigFromEnv` re-applies 6 keys from env if non-empty | `cmd/root.go:603-660`, `cmd/root.go:55-60`, `cmd/root.go:650`, `config.go:2586-2597,3215`, `persistence.go:915,1229`, `config.go:2435`, `persistence.go:42-104`, `cmd/root.go:381`, `persistence.go:2023` |
| Two config files | viper reads `<home>/.audiobook-organizer.yaml`; `SaveConfigToFile` writes `<db dir>/config.yaml` (merged, atomic, 0600) on every DB save — a different file — and `LoadConfigFromFile` reads it back as a fallback | `cmd/root.go:614-623`, `persistence.go:29-38,121-195`, comment `config.go:2290-2292` |
| DB persistence | whole non-secret struct as JSON in setting `config_blob`; 4 secrets as encrypted rows; ten audiobook-specific `migrate*Blob` funcs; boot-only key (`enabled_sort_indexes`) stripped from blob and pinned | `persistence.go:1932-2016`, `persistence.go:359-824`, `persistence.go:2111,2126-2139` |
| Secret rows on load | every `IsSecret` row is decrypted and handed to `applySetting`, which knows only the 4 encrypted secrets and rejects any other key as `unknown setting key` | `persistence.go:1191-1207`; cases `:1429,1441,1443,1813`; default `:1884` |
| Locks | `envLockedSettings` (env var per key, ~10 keys), `flagLockedSettings` (`database_path` → `--db`), config-file lock for Deluge keys; `SettingLockedBy`/`SettingLocks` feed the UI | `config.go:2241-2289,2353-2425` |
| Protection | 92-entry `configFieldRules` map of JSON path → class (`FieldSignIn`, `FieldExecutable`, `FieldDatabase`, `FieldServerPath`); `UpdateService` refuses protected changes unless `auth.Method.MayChangeCredentials()`; owner trust-root separately | `protected_fields.go:17-34,38-60`, `update_service.go:770-800` |
| Write API | `UpdateService.UpdateConfig(ctx, payload map[string]any)` — unknown-key detection by reflection, mask-echo detection, env-only refusal, owner refusal, protected refusal, `Validate`, persist; dedup score ladder hand-off via `DedupScoreConfigSink` returning "saved but not queued" | `update_service.go:533`, `unknown_keys.go:41`, `update_service.go:353-373`, `update_service.go:40-60` |
| Hot reload | none from files (no watcher, no SIGHUP — `raw/12` §2); reload is write-through `Mutate` + `Generation()` polling, used by exactly one consumer | `internal/ai/ai_metrics.go:130,169` |
| Global call sites | outside `internal/config`, non-test: **676** `config.AppConfig` occurrences in **215** files reading **113** distinct fields (plus 32 whole-struct uses); `config.Snapshot()` 87 in 53 files; `config.Mutate(` 5 in 3 files; `config.SupportedExtensionSet()` 18 in 16 files. Tests: 2,456 `AppConfig` occurrences in 341 files, 151 `Mutate(` in 58 files. Top fields: `RootDir` 132, `Scheduled` 46, `ITunes` 34, `DatabasePath` 31, `Maintenance` 29 | this writer's grep at `748fd787a`; `raw/12` §2 reports 214 files |
| Writes that bypass `Mutate` (races, invisible to `Generation`) | 8 assignments in 5 non-test files | `cmd/root.go:650`, `internal/metafetch/openlibrary.go:78`, `internal/server/handlers/scheduler_admin.go:275,533-535`, `internal/server/handlers/system/handler.go:585-586` |
| DI side door | `func init()` registers config in `serviceregistry`, returning the global | `internal/config/register.go:12,49` |
| Upward imports (layer-1 package reaching layer 2/3) | `database` (`persistence.go:22`, `register.go`, `update_service.go`, `ai_endpoints.go`), `auth` (`update_service.go:781,822`), `backup` (`config.go:24`), `aidispatch` (`ai_endpoints.go:94-179`), `dedup/unified` (`dedup_score_config.go:42-60`, `update_service.go:22,48`); also `audioext`, `tools`, `logger`, `pathutil`, `serviceregistry` | `raw/02` finding 3; `raw/03` `allowed` rows 1–5 (`internal/arch/layering_test.go:291-295`) |
| viper outside config | `cmd/root.go`, `internal/dedup/unified/config.go:222-226` reads viper directly | grep |
| Env names in shipped deploy files | unit: `DATABASE_PATH`, `AUDIOBOOK_ROOT_DIR`, `FP_PARALLEL_WORKERS`; plist: `DATABASE_PATH`, `AUDIOBOOK_ROOT_DIR`; compose: `AO_DIR`, `AO_DB`, `BASIC_AUTH_{ENABLED,USERNAME,PASSWORD}`; `local.conf.example`: `WHISPER_REMOTE_URL`, `OLLAMA_BASE_URL`, `ALLOWED_PEER_CIDRS`, `TUNNEL_CONNECTOR_ADDRS`, `LOGIN_SATURATED_{RATE,BURST}`. `AUDIOBOOK_ROOT_DIR` is deliberately **not** read; `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL` have no reader in Go | `deploy/audiobook-organizer.service:97,98,108`, `deploy/com.audiobook-organizer.plist:38-41`, `docker-compose.yml:18-22`, `deploy/local.conf.example:39-61`; `config.go:2569-2575`; `config.go:2735,2838`, `invite_config.go` |

### 1.3 Go API

Package `github.com/falkcorp/mediacore/config` (L3). Imports: stdlib, `go.yaml.in/yaml/v3`,
`github.com/google/jsonschema-go/jsonschema`, `github.com/falkcorp/mediacore/secrets` (L2; `secrets.Ref` and
`secrets.Resolver` only) and `github.com/falkcorp/mediacore/metrics` (L0; its own `*metrics.Scope`). It does
**not** import `storage`, `log`, `auth`, `compat`, `integrations` or any module (§1.9).

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
	ReloadBoot                      // read before storage opens (§1.3.5 boot record); persisted outside
	                                // the DB; restart required. Used for decisions taken before module
	                                // registration, e.g. core.modules.<id>.enabled (06, COR-14)
)

// Protection says who may change a key through Apply (the DB layer or the boot record).
// Ordered: each level includes the requirements of the levels above it in this list.
type Protection uint8

const (
	ProtectNone        Protection = iota + 1 // the section's write permission suffices
	ProtectInteractive                       // also an interactive, credential-changing session (05-authn)
	ProtectAdmin                             // also core:admin (SEC-6): sign-in, proxy, updater, path, URL keys
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

func Permissions(read, write string) SectionOption               // rbac strings; default derived from path (§1.3.4)
func Docs(d map[Path]string) SectionOption                       // key -> description for schema/UI
func Aliases(a ...Alias) SectionOption                           // legacy names (§1.3.6); registered with compat by the kernel
func Deprecated(p Path, replacement Path) SectionOption          // schema deprecated:true; removal is a contract PR (09 §9.2.9)
func ProtectionWaivers(w map[Path]string) SectionOption          // key -> reason it needs no class despite its shape (§1.3.7)

// Alias is one legacy spelling of a canonical key. 09 §9.2.5 refers to this type.
type Alias struct {
	Legacy    string               // flat key in YAML / config_blob, e.g. "root_dir"; "" if env-only
	LegacyEnv []string             // env names read today (AutomaticEnv upper-case key + BindEnv names)
	Canonical Path
	Convert   func(json.RawMessage) (json.RawMessage, error) // optional unit/shape change
	Class     AliasClass
}

type AliasClass uint8

const (
	AliasWindow    AliasClass = iota + 1 // removed by a contract PR after 09's window W; never by a clock
	AliasPermanent                       // ecosystem names, and names a shipped deploy file sets (§1.12.4)
	AliasRetired                         // contracted; still recognised so that using it is reported (§1.3.6)
)

// Schema is a JSON Schema 2020-12 document plus mediacore vendor keywords, produced by the
// shared reflector (§1.10.2). integrations descriptors use it for instance settings (05-metadata M1).
type Schema struct{ /* *jsonschema.Schema + resolved form */ }

func SchemaFor[T any]() (Schema, error)                          // same override table as sections and OpenAPI
func (s Schema) JSON() json.RawMessage
func (s Schema) Validate(v json.RawMessage) error                // errors.Join of *FieldError
```

Field metadata is struct tags on `T` (reflected once at `Register`):

| Tag | Meaning | Example |
|---|---|---|
| `json:"name"` | key segment; decoding uses `encoding/json`, so there is **one** decoder (today: dual `json` + `mapstructure` tags, e.g. `config.go:1636,1773`) | `json:"interval"` |
| `reload:"restart\|boot"` | `ReloadRestart` / `ReloadBoot`; default live | `reload:"restart"` |
| `protect:"interactive\|admin\|owner\|operator"` | `Protection`; default none | `protect:"admin"` |
| `schema:"…"` | JSON-Schema keywords: `minimum=`, `maximum=`, `enum=a\|b`, `format=duration\|uri\|cidr\|path\|email`, `pattern=` | `schema:"minimum=1,maximum=64"` |
| `sensitive:"true"` | not a secret, but redacted in diagnostics bundles and logs (emails, allow-lists) | owner e-mail |
| field type `secrets.Ref` | secret; schema `writeOnly: true`; value lives in `secrets` (Part 2) | `APIKey secrets.Ref \`json:"api_key"\`` |

Example (books module, ABO repo):

```go
type ScanSettings struct {
	Interval    config.Duration `json:"interval" schema:"format=duration"`
	Concurrency int             `json:"concurrency" schema:"minimum=1,maximum=64"`
	OnStartup   bool            `json:"on_startup"`
	SortIndexes []string        `json:"sort_indexes" reload:"restart"`
	AcoustIDKey secrets.Ref     `json:"acoustid_api_key" protect:"interactive"`
}

func (s ScanSettings) Validate() error { /* cross-field checks */ return nil }

var Scan = config.Define("modules.books.scan", ScanSettings{Interval: config.Duration(6 * time.Hour), Concurrency: 2},
	config.Aliases(config.Alias{
		Legacy: "concurrent_scans", LegacyEnv: []string{"CONCURRENT_SCANS"},
		Canonical: "modules.books.scan.concurrency", Class: config.AliasWindow,
	}))
```

#### 1.3.2 Binding and reading (module side)

```go
// Registry is used by the kernel to register sections listed in Manifest.Settings (06 §6.4).
// It has unexported methods, so only the kernel can implement it. Registration is legal only before Store.Load.
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
`coretest` fingerprints every published snapshot and fails the test if one changed (§1.11).

#### 1.3.3 The store (kernel side)

```go
type Options struct {
	AppID       string            // app.Options.AppID, e.g. "abo"; schema $id
	EnvPrefix   string            // "ABO"; [A-Z][A-Z0-9]{1,15}
	Environ     []string          // os.Environ() captured once by app.New; config never calls os.Getenv
	FilePath    string            // operator YAML file; "" = none. Read-only: core never writes it.
	Flags       map[Path]string   // ONLY flags the operator explicitly set (FromFlagSet / app adapter)
	BootRecord  *os.Root          // opened on core.paths.state_dir; holds config.boot.json (§1.3.5)
	Passthrough func(Path) bool   // operator-layer paths config reads raw and does not own (v1: file-defined
	                              // integration instances, core.integrations.<kind>.instances.<id>.*)
	OnLegacyUse func(a Alias, entry string) // kernel wires this to compat's legacy-use counter; entry ∈ file, env, blob, api
	Logger      *slog.Logger
	Metrics     *metrics.Scope    // core scope "mediacore.config" (05-metrics)
	Secrets     secrets.Resolver  // may be nil until secrets opens; Refs resolve after SetSecrets
	Now         func() time.Time
}

func NewStore(o Options) (*Store, error)
func (s *Store) Registry() Registry
func (s *Store) Boot(ctx context.Context) (BootView, error)              // core sections only; operator layers + boot record
func (s *Store) Load(ctx context.Context) error                          // seals Registry; defaults+file+env+flags+boot record
func (s *Store) SetSecrets(r secrets.Resolver)
func (s *Store) AttachOverrides(ctx context.Context, o OverrideStore) (AttachReport, error)
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error)
func (s *Store) Lookup(p Path) (KeyState, bool)
func (s *Store) Keys() iter.Seq2[Path, KeyState]                         // sorted; secret values never included
func (s *Store) Aliases() iter.Seq[Alias]                                // for compat ledger registration (09 §9.2.9)
func (s *Store) PassthroughValues() iter.Seq2[Path, OperatorValue]       // raw; handed to integrations by the kernel
func (s *Store) Schema(include func(section Path) bool) (json.RawMessage, error)
func (s *Store) Generation() uint64
func (s *Store) Close(ctx context.Context) error

// BootView exposes the resolved ReloadBoot keys of core sections before module sections exist.
type BootView interface {
	Bool(p Path) (v bool, src Source, ok bool)
	Raw(p Path) (json.RawMessage, Source, bool)
}

// OperatorValue is one raw passthrough value with its origin, so integrations can show a lock reason.
type OperatorValue struct {
	Value json.RawMessage
	Src   Source // SourceFile, SourceEnv or SourceFlag
	By    string // "file <name>", "env ABO_…"
}

// FromFlagSet converts a stdlib FlagSet to Options.Flags using Visit (explicitly-set flags only).
// Apps on cobra/pflag write the same 10-line adapter over pflag.FlagSet.Visit; core does not import pflag.
func FromFlagSet(fs *flag.FlagSet, bind map[string]Path) map[Path]string

// OverrideStore persists the DB layer. Implemented by kernel (core/internal/configstore) over the
// storage keyspace c/config/; config itself never imports storage (§1.9).
type OverrideStore interface {
	Load(ctx context.Context) iter.Seq2[Override, error]
	// Commit writes set/unset rows, the staged secret writes and (during the migration window) the
	// legacy mirror (§1.12.2) in ONE storage batch.
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

// LegacyBlob is the shape knowledge of a legacy whole-struct blob, supplied by the app module that owns
// that struct (ABO's books module) and used only by configstore's migration (§1.12.2).
type LegacyBlob interface {
	// Decode returns every legacy key in the blob with its raw value (secret fields included).
	Decode(raw []byte) (map[string]json.RawMessage, error)
	// Encode renders the blob the previous build expects from the current effective view.
	// prior is the stored blob; keys config does not own are carried forward from it unchanged.
	Encode(prior []byte, current func(legacy string) (json.RawMessage, bool)) ([]byte, error)
}

// Patch is the API write unit. Set values are JSON; for a secret key the JSON string is the
// secret value and listing it in Unset deletes it.
type Patch struct {
	Set   map[Path]json.RawMessage
	Unset []Path
}

// Actor is filled by the HTTP layer (05-http-api) from the request's auth context, so config never imports auth.
type Actor struct {
	Subject     string
	Interactive bool                   // auth.Method.MayChangeCredentials()
	Owner       bool                   // owner proof verified
	Can         func(perm string) bool // rbac check; ProtectAdmin asks Can("core:admin")
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
	SourceBootRecord
	SourceOverride
)

type KeyState struct {
	Path        Path
	Section     Path
	Schema      json.RawMessage // this key's sub-schema
	Effective   json.RawMessage // nil for secrets
	Source      Source
	LockedBy    string // "env ABO_CORE_LOG_LEVEL", "flag --db", "file <name>", "" when free
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
	ErrUnknownKey    = errors.New("config: unknown key")
	ErrLocked        = errors.New("config: key is set by the operator")      // wrapped in *LockedError
	ErrProtected     = errors.New("config: key needs a stronger credential")  // wrapped in *ProtectedError
	ErrForbidden     = errors.New("config: missing section permission")
	ErrInvalid       = errors.New("config: invalid value")                    // errors.Join of *FieldError
	ErrSealed        = errors.New("config: registry sealed after Load")
	ErrDuplicate     = errors.New("config: duplicate path or env name")
	ErrRetiredAlias  = errors.New("config: a retired legacy name is set")     // wrapped in *RetiredAliasError
)

type LockedError struct{ Path Path; By string }
type ProtectedError struct{ Path Path; Need Protection }
type PostCommitError struct{ Section Path; Err error }
type RetiredAliasError struct{ Name string; Canonical Path; CanonicalEnv string }
```

#### 1.3.4 Settings API semantics (routes owned by 05-http-api)

| Route | Permission | Semantics |
|---|---|---|
| `GET /api/core/v1/settings/schema` | any section read permission | JSON Schema 2020-12 document, one property per section the caller may read. Vendor keywords: `x-mediacore-reload` (`live`\|`restart`\|`boot`), `x-mediacore-protect`, `x-mediacore-secret`, `x-mediacore-aliases`; standard `writeOnly: true` on secrets, `deprecated: true` on deprecated paths. |
| `GET /api/core/v1/settings` | per section read | `{path: KeyState}` for readable keys. Secrets appear as `Secret` meta only. |
| `PATCH /api/core/v1/settings` | per section write + protection | Body `{"set": {path: value}, "unset": [path]}` → `Store.Apply`. 400 unknown key, 409 locked (`locked_by` in body), 403 forbidden/protected (`need` names the protection level), 422 invalid (field errors), 500 storage. 200 with `restart_pending` and `post_commit_error` when present. |

Default section permissions: `core.*` → read and write `core:settings.manage`; `modules.<id>.*` → read and
write `<id>:settings.manage` (registered by the module in its permission list; [05-rbac](05-rbac.md)).
ABO's `settings.manage` atom maps to both during the compat window ([09](09-abo-on-core.md) §9.2.6).
A section permission never suffices for a key with `ProtectAdmin` or higher: holding `core:settings.manage`
cannot be turned into `core:admin` through configuration (§1.3.7).

#### 1.3.5 Precedence, locks and the boot record (normative)

Merge order, lowest to highest: **defaults → file → env → flags → DB overrides** (for `ReloadBoot` keys the
last layer is the **boot record** instead of DB overrides). One rule constrains the last layer: if any
operator layer (file, env, flag) supplies a key, that key is **locked**: its stored value is ignored at load
(listed in `AttachReport.Ignored`, kept, not deleted) and `Apply` refuses it with `*LockedError{By}`. Keys with
`ProtectOperator` are never read from or written to the DB layer or the boot record at all. Consequently the
stored layers only ever override defaults, which is what an operator expects: what they put in the file,
environment or command line is what runs, and the UI says so.

Within operator layers flags beat env beat file. For one key, the canonical env name beats an alias; if
both are set with different values the canonical wins and a warning plus health token
`config_alias_conflict` is raised (§1.5).

**Boot record.** `ReloadBoot` keys are needed before storage opens (module enablement decides which module
sections are registered and which keyspaces are claimed, 06 P1). Their UI-made values are persisted in
`<state_dir>/config.boot.json` (0600, directory 0700, written atomically through `Options.BootRecord`:
temp file, fsync, rename), a JSON object `{"format":"mediacore-boot/1","gen":N,"values":{path: value}}`.
`Boot` and `Load` read it; `Apply` writes it. Unknown paths in it (a module this binary lacks) are retained
and reported like unknown override rows. No other component writes the file. [05-backup](05-backup.md)
archives it with the config file.

#### 1.3.6 Environment naming and aliases

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

**Aliases.** Every legacy name is an `Alias`. Config itself holds no window: the kernel registers
`Store.Aliases()` with `compat` (L4, which may import config; config may not import compat), and every use
of an alias calls `Options.OnLegacyUse`, which the kernel wires to `mediacore_compat_legacy_use_total`
(`surface="config"`). When a window alias may be removed is decided by 09 §9.2.1 (W plus the usage gate) and
done by a contract PR checked by `TestNoContractBeforeSunset`; nothing is removed or refused by a clock, and no
runtime switch turns aliases off.

| Class | Read? | On use | Removed |
|---|---|---|---|
| `AliasWindow` | yes | applied; deprecation warning once; counter | contract PR after W (09) |
| `AliasPermanent` | yes | applied; counter only (no warning) | never (or at the next major, 09 §9.6 row 18) |
| `AliasRetired` | no | key has `ProtectAdmin` or higher (incl. `ProtectOperator`): **boot fails** with `*RetiredAliasError` naming the canonical variable; other keys: one warning per boot | retired rows of protected keys are kept until the next major; others are deleted one minor after contract (09) |

The retired rule exists because silently ignoring a legacy database-path or sign-in name after the window
points the app at a new, empty database or turns basic auth off (OPS-5; ABO recorded the same hazard,
`config.go:2519-2544`).

#### 1.3.7 Protection classes (normative)

The class rule replaces ABO's `configFieldRules` (`protected_fields.go:53`). It is applied by every spec that
declares keys; each key table in the 05 files carries a **Protection** column.

| ABO class today | Protection | Keys (core examples; owning spec tables each key) |
|---|---|---|
| `FieldSignIn` | `ProtectAdmin` | everything that decides who signs in and how: `core.auth.*` sign-in keys (auth on/off, basic auth, OAuth/OIDC providers, client ids and secrets, redirect base, allowed e-mails, default role, CF Access team and audience) ([05-authn](05-authn.md)); the books ABS token sign-in switch |
| — (new, SEC-6) | `ProtectAdmin` | `core.http.{trusted_proxies, client_ip_header, peer_allowlist, cors.allowed_origins, basic_gate.*}` ([05-http-api](05-http-api.md)); `core.updater.{mode, channel, restart_*}` ([05-updater](05-updater.md)); OTLP endpoints and `core.metrics.{path, listen}` ([05-metrics](05-metrics.md)); `core.integrations.egress.lan_allow` (SEC-15; see below) |
| `FieldExecutable` | `ProtectAdmin` | a program the server runs, or the managed-copy-vs-path switch |
| `FieldServerPath` | `ProtectAdmin` | a path the server reads, writes, scans or serves (`core.log.file.path`, library and tool paths) |
| owner trust root (`update_service.go:770-800`) | `ProtectOwner` | the owner identity (`core.auth.owner.*`) |
| `FieldDatabase` | `ProtectOperator` | `core.storage.path`, `core.paths.*` |

`core.integrations.egress.lan_allow` — `[]string` of CIDRs or host names, default `[]`, restart-free, owned and
enforced by `integrations` ([05-metadata](05-metadata.md) M1/M2): the LAN targets an instance not created by an
interactive `core:integrations.manage` holder (facade-pushed, or created by an API key) may reach; every other
such instance gets public-only egress. It is a network trust list, so it is `ProtectAdmin` as SEC-15 specifies,
not `ProtectInteractive`: an interactive non-admin holding `core:settings.manage` must not be able to open LAN
targets to facade-pushed URLs.

`ProtectAdmin` is checked as `Actor.Can("core:admin") && Actor.Interactive`. The auth-off principal never holds
`core:admin` ([05-rbac](05-rbac.md)), so these keys cannot be changed with auth off. Integration instance
settings follow the same rule through their descriptor schema ([05-metadata](05-metadata.md) M1): instance URL
and credential fields are `ProtectAdmin`-equivalent under `core:integrations.manage`.

`TestEveryURLPathOrSignInKeyClassified` (port of `protected_fields_test.go`) fails when a registered key whose
name or format looks like a URL (`format=uri`, `*_url`, `*endpoint*`), a path (`format=path`, `*_dir`, `*_path`,
`*_file`), an executable (`*_bin`, `*_command`, `*_exe`), a network trust list (`format=cidr`, `*proxies*`,
`*origins*`, `*allowlist*`, `*client_ip*`) or a sign-in setting (`core.auth.*`) has a Protection below
`ProtectAdmin` and no `ProtectionWaivers` entry with a reason.

### 1.4 Lifecycle

Inside `app.New` (03 §3.7), in this order:

1. `config.NewStore(opts)`; the kernel registers **core** sections.
2. `Store.Boot`: reads the file (parse error = boot error), env, flags and the boot record for core sections;
   validates operator values; the kernel reads module enablement (`core.modules.<id>.enabled`, `ReloadBoot`) from
   the returned `BootView` — the phase that decides enablement reads it (COR-14; 06 P1).
3. The kernel registers the sections of every **enabled** module (from manifests), then `Store.Load` seals the
   registry and builds every section from defaults, operator layers and the boot record. Registration after
   `Load` → `ErrSealed`, boot fails. A section whose operator values fail decode or `Validate` fails boot (the
   operator must fix their input). A retired protected alias set in env or file fails boot (§1.3.6).
4. The kernel builds logging and metrics from `core.log` / `core.metrics` (operator layers and defaults).
5. Storage opens using `core.storage.path` (`ProtectOperator`: needed before the DB layer exists).
6. `secrets.Open`, then `Store.SetSecrets`.
7. The kernel's `configstore` runs the migration step for this boot (§1.12.2: legacy import or re-import while
   the window is open), then `Store.AttachOverrides`: rows are decoded per section; if a section fails decode or
   `Validate` with its overrides but passes without them, **all of that section's overrides are quarantined**
   (kept, ignored, reported, health token `config_overrides_quarantined`) and boot continues. Rationale: an
   operator cannot repair the DB without booting; ABO's equivalent path is fatal (`cmd/root.go:385-391`).
8. The kernel hands `Store.PassthroughValues()` to the integrations registry (05-metadata M1.4 step 2).
9. Runtime: `Apply` = authorize (permission, then protection) → decode candidate per touched section →
   `Validate` → write the boot record if boot keys changed → `OverrideStore.Commit` (overrides + staged secrets +
   legacy mirror, one batch) → publish snapshots for live keys (atomic swap) → bump generation → `AfterCommit`
   hooks → wake `OnChange` subscribers. If `Commit` fails after the boot record was written, the previous boot
   record is restored before `Apply` returns (boot keys are restart-class, so nothing was published).
10. `Close` cancels subscriber contexts and waits up to the drain timeout.

Restart-required and boot keys: `Apply` commits them and returns them in `RestartPending`; `Get` keeps the boot
value; `Pending` shows the stored value; `KeyState.Pending` drives a "restart required" badge in the shell.

### 1.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Config file exists but unreadable or unparsable | fail-closed (boot error) | operator intent unknown; ABO ignores viper read errors (`cmd/root.go:627`) — behaviour change, release-noted |
| Unknown key in file / unknown prefixed env var | fail-open, warn once | stale keys must not stop startup (`config.go:2587-2590`) |
| Window or permanent alias used | fail-open, applied, counter (+ deprecation warning once for window aliases) | compat window (09 §9.2.1) |
| Retired alias set, key `ProtectAdmin` or higher | fail-closed (boot), message names the canonical variable | silent fallback opens a wrong database or disables sign-in (OPS-5) |
| Retired alias set, other key | fail-open, warn once per boot | harmless leftovers |
| Canonical and alias both set, different | fail-open, canonical wins, warn, health `config_alias_conflict` | an obvious mistake, but not worth an outage |
| Operator value fails decode or `Validate` | fail-closed | operator must fix input |
| `<NAME>_FILE` unreadable | fail-closed | a missing credential file is a deployment error |
| Boot record unreadable (I/O) | fail-closed | never boot with a module silently re-enabled or disabled |
| Boot record unparsable | fail-open: file renamed `config.boot.json.bad`, ignored, health `config_boot_record_quarantined` | the operator can repair a file, but the app must boot to show why |
| Override rows unreadable (I/O) | fail-closed | running on defaults would let the next save overwrite stored config (`cmd/root.go:360-364`, comment at `:361`) |
| Override row fails decode/validate | fail-open per section: quarantine (§1.4 step 7) | DB cannot be fixed without booting |
| Override row for unknown key | fail-open: retained, ignored, reported | downgrade safety (gate G5, 02 §2.4) |
| `Apply` unknown key (incl. a passthrough path) | refuse `ErrUnknownKey`, nothing persisted | typos must not silently no-op; instances are written through 05-metadata |
| `Apply` locked key | refuse `*LockedError` | the value would be discarded at next boot (`config.go:2224-2229` rationale) |
| `Apply` missing permission / protection | refuse `ErrForbidden` / `*ProtectedError` | fail-closed security |
| `Apply` validation failure | refuse `ErrInvalid` with `*FieldError`s | nothing partially applied |
| `OverrideStore.Commit` fails (incl. the legacy mirror) | fail-closed: nothing published, generation unchanged, boot record restored | durable-before-visible; the previous build must never read a stale mirror |
| `AfterCommit` hook error | committed; `Result.PostCommit` set | hooks cannot veto; caller must know (ABO contract `update_service.go:30-48`) |
| `OnChange` fn panics | fail-open: recovered, logged, metric; others continue | one subscriber must not break config |
| `Ref.Resolve` on unset secret | `secrets.ErrNotSet` to the consumer | consumer decides (e.g. provider disabled) |
| Duplicate path / env-name collision / invalid path | fail-closed at registration | programming error |
| `ReloadBoot` key in a module section read before that module is registered | fail-closed at registration | only core sections are visible to `Boot` |

### 1.6 Concurrency contract

- `Handle.Get`, `Generation`, `Lookup`, `Keys` are lock-free (one `atomic.Pointer` load per section; `Keys`
  iterates an immutable published index).
- `Apply` calls are serialized by one store mutex held from candidate build through publish, replacing ABO's
  `configBlobSaveMu` (`persistence.go:1897`) and `mu`. The boot-record write and `Commit` happen under it;
  publish happens after `Commit` returns.
- `OnChange`: one goroutine per subscription with a one-slot coalescing mailbox; never blocks `Apply`.
  Subscription count is bounded by code (registered at init). `stop()` is idempotent and waits for an
  in-flight call.
- `AfterCommit`: runs on the `Apply` goroutine with `Apply`'s `ctx`; total hook time is bounded by the
  request deadline. Hooks must enqueue work (`tasks`) rather than do it.
- No goroutine is started before `Load`; `Close` cancels and joins all (verified with `testing/synctest`).
- Config runs no timers. Anything periodic is a task with `tasks.Schedule` bound to a declared key through
  `tasks.ScheduleFromConfig(key)` ([05-tasks](05-tasks.md)).

### 1.7 Config keys (core-owned sections)

This file owns only `core.config.*`, `core.paths.*` and `core.secrets.*` (§2.7); other `core.*` sections are
owned and tabled by their specs ([05-logging](05-logging.md), [05-metrics](05-metrics.md),
[05-storage](05-storage.md), [05-http-api](05-http-api.md), [05-authn](05-authn.md), [05-backup](05-backup.md),
[05-updater](05-updater.md), [05-tasks](05-tasks.md), [05-ai](05-ai.md)); integration instances are not config
keys at all (C11).

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.paths.state_dir` | path | app-defined (ABO: its compiled default, `internal/config/state_dir.go:54`) | no | operator |
| `core.paths.data_dir` | path | `<state_dir>/data` | no | operator |
| `core.config.file` | path | `""` (also `--config`, `<APP>_CONFIG`) | no | operator |
| `core.config.subscriber_drain_timeout` | duration | `5s` | no | none |

There is no `core.config.legacy_aliases` switch: aliases are governed by 09's ledger (§1.3.6). No key in this
file changes persisted state shape; such switches are release-bound, not keys (09 §9.2.8). Keys that only tune
behaviour (levels, endpoints, limits) are operational dials and are allowed (OPS-17).

### 1.8 Metrics

All on scope `mediacore.config` (`*metrics.Scope` from `Options.Metrics`).

| Name | Type | Labels |
|---|---|---|
| `mediacore_config_applies_total` | counter | `outcome` ∈ applied, unknown_key, locked, forbidden, protected, invalid, storage_error |
| `mediacore_config_generation` | gauge | — |
| `mediacore_config_restart_pending_keys` | gauge | — |
| `mediacore_config_quarantined_overrides` | gauge | — |
| `mediacore_config_post_commit_failures_total` | counter | `section` (closed set: registered sections) |
| `mediacore_config_subscriber_panics_total` | counter | `section` |
| `mediacore_config_legacy_reimports_total` | counter | `kind` ∈ value, secret (§1.12.2 return path) |

Legacy-name use is counted once, by `compat` (`mediacore_compat_legacy_use_total{surface="config"}`), not by a
second config counter.

### 1.9 Breaking config's upward imports

| Import today | What it is used for | Where it goes |
|---|---|---|
| `internal/database` (`SettingsStore`, `Setting`, `DecryptValue`, `MaskSecret`, `IsEchoedMask`, `LooksLikeMask`) | blob/row persistence, secret decrypt, masks (`persistence.go:883-1260`, `update_service.go:91-373`) | persistence → `OverrideStore` interface implemented by kernel over `storage`; decrypt → `secrets`; masks → deleted (write-only, C9); the ABO compat handler keeps mask echo logic for the window only |
| `internal/auth` (`MethodFromContext().MayChangeCredentials`, `AccessEmailFromContext`) | protected-field and owner checks (`update_service.go:781,822`) | `config.Actor` filled by the HTTP layer; protection is per-key metadata |
| `internal/backup` (`LevelDefault`, `ResolveMaxTotalBytes`) | backup defaults documented in config (`config.go:1086-1107`) | `core/backup` (L4) defines and registers `core.backup` itself (imports config at L3 — legal) |
| `internal/aidispatch` (`Endpoint`, `Protocol`, `ParseCapability`) | AI endpoint conversion/validation (`ai_endpoints.go:94-179`) | endpoints become `ai/<id>` integration instances validated by their descriptor schema ([05-ai](05-ai.md), C11); global AI tuning is `core.ai.*` owned by `core/integrations/ai` |
| `internal/dedup/unified` (`ScoreConfig`, `LoadScoreConfig`) + `DedupScoreConfigSink` | dedup score ladder (`dedup_score_config.go:42-60`, `update_service.go:48`) | books module owns `modules.books.dedup` type; the sink becomes an `AfterCommit` hook on that handle |
| `internal/audioext`, `internal/tools`, `internal/pathutil` | extension set, tool modes, iTunes paths (`config.go:1971,3717`, `itunes_libraries.go`) | books module section types |
| `internal/logger` | logging (`update_service.go`, `chapter_consolidation.go`) | `*slog.Logger` in `Options` |
| `internal/serviceregistry` + `init()` | DI side door (`register.go:12,49`) | deleted; the kernel's typed services struct builds the store explicitly (03 §3.3; no DI container) |

Enforced by R4 (`core/internal/archtest_layers_test.go`) and a targeted test
`TestConfigImportsOnlyAllowed` (§1.11).

### 1.10 Dependencies

#### 1.10.1 viper: drop it

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

#### 1.10.2 JSON Schema: one reflector, `google/jsonschema-go` (decision, 2026-11-04)

The previous draft chose an in-house reflector while [05-http-api](05-http-api.md) chose
`github.com/google/jsonschema-go`; each claimed to share the other's (SIMP-14). Decision: **ADOPTED —
`github.com/google/jsonschema-go` v0.4.3**, pinned exactly, for both the settings schema and OpenAPI; the
in-house reflector is **rejected**. Reason: the checks below pass (MIT licence, maintained, a v1 downstream
consumer, no non-test dependencies), and its `TypeSchemas` and `Extra` hooks cover the shared type rules and
vendor keywords that were the in-house plan's only justification, so ~400 LOC of reflector would buy nothing.
This meets the condition under which 13 lists the dependency.

| Check (2026-11-04) | Result |
|---|---|
| Latest stable | `v0.4.3` (`go list -m -versions`: v0.1.0 … v0.4.3; proxy `@latest` = v0.4.3, tagged 2026-04-17) |
| Licence | MIT (`LICENSE` in the v0.4.3 module zip) |
| Maintenance | 10 tags from 2025-08-07 to 2026-04-17; `@main` resolves to a pseudo-version dated 2026-05-22; the Go MCP SDK `github.com/modelcontextprotocol/go-sdk` v1.8.0 (2026-09-04), a v1 module, requires it at v0.4.3 |
| Dependencies | `go.mod`: `go 1.23.0`, one requirement (`github.com/google/go-cmp`), imported only by its `_test.go` files |
| Fit | `ForOptions.TypeSchemas map[reflect.Type]*Schema` overrides type translation; `Schema.Extra map[string]any` carries vendor keywords; `Schema` has `WriteOnly`, `Deprecated`, `Minimum`, `Enum`, `Format`, `Pattern`, `$defs`; `Resolve` + `Resolved.Validate` validate instances (2020-12 and draft-07) |

How config uses it: `jsonschema.For[T]` with the **core override table** (`config.Duration` → string/duration,
`secrets.Ref` → write-only string, `time.Time` → string/date-time, `rbac.Permission` → string pattern, owned by
config and exported to `httpapi` through `core/internal/jsonschemax`), then one config-owned pass over `T`'s
fields that maps the `reload`, `protect`, `schema` and `sensitive` tags onto keywords and `Extra`
(`x-mediacore-*`). `jsonschema-go` reads only its own `jsonschema:"…"` description tag, so the tag pass is
required either way (~100 LOC instead of ~400 for a whole reflector). Operator and patch values are checked
with `Resolved.Validate` before `encoding/json` decoding and `Validator`. Risk: pre-1.0, so the pin is exact and
an upgrade is a reviewed change gated by `TestSchema_Golden`. 13 §13.5.2 records the dependency.

### 1.11 Tests (core must have)

| Test | Asserts |
|---|---|
| `TestPrecedence_Matrix` | table over every (default, file, env, flag, boot record, override) combination: effective value, `Source`, `LockedBy` |
| `TestOverride_IgnoredWhenOperatorLocked` | DB row kept, not applied, reported in `AttachReport.Ignored` |
| `TestApply_RefusesLocked` / `_Protected` / `_Forbidden` / `_UnknownKey` / `_Invalid` | error type, nothing persisted, generation unchanged |
| `TestApply_SettingsManageCannotChangeAdminKeys` | actor with `core:settings.manage`, interactive, no `core:admin` → `*ProtectedError{Need: ProtectAdmin}` on a sign-in, proxy and updater key |
| `TestEveryURLPathOrSignInKeyClassified` | §1.3.7 shape rule over every registered core and fixture key |
| `TestApply_RestartKeyNotPublished` | `Get` unchanged, `Pending` changed, `RestartPending` returned |
| `TestApply_CommitFailureNothingPublished` | fake `OverrideStore` error; boot record restored |
| `TestApply_SecretAndOverrideOneBatch` | `Commit.Secrets` and `Commit.Set` arrive together |
| `TestBoot_ModuleEnablementFromBootRecord` | a UI-written disable is honoured by `Boot` at the next start; operator env beats it |
| `TestBootRecord_AtomicAndQuarantine` | crash between temp write and rename keeps the old file; garbage → `.bad`, boot continues |
| `TestAttach_QuarantineInvalidSection` | boot continues, section on operator view, health token set |
| `TestOnChange_CoalescesUnderSynctest` | `testing/synctest`: 100 applies, slow subscriber sees ≤2 calls, last = latest |
| `TestAfterCommit_ErrorReportedNotRolledBack` | `Result.PostCommit` non-nil, value committed |
| `TestEnvName_Derivation` / `TestEnvName_CollisionRejected` | naming rule; `ErrDuplicate` |
| `TestEnvFileSuffix` | `_FILE` read once, newline trimmed, unreadable = boot error |
| `TestAlias_UseCallsOnLegacyUse` / `TestAlias_RetiredProtectedFailsBoot` | counter callback per entry kind; retired protected alias → `*RetiredAliasError` |
| `TestPassthrough_NotOwnedNotWritable` | passthrough values returned raw with origin; `Apply` on them → `ErrUnknownKey` |
| `TestSchema_Golden` | schema of a fixture section equals `testdata/schema.golden.json` (guards the jsonschema-go pin) |
| `TestSchema_SameOverridesAsOpenAPI` | `config.Duration`, `secrets.Ref` render identically in the settings schema and an `httpapi` fixture |
| `TestSchema_SecretIsWriteOnly` / `TestKeys_NeverContainSecretValues` | secret never in `Effective` or schema defaults |
| `TestSnapshot_ImmutableUnderRace` | `-race`; `coretest` fingerprint check |
| `TestRegistry_SealedAfterLoad` | `ErrSealed` |
| `TestConfigImportsOnlyAllowed` | `go list -deps` allow-list for `core/config`: stdlib, yaml, jsonschema-go, `secrets`, `metrics` |
| `FuzzEnvDecode`, `FuzzPatchDecode` | no panic, typed errors only |

### 1.12 Migration from ABO

#### 1.12.1 Code: façade required, call-site migration optional

R5 binds core only (SIMP-3). ABO's globals survive as books-module façades over core handles; removing them is
an optional ratchet, not an extraction stage.

1. **Stage 0 (required; in ABO, no core needed):** convert the 8 bypassing writes (§1.2) to `Mutate`, and add
   `TestNoDirectAppConfigWrites` (AST: no assignment to `config.AppConfig` outside `internal/config`). Without
   this, the façade's projection would silently drop those writes.
2. **Stage 1 (required; 09 S3) — façade.** ABO's `internal/config` keeps its exported surface and becomes a
   façade. The books module constructs one `legacyconfig.Facade` **value** per App from the handles it registered
   and the core handles it reads (`core.storage`, `core.auth`, …), and installs it as the target of ABO's
   package-level `AppConfig`, `Snapshot`, `Mutate`, `Generation`, `SettingLocks`, `EnvLockedSettings`. `AppConfig`
   is a **projection** rebuilt under the façade's lock on every core generation change (one `OnChange` per
   section); secret fields in the projection are resolved from `secrets` in memory only. `Mutate(fn)` diffs the
   before/after `Config` and turns it into one `Store.Apply` through the alias table. All 215 files and 341 test
   files compile unchanged; tests use a `coretest` store behind the façade. viper is removed from the boot path
   here (`configFromViper` is replaced by core `Load` + aliases). Multi-app is not ruled out: the façade is a
   per-App value; only ABO's delegation from package-level names is process-wide.
3. **Optional ratchet — accessor.** `TestAppConfigRatchet` is added with stage 1: a per-package ceiling of
   `config.AppConfig`/`Snapshot`/`Mutate` uses that may only go down, like
   `internal/logger/slog_guard_ratchet_test.go`. Packages move to an injected `booksettings.Accessor` (ABO repo,
   `internal/books/settings`) when they are touched anyway; largest first if anyone works it deliberately
   (`internal/server` 71 files, `internal/plugins` 33, `internal/metafetch` 15, `internal/maintenance` 11,
   `internal/scanner` 9, `cmd` 9, `internal/audiobooks` 8, then 28 packages with ≤6 files). When the ratchet
   reaches zero, the façade, `AppConfig`, `Mutate`, `Snapshot` and viper are deleted. This work is listed in the
   11 §11.5 checklist ("books: remove `config.AppConfig` façade and package-level loggers", size L) because it is
   a prerequisite only for sharing a binary with other modules. **Trade:** until then ABO tests mutate a façade
   global and cannot use `t.Parallel()` on config.

#### 1.12.2 Data: the config and secrets migration (single mechanism)

This is the only specification of the config and secrets data migration. 09 S3 and contract C3 schedule it,
the R-test (09 §9.10) exercises its rollback rows, and 14 Q-E is answered by it.

**Owner and keys.** The kernel's `core/internal/configstore` runs it (DATA-2). It holds exact-key claims, listed
in the single ownership table of [05-storage](05-storage.md) §5S.9.1, on `setting:config_blob` and on each legacy
secret row named in §2.2 — and on nothing else under `setting:`, `preference:` or `pref:`, which stay books
families (DATA-1). New values live only in `c/config/` (overrides), `c/config/import/` (import record) and
`c/secrets/`. The books module contributes only shape knowledge: its `config.LegacyBlob` and its alias table.
All writes below are core-owned keys in **one** storage batch per step.

**Phases** (09 §9.2.1 vocabulary):

| Phase | Build | What happens |
|---|---|---|
| **T — Teach** | a deployed build before X (09 places it; it gets its own reader-capability level, 09 §9.2.2) | ABO's legacy loader learns to overlay `IsSecret` rows named `setting:<legacy-name>` for **every** secret field in §2.2, not only the 4 it knows (`persistence.go:1429,1441,1443,1813`; others are rejected as `unknown setting key`, `:1884`). Nothing else changes. |
| **X — Expand** | first build at the S3 level, and every build until C | Every boot: keyring import (§2.4 step 1) and the **import / re-import** step below. Every config or secret write: the **mirror** below, in the same batch. `c/config/` and `c/secrets/` are truth from the first X boot. |
| **C — Contract** | 09 C3 (≥ W after X; preconditions per 09: the served ledger shows X served this store for ≥ W) | Run import / re-import one last time (adopt-if-needed: a store that never ran X imports first and never deletes unimported data, OPS-2); stop the mirror; delete `setting:config_blob`, the legacy secret rows and `c/config/import/`. `k0-legacy` stays in the keyring as `retired`; `.encryption_key` is left in place and the boot log tells the operator it may be deleted. |

**Import / re-import (every X boot).** Decode the blob with `LegacyBlob.Decode`, read the legacy secret rows
(decrypted with `k0-legacy`) and, once, `<db dir>/config.yaml` (only keys the store lacks; that file is never
written again and the boot log tells the operator to delete it because it holds plaintext secrets). For each
legacy key with a canonical target compute `h = SHA-256(canonical JSON of its value)` (ciphertext for secret
rows). If `c/config/import/<legacy-key>` is missing or holds a different `h`, (re)import it:

- non-secret key → set the override row if the value differs from **this binary's** shipped default, otherwise
  unset it (future default changes propagate; values a user set equal to the default become "default" —
  release-noted); `ProtectOperator` keys are never imported;
- secret field (blob plaintext, legacy row) → `secrets.Stage` under its final name (§2.2); an empty value is not
  an import (it is what the mirror writes when blanking);
- key whose target is an integration instance (§1.12.3 rows marked *integrations*) → not imported by config:
  its secret is staged under its final `integrations.*` name (instance IDs from the integrations import table,
  [05-metadata](05-metadata.md) M1) and the rest is left in the blob for the integrations import;
- record `h` in the same batch.

The first X boot imports everything. Later boots re-import exactly what another writer changed: the mirror
updates every `h` in the same batch as its own blob and row writes, so a mismatch can only come from the
previous build after a rollback, whose change is newer and wins (DATA-6). There is no field merge.

**Mirror (every write while X is in force).** In the same batch as the `c/config/` or `c/secrets/` change:

1. the blob, re-rendered by `LegacyBlob.Encode` from the new effective view (keys config does not own, such as
   integration families until their import, are carried forward from the stored blob unchanged);
2. for each changed secret the T build reads, the legacy-format row `setting:<legacy-name>`: AES-256-GCM with
   the `k0-legacy` material, random nonce, **no** AAD, as `internal/database/settings.go:189-208` writes today;
3. the new import hashes.

Plaintext secret fields in the rendered blob are written **empty** when the build that booted this store
immediately before the current one (09 served ledger, OPS-2) is at level T or later — i.e. the build a
`make rollback` returns to reads the legacy-format rows. Otherwise (an install that skipped T) they are written
in plaintext so that rollback keeps its credentials, and health token `secrets_plaintext_mirror` is raised. The
rule is evaluated at every boot; the blob is re-rendered once when the outcome changes.

**Rollback matrix** (inputs to 09's R-test):

| Move | Config the target reads | Secrets the target reads |
|---|---|---|
| X → T | blob (current through the mirror) | legacy-format rows (T overlays them) |
| X → pre-T (T skipped) | blob | the 4 legacy rows + plaintext in the blob (not blanked, rule above) |
| T → X again | re-import of every key T changed | re-import of secrets changed on T, whether entered as rows or as blob plaintext; the blob is re-blanked |
| any build ≥ X → `.prev` ≥ X (incl. after C) | `c/config/` | `c/secrets/` |

**Reader capability (OPS-3).** Every build at or after X reads `c/config/` and `c/secrets/` as truth; no format
conversion happens at C, so a rollback from C to `.prev` never boots on defaults.

**Window and rotation.** The blob and legacy secret rows are kept and mirrored for the whole window W (not "one
release"). `k0-legacy` must stay usable until C; key rotation, when it is built (§2.11), refuses to run while
the mirror is active (SEC-20).

#### 1.12.3 Key map (family level)

The per-key golden `core/config/testdata/abo_keys.golden` is generated by walking ABO's `Config` and is owned by
[09-abo-on-core](09-abo-on-core.md). Of the 173 top-level keys (nested sub-keys move with their parent), 64
match platform families and 109 are audiobook; 6 of the 64 (Cloudflare guest provisioning) stay app-side per
`raw/10` G11. Of the remaining 58, **9 become integration instances** (rows marked *integrations*), leaving
**49 → `core.*`**. Books families that become integration instances (Deluge web `deluge_*`, `metadata_sources`,
the Google Books and Hardcover keys) are identified per key in the golden and in 05-metadata M1's import table,
so **≤115 → `modules.books.*`**.

| ABO flat key family | Count | New home | Owner spec |
|---|---|---|---|
| `database_path`, `database_type` | 2 | `core.storage.path`, (`database_type` retired: Pebble only, `config.go:1053`, `config.go:3837`) | 05-storage |
| `api_rate_limit_per_minute`, `json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`, `public_base_url`, `tunnel_connector_addrs`, `allowed_peer_cidrs`, `login_saturated_*` | 9 | `core.http.*` | 05-http-api |
| `log_level`, `log_retention_days` | 2 | `core.log.level`, `core.log.journal.retention_days` | 05-logging |
| `otel_*` (4), `telemetry_environment` | 5 | `core.metrics.*`, `core.tracing.*` | 05-metrics |
| `enable_auth`, `basic_auth_*`, `oauth_*`, `cf_access_*`, `owner_email`, `bootstrap_key_ttl*`, `write_startup_readonly_key` | 18 | `core.auth.*` | 05-authn |
| `cf_account_id`, `cf_guests_policy_id`, `cf_policy_api_token`, `cf_guest_service_tokens`, `cf_service_auth_policy_id`, `invite_email_service_token` | 6 | `modules.books.cfguests.*` | app-side (G11) |
| `smtp_*` | 5 | *integrations*: a `notify/<id>` mail instance | 05-notify, 05-metadata M1 |
| `backup_dir`, `backup_max_total_bytes`, `backup_compression[_level]`, `backup_retention_days` | 5 | `core.backup.*` | 05-backup |
| `auto_update` | 1 | `core.updater.*` | 05-updater |
| `operation_timeout_minutes`, `operation_log_retention_days`, `maintenance`, `scheduled` | 4 | `core.tasks.*`; each `scheduled.<task>` entry becomes the config key its task kind binds with `tasks.ScheduleFromConfig` | 05-tasks |
| `openai_api_key`, `openai_base_url`, `ai_endpoints` | 3 | *integrations*: `ai/<id>` instances | 05-ai, 05-metadata M1 |
| `ai_endpoints_routing`, `ai_backend`, `ai_journal_retention_days` | 3 | `core.ai.*` (global tuning, not instances) | 05-ai |
| `download_client` | 1 | *integrations*: `download/<id>` instance | 05-download, 05-metadata M1 |
| everything else (scan, organize, naming, metadata, dedup, whisper, fingerprint, iTunes, ABS, activity log, catalog, search, embedding, tools, plugins, …) | 109 | `modules.books.<family>.*`, except the integration-bound books families named above | 09 |

#### 1.12.4 Env and flag compatibility

Every env name ABO reads today is registered as an `Alias` of its new path:

- **`AliasWindow`:** the 131 explicit `BindEnv` names (e.g. `ACOUSTID_API_KEY`, `ABS_AUTH_PROBE`,
  `config.go:2702-2747`), the bare upper-case form of every flat key that viper's prefix-less `AutomaticEnv`
  made settable (`cmd/root.go:625`), and the `ABK_*` operational names (`ABK_STATE_DIR` `state_dir.go:25`,
  `ABK_ALLOW_ENCRYPTION_KEY_REGEN` `cmd/root.go:733`).
- **`AliasPermanent`:** (a) ecosystem names: `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`
  (`config.go:2746-2747`), `OPENAI_API_KEY`, `OPENAI_BASE_URL` (09 §9.6 row 16; the `OPENAI_*` pair addresses
  passthrough paths of the AI instance, C11); (b) every name ABO reads today that a shipped deploy artifact
  sets (OPS-5): `DATABASE_PATH` (`deploy/audiobook-organizer.service:98`, `deploy/com.audiobook-organizer.plist:40`),
  `FP_PARALLEL_WORKERS` (unit `:108`, read at `config.go:2735`), `BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME`,
  `BASIC_AUTH_PASSWORD` (`docker-compose.yml:20-22`, read through `AutomaticEnv`), `WHISPER_REMOTE_URL`,
  `ALLOWED_PEER_CIDRS`, `TUNNEL_CONNECTOR_ADDRS`, `LOGIN_SATURATED_RATE`, `LOGIN_SATURATED_BURST`
  (`deploy/local.conf.example:39-61`, read at `config.go:2838` and `invite_config.go`).
- **Not aliases:** names a shipped file sets that ABO does **not** read today stay unread, because honouring them
  would change behaviour: `AUDIOBOOK_ROOT_DIR` (unit `:97`, plist `:38`; deliberately unread because the unit's
  generic value does not exist on the production host, `config.go:2569-2575`), `AO_DIR`, `AO_DB`
  (`docker-compose.yml:18-19`) and `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`), none of which has a reader.
- `TestShippedDeployEnvNamesArePermanent` scans `deploy/*.service`, `deploy/*.plist`, `deploy/local.conf.example`,
  `docker-compose.yml` and `Dockerfile*`: every name found is either a permanent alias or on the explicit
  not-read list above; anything else fails.
- After contract, window aliases of keys with `ProtectAdmin` or higher become `AliasRetired` and boot refuses to
  start if one is set (§1.3.6). [05-storage](05-storage.md) additionally refuses to **create** a store at the
  resolved path when none exists there and a retired database-path name is set.

Flags: ABO's cobra adapter maps `--dir`, `--db`, `--db-type`, `--playlists`, `--log-level` (and the other flags of
09 §9.6 row 18, kept until the next major) to paths.

### 1.13 Open items

1. Whether `encoding/json/v2` is GA in go1.27.3; if so, decode sections with it (strict unknown-field
   handling and `omitzero` are better fits). Decide in wave 0 with the toolchain spec ([13-toolchain](13-toolchain.md)).
2. Exact per-key reload class for ABO keys: default every books key to `ReloadLive` only where the consumer
   reads the handle per use; keys captured at construction (e.g. worker counts) are `ReloadRestart` until the
   consumer subscribes. The per-key audit is a 09 task.
3. Whether the shell needs `x-mediacore-ui` hints (grouping, ordering, widgets) in the schema or a separate
   UI contribution ([07-ui-shell](07-ui-shell.md) owns the renderer).

---

## Part 2 — `core/secrets`

### 2.1 Purpose & scope

Encrypted-at-rest storage for **recoverable** credentials the app must present to someone else (provider API
keys, OAuth client secrets, download-client passwords, SMTP password, signing keys), the **one keyring design**
of the platform (key IDs, a derivation key, loss handling; rotation and prune tooling deferred), the `Value`
type that cannot be printed, `Ref` for config fields and `Scope` for module handles. Not in scope:
**verifiers** — passwords, API keys, session ids, bootstrap/temp-login tokens — which `auth` stores as hashes
and never needs to recover ([05-authn](05-authn.md); session ids stored raw today,
`internal/database/pebble_store_auth.go:1656-1663`, is fixed there). 09 S3 and 14 Q-E reference this keyring
instead of describing their own.

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

**Secret-bearing fields** (verified: the blob builder zeroes only the 4 above, `persistence.go:1955-1958`, and
`marshalConfigBlob` strips only boot-only keys, `persistence.go:2126-2139`). The *legacy row* column is the
`setting:` row name the mirror writes during the window (§1.12.2); "—" means the previous build reads the field
only from env or file, so no row is mirrored.

| Field (JSON key) | Anchor | At rest today | New name (secret) | Legacy row |
|---|---|---|---|---|
| `openai_api_key` | `persistence.go:1955` | encrypted row | `integrations.ai.<instance>.api_key` | `openai_api_key` |
| `google_books_api_key` | `persistence.go:1956` | encrypted row | `integrations.metadata.<instance>.api_key` | `google_books_api_key` |
| `hardcover_api_token` | `persistence.go:1957` | encrypted row | `integrations.metadata.<instance>.api_token` | `hardcover_api_token` |
| `basic_auth_password` | `persistence.go:1958` | encrypted row | `core.auth.basic.password` | `basic_auth_password` |
| `acoustid_api_key` | `config.go:1141` | plaintext `config_blob` (masked in API, `update_service.go:94`) | `modules.books.acoustid.api_key` | `acoustid_api_key` |
| `oauth_github_client_secret` | `config.go:1588` | plaintext `config_blob` (value re-applied from env each boot, still written) | `core.auth.oauth.github.client_secret` | `oauth_github_client_secret` |
| `oauth_google_client_secret` | `config.go:1590` | plaintext `config_blob` | `core.auth.oauth.google.client_secret` | `oauth_google_client_secret` |
| `deluge_web_password` | `config.go:1777` | plaintext `config_blob` (carried forward when env-locked, `persistence.go:1965-1971`) | `integrations.download.<instance>.password` | `deluge_web_password` |
| `download_client.torrent.deluge.password` | `config.go:240` | plaintext `config_blob` | `integrations.download.<instance>.password` | `download_client.torrent.deluge.password` |
| `metadata_sources[].credentials` (map) | `config.go:154` | plaintext `config_blob` | `integrations.metadata.<instance>.credentials.<k>` (one secret per entry) | `metadata_sources.<id>.credentials.<k>` |
| `plugins[].settings` (free-form map) | `config.go:244-247` | plaintext `config_blob`; may hold credentials (unknowable) | retired with `internal/plugin` (02 N4); any surviving entry declared per adapter | — |
| `abs_jwt_secret` | `config.go:1636` (`json:"-"`) | env / viper file only | `modules.books.abs.jwt_secret` | — |
| `smtp_password` | `config.go:1678` (`json:"-"`) | env / viper file only | `integrations.notify.<instance>.password` | — |
| `cf_policy_api_token` | `config.go:1688` (`json:"-"`) | env / viper file only | `modules.books.cfguests.policy_api_token` | — |
| userinfo in URLs (`openai_base_url`, `whisper_remote_url`, `ai_endpoints[].url`, `embedding.base_url`) | `config.go:1222,1319`, `ai_endpoints.go:40` | plaintext if an operator embeds `user:pass@` | `schema:"format=uri"` validation **rejects** userinfo; credentials go in a sibling `secrets.Ref` | — |

`<instance>` for each legacy singleton is fixed by the integrations import table ([05-metadata](05-metadata.md)
M1 migration, with [05-download](05-download.md), [05-ai](05-ai.md), [05-notify](05-notify.md)) before the X build
ships, so each secret is imported once, under its final name.

### 2.3 Go API

Package `github.com/falkcorp/mediacore/secrets` (L2). Imports stdlib (`crypto/aes`, `crypto/cipher`,
`crypto/hkdf`, `crypto/hmac`, `crypto/sha256`, `crypto/rand`, `os`), `github.com/falkcorp/mediacore/storage`
(L1) and `github.com/falkcorp/mediacore/metrics` (L0).

```go
package secrets

// Name grammar: seg = [a-z][a-z0-9_-]{0,63}; name = seg ("." seg)*; the first seg is "core", "modules" or
// "integrations". A config secret's name defaults to its config path; an integration instance secret is
// "integrations.<kind>.<instance>.<field>".
type Name string

func (n Name) Validate() error

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

// Scope is the handle a module receives as module.Deps.Secrets (06 §6.4.4): every method refuses a name
// outside its prefix "modules.<id>." with ErrOutOfScope. Integration-instance secrets are never reachable
// through a Scope; adapters read them only through integrations.Env.Secrets, limited to the secret fields
// their instance declares (05-metadata M1). Core subsystems get a Scope with prefix "core.<subsystem>.".
type Scope struct{ /* store, prefix */ }

func (s *Store) Scope(prefix string) Scope // kernel only; prefix "modules.<id>." or "core.<subsystem>."
func (sc Scope) Prefix() string
func (sc Scope) Get(ctx context.Context, n Name) (Value, Meta, error)
func (sc Scope) Meta(ctx context.Context, n Name) (Meta, error)
func (sc Scope) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (sc Scope) Delete(ctx context.Context, n Name, by string) error
func (sc Scope) DeriveKey(info string) ([]byte, error) // info is prefixed with the scope, so scopes never collide

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
	AppID         string            // app.Options.AppID: a compiled constant of the app's main, bound into AAD
	AllowKeyRegen bool              // core.secrets.allow_key_regen (operator only)
	Legacy        *LegacyKey        // ABO .encryption_key import and legacy-format mirror (§2.4, §1.12.2); nil otherwise
	Logger        *slog.Logger
	Metrics       *metrics.Scope    // core scope "mediacore.secrets"
	Now           func() time.Time
}

type LegacyKey struct {
	Dirs []*os.Root // where a raw 32-byte ".encryption_key" may live (state dir, legacy dir); read-only
}

func Open(ctx context.Context, o Options) (*Store, error)

func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
func (s *Store) Stage(b storage.Batch, w Write) error            // same-batch write used by configstore and integrations
func (s *Store) StageLegacy(b storage.Batch, key []byte, v Value) error // legacy-format row (k0-legacy, no AAD); window only
func (s *Store) OpenLegacy(ciphertext []byte) (Value, error)     // decrypt a legacy row with k0-legacy
func (s *Store) Ephemeral(n Name, v Value, src Source)            // operator-supplied value; locks the key
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]   // never values
func (s *Store) KeyIDsReferenced(ctx context.Context) ([]string, error) // computed by scanning records; used by restore reports
func (s *Store) DeriveKey(info string) ([]byte, error)           // from the derivation key; stable across data-key rotation
func (s *Store) Close() error

var (
	ErrNotSet          = errors.New("secrets: not set")
	ErrKeyUnavailable  = errors.New("secrets: encryption key for this value is not in the keyring")
	ErrCorrupt         = errors.New("secrets: ciphertext failed authentication")
	ErrUnbound         = errors.New("secrets: Ref used before the store was attached")
	ErrNotSerializable = errors.New("secrets: Value must not be serialized")
	ErrKeyfileInsecure = errors.New("secrets: keyring file permissions are too open")
	ErrKeyMissing      = errors.New("secrets: no keyring and encrypted values exist")
	ErrOutOfScope      = errors.New("secrets: name outside this handle's scope")
	ErrAppIDMismatch   = errors.New("secrets: store belongs to a different app ID")
	ErrNoLegacyKey     = errors.New("secrets: legacy key not imported")
)
```

**Formats (normative; the format carries key IDs from v1 so it never changes when rotation lands).**

- Keyring file `<state_dir>/secrets.keyring`, 0600, directory 0700, written atomically via `os.Root`
  (temp + fsync + rename):
  `{"format":"mediacore-keyring/1","keys":[{"id":"k1","use":"data","created":"…","status":"active","material":"<base64 32 bytes>"},{"id":"d1","use":"derive",…},{"id":"k0-legacy","use":"data","status":"retired",…}]}`.
  Exactly one `active` key per `use`. `use: "data"` keys encrypt records; the single `use: "derive"` key feeds
  `DeriveKey` and the fingerprint subkey and is **never** replaced by data-key rotation, so OAuth state codecs,
  cursor HMACs and refresh derivations survive a rotation (SEC-28).
- Subkeys via `crypto/hkdf` (SHA-256): data subkey from the active data key, info
  `"mediacore/secrets/aes-256-gcm/v1"`; fingerprint subkey from the derive key, info
  `"mediacore/secrets/fingerprint/v1"`. Other specs derive their own subkeys with distinct `info` strings through
  `DeriveKey` / `Scope.DeriveKey` instead of storing separate secrets.
- Stored record at key `c/secrets/v/<name>`: JSON `{"v":1,"kid":"k1","nonce":"…","ct":"…","at":"…","by":"…"}`;
  AAD = `"mediacore/secrets/v1\x00" + AppID + "\x00" + name`, so a ciphertext moved to another name or app
  fails with `ErrCorrupt`.
- Store metadata at `c/secrets/meta`: `{"app_id":"abo","keyring_format":"mediacore-keyring/1","created":"…"}`.
  `Open` compares it with `Options.AppID`; a mismatch is `ErrAppIDMismatch` (boot error naming both IDs), so an
  accidental AppID change fails loudly instead of turning every secret into `ErrCorrupt` (SEC-28). AppID is a
  compiled constant of the app's `main` (`app.Options.AppID`), never configuration.

### 2.4 Lifecycle

1. After storage opens (§1.4 step 6): `Open` checks `c/secrets/meta` (written on first open) and resolves the
   keyring:
   - keyring present → load; mode looser than 0600 → try `chmod 0600`; if that fails → `ErrKeyfileInsecure`
     (fail-closed);
   - keyring absent, no stored records → generate `k1` (data) and `d1` (derive), write keyring (first run);
   - keyring absent, stored records exist → `ErrKeyMissing` (boot refused, message names the file, the exported
     copy (§2.4 Backups) and the override). With `core.secrets.allow_key_regen=true` new keys are generated **and
     existing records are kept** under their old key id (`Meta.Err = ErrKeyUnavailable`); putting the old keyring
     back later recovers them. Nothing is ever deleted automatically (contrast `persistence.go:1232-1260`).
   - `Legacy` set and an ABO `.encryption_key` found → its material is copied into the keyring as `k0-legacy`
     (`use: data`, `status: retired`). The file itself is left untouched at its path: the previous build keeps
     using it during the window (09 S3 "same key file" means this). The import, re-encryption of the legacy rows
     under the new format, and the legacy-format mirror are the secrets half of §1.12.2.
2. `config.Load` registered `Ephemeral` values for secret keys supplied by env/file.
3. Runtime: `Get` decrypts on demand (no plaintext cache beyond the call; callers hold `Value` briefly).
   `Put`/`Stage`/`Delete` encrypt with the active data key. No background goroutine and no timer.

**Backups and loss (SIMP-9).** The keyring lives in the state dir, not the data dir. Today no backup carries the
key (`internal/backup` archives the DB directory, `raw/12` §6; `grep` finds no key handling in `internal/backup`),
so a lost key file loses the secrets. Rules:

- The keyring is **never** stored only inside an archive it protects. Unencrypted archives never contain key
  material, so the secrets in them stay ciphertext. Whether an encrypted archive also carries a copy is
  [05-backup](05-backup.md)'s decision; this design never treats that copy as the recovery path, because the
  state directory that holds the keyring may also hold the archive's identity.
- Recovery is an explicit operator action: `<app> secrets export-keyring --out <file>` (CLI, filesystem access
  required, refuses to write a file looser than 0600, works while the server runs because it reads only the
  state dir). It records the exported key IDs and time in `<state_dir>/secrets.keyring.exported` (no key
  material). Health warning `secrets_keyring_not_exported` stays raised while any key in the keyring is missing
  from that record.
- Losing the state directory without an exported keyring means re-entering secrets (each affected secret shows
  "must be re-entered"); the data restores. Restore reports key IDs referenced by restored records
  (`KeyIDsReferenced`, computed by scanning, not from a manifest) that the current keyring lacks, before it
  starts.

### 2.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Keyring unreadable (exists) | fail-closed (boot) | never overwrite a key we failed to read (ABO's lesson, `settings.go:83-98`) |
| Keyring too permissive and not fixable | fail-closed | secret material exposure |
| No keyring, encrypted records exist | fail-closed unless `allow_key_regen`; with override, records kept, not deleted | recovery must stay possible |
| `c/secrets/meta` app ID ≠ `Options.AppID` | fail-closed (`ErrAppIDMismatch`) | a rename would otherwise make every secret `ErrCorrupt` |
| Record under unknown key id | fail-open per secret: `ErrKeyUnavailable`, health `secrets_undecryptable`, UI "re-enter" | the app keeps working; the integration using it reports itself unconfigured |
| AAD/auth failure | fail-open per secret: `ErrCorrupt`, same reporting | tampering or swap; never "repaired" automatically |
| `Put` storage error | fail-closed: error to caller, nothing changed | |
| `Scope` name outside prefix | `ErrOutOfScope` | handle scoping (06 A3) |
| Legacy row undecryptable during import | fail-open per secret: not imported, health `secrets_undecryptable`, legacy row kept | same as ABO's guard, without the deletion |
| `StageLegacy` without `k0-legacy` | fail-closed (`ErrNoLegacyKey`): the batch is not committed | the previous build must never read a stale secret |
| `Ref.Resolve` with no value | `ErrNotSet` | consumer decides |
| `Value` JSON/text marshal | error `ErrNotSerializable` | accidental exposure fails the response instead of leaking |

### 2.6 Concurrency contract

`Store` is safe for concurrent use. Reads take a read lock on the in-memory keyring only; storage reads are
lock-free snapshots. `Put`/`Delete`/`Stage` serialize per name via a keyed mutex (the existing
`internal/keyedmutex` moves into core per 03 §3.2.1 E2). The keyring changes only at `Open`. The store starts
no goroutine.

### 2.7 Config keys

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.secrets.key_file` | path (relative to state dir) | `secrets.keyring` | no | operator |
| `core.secrets.allow_key_regen` | bool | `false` (ABO alias `ABK_ALLOW_ENCRYPTION_KEY_REGEN`, window class) | no | operator |

### 2.8 Metrics

All on scope `mediacore.secrets`.

| Name | Type | Labels |
|---|---|---|
| `mediacore_secrets_operations_total` | counter | `op` ∈ get, put, delete; `outcome` ∈ ok, not_set, key_unavailable, corrupt, out_of_scope, error |
| `mediacore_secrets_undecryptable` | gauge | — |
| `mediacore_secrets_keyring_keys` | gauge | `use` ∈ data, derive; `status` ∈ active, retired |
| `mediacore_secrets_legacy_mirror_writes_total` | counter | `outcome` ∈ ok, error (window only) |

### 2.9 Tests

| Test | Asserts |
|---|---|
| `TestValue_NeverFormats` | `%v %s %q %x %#v %+v`, `slog` text and JSON handlers, `errors.New(fmt.Sprint(v))` all print `[REDACTED]`; `json.Marshal` errors |
| `TestAAD_SwapBetweenNamesFails` | copy record A to B → `ErrCorrupt` |
| `TestOpen_AppIDMismatchRefuses` | stored `abo`, opened as `xyz` → `ErrAppIDMismatch` |
| `TestOpen_MissingKeyringWithRecordsRefuses` / `TestOpen_RegenKeepsOrphans` | `ErrKeyMissing`; override keeps rows; old keyring restores them |
| `TestOpen_KeyringPermissions` | 0644 fixed to 0600 or refused |
| `TestDeriveKey_IndependentOfDataKey` | replacing the active data key in a fixture keyring leaves `DeriveKey` output and fingerprints unchanged |
| `TestScope_RefusesOutsidePrefix` | `modules.books.` scope cannot read `core.*` or `integrations.*` names |
| `TestLegacyImport_ABOEncryptionKey` | raw 32-byte key + 4 legacy rows → readable, re-encrypted with AAD, `k0-legacy` retired, `.encryption_key` untouched |
| `TestLegacyMirror_PreviousBuildReads` | a row written by `StageLegacy` decrypts with ABO's `database.DecryptValue` algorithm (fixture copy) |
| `TestEphemeral_NeverPersisted` | env secret never reaches storage |
| `TestFingerprint_StableNotReversible` | same value same fingerprint; differs per keyring |
| `TestExportKeyring_ClearsHealthWarning` | export record covering every key ID clears `secrets_keyring_not_exported` |

The migration tests (`TestMigration_ReimportAfterRollback`, `TestMigration_BlankOnlyAfterTeachServed`,
`TestMigration_ContractAdoptsFirst`) live with `configstore` and run the §1.12.2 rollback matrix.

### 2.10 Migration from ABO

The data half (key import, secret import and re-import, legacy-format mirror, plaintext blanking, contract) is
specified once, in §1.12.2. Code replacement:

1. Replace `database.EncryptValue/DecryptValue/InitEncryption/MaskSecret/IsEchoedMask/LooksLikeMask`
   (`settings.go:110,189,214,271,285,293`) and `cmd/root.go` `guardAgainstKeyRegeneration` (`cmd/root.go:735`) with
   `secrets.Open`; the legacy key search order (state dir, then legacy dir) is that of `database.InitEncryption`
   (`settings.go:110-140`).
2. ABO's `/api/v1/config` compat handler returns masks during the window by composing `Meta` (`set`) with the old
   mask shape; the core settings API never does.
3. ABS JWT secret, Cloudflare policy token, SMTP password: env stays authoritative (aliases); a value entered in
   the UI is stored encrypted for the first time (no legacy row: the previous build reads these from env only).

### 2.11 Open items

1. **Rotation and prune (deferred, SIMP-19).** Not built in v1: after a suspected key leak the operator re-enters
   secrets. Constraints already fixed for when it is built: records name their `kid`, so rotation is "new active
   data key + re-encrypt"; the derive key is rotated by a separate command because rotating it invalidates
   derived artifacts; rotation refuses while the §1.12.2 mirror is active; pruning a retired key is safe only when
   `KeyIDsReferenced` no longer lists it and the export record covers the current keyring (no coupling to backup
   manifests).
2. **UI recovery kit.** A shell action that downloads the keyring would put master key material in an HTTP
   response; if wanted, it needs `ProtectOwner`-level checks, a fresh credential proof and an audit record, and
   it would clear `secrets_keyring_not_exported` the same way as the CLI. Owner decides; the CLI ships in v1.
3. Hardware/OS keystores (TPM, macOS Keychain) as keyring backends: out of scope; the keyring file interface
   leaves room (`Options.StateDir` could become a `KeyringBackend`).
4. Cross-spec alignment: [05-authn](05-authn.md) writes its dependency as `secrets.Store` (value); this file
   defines `*secrets.Store` (it holds a keyring and locks) — authn should take a `secrets.Scope` (prefix
   `core.auth.`) or the `secrets.Resolver` interface. [05-backup](05-backup.md)'s manifest field `SecretsKeyID`
   is not needed: restore computes `KeyIDsReferenced` from the restored records.

---

## Objections to 03

None open. The round-0 items (an internal order inside the old shared layer; the §3.3 `config` row not saying
that operator-set keys are locked) are resolved by 03 v1.2.0+ (R4 L1/L2/L3; §3.3 `config` row); 03 v1.3.0 R5 also states that ABO's façades are allowed. Note for readers:
`compat` (L4) sits above `config` (L3), so config aliases are registered with `compat` by the kernel and use is
reported through `Options.OnLegacyUse` (§1.3.6); no R4 change is needed.

## Out-of-scope defects found

1. Eight writes to `config.AppConfig` bypass `Mutate` (§1.2 anchors): unsynchronized, and invisible to
   `config.Generation()` consumers (`internal/ai/ai_metrics.go:169`). Fixed in migration stage 0 because the
   façade depends on it.
2. Three encrypted secrets are also persisted in plaintext to `<db dir>/config.yaml` on every settings save
   (`persistence.go:168-178`), which defeats their encryption. Fixed by §1.12.2 (the file is imported once and
   never written again).
3. Secret ciphertext has no associated data (`settings.go:189-208`): rows are swappable between keys.
4. `LoadConfigFromDatabase` deletes secret rows it cannot decrypt or recover (`persistence.go:1253-1260`, `DeleteSetting` at `:1258`),
   turning a temporarily missing key file into permanent loss when the guard is overridden.
5. The shipped `docker-compose.yml` sets `AO_DIR` and `AO_DB` (`:18-19`) and `deploy/local.conf.example` sets
   `OLLAMA_BASE_URL` (`:40`), but no Go code reads any of them at `748fd787a`: a compose user's database is not
   where the file says. The fix (rename them in the shipped files to names ABO reads) belongs to 09 S3's
   deploy-file update; this spec keeps them unread (§1.12.4).

## Review history

- SIMP-3 — §1.12.1: stages 2–3 became an optional ratchet; façade is a per-App books-module value; C1 updated.
- SIMP-4 — C11, §1.1, §1.3.3 `Passthrough`, §1.9, §1.12.3 (*integrations* rows), §2.2 `integrations.*` secret names.
- SIMP-7 — §1.3.6: aliases registered with `compat`, no window logic, `core.config.legacy_aliases` deleted, one counter.
- SIMP-9 — §2.4 Backups and loss: keyring never only in an archive; export-keyring record and health warning.
- SIMP-13 — n/a here (see 05-metrics); none of this file's metric names change.
- SIMP-14 — C10, §1.10.2: `google/jsonschema-go` v0.4.3 verified 2026-11-04 (MIT, v0.4.3 latest stable, maintained) and ADOPTED; in-house reflector rejected.
- SIMP-15 — C12, §1.12.2: single config/secrets migration mechanism owned here; §2.10 points to it.
- SIMP-19 — §2.3/§2.4/§2.7/§2.8/§2.11: keyring with key IDs ships; `Rotate`, `PruneKeys`, schedule, straggler goroutine deferred.
- SEC-6 — `ProtectAdmin`, §1.3.4, §1.3.7 class table and `TestEveryURLPathOrSignInKeyClassified`; Protection columns.
- SEC-20 — §2.1/§2.4 one keyring; §1.12.2 legacy-format mirror with `k0-legacy`; rotation refused while mirroring.
- SEC-28 — §2.3: separate derive key; `c/secrets/meta` app-ID check (`ErrAppIDMismatch`); AppID is `app.Options.AppID`.
- SEC-29 — §2.3 `Name` grammar and `secrets.Scope` (`modules.<id>.`); instance secrets only via `integrations.Env.Secrets`.
- DATA-1 — C4, §1.12.2: overrides only in `c/config/`; `setting:`/`preference:`/`pref:` never adopted; exact-key claims only.
- DATA-2 — §1.12.2: migration owned by kernel `configstore`, claims listed in 05-storage §5S.9.1.
- DATA-6 — §1.12.2: blob mirrored for W, hash-based re-import at every boot, secrets re-imported and re-blanked.
- OPS-3 — §1.12.2: truth is `c/config/` from X, so no contract-time format change.
- OPS-5 — C7, §1.3.6 `AliasRetired` fail-closed, §1.12.4 permanent shipped-file names and not-read list.
- OPS-9 — §1.12.2: one S3 model (Teach + mirror + conditional blanking).
- OPS-17 — §1.7: no runtime compat switch; operational dials allowed.
- COR-14 — `ReloadBoot`, boot record (§1.3.5), `Store.Boot` (§1.4 step 2) for module enablement.
- COR-19 — header and §1.3/§2.3: R4 v1.3.0 layers (L1 storage, L2 secrets, L3 config).
- COR-20 — `Options.Metrics *metrics.Scope` in config and secrets.
- COR-26 — `Options.AppID` documented as `app.Options.AppID`.
- SEC-15 (R-F request) — §1.3.7: `core.integrations.egress.lan_allow` classified `ProtectAdmin` (request asked `ProtectInteractive`; SEC-15 fix says admin).
- SIMP-6 — §1.6, §1.12.3: no timers in config; `scheduled.*` via `tasks.ScheduleFromConfig`; rotate schedule removed.
