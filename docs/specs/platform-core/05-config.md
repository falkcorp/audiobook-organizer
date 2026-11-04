<!-- file: docs/specs/platform-core/05-config.md -->
<!-- version: 1.4.0 -->
<!-- guid: fb637cbc-bbfa-4aea-807c-71f02534b3c9 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Config and secrets (`core/config`, `core/secrets`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §2, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7, G12;
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md) finding 3;
[`raw/03-layering-guard.md`](../../plans/platform-core/raw/03-layering-guard.md) `allowed` rows 1–5;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md). Normative names come from
[03-architecture](03-architecture.md) v1.5.1: R4 layers **L1** `storage` → **L2** `secrets` → **L3** `config`
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
| C4 | **One stored format per family during the extraction (R3-1).** The DB layer is a **legacy-format adapter**: a key that has a legacy blob name is read and written **in place** in ABO's `setting:config_blob`, in the legacy JSON shape; a key with no legacy name is a per-key row in the core-only family `c/config/`, which older builds never read (they have no such key). Nothing is copied, mirrored or adopted, and no level step exists for config. Config reaches the blob and the four legacy secret rows (exact keys) through one logged storage grant and reads nothing else under `setting:`, `preference:` or `pref:`. **Cutover** to native keyspaces is deferred and out of the extraction (§1.12.2). | Every round's dual-format fix produced new cross-stage defects (round-3 dispositions, "Why round 3 changes course"); one format has none. A blob value equal to the shipped default reads as default, so the blob's frozen defaults (`persistence.go:1180-1188` measured 33 keys inheriting stale defaults) stop pinning future defaults. `setting:` is a general runtime-marker store, not config (DATA-1). |
| C5 | Every key declares `Reload` (live / restart / boot) and `Protection` (none / interactive / admin / owner / operator); both appear in the generated JSON Schema. | ABO has exactly one boot-only key handled ad hoc (`persistence.go:2111`) and a 92-entry protection map (`protected_fields.go`) separate from the schema. Roles-as-data can hand `core:settings.manage` to non-admins, so sign-in, proxy and updater keys need an admin level (§1.3.7). |
| C6 | Drop **viper** from core and, by the end of migration, from ABO. Stdlib + `go.yaml.in/yaml/v3` + `github.com/google/jsonschema-go` only. | §1.10. |
| C7 | Env names are `<APP>_<PATH>`; no `MEDIACORE_` prefix. ABO's existing names are aliases; each alias names its 09 §9.2.9 ledger row, which alone says whether it is a window, permanent or retired name. **Permanent** rows: ecosystem names (`OTEL_*`, `OPENAI_*`) and every name ABO reads today that a shipped deploy file sets (e.g. `DATABASE_PATH`). | §1.3.6, §1.12.4. Dropping `DATABASE_PATH` after the window would open a new, empty database (OPS-5). |
| C8 | Every recoverable secret is encrypted at rest by `core/secrets` with **one keyring design** (AES-256-GCM, key IDs in every record, AAD = app + name, a separate derivation key); config fields are `secrets.Ref`, never strings. The keyring is the install's **only** key artifact: backup's archive identity is derived from it, so one export recovers both (§2.4). Data-key rotation and prune tooling are **deferred**; the format already carries key IDs so it never changes. The derive key has an offline rotation command in v1 (§2.3). Verifiers (passwords, API keys, session ids) are hashed by `auth`, never stored in `secrets`. | `raw/10` G12: 4 of at least 10 secret-bearing fields are encrypted, and 3 of those 4 are also written in plaintext to a mirror YAML file (`persistence.go:168-178`). Rotation does not exist today (`raw/10` §7). A second key file would need a second export (SIMP2-2). |
| C9 | Secrets are **write-only** over the API: the UI receives `{set, source, updated_at, fingerprint}`, never a mask. | Masks reveal the last 2 characters (`internal/database/settings.go:250-277`) and force echo-detection logic (`update_service.go:353-373`). |
| C10 | **One JSON-Schema reflector**: `github.com/google/jsonschema-go`, held at v0.4.3 (Dependabot `ignore` entry plus `check_module_skew.py --pin`, 13), wrapped by the L0 leaf `core/internal/jsonschemax`, which owns the tag pass and an extensible override table. Each package contributes the entries for its own types: config adds `config.Duration` and `secrets.Ref`; [05-http-api](05-http-api.md) adds `rbac.Permission` for OpenAPI. | §1.10.2; verified 2026-11-04. A config-owned table could not name an L4 type (SIMP2-3). |
| C11 | **Config holds no integration instances.** Instance records (kind, instance, settings, `secrets.Ref`s) live in the `integrations` store ([05-metadata](05-metadata.md) M1, keyspace `c/integrations/instances/`); ABO's legacy singletons that today live in the blob stay there until cutover, and integrations reads and writes their blob keys in place through configstore's blob participant (§1.12.2), never through config keys. Config only reads the operator-layer values (file, env, flag) of the legacy integration-bound keys raw, at their `integrations.<kind>.<instance>.<field>` paths, and hands them over (§1.3.3 `Passthrough`). integrations applies them as an **in-memory overlay** on the instance's stored row, never persisted (05-metadata M1.4). Removing the operator value reverts to the stored row, as HEAD does (`persistence.go:1959-1971`). There is no `core.integrations.<kind>.instances.<id>` config form. | One store for one fact (SIMP-4): the Deluge password had four destinations across the specs. One lock model, config's operator-layer lock, for config keys and instance fields alike (SIMP2-5). |
| C12 | This file owns the **config and secrets legacy-format adapter** and the deferred cutover (§1.12.2); 09 S2–S3 sequence it and reference it. | Specified twice with contradictory semantics before (SIMP-15, OPS-9). |
| C13 | Config and secrets write nothing to the store at boot except `c/secrets/meta` on a new store, and that only after the boot's pre-migration checkpoint and the storage DAG; attaching the DB layer is read-only. Their boot steps are placed by the one boot-phase table, [06](06-app-sdk.md) §6.5.1 (R2-8). | A write before the checkpoint puts post-upgrade rows into the restore point (COR2-7). |
| C15 | **The state directory keeps HEAD's fallback (R3-4).** Explicit env or flag; else the compiled default when it can be created; else the database's directory, with a warning. An explicit value never falls back (§1.7). | Docker and launchd installs keep `.encryption_key` beside the database today because the compiled default is not creatable there (`internal/config/state_dir.go:118-155`); without the fallback they fail to boot or put the keyring in the container layer (OPS3-1). |
| C14 | **One schedule-key rule (R2-7).** A schedule key is `<owner>.<subsystem>.schedules.<name>` (`core.<subsystem>.schedules.<name>` or `modules.<id>.<subsystem>.schedules.<name>`), a field of type `tasks.ScheduleConfig` declared by its owner with a default and bound with `tasks.ScheduleFromConfig(key)` (§1.6). | Eight files declared keys under a form 05-tasks refused (SIMP2-1). |

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
| Sources, in actual application order | (1) viper defaults + `AutomaticEnv` (no prefix) + 142 explicit `BindEnv` + viper YAML file `<home>/.audiobook-organizer.yaml` or `--config`, decoded by `configFromViper`; (2) 4 flags bound (`dir`, `db`, `db-type`, `playlists`) plus `--log-level` written straight into `AppConfig`; (3) DB `config_blob` **replaces the whole struct**; (4) env-authoritative keys re-applied (auth-surface keys unconditionally, operational keys only when env supplies them); (5) a second YAML file `<db dir>/config.yaml` fills 4 empty fields; (6) `SyncConfigFromEnv` re-applies 6 keys from env if non-empty | `cmd/root.go:603-660`, `cmd/root.go:55-60`, `cmd/root.go:650`, `config.go:2586-2597,3215`, `persistence.go:915,1229`, `config.go:2435`, `persistence.go:42-104`, `cmd/root.go:381`, `persistence.go:2023` |
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
`github.com/google/jsonschema-go/jsonschema`, `core/internal/jsonschemax` (L0 leaf: the shared reflector
wrapper, §1.10.2), `github.com/falkcorp/mediacore/secrets` (L2; `secrets.Ref` and `secrets.Resolver` only) and
`github.com/falkcorp/mediacore/metrics` (L0; its own `*metrics.Scope`). It does **not** import `storage`, `log`,
`auth`, `rbac`, `tasks`, `compat`, `integrations` or any module (§1.9).

#### 1.3.1 Declaring a section (module side)

```go
package config

// Path is a dotted key path: "core.log.level", "modules.books.scan.interval".
// Grammar: seg = [a-z][a-z0-9_]{0,47}; path = seg ("." seg)*; first seg is "core" or "modules".
// The keys of a map-typed value are data, not path segments: they follow the map's own key type (a task
// kind such as "books.dedup.full-scan" inside a map key "core.tasks.kinds"), and env, file and patches set
// the whole map as one JSON value (§1.3.6). Config never encodes '.' or '-' into a segment (COR3-11).
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
	ProtectOwner                             // also the owner proof (05-authn) while an owner identity is set and an
	                                         // owner verifier is configured; until then it is checked as ProtectAdmin
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
// A section path may extend another section's path (core.log and core.log.journal) only at a segment
// the parent's type does not declare as a field; an overlap fails Register with ErrDuplicate.
type Section[T any] struct{ /* unexported: path, defaults, opts */ }

func Define[T any](path Path, defaults T, opts ...SectionOption) Section[T]

type SectionOption func(*sectionSpec)

func Permissions(read, write string) SectionOption               // rbac strings; default derived from path (§1.3.4)
func Docs(d map[Path]string) SectionOption                       // key -> description for schema/UI
func Aliases(a ...Alias) SectionOption                           // legacy names (§1.3.6); registered with compat by the kernel
func Deprecated(p Path, replacement Path) SectionOption          // schema deprecated:true; removal is a contract PR (09 §9.2.9)
func ProtectionWaivers(w map[Path]string) SectionOption          // key -> reason it needs no class despite its shape (§1.3.7)

// Alias is one legacy spelling of a canonical key. 09 §9.2.5 refers to this type. It carries nothing
// about its lifetime: window, permanent or retired, and every date, come from its ledger row (SIMP2-12).
// Aliases of a module's own keys are passed with config.Aliases; aliases that give an app's legacy names
// to core keys come from the app (Legacy.Aliases, §1.3.3), because core holds no app's key names (R1).
type Alias struct {
	Legacy    string               // flat key in the operator YAML file and in config_blob, e.g. "root_dir"; "" if env-only
	LegacyEnv []string             // env names read today (AutomaticEnv upper-case key + BindEnv names)
	Canonical Path
	Convert   func(json.RawMessage) (json.RawMessage, error) // optional legacy → canonical unit/shape change
	Revert    func(json.RawMessage) (json.RawMessage, error) // canonical → legacy; required when Convert is set and
	                                                         // Legacy is non-empty (the adapter writes the blob, §1.12.2)
	Ledger    string               // ledger row ID: the app's ledger (app.Options.CompatLedger) or core's own fragment
	                               // (09 §9.2.9, SIMP3-3); TestCompatLedgerMatchesCode joins them
}

// Schema is a JSON Schema 2020-12 document plus mediacore vendor keywords, produced by the
// shared reflector (§1.10.2). integrations descriptors use it for instance settings (05-metadata M1).
type Schema struct{ /* *jsonschema.Schema + resolved form */ }

func SchemaFor[T any]() (Schema, error)                          // jsonschemax with config's override entries (§1.10.2)

// Overrides returns config's static contribution to the shared reflector: config.Duration and secrets.Ref
// only (§1.10.2), as a fresh map (a function, not an exported map variable, R5). A value, never a store:
// httpapi imports it to render the same DTO types in OpenAPI (SIMP3-1).
func Overrides() jsonschemax.Overrides
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
| field type `secrets.Ref` | secret; schema `writeOnly: true`; value lives in `secrets` (Part 2); never `reload:"boot"` (the boot record holds no secret; `Register` refuses the combination) | `APIKey secrets.Ref \`json:"api_key"\`` |

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
		Canonical: "modules.books.scan.concurrency", Ledger: "config/concurrent_scans",
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
	EnvPrefix   string            // app.Options.EnvPrefix, default strings.ToUpper(AppID) ("ABO"); [A-Z][A-Z0-9]{1,15}
	Environ     []string          // os.Environ() captured once by app.New; config never calls os.Getenv
	FilePath    string            // operator YAML file; "" = none. Read-only: core never writes it.
	Flags       map[Path]string   // ONLY flags the operator explicitly set (FromFlagSet / app adapter)
	// BootRecordRoot is called by Boot with core.paths.state_dir as ResolveStateDir resolved it (env, flags,
	// compiled default, HEAD's fallback; never the file or the record it locates, §1.7); the kernel opens
	// and returns the os.Root that holds config.boot.json (§1.3.5).
	BootRecordRoot func(stateDir string) (*os.Root, error)
	Passthrough func(Path) bool   // operator-layer paths config reads raw and does not own (v1: the legacy
	                              // integration-bound keys of §1.12.3, at the integrations.<kind>.<instance>.<field>
	                              // paths of 05-metadata M1.10's import table; their env aliases, e.g. OPENAI_*, are permanent)
	// Retired reports whether an alias's ledger row is in state Retired (09 §9.2.9). The kernel wires it
	// to compat; nil means no alias is retired. It is config's only lifetime input (§1.3.6).
	Retired     func(ledger string) bool
	// OnLegacyUse reports one use of an alias. The kernel buffers reports made before storage opens and
	// flushes them to compat once the ledger is reachable (06 §6.5.1): SurfaceEnv reports go to
	// compat's per-boot presence record, SurfaceConfig reports to its use record (§1.3.6, OPS2-7).
	OnLegacyUse func(a Alias, s Surface)
	// Legacy is the app's legacy settings format (app.Options.LegacyConfig; nil for an app without one).
	// Its Aliases are registered with the core sections they name; the rest is used by the DB layer (§1.12.2).
	Legacy      *Legacy
	Logger      *slog.Logger
	Metrics     *metrics.Scope    // core scope "mediacore.config" (05-metrics)
	Secrets     secrets.Resolver  // may be nil until secrets opens; Refs resolve after SetSecrets
	Now         func() time.Time
}

func NewStore(o Options) (*Store, error)

// ResolveStateDir is the one pre-boot rule for core.paths.state_dir (§1.7, C15, OPS3-1). The kernel runs it
// once per process, before 06 P−1, and hands the result to P−1, P0a, Boot and every consumer, so all agree
// (HEAD memoises for the same reason, internal/config/state_dir.go:57-79). explicit is the env or flag value
// ("" if unset); def is app.Options.StateDir; dbPath is core.storage.path from the pre-boot layers (compiled
// default, env, flags; P−1 passes the db_path recorded in its marker); mkdir is the kernel's MkdirAll(dir, 0o700).
func ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error)

func (s *Store) Registry() Registry
func (s *Store) Boot(ctx context.Context) (BootView, error)              // core sections only; operator layers + boot record
func (s *Store) Load(ctx context.Context) error                          // seals Registry; defaults+file+env+flags+boot record
func (s *Store) SetSecrets(r secrets.Resolver)
func (s *Store) AttachOverrides(ctx context.Context, o OverrideStore) (AttachReport, error) // read-only; once per boot
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error)
func (s *Store) Lookup(p Path) (KeyState, bool)
func (s *Store) Keys() iter.Seq2[Path, KeyState]                         // sorted; secret values never included
func (s *Store) Aliases() iter.Seq[Alias]                                // for compat ledger registration (09 §9.2.9)
func (s *Store) PassthroughValues() iter.Seq2[Path, OperatorValue]       // raw; handed to integrations by the kernel
func (s *Store) Schema(include func(section Path) bool) (json.RawMessage, error)
func (s *Store) Generation() uint64
func (s *Store) Close(ctx context.Context) error

// ConfigFileHasSecrets reports whether the operator file supplies a value for any secret key (a
// secrets.Ref field, or a legacy file key aliased to one) or for any Passthrough path (integration-bound
// values may be credentials, so they count). 05-backup then archives the file as class Secret (DATA2-17).
func (s *Store) ConfigFileHasSecrets() bool
// RedactedConfigFile renders the operator file as YAML without those values, for unencrypted archives.
func (s *Store) RedactedConfigFile() ([]byte, error)

// Surface is the compat surface an alias use is recorded on (09 §9.2.9).
type Surface uint8

const (
	SurfaceEnv    Surface = iota + 1 // env var or operator-file key: read once per boot, reported once per boot
	SurfaceConfig                    // legacy API path (ABO's /api/v1/config compat handler): reported per use
)

// BootView exposes the resolved ReloadBoot and ProtectOperator keys of core sections (module enablement,
// core.paths.*, core.storage.path) before module sections exist and before storage opens.
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

// OverrideStore persists the DB layer. Implemented by the kernel (core/internal/configstore) as the
// legacy-format adapter of §1.12.2: blob-named keys in setting:config_blob through the storage grant,
// other keys in c/config/; config itself never imports storage (§1.9).
type OverrideStore interface {
	Load(ctx context.Context) iter.Seq2[Override, error]
	// Commit writes, in ONE storage batch: the blob re-render for changed blob-named keys, c/config/ rows
	// for the others, the staged secret writes (each blanking its legacy copy, §2.3), and the audit record
	// core.settings.update (05-rbac §7.1: section and changed key names, never secret values;
	// audit.RecordIn, actor = Override.By). configstore is kernel-side, so config (L3) never imports audit (L4).
	Commit(ctx context.Context, c Commit) error
	// Changed fires when another writer changed stored values: ABO's own legacy config path, which writes
	// the blob itself until the S3 façade (§1.12.2). The store then re-runs Load, republishes live keys and
	// marks restart keys pending. nil = never.
	Changed() <-chan struct{}
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

// Legacy is everything an app knows about its legacy settings storage, supplied once by its main
// (app.Options.LegacyConfig). Core holds none of these names (R1). ABO: books' blob codec, the row names
// and the alias table for core keys.
type Legacy struct {
	BlobKey string          // the blob row, ABO "setting:config_blob"
	Blob    LegacyBlob      // its shape
	Secrets []LegacySecret  // every secret field the previous build stores (§2.2)
	File    string          // gap-fill file beside the database, read only (ABO "config.yaml"); "" = none
	Fill    []string        // the legacy keys that file fills when the blob value is empty (ABO: 5, persistence.go:42-104)
	Aliases []Alias         // legacy names of CORE keys; module sections carry their own (§1.3.1)
	// Rows is raw read access to the legacy rows for a build that has no core storage yet (09 S2: the store
	// ABO's serve opened before app.New). From S2b the kernel reads through the storage grant and Rows is unused.
	Rows    LegacyRows
}

// LegacySecret says where the previous build keeps one secret.
type LegacySecret struct {
	Name      secrets.Name // final name (§2.2)
	BlobField string       // plaintext field in the blob; "" if none
	Row       string       // encrypted row (ABO .encryption_key format), e.g. "setting:openai_api_key"; "" if none
	FileKey   string       // key in the gap-fill file; "" if none
}

type LegacyRows interface {
	Get(ctx context.Context, key string) ([]byte, error) // nil, nil when absent
	Changed() <-chan struct{}                            // ABO's legacy config path wrote the blob
}

// LegacyBlob is the shape knowledge of a legacy whole-struct blob, supplied by the app module that owns
// that struct (ABO's books module) and used only by configstore's adapter (§1.12.2).
type LegacyBlob interface {
	// Decode returns every legacy key in the blob with its raw value (secret fields included), after the
	// app's own read-time transforms (ABO: the ten migrate*Blob functions, persistence.go:359-824).
	Decode(raw []byte) (map[string]json.RawMessage, error)
	// Encode re-renders prior with exactly the keys in set replaced (blank = the secret fields to empty);
	// every other key is carried forward unchanged, so the blob never absorbs defaults or
	// operator-layer values it did not hold.
	Encode(prior []byte, set map[string]json.RawMessage, blank []string) ([]byte, error)
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

The routes are mounted from 09 S4, when `rbac` exists to enforce section permissions (FE3-11); before S4,
settings are read and written only through ABO's `GET`/`PUT /api/v1/config`, which writes through
`Store.Apply` from the S3 façade on (§1.12.1).

Default section permissions: `core.*` → read and write `core:settings.manage`; `modules.<id>.*` → read and
write `<id>:settings.manage` (registered by the module in its permission list; [05-rbac](05-rbac.md)). A module
that declares a section must therefore declare `<id>:settings.manage` or pass `config.Permissions(read, write)`
naming permissions it does declare; otherwise the kernel fails boot on the undeclared permission (06 §6.6,
COR2-14).
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

Within operator layers flags beat env beat file. Within one layer, for one key, the canonical name beats an
alias. If two names for one key are set in one layer with different values:

- key at `ProtectAdmin` or higher (database path, sign-in, server paths): **boot fails** with an error that
  names both variables (or file keys). A drop-in that overrides the legacy name while the base unit sets the
  canonical one would otherwise open or create an empty store at the base unit's path (OPS2-12;
  `internal/config/config.go:2519-2544` records the same hazard);
- any other key: the canonical wins, with a warning and health token `config_alias_conflict` (§1.5).

**Boot record.** `ReloadBoot` keys are needed before storage opens (module enablement decides which module
sections are registered and which keyspaces are claimed, 06 §6.5.1). Their UI-made values are persisted in
`<state_dir>/config.boot.json` (0600, directory 0700, written atomically through the root that
`Options.BootRecordRoot` returns: temp file, fsync, rename), a JSON object
`{"format":"mediacore-boot/1","gen":N,"values":{path: value}}`.
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

**Aliases.** Every legacy name is an `Alias` naming its ledger row. Config itself holds no window and no
alias class: the kernel registers `Store.Aliases()` with `compat` (L4, which may import config; config may not
import compat), and the row's gate (window or permanent), its dates and its `Retired` state live in the ledger
(09 §9.2.9, SIMP2-12). Config's only lifetime input is `Options.Retired`. Every use calls
`Options.OnLegacyUse` with its surface (OPS2-7):

- **`SurfaceEnv`**: an env var or operator-file key that is set. Config reports it once per boot (at `Boot`
  or `Load`, before storage exists); the kernel buffers the report and flushes it after storage opens to
  compat's per-boot presence record, so a name set in a unit that is never restarted still counts as in use
  for as long as the process runs.
- **`SurfaceConfig`**: a legacy API path written through the ABO compat handler. Reported per use. Reading
  or writing the blob is **not** a legacy use: until cutover the blob is the storage format (§1.12.2).

**Blob names outlive the window.** An alias's `Legacy` name addresses two things: an operator-file key (a
user surface, governed by the ledger like env names) and a blob field (the storage format). Retiring the
row stops the operator-file and env spellings; the adapter keeps reading and writing the blob field until
that family's cutover, whatever the row's state.

**Where the rows come from (SIMP3-3).** The ledger the kernel hands to `compat` is the app's
(`app.Options.CompatLedger`; ABO embeds `docs/compat-surfaces.md` in its root `main`) merged with core's own
fragment for names every app shares (the permanent `OTEL_*` ecosystem aliases of [05-metrics](05-metrics.md)
§10), embedded by `compat`; a row ID in both is a boot error (09 §9.2.9). A config alias whose row is in
neither fails registration (`ErrNoLedgerRow`).

compat emits the one-time deprecation warning for window rows and feeds
`mediacore_compat_legacy_use_total`. When a window alias may be removed is decided by 09 §9.2.1 (W plus the
usage gate) and done by a contract PR checked by `TestNoContractBeforeSunset`; nothing is removed or refused
by a clock, and no runtime switch turns aliases off.

| Ledger row | Read? | On use | Removed |
|---|---|---|---|
| window gate | yes | applied; `OnLegacyUse` (compat warns once) | contract PR after W (09) |
| permanent gate | yes | applied; `OnLegacyUse` (no warning) | never (or at the next major, 09 §9.6 row 18) |
| `Retired` state | no | key has `ProtectAdmin` or higher (incl. `ProtectOperator`), **or is a secret key**: **boot fails** with `*RetiredAliasError` naming the canonical variable; other keys: one warning per boot | retired rows of protected and secret keys are kept until the next major; others are deleted one minor after contract (09) |

The retired rule exists because silently ignoring a legacy database-path or sign-in name after the window
points the app at a new, empty database or turns basic auth off (OPS-5; ABO recorded the same hazard,
`config.go:2519-2544`). Secret keys are included because an ignored legacy secret name makes its consumer
refuse to start without saying why (`ABS_JWT_SECRET`, a `BindEnv` window name at
`internal/config/abs_config.go:45-47`; absauth refuses to boot without it, `internal/server/absauth/config.go:20-22`).

#### 1.3.7 Protection classes (normative)

The class rule replaces ABO's `configFieldRules` (`protected_fields.go:53`). It is applied by every spec that
declares keys; each key table in the 05 files carries a **Protection** column.

| ABO class today | Protection | Keys (core examples; owning spec tables each key) |
|---|---|---|
| `FieldSignIn` | `ProtectAdmin` | everything else that decides who signs in and how: `core.auth.*` sign-in keys (basic auth, OAuth/OIDC providers, client ids and secrets, redirect base, default role) ([05-authn](05-authn.md)); the books ABS token sign-in switch |
| — (new, SEC-6) | `ProtectAdmin` | `core.http.{trusted_proxies, client_ip_header, peer_allowlist, cors.allowed_origins, basic_gate.*}` ([05-http-api](05-http-api.md)); `core.updater.{mode, channel, restart_*}` ([05-updater](05-updater.md)); OTLP endpoints and `core.metrics.{path, listen}` ([05-metrics](05-metrics.md)); `core.integrations.egress.lan_allow` (SEC-15; see below) |
| `FieldExecutable` | `ProtectAdmin` | a program the server runs, or the managed-copy-vs-path switch |
| `FieldServerPath` | `ProtectAdmin` | a path the server reads, writes, scans or serves (`core.log.file.path`, library and tool paths) |
| owner trust root (`ownerTrustRootFields`, `internal/config/protected_fields.go:209-227`; `update_service.go:770-800`) | `ProtectOwner` | the successors of all five HEAD owner-only fields, plus the switch that would turn the owner gate off (SEC3-6): books' `modules.books.identity.{owner_email, cf_access_team_domain, cf_access_aud}` (the identity ABO's `internal/cfaccess` proves; core ships no owner verifier, 05-authn §4.8), `core.auth.mode` (HEAD `enable_auth`), the `allowed_emails` and `allowed_domains` of every sign-in provider (HEAD `oauth_allowed_emails`) and `core.auth.owner.required` ([05-authn](05-authn.md) §8). HEAD's threat holds unchanged: a second admin, or a stolen admin session, must not repoint Access or the admitted set and mint the owner's identity |
| `FieldDatabase` | `ProtectOperator` | `core.storage.path`, `core.paths.*` |

`core.integrations.egress.lan_allow` — `[]string` of CIDRs or host names, default `[]`, restart-free, owned and
enforced by `integrations` ([05-metadata](05-metadata.md) M1/M2): the LAN targets that **every instance without
`Local`** may reach; anything not on it gets public-only egress. It is a network trust list, so it is
`ProtectAdmin` as SEC-15 specifies, not `ProtectInteractive`.

`ProtectAdmin` is checked as `Actor.Can("core:admin") && Actor.Interactive`. `ProtectOwner` adds `Actor.Owner`
once an owner identity is set and an owner verifier is configured, whatever the caller's sign-in method, as
HEAD does "once `owner_email` is set" (`protected_fields.go:209-227`); before that it is checked as
`ProtectAdmin`. The auth-off principal never holds
`core:admin` ([05-rbac](05-rbac.md)), so these keys cannot be changed with auth off. Integration instances
follow one rule, stated identically in 05-metadata M1.3 (SEC2-7): any `core:integrations.manage` holder writes
instance URL and credential fields; setting an instance's `Local` flag (which admits every loopback, private and
CGNAT address), or keeping it across a URL change, needs the `ProtectAdmin` check, and so does `Registry.Test`
with `Local=true`; every instance without `Local` reaches public addresses plus `lan_allow` only. The wide trust
therefore never needs less than the narrow list does.

`TestEveryURLPathOrSignInKeyClassified` (port of `protected_fields_test.go`) fails when a registered key whose
name or format looks like a URL (`format=uri`, `*_url`, `*endpoint*`), a path (`format=path`, `*_dir`, `*_path`,
`*_file`), an executable (`*_bin`, `*_command`, `*_exe`), a network trust list (`format=cidr`, `*proxies*`,
`*origins*`, `*allowlist*`, `*client_ip*`), key material or encryption policy (`*recipient*`, `*encryption*`,
`*keyring*`, or a non-secret field named `*_key` / `*_key_file`; SEC2-8: `core.backup.recipients` and
`core.backup.encryption` have none of the other shapes) or a sign-in setting (`core.auth.*`) has a Protection
below `ProtectAdmin` and no `ProtectionWaivers` entry with a reason.

### 1.4 Lifecycle

Inside `app.New` (03 §3.7). The phase names and the order of every boot step, config's included, are owned
by the one boot-phase table, [06](06-app-sdk.md) §6.5.1 (R2-8); this list gives config's and secrets' part of
it and the constraints it must keep. Normative constraint (C13): **nothing in steps 0–4 writes the store, and
steps 5–6 run strictly after the boot's pre-migration checkpoint and after the storage DAG**, as the **first**
every-boot reconcilers of 06 P4d, before the revocation-journal re-apply (which needs the keyring and the
effective `core.backup.dir`, COR3-2, DATA3-5), before handles are issued to services and before any listener
opens. Config's boot is read-only apart from `c/secrets/meta` on a new store.

0. Before 06 P−1 the kernel runs `config.ResolveStateDir` once (§1.7, C15) and passes the result to P−1, P0a,
   `Options.BootRecordRoot` and every later consumer.
1. `config.NewStore(opts)`; the kernel registers **core** sections, with the app's `Legacy.Aliases`.
2. `Store.Boot` (06 P0; it reads no storage): takes the state directory from step 0 (a config file that sets
   `core.paths.state_dir` fails boot, §1.7), calls `Options.BootRecordRoot`, then reads the file (parse error =
   boot error), env, flags and the boot record for core sections and validates operator values. The returned
   `BootView` carries module enablement (`core.modules.<id>.enabled`, `ReloadBoot`; COR-14) and the
   operator-only paths `core.paths.state_dir` and `core.storage.path`, which 06's storage-open step (P2) reads
   from it (COR2-8). If step 0 fell back to the database's directory, `Boot` checks that the effective
   `core.storage.path` (all operator layers) is the path step 0 used; if the file moved it, boot fails naming
   both remedies (set the state directory, or give the database path by env or flag), so the keyring never
   lands beside a database that is not the one opened. A retired protected or secret alias set in env or file
   fails boot here (§1.3.6), as does a conflict on a protected key (§1.3.5).
3. The kernel registers the sections of every **enabled** module (from manifests), then `Store.Load` (06 P3)
   seals the registry and builds every section from defaults, operator layers and the boot record.
   Registration after `Load` → `ErrSealed`, boot fails. A section whose operator values fail decode or
   `Validate` fails boot (the operator must fix their input). From S2b the DB layer is attached at step 6.
   **At S2**, before core storage exists, the kernel attaches it here instead, read-only, over
   `Legacy.Rows` (the store ABO's `serve` opened before `app.New`): a read before any write, so C13 holds
   (COR3-1).
4. The kernel builds logging and metrics from `core.log` / `core.metrics` (operator layers and defaults).
   Storage opens at `core.storage.path` (`ProtectOperator`: needed before the DB layer exists), takes its
   pre-migration checkpoint and runs the DAG (05-storage §5S.4).
5. `secrets.Open` (from S3; its only store write, `c/secrets/meta` on a new store, happens here), with the
   kernel's `LegacyStore` over the storage grant (§2.3), then `Store.SetSecrets`.
6. `Store.AttachOverrides` with `configstore` (§1.12.2): the blob is read in place through the grant and
   decoded, `c/config/` rows are read, nothing is written. Rows are decoded per section; if a section fails
   decode or `Validate` with its overrides but passes without them, **all of that section's overrides are
   quarantined** (kept, ignored, reported, health token `config_overrides_quarantined`) and boot continues.
   Rationale: an operator cannot repair the DB without booting; ABO's equivalent path is fatal
   (`cmd/root.go:385-391`). Services built after this step (06 P5) see the DB layer; nothing built before it
   reads a DB-layer value.
7. The kernel hands `Store.PassthroughValues()` to the integrations registry, which overlays them in memory
   (05-metadata M1.4; C11). In 06 P5, once the effective config is known, the kernel calls
   `secrets.(*Store).RetireUnused` with the backup service's in-use check (§2.3; COR3-2).
8. Until the S3 façade, `OverrideStore.Changed` fires when ABO's legacy config path wrote the blob; config
   re-runs `Load` on the DB layer, republishes changed live keys of core sections and marks restart keys
   pending, so a UI edit reaches core sections without a restart, as it does at HEAD (COR3-1).
9. Runtime (from S3): `Apply` = authorize (permission, then protection) → decode candidate per touched section →
   `Validate` → write the boot record if boot keys changed → `OverrideStore.Commit` (blob re-render, `c/config/`
   rows, staged secrets with their legacy blanking, audit; one batch) → publish snapshots for live keys (atomic
   swap) → bump generation → `AfterCommit` hooks → wake `OnChange` subscribers. If `Commit` fails after the boot
   record was written, the previous boot record is restored before `Apply` returns (boot keys are
   restart-class, so nothing was published).
10. `Close` cancels subscriber contexts and waits up to the drain timeout.

Restart-required and boot keys: `Apply` commits them and returns them in `RestartPending`; `Get` keeps the boot
value; `Pending` shows the stored value; `KeyState.Pending` drives a "restart required" badge in the shell.

### 1.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Config file exists but unreadable or unparsable | fail-closed (boot error) | operator intent unknown; ABO ignores viper read errors (`cmd/root.go:627`) — behaviour change, release-noted |
| Unknown key in file / unknown prefixed env var | fail-open, warn once | stale keys must not stop startup (`config.go:2587-2590`) |
| Window or permanent alias used | fail-open, applied, counter (+ deprecation warning once for window aliases) | compat window (09 §9.2.1) |
| Retired alias set, key `ProtectAdmin` or higher, or a secret key | fail-closed (boot), message names the canonical variable | silent fallback opens a wrong database, disables sign-in or starves a consumer of its credential (OPS-5, OPS2-7) |
| Retired alias set, other key | fail-open, warn once per boot | harmless leftovers |
| Canonical and alias both set in one layer, different, key `ProtectAdmin` or higher | fail-closed (boot), message names both names | a drop-in overriding only the legacy name would open an empty store (OPS2-12) |
| Canonical and alias both set in one layer, different, other key | fail-open, canonical wins, warn, health `config_alias_conflict` | an obvious mistake, but not worth an outage |
| Operator value fails decode or `Validate` | fail-closed | operator must fix input |
| `<NAME>_FILE` unreadable | fail-closed | a missing credential file is a deployment error |
| Boot record unreadable (I/O) | fail-closed | never boot with a module silently re-enabled or disabled |
| Boot record unparsable | fail-open: file renamed `config.boot.json.bad`, ignored, health `config_boot_record_quarantined` | the operator can repair a file, but the app must boot to show why |
| Override rows or the blob unreadable (I/O) | fail-closed | running on defaults would let the next save overwrite stored config (`cmd/root.go:360-364`, comment at `:361`) |
| Blob unparsable (`LegacyBlob.Decode` error) | fail-open: every blob-named key quarantined (health `config_overrides_quarantined`), `Apply` refuses blob-named keys with a storage error until the row is repaired; the bytes are never overwritten | a render from bytes the adapter could not read would destroy them; the operator must be able to boot to see why |
| State directory: default not creatable, no explicit value, database path relative or empty | fail-closed (boot), message names `<APP>_CORE_PATHS_STATE_DIR` | HEAD's fallback needs an absolute database path (`state_dir.go:144-147`) |
| State directory: explicit value not usable | fail-closed (boot); never falls back | the operator named it (`state_dir.go:140-142`) |
| Override row fails decode/validate | fail-open per section: quarantine (§1.4 step 7) | DB cannot be fixed without booting |
| Override row for unknown key | fail-open: retained, ignored, reported | downgrade safety (gate G5, 02 §2.4) |
| `Apply` unknown key (incl. a passthrough path) | refuse `ErrUnknownKey`, nothing persisted | typos must not silently no-op; instances are written through 05-metadata |
| `Apply` locked key | refuse `*LockedError` | the value would be discarded at next boot (`config.go:2224-2229` rationale) |
| `Apply` missing permission / protection | refuse `ErrForbidden` / `*ProtectedError` | fail-closed security |
| `Apply` validation failure | refuse `ErrInvalid` with `*FieldError`s | nothing partially applied |
| `OverrideStore.Commit` fails (incl. a blob render conflict after `storage.RetryOnConflict`'s attempts) | fail-closed: nothing published, generation unchanged, boot record restored | durable-before-visible; the blob is never half-written |
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
- The blob has several writers inside one process (config's `Apply`, integrations through the blob
  participant, §1.12.2). Each render reads the stored bytes with `GetForUpdate` (the exact key is
  `Serialized`, 05-storage), so a concurrent render fails the later commit with `storage.ErrConflict` and
  the writer re-reads and retries (`storage.RetryOnConflict`, 3 attempts). configstore keeps one pending
  render per batch, so a key change and a secret blanking in one batch are one blob write.
- `OnChange`: one goroutine per subscription with a one-slot coalescing mailbox; never blocks `Apply`.
  Subscription count is bounded by code (registered at init). `stop()` is idempotent and waits for an
  in-flight call.
- `AfterCommit`: runs on the `Apply` goroutine with `Apply`'s `ctx`; total hook time is bounded by the
  request deadline. Hooks must enqueue work (`tasks`) rather than do it.
- No goroutine is started before `Load`; `Close` cancels and joins all (verified with `testing/synctest`).
- Config runs no timers. Anything periodic is a task with `tasks.Schedule` bound to a declared key through
  `tasks.ScheduleFromConfig(key)` ([05-tasks](05-tasks.md)).

**Schedule keys (R2-7, normative).** Every schedule key has the form `<owner>.<subsystem>.schedules.<name>`:
`core.<subsystem>.schedules.<name>` for a core kind (e.g. `core.ai.schedules.batch_poll`,
`core.log.schedules.journal_prune`, [05-logging](05-logging.md)) and `modules.<id>.<subsystem>.schedules.<name>`
for a module kind. Each segment matches `[a-z][a-z0-9_]{0,47}` (§1.3.1; no `-`). The owner declares it as a field
of type `tasks.ScheduleConfig` with a default, in a `….schedules` section of its own (a nested section path,
§1.3.1, when the subsystem's main section type lives in a package that cannot import `tasks`). Config treats it as an
ordinary live object key: it is overridden, locked, validated and listed like any other key, and config needs
no knowledge of the type (config cannot import `tasks`; the section type lives in the owner's package). `tasks`
refuses at registration a key that is undeclared, not of type `tasks.ScheduleConfig`, not of that form, or
outside the registering owner's namespace (05-tasks §5T.3.8). A legacy
interval or enable key becomes an `Alias` whose `Convert` maps it onto `ScheduleConfig` (`{"enabled":true,
"every":<duration>}`; a legacy `0` that meant "off" maps to `{"enabled":false}`).

### 1.7 Config keys (core-owned sections)

This file owns only `core.config.*`, `core.paths.*` and `core.secrets.*` (§2.7); other `core.*` sections are
owned and tabled by their specs ([05-logging](05-logging.md), [05-metrics](05-metrics.md),
[05-storage](05-storage.md), [05-http-api](05-http-api.md), [05-authn](05-authn.md), [05-backup](05-backup.md),
[05-updater](05-updater.md), [05-tasks](05-tasks.md), [05-ai](05-ai.md)); integration instances are not config
keys at all (C11).

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.paths.state_dir` | path | the app's compiled default, passed through `app.Options.StateDir` (06; ABO: `internal/config/state_dir.go:54`), with HEAD's fallback below | no — **pre-boot**: read from env and flags; a config file that sets it fails boot at P0, because 06 P−1 and P0a read it before any config file (06 §6.5.1) | operator |
| `core.config.file` | path | `""` (also `--config`, `<APP>_CONFIG`) | no | operator |
| `core.config.subscriber_drain_timeout` | duration | `5s` | no | none |

**Pre-boot resolution (C15, R3-4, OPS3-1)** — `ResolveStateDir`, run once by the kernel (§1.4 step 0):

1. an explicit env or flag value is used as given; if it cannot be created or used, boot fails (never a
   fallback, HEAD `state_dir.go:140-142`);
2. otherwise the compiled default, created `0700` if absent;
3. if the default cannot be created, the directory of `core.storage.path` as resolved from the pre-boot
   layers (compiled default, env, flags), when that path is absolute, with HEAD's warning naming both
   directories and the variable that chooses one deliberately (`state_dir.go:118-155`); P−1 passes the
   `db_path` recorded in its marker. Otherwise boot fails.

On Docker and launchd installs step 3 is where `.encryption_key` lives today (the image runs as an
unprivileged user whose only persistent path is the data volume, `Dockerfile:93-96,104`; neither compose nor
the plist sets a state directory), so `k0-legacy` is found and the keyring lands on the volume. 09 S2 also sets
the canonical variable to the database's directory in the shipped Dockerfile, compose file and plist, which is
the same directory made explicit.

There is no `core.paths.data_dir`: no component read it, and the database location is `core.storage.path`
alone (05-storage), so a second data-path key was one of three sources for one fact (COR2-8). The state
directory holds the boot record, the keyring and the log file. There is no `core.config.legacy_aliases` switch:
aliases are governed by 09's ledger (§1.3.6). No key in this
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
| `mediacore_config_legacy_plaintext_secrets` | gauge | — (secret fields still stored as plaintext in the blob; each moves to `secrets` on its first write, §1.12.2) |

Legacy-name use is counted once, by `compat` (`mediacore_compat_legacy_use_total{surface="config"}`), not by a
second config counter.

### 1.9 Breaking config's upward imports

| Import today | What it is used for | Where it goes |
|---|---|---|
| `internal/database` (`SettingsStore`, `Setting`, `DecryptValue`, `MaskSecret`, `IsEchoedMask`, `LooksLikeMask`) | blob/row persistence, secret decrypt, masks (`persistence.go:883-1260`, `update_service.go:91-373`) | persistence → `OverrideStore` interface implemented by the kernel's legacy-format adapter over `storage` (§1.12.2); decrypt → `secrets`; masks → deleted (write-only, C9); the ABO compat handler keeps mask echo logic while its ledger row is live (§2.10) |
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
`github.com/google/jsonschema-go` v0.4.3** for both the settings schema and OpenAPI; the
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

**Where the reflector lives (SIMP2-3).** `core/internal/jsonschemax` is an L0 leaf that imports only
`jsonschema-go`. It owns the wrapper, the tag pass and an **extensible** override table:

```go
package jsonschemax

type Overrides map[reflect.Type]*jsonschema.Schema

func (o Overrides) With(more Overrides) Overrides            // union; a duplicate type panics at init
func For[T any](o Overrides) (*jsonschema.Schema, error)   // jsonschema.For[T] + the tag pass
func Base() Overrides                                        // time.Time → string/date-time; the ONLY owner of time.Time
```

Each package contributes the entries for **its own** types at its own layer, and no type appears in two sets:
`jsonschemax.Base()` owns `time.Time`; `config.Overrides()` adds `config.Duration` → string/duration and
`secrets.Ref` → write-only string (L2/L3 types; config imports both); `httpapi` adds only `rbac.Permission` →
string pattern, which it already imports. There is one reflector and one composition,
`jsonschemax.Base().With(config.Overrides()).With(httpapi's own)`, so a DTO that carries a `config.Duration` or a
`secrets.Ref` renders identically in OpenAPI and in the settings schema. **Import direction (SIMP3-1):**
`httpapi` (L6) imports package `config` (L3) for the static `config.Overrides()` value only, never a
`*config.Store`: a router needs no config store (`httpapi.Deps` has none for this, 05-http-api). Both are
functions returning fresh maps, not exported map variables (R5).

How config uses it: `jsonschemax.For[T]` with `Base().With(config.Overrides())`; the tag pass maps the `reload`,
`protect`, `schema` and `sensitive` tags onto keywords and `Extra` (`x-mediacore-*`). `jsonschema-go` reads only
its own `jsonschema:"…"` description tag, so the tag pass is required either way (~100 LOC instead of ~400 for a
whole reflector). Operator and patch values are checked with `Resolved.Validate` before `encoding/json` decoding
and `Validator`.

**Holding the version (MOD2-20).** A Go `require` is a minimum: any module in either build list that requires
a higher version wins silently, and Dependabot would open bump PRs. The version is therefore held by two
mechanisms owned by 13: a Dependabot `ignore` entry (all update types) for `github.com/google/jsonschema-go`, and
`check_module_skew.py --pin github.com/google/jsonschema-go=v0.4.3`, which asserts the resolved version in both
modules' `go list -m all` output. An upgrade is a deliberate PR gated by `TestSchema_Golden`. 13 §13.5.2 records
the dependency as adopted.

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
| `TestOwnerTrustRootKeysAreOwnerProtected` | port of HEAD's owner-field test: the §1.3.7 owner row's keys are `ProtectOwner`; with an owner set, an interactive `core:admin` actor without owner proof gets `*ProtectedError{Need: ProtectOwner}`; with no owner set the same actor succeeds (SEC3-6) |
| `TestBoot_ModuleEnablementFromBootRecord` | a UI-written disable is honoured by `Boot` at the next start; operator env beats it |
| `TestBootRecord_AtomicAndQuarantine` | crash between temp write and rename keeps the old file; garbage → `.bad`, boot continues |
| `TestAttach_QuarantineInvalidSection` | boot continues, section on operator view, health token set |
| `TestOnChange_CoalescesUnderSynctest` | `testing/synctest`: 100 applies, slow subscriber sees ≤2 calls, last = latest |
| `TestAfterCommit_ErrorReportedNotRolledBack` | `Result.PostCommit` non-nil, value committed |
| `TestEnvName_Derivation` / `TestEnvName_CollisionRejected` | naming rule; `ErrDuplicate` |
| `TestEnvFileSuffix` | `_FILE` read once, newline trimmed, unreadable = boot error |
| `TestAlias_UseCallsOnLegacyUse` / `TestAlias_RetiredProtectedFailsBoot` | env and file aliases reported once per boot on `SurfaceEnv`, blob and API uses per use on `SurfaceConfig`; an alias whose ledger row is `Retired` on a protected or secret key → `*RetiredAliasError` |
| `TestAliasConflictOnProtectedKeyRefusesBoot` | `ABO_CORE_STORAGE_PATH` and `DATABASE_PATH` set to different values → boot error naming both; same conflict on a `ProtectNone` key → canonical wins, `config_alias_conflict` |
| `TestBoot_ResolvesPathsWithoutStorage` | `BootView` yields `core.paths.state_dir` from env and flags (a file value fails boot) and `core.storage.path` from file, env and flags; `BootRecordRoot` receives the resolved state dir; no storage handle exists |
| `TestStateDirFallbackMatchesHEAD` | table over (explicit set or not, default creatable or not, DB path absolute / relative / empty): explicit never falls back; uncreatable default → `Dir(core.storage.path)` with the warning; relative or empty DB path → boot error; a fallback whose DB path the config file later moves → boot error naming both remedies; P−1, P0a and `Boot` receive the same directory (OPS3-1) |
| `TestNoStoreWriteBeforeCheckpoint` | a recording fake store sees no write from `secrets.Open`, `configstore` or `AttachOverrides` before the checkpoint marker, and none at all from `AttachOverrides` (C13) |
| `TestLegacyAdapter_ReadsBlobInPlace` | a HEAD-shaped blob fixture (incl. a `migrate*Blob` case) yields the same effective values as HEAD's loader for every aliased key; a blob value equal to the shipped default reports `SourceDefault`; `ProtectOperator` keys in the blob are ignored; the `config.yaml` gap fill applies only to `Legacy.Fill` keys whose blob value is empty |
| `TestLegacyAdapter_RenderChangesOnlyTouchedKeys` | `Apply` of one key changes exactly that blob field (others byte-equal after a JSON round trip); unset writes the shipped default; `Revert` is applied to converted keys; a key without a legacy name lands in `c/config/` and never in the blob |
| `TestLegacyAdapter_BlobRenderConflictRetries` | two concurrent renders (config `Apply`, integrations participant): both changes survive; no lost update |
| `TestLegacyLayer_S2BlobOnlyValuesHonoured` | S2 shape: no core storage, `Legacy.Rows` over a fixture store; a core key set only in the blob (e.g. the Basic-gate switch) is effective after `Load`; a change written by the legacy path and signalled on `Changed` republishes the live key without a restart (COR3-1) |
| `TestLegacyAdapter_UnparsableBlobQuarantined` | garbage blob → blob-named keys quarantined, health token set, `Apply` on a blob-named key refused, the stored bytes unchanged |
| `TestSectionPath_NestedNoOverlap` | `core.log` + `core.log.journal` + `core.log.schedules` register; a parent field named `journal` → `ErrDuplicate` |
| `TestConfigFileHasSecrets_Redacted` | a file with a secret key or a passthrough value → `true`; `RedactedConfigFile` contains neither |
| `TestPassthrough_NotOwnedNotWritable` | passthrough values returned raw with origin; `Apply` on them → `ErrUnknownKey` |
| `TestSchema_Golden` | schema of a fixture section equals `testdata/schema.golden.json` (guards the jsonschema-go pin) |
| `TestSchema_SameOverridesAsOpenAPI` | `config.Duration`, `secrets.Ref` render identically in the settings schema and an `httpapi` fixture built from `jsonschemax.Base().With(config.Overrides())` without a config store |
| `TestSchema_OverrideSetsDisjoint` | `Base().With(config.Overrides()).With(<httpapi's set>)` does not panic; `time.Time` is in `Base()` only (SIMP3-1) |
| `TestSchema_SecretIsWriteOnly` / `TestKeys_NeverContainSecretValues` | secret never in `Effective` or schema defaults |
| `TestSnapshot_ImmutableUnderRace` | `-race`; `coretest` fingerprint check |
| `TestRegistry_SealedAfterLoad` | `ErrSealed` |
| `TestConfigImportsOnlyAllowed` | `go list -deps` allow-list for `core/config`: stdlib, yaml, jsonschema-go, `internal/jsonschemax`, `secrets`, `metrics` (and their own allowed imports); `jsonschemax` imports only jsonschema-go |
| `FuzzEnvDecode`, `FuzzPatchDecode` | no panic, typed errors only |

### 1.12 Migration from ABO

#### 1.12.1 Code: façade required, call-site migration optional

R5 binds core only (SIMP-3). ABO's globals survive as books-module façades over core handles; removing them is
an optional ratchet, not an extraction stage.

1. **Stage 0 (required; in ABO, no core needed):** convert the 8 bypassing writes (§1.2) to `Mutate`, and add
   `TestNoDirectAppConfigWrites` (AST: no assignment to `config.AppConfig` outside `internal/config`). Without
   this, the façade's projection would silently drop those writes.
2. **Stage 1 (required; 09 S3) — façade.** Its done-check is that the effective config of the production env,
   YAML file and blob equals HEAD's (not the previous stage's, COR3-1); the only stored-data change it brings
   is §1.12.2's secret rule, which applies to secrets written on it. ABO's `internal/config` keeps its exported surface and becomes a
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

#### 1.12.2 Data: the legacy-format adapter (single mechanism; cutover deferred)

This is the only specification of how core config and secrets use ABO's stored settings during the extraction
(R3-1; 02 decision 13). 09 S2–S3 schedule it, the R-test (09 §9.10) exercises its rollback rows, and 14 Q-E is
answered by it. There is no import, no re-import, no mirror, no Teach build, no adopt step, no level step and no
contract for config or secrets: **one stored format per family**, read and written in place.

**Where each value lives.**

| Value | Stored | Read by |
|---|---|---|
| a key that has a legacy blob name (`Alias.Legacy`) | `setting:config_blob`, in the legacy JSON shape, written in place | every build |
| a key with no legacy name (new core and module keys) | `c/config/<path>`, one row per explicitly set key (core-only family) | builds that declare the key; an older build has no such key and never reads the family |
| a secret a core build has written (set or cleared) | `c/secrets/v/<name>` (§2.3); its legacy copy is blanked in the same batch | core builds; an older build sees it unset |
| a secret no core build has written yet | where the previous build put it: a plaintext blob field, one of the four `.encryption_key` rows (§2.2), or the gap-fill file `<db dir>/config.yaml` | every build (core builds through the adapter) |
| `ReloadBoot` keys (module enablement) | boot record `<state_dir>/config.boot.json` (§1.3.5) | core builds |

Routing is fixed per key: a key with a legacy blob name is never written to `c/config/`, and a key never moves
between the two before its family's cutover.

**Access: one logged storage grant.** Books keeps owning `setting:` ([05-storage](05-storage.md) §5S.9.1).
The kernel's `configstore` reaches the legacy rows through one explicit storage grant (05-storage) over exact
keys only, named by `Legacy` (§1.3.3), so core holds none of them: read-write on the blob row (`Serialized`,
so a render can `GetForUpdate`), and read and delete on the four legacy secret rows. It reads nothing else
under `setting:`, `preference:` or `pref:` (DATA-1). Every write through the grant is logged and counted by
05-storage. The gap-fill file is read, never written. The blob therefore has exactly one core write path
(configstore) and is deleted by no step of the extraction (DATA3-3).

**Reading (`AttachOverrides`, every boot; read-only).** The DB layer is the union of:

- the blob, decoded with `Legacy.Blob.Decode` (which applies ABO's read-time transforms, as HEAD's loader
  does) and mapped onto canonical paths through the aliases (`Convert`). A blob value equal to **this
  binary's** shipped default is reported as `SourceDefault`, not as an override: the effective value is the
  same today, and a later default change reaches keys that hold the old default (release-noted: a value a user
  set equal to the default follows it too; HEAD froze every default at save time,
  `persistence.go:1180-1188`). `ProtectOperator` keys in the blob are ignored;
- for the `Legacy.Fill` keys whose blob value is empty, the gap-fill file, as HEAD does
  (`persistence.go:42-104`); it holds plaintext secrets, so it is never archived and the boot log asks the
  operator to clear them from it (out-of-scope defect 2);
- the `c/config/` rows.

The operator lock (C3) then applies as for any DB-layer value. Before S3 there is no `secrets` store, so
secret fields are not part of the layer (a core stage that needs a legacy secret before S3 stays in ABO, 09
S2). The source of the bytes is `Legacy.Rows` at S2 (attached at P3; no core storage yet) and the grant from
S2b (attached at P4d, §1.4).

**Edits made outside core config (S2 until the S3 façade).** ABO's legacy config path (`PUT /api/v1/config`,
viper's loader) still writes the blob itself. `OverrideStore.Changed` fires after such a write and config
republishes changed live keys of core sections (§1.4 step 8), so a UI edit of a perimeter key (the Basic-gate
switch, the rate limiter, CORS origins, the log level) takes effect without a restart, as at HEAD (COR3-1).
From the façade on, every write goes through `Store.Apply`.

**Writing (`Apply` → `Commit`, from S3).** One batch holds: (1) the blob re-rendered by
`Legacy.Blob.Encode(prior, set, blank)` from the bytes read with `GetForUpdate`, replacing only the blob
fields this commit changes (an unset writes this binary's default; converted keys pass through `Revert`), so
the blob never absorbs default or operator-layer values it did not hold; (2) `c/config/` sets and deletes for
keys without a legacy name; (3) the staged secret writes, each of which blanks its legacy copy (below);
(4) the audit record. Concurrent renders retry on conflict (§1.6).

**Secrets: moved on first write.** A secret moves to `c/secrets/` when a core build **first writes it** (set
or clear), never at boot and never because an unrelated key was saved. `secrets.Stage` writes the record and,
in the same batch, blanks every legacy copy through the kernel's `LegacyStore` (§2.3): the blob field is
written empty and the legacy row deleted; a clear stores a cleared marker so the gap-fill file cannot bring
the value back. The read order is in §2.3: a **non-empty** legacy copy in the blob or a legacy row beats the
stored record, because a core build always blanks both when it writes, so such a value can only have been
written by an older build after a rollback, and it is the newer one; it moves at that secret's next core write.
An undecryptable legacy row (no `k0-legacy`) reads as "must be re-entered" (`Meta.Err = ErrKeyUnavailable`),
the boot continues and the row is kept (DATA3-12). Plaintext fields still in the blob are counted by
`mediacore_config_legacy_plaintext_secrets` and shown with `Source: legacy` in the settings UI; saving one
encrypts it.

**Integration-bound keys (C11).** ABO's legacy singletons (Deluge web, `download_client`, AI endpoints and the
`openai_*` pair, `metadata_sources`, the Google Books and Hardcover keys, SMTP, webhooks; §1.12.3 rows marked
*integrations*) stay in the blob. Config does not own or interpret them. integrations reads and writes their
blob fields in place through **configstore's blob participant**, which joins the caller's `*storage.Batch`
(the same pattern as `secrets.Stage`) and shares the render of §1.6 ([05-metadata](05-metadata.md) M1). Their
secrets follow the secret rule above. The blob is owned by books as a row, written by core only through
configstore, and outlives every extraction stage (DATA3-3).

**Rollback matrix** (inputs to 09's R-test):

| Move | Config the target reads | Secrets the target reads |
|---|---|---|
| S3+ → S0–S2b or HEAD | the blob, current (written in place); keys with no legacy name do not exist there | secrets written on S3+ are **unset**: re-enter them there (release note). Untouched secrets read as before. The three keys ABO also copies to `<db dir>/config.yaml` (`openai_api_key`, `google_books_api_key`, `hardcover_api_token`) come back from that file with the value an older build last saved |
| S0–S2b or HEAD → S3+ again | the blob read in place: every change made on the older build is effective at the next boot | a secret re-entered on the older build wins over the stored record (non-empty legacy copy) and moves at its next write; a secret the older build saw as unset cannot be cleared there, clear it after rolling forward |
| S2b → S2, S2 → HEAD | the blob (core config never writes it before S3) | unchanged |
| S3+ → S3+ | the blob and `c/config/` | `c/secrets/` and the untouched legacy copies |

R-test cells: "save a non-4 secret on S3, roll back, it is unset on prev; re-enter it on prev, roll forward, the
re-entered value is effective"; "edit a perimeter key through `PUT /api/v1/config` at S2, it takes effect
without a restart"; "edit a Deluge URL on prev, roll forward, the instance uses it".

**Release note (every release from S3 until cutover):** "Secrets saved on this release are stored encrypted
and are blanked in the legacy settings row. After a rollback to a release older than S3, re-enter them there."

**Cutover (deferred; out of the extraction).** Moving the blob's keys into `c/config/`, the untouched legacy
secrets into `c/secrets/`, and deleting the blob and the four rows is a later, per-family, one-way,
checkpointed step behind the single per-owner level guard (05-storage §5S.3.7), decided on measured data; its
floor is the cutover release itself. Until then the blob and the four rows are the storage format, `k0-legacy`
stays in the keyring, and nothing in this file deletes them.

#### 1.12.3 Key map (family level)

The per-key golden `core/config/testdata/abo_keys.golden` is generated by walking ABO's `Config` and is owned by
[09-abo-on-core](09-abo-on-core.md). Of the 173 top-level keys (nested sub-keys move with their parent), 64
match platform families and 109 are audiobook; 9 of the 64 stay app-side per `raw/10` G11: 6 Cloudflare guest
provisioning keys and the 3 Cloudflare Access identity keys (`cf_access_team_domain`, `cf_access_aud`,
`owner_email`, `config.go:1603-1613`, read by ABO's `internal/cfaccess`, 04 §2.4). Of the remaining 55, **9 become
integration instances** (rows marked *integrations*), leaving **46 → `core.*`**. Books families that become integration instances (Deluge web `deluge_*`, `metadata_sources`,
the Google Books and Hardcover keys) are identified per key in the golden and in 05-metadata M1's import table,
so **≤118 → `modules.books.*`**.

| ABO flat key family | Count | New home | Owner spec |
|---|---|---|---|
| `database_path`, `database_type` | 2 | `core.storage.path`, (`database_type` retired: Pebble only, `config.go:1053`, `config.go:3837`) | 05-storage |
| `api_rate_limit_per_minute`, `json_body_limit_mb`, `upload_body_limit_mb`, `enable_rate_limit`, `public_base_url`, `tunnel_connector_addrs`, `allowed_peer_cidrs`, `login_saturated_*` | 9 | `core.http.*` | 05-http-api |
| `log_level`, `log_retention_days` | 2 | `core.log.level`, `core.log.journal.retention_days` | 05-logging |
| `otel_*` (4), `telemetry_environment` | 5 | `core.metrics.*`, `core.tracing.*` | 05-metrics |
| `enable_auth`, `basic_auth_*`, `oauth_*`, `bootstrap_key_ttl*`, `write_startup_readonly_key` | 15 | `core.auth.*` | 05-authn |
| `cf_access_team_domain`, `cf_access_aud`, `owner_email` | 3 | `modules.books.identity.*` | app-side (G11; 09 S4) |
| `cf_account_id`, `cf_guests_policy_id`, `cf_policy_api_token`, `cf_guest_service_tokens`, `cf_service_auth_policy_id`, `invite_email_service_token` | 6 | `modules.books.cfguests.*` | app-side (G11) |
| `smtp_*` | 5 | *integrations*: a `notify/<id>` mail instance | 05-notify, 05-metadata M1 |
| `backup_dir`, `backup_max_total_bytes`, `backup_compression[_level]`, `backup_retention_days` | 5 | `core.backup.*` | 05-backup |
| `auto_update` | 1 | `core.updater.*` | 05-updater |
| `operation_timeout_minutes`, `operation_log_retention_days`, `maintenance`, `scheduled` | 4 | `core.tasks.*`; each `scheduled.<task>` entry becomes the `tasks.ScheduleConfig` key its task kind's **owner** declares (§1.6, R2-7): books kinds at `modules.books.<subsystem>.schedules.<name>`, core kinds at `core.<subsystem>.schedules.<name>`. The family is counted here once; the golden places each entry | 05-tasks |
| `openai_api_key`, `openai_base_url`, `ai_endpoints` | 3 | *integrations*: `ai/<id>` instances | 05-ai, 05-metadata M1 |
| `ai_endpoints_routing`, `ai_backend`, `ai_journal_retention_days` | 3 | `core.ai.*` (global tuning, not instances) | 05-ai |
| `download_client` | 1 | *integrations*: `download/<id>` instance | 05-download, 05-metadata M1 |
| everything else (scan, organize, naming, metadata, dedup, whisper, fingerprint, iTunes, ABS, activity log, catalog, search, embedding, tools, plugins, …) | 109 | `modules.books.<family>.*`, except the integration-bound books families named above | 09 |

#### 1.12.4 Env and flag compatibility

Every env name ABO reads today is registered as an `Alias` of its new path. This section is the **single
list** of env-name classes; 09 §9.6 row 16 points to it (OPS2-21). Counts are generated from the alias table
into `abo_keys.golden`, not maintained by hand.

- **Window ledger rows:** the 142 non-test `BindEnv` names at `748fd787a` (`internal/config/config.go` 132,
  `abs_config.go` 9, `invite_config.go` 1; e.g. `ACOUSTID_API_KEY`, `ABS_AUTH_PROBE`, `config.go:2702-2747`),
  except those listed as permanent below, the bare upper-case form of every flat key that viper's prefix-less
  `AutomaticEnv` made settable (`cmd/root.go:625`), and the `ABK_*` operational names (`ABK_STATE_DIR`
  `state_dir.go:25`, `ABK_ALLOW_ENCRYPTION_KEY_REGEN` `cmd/root.go:733`).
- **Permanent ledger rows:** (a) ecosystem names: `OTEL_EXPORTER_OTLP_ENDPOINT`,
  `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_METRIC_EXPORT_INTERVAL` (a bare integer is milliseconds, the OTel
  convention ABO already honours; the alias `Convert` keeps that) and `OTEL_EXPORTER_OTLP_METRICS_INSECURE`
  (`config.go:2746-2749`; targets in [05-metrics](05-metrics.md) §10), `OPENAI_API_KEY`, `OPENAI_BASE_URL` (the
  `OPENAI_*` pair addresses passthrough paths of the AI instance, C11); (b) every name ABO reads today that a
  shipped deploy artifact sets (OPS-5): `DATABASE_PATH` (`deploy/audiobook-organizer.service:98`,
  `deploy/com.audiobook-organizer.plist:40`), `FP_PARALLEL_WORKERS` (unit `:108`, read at `config.go:2735`),
  `BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME`, `BASIC_AUTH_PASSWORD` (`docker-compose.yml:20-22`, read through
  `AutomaticEnv`), `WHISPER_REMOTE_URL`, `ALLOWED_PEER_CIDRS`, `TUNNEL_CONNECTOR_ADDRS`, `LOGIN_SATURATED_RATE`,
  `LOGIN_SATURATED_BURST` (`deploy/local.conf.example:39-61`, read at `config.go:2838` and `invite_config.go`).
- **Not aliases — shipped but unread by ABO:** names a shipped file sets that ABO does **not** read today stay
  unread, because honouring them would change behaviour: `AUDIOBOOK_ROOT_DIR` (unit `:97`, plist `:38`;
  deliberately unread because the unit's generic value does not exist on the production host,
  `config.go:2569-2575`), `AO_DIR`, `AO_DB` (`docker-compose.yml:18-19`) and `OLLAMA_BASE_URL`
  (`deploy/local.conf.example:40`), none of which has a reader.
- **Not aliases — runtime and ecosystem names config never reads:** `HOME`, `GOMEMLIMIT`, `GOGC`
  (`deploy/audiobook-organizer.service:96,101,104`), `TZ` and `NOTIFY_SOCKET` (read by the Go runtime, the
  systemd notify helper or the OS, never by config).
- `TestShippedDeployEnvNamesArePermanent` scans `deploy/*.service`, `deploy/*.plist`, `deploy/local.conf.example`,
  `docker-compose.yml` and `Dockerfile*`, **including commented-out `Environment=` lines** (an operator uncomments
  them, e.g. `local.conf.example:50-61`): every name found is a permanent row or on one of the two not-alias
  lists above; anything else fails.
- After contract, window rows of keys with `ProtectAdmin` or higher, and of secret keys, move to state `Retired`
  and boot refuses to start if one is set (§1.3.6). [05-storage](05-storage.md) additionally refuses to
  **create** a store at the resolved path when none exists there and a retired database-path name is set.

Flags: ABO's cobra adapter maps `--dir`, `--db`, `--db-type`, `--playlists`, `--log-level` (and the other flags of
09 §9.6 row 18, kept until the next major) to paths.

### 1.13 Open items

1. Whether `encoding/json/v2` is GA in go1.27.3; if so, decode sections with it (strict unknown-field
   handling and `omitzero` are better fits). Decide at 09 S0 with the toolchain spec ([13-toolchain](13-toolchain.md)).
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
`marshalConfigBlob` strips only boot-only keys, `persistence.go:2126-2139`). The *legacy copies* column lists
where the previous build keeps the value; it is the app's `Legacy.Secrets` table (§1.3.3), read in place by the
adapter and blanked in the same batch as the secret's first core write (§1.12.2). "—" means the previous build
reads the field only from env or file, so there is nothing to read or blank. No legacy row is ever written by a
core build.

| Field (JSON key) | Anchor | At rest today | New name (secret) | Legacy copies |
|---|---|---|---|---|
| `openai_api_key` | `persistence.go:1955` | encrypted row | `integrations.ai.<instance>.api_key` | row `setting:openai_api_key`; gap-fill file key |
| `google_books_api_key` | `persistence.go:1956` | encrypted row | `integrations.metadata.<instance>.api_key` | row `setting:google_books_api_key`; gap-fill file key |
| `hardcover_api_token` | `persistence.go:1957` | encrypted row | `integrations.metadata.<instance>.api_token` | row `setting:hardcover_api_token`; gap-fill file key |
| `basic_auth_password` | `persistence.go:1958` | encrypted row | `core.auth.basic.password` | row `setting:basic_auth_password` |
| `acoustid_api_key` | `config.go:1141` | plaintext `config_blob` (masked in API, `update_service.go:94`) | `modules.books.acoustid.api_key` | blob field |
| `oauth_github_client_secret` | `config.go:1588` | plaintext `config_blob` (value re-applied from env each boot, still written) | `core.auth.oauth.github.client_secret` | blob field |
| `oauth_google_client_secret` | `config.go:1590` | plaintext `config_blob` | `core.auth.oauth.google.client_secret` | blob field |
| `deluge_web_password` | `config.go:1777` | plaintext `config_blob` (carried forward when env-locked, `persistence.go:1965-1971`) | `integrations.download.<instance>.password` | blob field |
| `download_client.torrent.deluge.password` | `config.go:240` | plaintext `config_blob` | `integrations.download.<instance>.password` | blob field |
| `metadata_sources[].credentials` (map) | `config.go:154` | plaintext `config_blob` | `integrations.metadata.<instance>.credentials.<k>` (one secret per entry) | blob field (per entry) |
| `plugins[].settings` (free-form map) | `config.go:244-247` | plaintext `config_blob`; may hold credentials (unknowable) | retired with `internal/plugin` (02 N4); any surviving entry declared per adapter | — |
| `abs_jwt_secret` | `config.go:1636` (`json:"-"`) | env / viper file only | `modules.books.abs.jwt_secret` | — |
| `smtp_password` | `config.go:1678` (`json:"-"`) | env / viper file only | `integrations.notify.<instance>.password` | — |
| `cf_policy_api_token` | `config.go:1688` (`json:"-"`) | env / viper file only | `modules.books.cfguests.policy_api_token` | — |
| userinfo in URLs (`openai_base_url`, `whisper_remote_url`, `ai_endpoints[].url`, `embedding.base_url`) | `config.go:1222,1319`, `ai_endpoints.go:40` | plaintext if an operator embeds `user:pass@` | `schema:"format=uri"` validation **rejects** userinfo; credentials go in a sibling `secrets.Ref` | — |

`<instance>` for each legacy singleton is fixed by the integrations import table ([05-metadata](05-metadata.md)
M1 migration, with [05-download](05-download.md), [05-ai](05-ai.md), [05-notify](05-notify.md)) before S3 ships,
so each secret has one final name from its first core write on.

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
func (sc Scope) DeriveKeyFor(kid, info string) ([]byte, error) // same, from a named derive key (verification, §2.3)

type Source uint8

const (
	SourceStored Source = iota + 1 // encrypted in storage
	SourceEnv                      // operator env (incl. <NAME>_FILE); in memory only, never persisted
	SourceFile                     // operator config file; in memory only
	SourceLegacy                   // still where the previous build put it (blob plaintext, legacy row, gap-fill file)
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
	Legacy        *LegacyKey        // ABO .encryption_key import as k0-legacy (§2.4); nil otherwise
	LegacyStore   LegacyStore       // the previous build's copies, read in place and blanked on write (§1.12.2); nil = none
	Logger        *slog.Logger
	Metrics       *metrics.Scope    // core scope "mediacore.secrets"
	Now           func() time.Time
}

type LegacyKey struct {
	Dirs []*os.Root // where a raw 32-byte ".encryption_key" may live (state dir, legacy dir); read-only
}

// LegacyStore is the previous build's storage of secrets, implemented by the kernel's configstore over the
// storage grant and the app's Legacy.Secrets table (05-config §1.12.2). secrets cannot import config, so the
// interface lives here.
type LegacyStore interface {
	// Read returns the legacy value of n: Primary from the blob field or the legacy row (decrypted with
	// OpenLegacy), else FileFill from the gap-fill file; LegacyNone when neither holds a non-empty value.
	Read(ctx context.Context, n Name) (Value, LegacyWhere, error)
	// Blank joins b: empties n's blob field and deletes its legacy row. Called by every write of n.
	Blank(b *storage.Batch, n Name) error
	// Has reports whether n has any legacy copy at all (decides whether Delete leaves a cleared marker).
	Has(n Name) bool
}

type LegacyWhere uint8

const (
	LegacyNone LegacyWhere = iota
	LegacyPrimary
	LegacyFileFill
)

func Open(ctx context.Context, o Options) (*Store, error)

func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
func (s *Store) Stage(b *storage.Batch, w Write) error           // same-batch write used by configstore and integrations (COR2-12: a pointer, so Bind and the writes land on the caller's batch); also calls LegacyStore.Blank in b
func (s *Store) OpenLegacy(ciphertext []byte) (Value, error)     // decrypt a legacy row with k0-legacy (used by LegacyStore.Read)
func (s *Store) Ephemeral(n Name, v Value, src Source)            // operator-supplied value; locks the key
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]   // never values
// KeyIDsReferenced scans the records readable through r (the live store, or a staged restore) and reports
// every key ID they need, including "k0-legacy" for every non-empty legacy-format row (DATA3-12). Restore
// reports and key pruning use it.
func (s *Store) KeyIDsReferenced(ctx context.Context, r storage.Reader) ([]string, error)
func (s *Store) DeriveKey(info string) ([]byte, error)           // from the active derive key; stable across data-key rotation
func (s *Store) ActiveDeriveID() string                          // e.g. "d1"; producers record it next to what they MAC or encrypt
func (s *Store) DeriveKeyFor(kid, info string) ([]byte, error)   // from an active or verify-status derive key; ErrKeyUnavailable for retired/unknown
// RetireUnused treats as retired, for this process, every verify-status derive key past retire_after that
// inUse reports unused. The kernel calls it in 06 P5, after the DB layer is attached, with the backup
// service's check (05-backup): retained archives, undo points, unconsumed checkpoints, and every kid named by
// a retained revocation-journal line or the journal header, all resolved from the effective core.backup.dir
// (COR3-2, DATA3-13, SEC3-10). An inUse error keeps the key. Until it runs, verify keys stay usable. In memory
// only: the keyring file keeps status "verify" and the decision is remade every boot.
func (s *Store) RetireUnused(ctx context.Context, inUse func(ctx context.Context, kid string) (bool, error)) (retired []string, err error)
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
  Exactly one `active` key per `use`. `use: "data"` keys encrypt records. The active `use: "derive"` key feeds
  `DeriveKey` and the fingerprint subkey and is **never** replaced by data-key rotation, so OAuth state codecs,
  refresh derivations, the backup provenance MAC and the backup archive identity (§2.4) survive a data-key
  rotation (SEC-28).
- **Derive-key rotation (SEC2-24).** A leaked keyring export would otherwise let its holder forge backup MACs
  and state codecs forever, so v1 ships `<app> secrets rotate-derive` (CLI, offline: refuses while the server
  holds the store lock). It adds `d<n+1>` as the active derive key and sets the previous one to
  `"status":"verify"` with `"retire_after"` = now + the longer of the backup retention (`core.backup.*`,
  05-backup) and the longest refresh-token TTL (05-authn), both read from the app's config. A `verify` key is used only through
  `DeriveKeyFor(kid, …)`, to verify and decrypt what was produced under it (older archives' MACs, archives
  encrypted to the identity derived from it, outstanding refresh tokens); producers always use the active key and
  record `ActiveDeriveID()` beside what they produce (05-backup records it in the manifest). `Open` never
  retires a key: the decision needs the effective `core.backup.dir`, which is a DB-layer value, so it is taken
  by `RetireUnused` in 06 P5 (COR3-2). A `verify` key past `retire_after` is treated as `retired` (never used
  again) **only when the in-use check reports that no retained archive, undo point, unconsumed checkpoint or
  retained revocation-journal line (or the journal header) still names it**; otherwise it stays `verify` and
  health is `degraded` (`derive_key_retire_blocked`, naming the kid) until 05-backup's verify task has rewrapped
  those archives and retention has dropped those journal lines, so a key is never retired under an archive or a
  journal line that needs it (DATA3-13, SEC3-10; an error from the check keeps it, fail-safe). The rotation changes every
  fingerprint and the derived archive identity, and the export record no longer covers the keyring, so
  `secrets_keyring_not_exported` is raised until the operator exports again.
- Subkeys via `crypto/hkdf` (SHA-256): data subkey from the active data key, info
  `"mediacore/secrets/aes-256-gcm/v1"`; fingerprint subkey from the derive key, info
  `"mediacore/secrets/fingerprint/v1"`. Other specs derive their own subkeys with distinct `info` strings through
  `DeriveKey` / `Scope.DeriveKey` instead of storing separate secrets.
- Stored record at key `c/secrets/v/<name>`: JSON `{"v":1,"kid":"k1","nonce":"…","ct":"…","at":"…","by":"…"}`;
  AAD = `"mediacore/secrets/v1\x00" + AppID + "\x00" + name`, so a ciphertext moved to another name or app
  fails with `ErrCorrupt`.
- Cleared marker at the same key, `{"v":1,"cleared":true,"at":"…","by":"…"}`: written by `Delete` and by a
  clearing `Stage` when `LegacyStore.Has(name)`, so a legacy gap-fill value cannot resurrect a cleared secret.
- **Read order (`Get`, `Meta`; R3-1).** (1) an operator value (`Ephemeral`; locks the key); (2) a **non-empty**
  legacy primary copy (`SourceLegacy`): every core write blanks it in the same batch, so a non-empty one was
  written by an older build after the core build's last write, and is the newer value; (3) the stored record
  (`SourceStored`), or a cleared marker (`ErrNotSet`, stop); (4) the legacy gap-fill value (`SourceLegacy`);
  (5) `ErrNotSet`. Every `Put`, `Stage` and `Delete` of a name blanks its legacy copies in the same batch
  (`LegacyStore.Blank`), so the first core write of a secret moves it (05-config §1.12.2).
- Store metadata at `c/secrets/meta`: `{"app_id":"abo","keyring_format":"mediacore-keyring/1","created":"…"}`.
  `Open` compares it with `Options.AppID`; a mismatch is `ErrAppIDMismatch` (boot error naming both IDs), so an
  accidental AppID change fails loudly instead of turning every secret into `ErrCorrupt` (SEC-28). AppID is a
  compiled constant of the app's `main` (`app.Options.AppID`), never configuration.

### 2.4 Lifecycle

1. After storage opens and after the pre-migration checkpoint and DAG, as the first P4d reconciler (§1.4
   step 5, C13): `Open` checks `c/secrets/meta` (written on first open) and resolves the keyring:
   - keyring present → load; mode looser than 0600 → try `chmod 0600`; if that fails → `ErrKeyfileInsecure`
     (fail-closed);
   - keyring absent, no stored records → generate `k1` (data) and `d1` (derive), write keyring (first run);
   - keyring absent, stored records exist, **or** legacy-format rows exist and no `.encryption_key` is found
     (HEAD's `guardAgainstKeyRegeneration` case, `cmd/root.go:726-783`) → `ErrKeyMissing` (boot refused, message names the file, the exported
     copy (§2.4 Backups) and the override). With `core.secrets.allow_key_regen=true` new keys are generated **and
     existing records are kept** under their old key id (`Meta.Err = ErrKeyUnavailable`); putting the old keyring
     back later recovers them. Nothing is ever deleted automatically (contrast `persistence.go:1232-1260`).
   - `Legacy` set and an ABO `.encryption_key` found → its material is copied into the keyring as `k0-legacy`
     (`use: data`, `status: retired`: decrypt-only, through `OpenLegacy`). The file itself is left untouched at
     its path: older builds keep using it (09 S3 "same key file" means this). Legacy rows are never re-encrypted
     or rewritten; they are read in place until each secret's first core write blanks them, or until cutover
     (§1.12.2).
2. `config.Load` registered `Ephemeral` values for secret keys supplied by env/file.
3. Runtime: `Get` decrypts on demand (no plaintext cache beyond the call; callers hold `Value` briefly).
   `Put`/`Stage`/`Delete` encrypt with the active data key. No background goroutine and no timer.

**Backups and loss (SIMP-9, SIMP2-2).** The keyring lives in the state dir (beside the database only when the
state directory fell back there, C15). Today no backup carries the
key (`internal/backup` archives the DB directory, `raw/12` §6; `grep` finds no key handling in `internal/backup`),
so a lost key file loses the secrets. Rules:

- The keyring is the install's **only** key artifact. [05-backup](05-backup.md)'s install archive identity is
  not a second file: its X25519 scalar is `DeriveKey` with info `core.backup/age-identity/v1` (through the
  `core.backup.` scope), stable across data-key rotation. There is no identity file, no identity export and no
  second "not exported" health reason; the keyring export below **is** the identity export. Trade: whoever
  holds the keyring can decrypt the archives, which adds only the non-secret data to what that holder could
  already decrypt; an operator who wants the two separated adds an operator recipient (05-backup).
- The keyring is **never** stored only inside an archive it protects. Unencrypted archives never contain key
  material, so the secrets in them stay ciphertext. No archive carries a copy of the keyring as its recovery
  path.
- Recovery is an explicit operator action: `<app> secrets export-keyring --out <file>` (CLI, filesystem access
  required, refuses to write a file looser than 0600, works while the server runs because it reads only the
  state dir). It records the exported key IDs and time in `<state_dir>/secrets.keyring.exported` (no key
  material). Health warning `secrets_keyring_not_exported` stays raised while any key in the keyring is missing
  from that record.
- **Host-loss restore** ([05-backup](05-backup.md) §5B.3.3 procedure): the operator copies the exported file
  into the new state directory as `<state_dir>/secrets.keyring` (mode 0600) before the first start; there is
  no import command, and the next `Open` loads it as the keyring-present case above, then
  `<app> restore --archive <file>`. An archive encrypted only to the install identity needs the keyring to be
  decrypted at all; `--identity` remains only for operator-recipient identities. For an unencrypted archive, or
  one also encrypted to an operator recipient, copying the keyring back is **optional**: without it the restore
  still runs (05-backup `AcceptUnreadableSecrets`), the data restores, and the secrets it cannot decrypt show
  "must be re-entered". The archive's MAC is then unverifiable, so the restore is foreign (05-backup B5).
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
| Legacy row undecryptable (no `k0-legacy`, or bad ciphertext) | fail-open per secret: `Meta.Err = ErrKeyUnavailable` or `ErrCorrupt`, "must be re-entered", health `secrets_undecryptable`, legacy row kept, boot continues | same as ABO's guard, without the deletion (DATA3-12) |
| `LegacyStore.Blank` fails inside a write | fail-closed: the batch is not committed, nothing changed | a written secret with a live legacy copy would let the stale copy win on the next read |
| `Ref.Resolve` with no value | `ErrNotSet` | consumer decides |
| `Value` JSON/text marshal | error `ErrNotSerializable` | accidental exposure fails the response instead of leaking |

### 2.6 Concurrency contract

`Store` is safe for concurrent use. Reads take a read lock on the in-memory keyring only; storage reads are
lock-free snapshots. `Put`/`Delete`/`Stage` serialize per name via a keyed mutex from the core-internal copy
`core/internal/keyedmutex` (04 §9, added at S3; ABO keeps its own `internal/keyedmutex`, which core cannot
import, COR2-16). The keyring changes only at `Open`. The store starts
no goroutine.

### 2.7 Config keys

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.secrets.key_file` | path (relative to state dir) | `secrets.keyring` | no | operator |
| `core.secrets.allow_key_regen` | bool | `false` (ABO alias `ABK_ALLOW_ENCRYPTION_KEY_REGEN`, window ledger row) | no | operator |

### 2.8 Metrics

All on scope `mediacore.secrets`.

| Name | Type | Labels |
|---|---|---|
| `mediacore_secrets_operations_total` | counter | `op` ∈ get, put, delete; `outcome` ∈ ok, not_set, key_unavailable, corrupt, out_of_scope, error |
| `mediacore_secrets_undecryptable` | gauge | — |
| `mediacore_secrets_keyring_keys` | gauge | `use` ∈ data, derive; `status` ∈ active, verify, retired |
| `mediacore_secrets_legacy_reads_total` | counter | `where` ∈ primary, file_fill (values still read from the previous build's copies) |

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
| `TestLegacyKey_ABOEncryptionKey` | raw 32-byte key + 4 legacy rows → readable in place through `LegacyStore`, `k0-legacy` retired (decrypt-only), rows and `.encryption_key` untouched |
| `TestGet_ReadOrder` | operator > non-empty legacy primary > stored record or cleared marker > gap fill > `ErrNotSet`; a legacy value re-entered after the record was written wins (R3-1) |
| `TestStage_BlanksLegacyInSameBatch` | the first `Stage` of a blob-plaintext secret and of a legacy-row secret writes `c/secrets/v/<name>`, empties the blob field and deletes the row in one commit; a failing `Blank` commits nothing |
| `TestDelete_ClearedMarkerStopsGapFill` | clear of a key with a gap-fill copy → `ErrNotSet`, not the file value |
| `TestOpen_LegacyRowsWithoutKeyRefuses` | legacy rows, no keyring, no `.encryption_key` → `ErrKeyMissing`; with `allow_key_regen` the rows read as "must be re-entered" and are kept |
| `TestKeyIDsReferenced_ReportsLegacyRows` | a staged pre-S3 store with legacy rows and no `c/secrets/` reports `k0-legacy` (DATA3-12) |
| `TestEphemeral_NeverPersisted` | env secret never reaches storage |
| `TestFingerprint_StableNotReversible` | same value same fingerprint; differs per keyring |
| `TestExportKeyring_ClearsHealthWarning` | export record covering every key ID clears `secrets_keyring_not_exported` |
| `TestRotateDerive_VerifyThenRetire` | after `rotate-derive`: `DeriveKey` uses `d2`; `DeriveKeyFor("d1", …)` works until `retire_after`, then `ErrKeyUnavailable`; data records still decrypt; refused while the store is locked; export warning raised |
| `TestRotateDerive_NotRetiredWhileArchiveUsesIt` | a `verify` key past `retire_after` stays `verify` while the in-use check reports a retained archive under it (health `derive_key_retire_blocked`); after the rewrap it retires |
| `TestRotateDerive_JournalStaysVerifiable` | after `rotate-derive` and the rewrap, `d1` stays `verify` while a retained revocation-journal line (or the header) names it; `Open` retires nothing; `RetireUnused` in P5 sees the overridden `core.backup.dir` (DATA3-13, SEC3-10, COR3-2) |
| `TestStage_TakesBatchPointer` | `Stage` writes and its `Blank` commit with the caller's batch and are refused outside its bound keyspaces (COR2-12) |

The adapter tests (`TestRollback_SecretSavedOnCurUnsetOnPrev`, `TestRollback_ReenteredOnPrevWinsOnCur`,
`TestRollback_BlobEditOnPrevEffectiveOnCur`, `TestAdapter_IntegrationParticipantSharesRender`) live with
`configstore` and run the §1.12.2 rollback matrix.

### 2.10 Migration from ABO

The data half (key import as `k0-legacy`, legacy copies read in place, blanking on first write, deferred
cutover) is specified once, in §1.12.2. Code replacement:

1. Replace `database.EncryptValue/DecryptValue/InitEncryption/MaskSecret/IsEchoedMask/LooksLikeMask`
   (`settings.go:110,189,214,271,285,293`) and `cmd/root.go` `guardAgainstKeyRegeneration` (`cmd/root.go:735`) with
   `secrets.Open`; the legacy key search order (state dir, then legacy dir) is that of `database.InitEncryption`
   (`settings.go:110-140`).
2. ABO's `/api/v1/config` compat handler returns masks for as long as its ledger row is live, by composing `Meta`
   (`set`) with the old mask shape; the core settings API never does. On the way in, an empty, echoed or
   mask-shaped secret value (`IsEchoedMask`/`LooksLikeMask`, evaluated on the raw request payload) means
   "unchanged" and is never staged, so a round-tripped mask neither overwrites nor moves a secret.
3. ABS JWT secret, Cloudflare policy token, SMTP password: env stays authoritative (aliases); a value entered in
   the UI is stored encrypted for the first time (no legacy row: the previous build reads these from env only).

### 2.11 Open items

1. **Rotation and prune (deferred, SIMP-19).** Not built in v1: after a suspected key leak the operator re-enters
   secrets. Constraints already fixed for when it is built: records name their `kid`, so rotation is "new active
   data key + re-encrypt"; the derive key is rotated by its own command, which ships in v1 (`rotate-derive`,
   §2.3), because rotating it invalidates derived artifacts; pruning a retired key, `k0-legacy` included, is safe
   only when `KeyIDsReferenced` no longer lists it (legacy rows keep `k0-legacy` listed until each is blanked or
   the family's cutover) and the export record covers the current keyring (no coupling to backup manifests).
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
   (`persistence.go:168-178`), which defeats their encryption. Core builds never write the file; they read it only as
   HEAD's gap fill (§1.12.2). While it still holds a secret key, the boot log names the file and tells the operator
   to re-save those secrets in Settings (which encrypts them) and then remove the keys from it; it is never
   archived by [05-backup](05-backup.md).
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
- SIMP-15 — C12, §1.12.2: single config/secrets migration mechanism owned here; §2.10 points to it. (Mechanism replaced in round 3 by R3-1's legacy-format adapter; still specified once.)
- SIMP-19 — §2.3/§2.4/§2.7/§2.8/§2.11: keyring with key IDs ships; `Rotate`, `PruneKeys`, schedule, straggler goroutine deferred.
- SEC-6 — `ProtectAdmin`, §1.3.4, §1.3.7 class table and `TestEveryURLPathOrSignInKeyClassified`; Protection columns.
- SEC-20 — §2.1/§2.4 one keyring; §1.12.2 legacy-format mirror with `k0-legacy`; rotation refused while mirroring. (Superseded in round 3 by R3-1: no legacy-format mirror; legacy rows are read in place.)
- SEC-28 — §2.3: separate derive key; `c/secrets/meta` app-ID check (`ErrAppIDMismatch`); AppID is `app.Options.AppID`.
- SEC-29 — §2.3 `Name` grammar and `secrets.Scope` (`modules.<id>.`); instance secrets only via `integrations.Env.Secrets`.
- DATA-1 — C4, §1.12.2: overrides only in `c/config/`; `setting:`/`preference:`/`pref:` never adopted; exact-key claims only. (Narrowed in round 3 by R3-1: keys ABO already stores stay in the blob; only native-only keys use `c/config/`.)
- DATA-2 — §1.12.2: migration owned by kernel `configstore`, claims listed in 05-storage §5S.9.1. (Superseded in round 3 by R3-1: no migration; configstore reaches books' rows through a logged grant.)
- DATA-6 — §1.12.2: blob mirrored for W, hash-based re-import at every boot, secrets re-imported and re-blanked. (Superseded in round 3 by R3-1: the blob is read and written in place; no mirror, no re-import.)
- OPS-3 — §1.12.2: truth is `c/config/` from X, so no contract-time format change. (Superseded in round 3 by R3-1: truth is the blob in place, `c/config/` for native-only keys; cutover deferred.)
- OPS-5 — C7, §1.3.6 `AliasRetired` fail-closed, §1.12.4 permanent shipped-file names and not-read list.
- OPS-9 — §1.12.2: one S3 model (Teach + mirror + conditional blanking). (Superseded in round 3 by R3-1: no Teach build or mirror.)
- OPS-17 — §1.7: no runtime compat switch; operational dials allowed.
- COR-14 — `ReloadBoot`, boot record (§1.3.5), `Store.Boot` (§1.4 step 2) for module enablement.
- COR-19 — header and §1.3/§2.3: R4 v1.3.0 layers (L1 storage, L2 secrets, L3 config).
- COR-20 — `Options.Metrics *metrics.Scope` in config and secrets.
- COR-26 — `Options.AppID` documented as `app.Options.AppID`.
- SEC-15 (R-F request) — §1.3.7: `core.integrations.egress.lan_allow` classified `ProtectAdmin` (request asked `ProtectInteractive`; SEC-15 fix says admin).
- SIMP-6 — §1.6, §1.12.3: no timers in config; `scheduled.*` via `tasks.ScheduleFromConfig`; rotate schedule removed.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-H request — one rule for `OPENAI_*` and the other integration-bound env names: permanent aliases onto `Passthrough` paths `integrations.<kind>.<instance>.<field>`; 05-metadata applies `Origin=pinned` rows while an operator value is set (C11, §1.3.3).
- integration pass: R-H request (G11) — Cloudflare Access keys (`cf_access_team_domain`, `cf_access_aud`, `owner_email`) are books keys `modules.books.identity.*`; protection table and §1.12.3 counts updated (46 → `core.*`, ≤118 → books).
- integration pass: R-B request (SIMP-9) — §2.4: host-loss restore copies the exported keyring file into the state directory; copying it is optional (secrets then re-entered, restore foreign).
- integration pass: Verified already present (R-D/R-E requests): `ProtectAdmin` defined; SEC-28 `DeriveKey` from a derivation key stable across data-key rotation; SEC-29 name grammar and `secrets.Scope`; no integration-instance rows; `core.integrations.egress.lan_allow` is `ProtectAdmin` (the R-F request for `ProtectInteractive` stays rejected per SEC-15).

Round 2:

- R2-7 / SIMP2-1 / COR2-6 — C14, §1.6 schedule-key rule: `<owner>.<subsystem>.schedules.<name>`, owner-declared `tasks.ScheduleConfig` field, no `-` in paths, legacy interval keys aliased with a `Convert`; §1.12.3 `scheduled.<task>` row split by owner.
- R2-8 / COR2-7 / DATA2-24 — C13, §1.4 rewritten against 06 §6.5.1: `Boot`/`Load` touch no storage; `secrets.Open`, configstore import and `AttachOverrides` run after the checkpoint and DAG at reconciler position (c); §2.4 step 1; `TestNoStoreWriteBeforeCheckpoint`. (Amended in round 3: no configstore import; order per COR3-2.)
- SIMP2-2 — C8, §2.4: backup archive identity derived from the keyring (`core.backup/age-identity/v1`); one export; host-loss restore wording.
- SIMP2-3 — C10, §1.3 imports, §1.10.2: reflector in L0 leaf `core/internal/jsonschemax` with an extensible override table; config contributes `config.Duration`/`secrets.Ref`, httpapi `rbac.Permission`; tests updated.
- SIMP2-5 — C11, §1.4 step 8: operator values are an in-memory overlay, never persisted; HEAD's revert-to-stored semantics.
- SIMP2-8 / SIMP2-16 — §2.3: "cursor HMACs" removed from the derive-key consumers.
- SIMP2-12 — §1.3.1 `Alias.Ledger` replaces `AliasClass`; §1.3.6 lifetime from the ledger, `Options.Retired`; C7, §1.12.4, §2.7 wording.
- MOD2-20 — C10, §1.10.2: "pinned exactly" replaced by Dependabot `ignore` + `check_module_skew.py --pin`.
- COR2-8 — `Options.EnvPrefix` default, `Options.BootRecordRoot` (no circular state-dir root), `BootView` carries `core.paths.state_dir`/`core.storage.path`; `core.paths.data_dir` deleted; `TestBoot_ResolvesPathsWithoutStorage`. (Amended in round 3 by R3-4/OPS3-1: HEAD's state-directory fallback.)
- COR2-12 — §2.3 `Stage`/`StageLegacy` (and new `DeleteLegacy`) take `*storage.Batch`; `TestStage_TakesBatchPointer`. (`StageLegacy`/`DeleteLegacy` removed in round 3 by R3-1.)
- COR2-14 — §1.3.4: a module section needs a declared `<id>:settings.manage` or explicit `config.Permissions`.
- COR2-16 — §2.6: `core/internal/keyedmutex` copy, not ABO's package.
- DATA2-9 / OPS2-8 — §1.12.2 blanking needs both the ledger run (no foreign open) and a `.prev --print-storage-levels` probe; otherwise plaintext until C3; matrix row and R-test cell. (Superseded in round 3 by R3-1: blanking happens per secret on its first core write.)
- DATA2-10 — §1.12.2 integration-bound keys: from 09 S7 to C7 integrations owns the unit re-import and mirror (05-metadata M1.4 step 2a), blob keys through configstore's mirror participant; matrix rows. (Superseded in round 3 by R3-1: integration-bound keys stay in the blob, written through configstore's participant.)
- DATA2-11 — §1.12.2 phase T writes/deletes legacy rows and blanks the blob; import hash over what the previous build reads; clears re-imported as `secrets.Delete`; empty-value exception dropped. (Superseded in round 3 by R3-1: no phase T.)
- DATA2-17 — §1.3.3 `ConfigFileHasSecrets`, `RedactedConfigFile`; `<db dir>/config.yaml` never archived; test.
- DATA2-23 — §1.12.2 claims add sub-prefix `setting:metadata_sources.` (verified unused at `748fd787a`). (Superseded in round 3 by R3-1: no claim on `setting:metadata_sources.`; no legacy row is written.)
- OPS2-7 — §1.3.3/§1.3.6 `Surface` (env/file → `SurfaceEnv` once per boot, buffered until storage; blob/API → `SurfaceConfig`); retired secret-key aliases fail boot.
- OPS2-12 — §1.3.5, §1.5: alias conflict on a `ProtectAdmin`+ key fails boot naming both; `TestAliasConflictOnProtectedKeyRefusesBoot`.
- OPS2-15 — §1.12.1: façade stage 1 is S3's first release, phase X a later one. (Superseded in round 3 by R3-1: no phase X.)
- OPS2-21 — §1.12.4 single list: 142 `BindEnv` names (generated), OTEL interval/insecure permanent, runtime names class, commented lines scanned.
- SEC2-7 — §1.3.7: `lan_allow` applies to every instance without `Local`; `Local` and `Test` with `Local` need `ProtectAdmin`; one URL-field rule shared with 05-metadata.
- SEC2-8 — §1.3.7 classifier gains key-material/encryption shapes.
- SEC2-24 — §2.3 `secrets rotate-derive` (offline), `verify` status with `retire_after`, `ActiveDeriveID`/`DeriveKeyFor`; §2.11 updated; test. (Amended in round 3: retirement decided in P5 by `RetireUnused`, journal lines included.)

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): `core.paths.state_dir` is pre-boot (default, env, flags; a file value fails boot at P0); no `data_dir`; `Store.Load` reads no DB (06 P3), steps 5–7 are 06 P4d step 3; `TestBoot_ResolvesPathsWithoutStorage` updated. (Amended in round 3: config's steps are the first P4d reconcilers, and the state directory has HEAD's fallback.)
- integration pass (round 2): §1.12.2: X runs adopt step `core/config#1` stamping `Floor` 1 (Teach build S0); C3 is `Breaking`, `Adopts: [core/config#1]`, `RaisesLevel` 2. (Superseded in round 3 by R3-1: no adopt step, no C3.)
- integration pass (round 2): `OverrideStore.Commit` carries the `core.settings.update` audit record (05-rbac §7.1).
- integration pass (round 2): Derive-key rotation: a `verify` key is not retired while a retained archive uses it (`secrets.Options.DeriveKeyInUse`, health `derive_key_retire_blocked`, test). Key/recipient/encryption classifier shapes: already applied. (Amended in round 3: `DeriveKeyInUse` replaced by `RetireUnused`.)
- integration pass (round 2): "wave 0" → 09 S0 (MOD2-21).

Round 3:

- R3-1 / COR3-1 / DATA3-3 — C4, C11, C12, C13, §1.3.3 (`Legacy`, `LegacyBlob.Encode`, `OverrideStore.Changed`), §1.4, §1.12.2 rewritten: legacy-format adapter reads and writes `config_blob` in place, native-only keys in `c/config/`, S2 read-only layer over `Legacy.Rows` with live republish, blob never deleted; Teach/Expand-mirror/Contract machinery removed; cutover deferred.
- R3-1 (secrets) — §2.2 legacy-copies column, §2.3 `LegacyStore`, read order, cleared marker, `SourceLegacy`; secrets move on first write and their legacy copies are blanked in the same batch; release note; `StageLegacy`/`DeleteLegacy`, mirror metric and mirror tests removed.
- R3-4 / OPS3-1 — C15, §1.3.3 `ResolveStateDir`, §1.4 step 0, §1.7 pre-boot rule with HEAD's fallback beside the database; `TestStateDirFallbackMatchesHEAD`.
- OPS3-2 — superseded by R3-1 (no phase T; nothing is blanked on a pre-S3 build).
- COR3-2 / DATA3-5 — §1.4: `secrets.Open` and the read-only attach are the first P4d reconcilers, before the journal re-apply; derive-key retirement moved out of `Open` into `RetireUnused` in P5.
- DATA3-13 / SEC3-10 — §2.3: retirement in-use check covers retained revocation-journal lines and the journal header; test.
- DATA3-12 — §2.3 `KeyIDsReferenced(ctx, storage.Reader)` reports `k0-legacy` for legacy rows; undecryptable legacy rows read as "must be re-entered" (§1.12.2, §2.5).
- SEC3-6 — §1.3.1 `ProtectOwner` activation, §1.3.7 owner row lists all five HEAD owner fields plus `core.auth.owner.required`; `TestOwnerTrustRootKeysAreOwnerProtected`.
- SIMP3-1 — §1.3.1 `config.Overrides()`, §1.10.2: httpapi imports `config` for the static value only; `time.Time` only in `jsonschemax.Base()`; both are functions (R5); `TestSchema_OverrideSetsDisjoint`.
- SIMP3-3 — §1.3.1 `Alias.Ledger`, §1.3.6: ledger from `app.Options.CompatLedger` merged with core's fragment; core-key aliases with app names come from `Legacy.Aliases`.
- FE3-11 — §1.3.4: settings routes mounted from S4; before that through ABO's `/api/v1/config`.
- COR3-11 — §1.3.1: map keys are data, not path segments (`core.tasks.kinds`).
- SIMP3-12 (convention) — superseded history lines above are marked.
