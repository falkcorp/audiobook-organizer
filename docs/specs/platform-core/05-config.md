<!-- file: docs/specs/platform-core/05-config.md -->
<!-- version: 1.13.15 -->
<!-- guid: fb637cbc-bbfa-4aea-807c-71f02534b3c9 -->
<!-- last-edited: 2026-11-10 -->

# 05 — Config and secrets (`core/config`, `core/secrets`)

Evidence commit `748fd787a` (all `path:line` anchors; `internal/config` and `cmd/root.go` are unchanged at HEAD of
the plan worktree, so the anchors hold there too). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §2, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7, G12;
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md) finding 3;
[`raw/03-layering-guard.md`](../../plans/platform-core/raw/03-layering-guard.md) `allowed` rows 1–5;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md). Normative names come from
[03-architecture](03-architecture.md): R4 layers **L1** `storage` → **L2** `secrets` → **L3** `config`
(`metrics` and `log` are L0; `integrations` is L4, above config); R5 (no globals) binds **core**
only; §3.6 `core.*` / `modules.<id>.*`. ABO's settings blob, its plaintext and encrypted secrets and its gap-fill
file move into core's native keyspaces and the keyring **once**, in the R2 cutover step M2 (§1.12.2). HEAD's env and
file names are retired in the same release (§1.3.6, §1.12.4): there is no alias, no window and no legacy store.

## Decisions at a glance

| # | Decision | Why (short) |
|---|---|---|
| C1 | One `config.Store` per `App`; no package-level config value anywhere in **core** (R5). ABO keeps `config.AppConfig` as a books-module façade over core handles (§1.12.1). | `var AppConfig Config` (`internal/config/config.go:1939`) cannot host two apps and is read racily by design (`config.go:1930-1938`). R5 governs core only (03 §3.5), so ABO's façade is allowed. |
| C2 | **Config owns the mechanism, never a section's type.** Each section struct lives in the package that consumes it and is registered through the manifest as `config.Section[T]`. | This is what breaks all upward imports (§1.9): config imports `aidispatch`, `dedup/unified`, `backup`, `auth`, `database` today only because it owns their types. |
| C3 | Merge order defaults → file → env → flags → DB overrides, with **operator lock**: a key supplied by file, env or flag cannot be overridden from the DB layer and `Apply` refuses it with the lock reason. **One fail-closed exception (§1.3.5):** for a key its owner declares `FailClosed` (`core.auth.mode`, `core.http.basic_gate.enabled`) the more restrictive of the operator value and the stored value wins, and a stored value that beats a weaker operator value is reported. Only canonical names and ecosystem bindings are read, and values decode strictly. An env variable set to `""` is unset. | Generalizes ABO's per-key `envLockedSettings`/`flagLockedSettings` (`config.go:2241,2353`) to every key; today a DB blob silently beats env for all other keys (`persistence.go:915` then `config.go:2435`). Operator values now lock, so a stale disabling value in a unit, or a deploy file that always sets the Basic gate's switch off (`docker-compose.yml:20`), must not open what an admin closed (14 E5). |
| C4 | **The DB layer is a store interface, `config.OverrideStore` (R4-1), with one implementation:** core's native store over its keyspace `c/config/` (one row per explicitly set key), used by every app. ABO's settings are migrated into it once, through `config.Importer` (§1.12.2). No app supplies a store, and core holds no app key name, row format or storage grant. A value equal to the key's default is never stored. | One stored format per family, written by one owner (R3-1). HEAD's whole-config blob froze every default at save time (`persistence.go:1180-1188` measured 33 keys inheriting stale defaults). |
| C5 | Every key declares `Reload` (live / restart / boot) and `Protection` (none / interactive / admin / owner / operator); both appear in the generated JSON Schema. | ABO has exactly one boot-only key handled ad hoc (`persistence.go:2111`) and a 92-entry protection map (`protected_fields.go`) separate from the schema. Roles-as-data can hand `core:settings.manage` to non-admins, so sign-in, proxy and updater keys need an admin level (§1.3.7). |
| C6 | Drop **viper** from core and, in R2, from ABO's boot path. Stdlib + `go.yaml.in/yaml/v3` + `github.com/google/jsonschema-go` only. | §1.10. |
| C7 | Env names are `<APP>_<PATH>`; no `MEDIACORE_` prefix. The only other names are **ecosystem bindings** an owner declares (`OTEL_*`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`). Every other env name and flat file key HEAD reads is **retired** in R2: a retired name of a key at `ProtectAdmin` or higher, or of a secret key, **refuses boot** naming its canonical name; any other retired name is ignored with a warning (§1.3.6). ABO's deploy files switch to canonical names in the same release (§1.12.4). | An ignored `DATABASE_PATH` would open a new, empty store at the default path (OPS-5, 14 K15); an ignored `ABS_JWT_SECRET` stops absauth (`internal/server/absauth/config.go:20-22`). |
| C8 | Every recoverable secret is encrypted at rest by `core/secrets` with **one keyring design** (AES-256-GCM, key IDs in every record, AAD = app + name, a separate derivation key); config fields are `secrets.Ref`, never strings. The keyring is the install's **only** key artifact: backup's archive identity is derived from it, so one export recovers both (§2.4). Data-key rotation and prune tooling are **deferred**; the format already carries key IDs so it never changes. The derive key has an offline rotation command in v1 (§2.3). Verifiers (passwords, API keys, session ids) are hashed by `auth`, never stored in `secrets`. | `raw/10` G12: 4 of at least 10 secret-bearing fields are encrypted, and 3 of those 4 are also written in plaintext to a mirror YAML file (`persistence.go:168-178`). Rotation does not exist today (`raw/10` §7). A second key file would need a second export (SIMP2-2). |
| C9 | Secrets are **write-only** over the API: the UI receives `{set, source, updated_at, fingerprint}`, never a mask. | Masks reveal the last 2 characters (`internal/database/settings.go:250-277`) and force echo-detection logic (`update_service.go:353-373`). |
| C10 | **One JSON-Schema reflector**: `github.com/google/jsonschema-go`, held at v0.4.3 (Dependabot `ignore` entry plus `check_module_skew.py --pin`, 13), wrapped by the L0 leaf `core/internal/jsonschemax`, which owns the tag pass and an extensible override table. Each package contributes the entries for its own types: config adds `config.Duration` and `secrets.Ref`; [05-http-api](05-http-api.md) adds `rbac.Permission` for OpenAPI. | §1.10.2; verified 2026-11-04. A config-owned table could not name an L4 type (SIMP2-3). |
| C11 | **Config holds no integration instances.** Instance records (kind, instance, settings, `secrets.Ref`s) live in the `integrations` registry ([05-metadata](05-metadata.md) M1; `c/integrations/instances/`); ABO's blob singletons become native instances in the R2 integrations step M6, and M2 leaves their non-secret blob fields in place for it (§1.12.2). Config only reads the operator-layer values (file, env, ecosystem bindings) at their `integrations.<kind>.<instance>.<field>` paths raw and hands them over (§1.3.3 `Passthrough`); integrations applies them as an **in-memory overlay** on the instance's stored row under config's one lock rule: an operator value overlays its field and locks it. Private-network trust attaches only when the **effective** field's `Source` is an operator layer. Removing the operator value reverts to the stored row (`persistence.go:1959-1971`). There is no `core.integrations.<kind>.instances.<id>` config form. | One store for one fact (SIMP-4): the Deluge password had four destinations across the specs. One lock model, config's operator-layer lock, for config keys and instance fields alike (SIMP2-5). |
| C12 | This file owns `config.OverrideStore`, the native store, `config.Importer`, the key grammar and ABO's rename table (§1.12.3), the retired-name rule (§1.3.6) and the config-and-secrets cutover step M2 (§1.12.2); 09's R2 tables sequence them and reference them. | Specified twice with contradictory semantics before (SIMP-15, OPS-9). |
| C13 | Config and secrets write nothing to the store before the boot's pre-migration checkpoint. After it, `secrets.Open` writes `c/secrets/meta` on a new store, and M2 writes `c/config/` and `c/secrets/` through the importers inside the migration list; attaching the DB layer is read-only. Their boot steps are placed by the one boot-phase table, [06](06-app-sdk.md) §6.5.1 (R2-8). | A write before the checkpoint puts post-upgrade rows into the restore point (COR2-7). |
| C16 | **HEAD's secrets migrate once.** M2 re-encrypts the four rows HEAD encrypts (OpenAI key, Google Books key, Hardcover token, Basic password, `persistence.go:1955-1958,1989-2006`) and every plaintext blob secret into `c/secrets/` under their canonical names, deletes the rows and blanks the blob fields in the same batch, and strips HEAD's three plaintext copies from the gap-fill file (§1.12.2). HEAD's file-only secrets (§2.2 "—" rows) were never stored, so nothing of them migrates; their retired env names refuse boot. An undecryptable row is quarantined and its name reads "must be re-entered". | No older build opens a migrated store (05-storage's level step M7), so keeping HEAD's representation buys nothing, and the plaintext copies were defects (`raw/10` G12; out-of-scope defect 2). |
| C17 | **No app boot step runs inside config.** HEAD's settings load, encryption init and key-regeneration guard (`cmd/root.go:345-369`) are replaced: the load by core `Load` and `Attach`, the init and the guard by M2, which is the only reader of HEAD's key file and refuses, as HEAD does, to run when encrypted rows exist and the key file does not (§1.12.2). | HEAD's pre-server steps exist to read HEAD's rows; after M2 there are none. |
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
mounted by [05-http-api](05-http-api.md)), the permission check itself ([05-rbac](05-rbac.md)), and ABO's own
migration code (the cutover step M2 is books code; this file fixes its contract, §1.12.2).

### 1.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Shape | One `type Config struct`, 864 lines, **173 top-level fields** (this writer's count of field lines in the struct), 247 `SetDefault` registrations | `internal/config/config.go:1048-1912`, `registerConfigDefaults` `config.go:2621` |
| Global | `var mu sync.RWMutex`, `var AppConfig Config`, `Snapshot()` copy under RLock, `Mutate(fn)` under Lock, `generation atomic.Uint64` bumped by every Mutate | `config.go:1928,1939,1944,2011,2023,2035` |
| Single-field locked readers | `SupportedExtensionSet`, `AuthorityEvidenceEnabled`, `RetireVersionsApplyEnabled`, `KeptSweepApplyEnabled` | `config.go:1971-2006` |
| Sources, in actual application order | (1) viper defaults + `AutomaticEnv` (no prefix) + 142 explicit `BindEnv` + viper YAML file `<home>/.audiobook-organizer.yaml` or `--config`, decoded by `configFromViper`; (2) 4 flags bound (`dir`, `db`, `db-type`, `playlists`) plus `--log-level` written straight into `AppConfig`; (3) DB `config_blob` **replaces the whole struct**; (4) env-authoritative keys re-applied (auth-surface keys unconditionally, operational keys only when env supplies them); (5) a second YAML file `<db parent>/config.yaml` fills 4 empty fields; (6) `SyncConfigFromEnv` re-applies 6 keys from env if non-empty | `cmd/root.go:603-660`, `cmd/root.go:55-60`, `cmd/root.go:650`, `config.go:2586-2597,3215`, `persistence.go:915,1229`, `config.go:2435`, `persistence.go:42-104`, `cmd/root.go:381`, `persistence.go:2023` |
| Two config files | viper reads `<home>/.audiobook-organizer.yaml`; `SaveConfigToFile` writes `<db parent>/config.yaml` (merged, atomic, 0600) on every DB save — a different file — and `LoadConfigFromFile` reads it back as a fallback | `cmd/root.go:614-623`, `persistence.go:29-38,121-195`, comment `config.go:2290-2292` |
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
wrapper, §1.10.2), `github.com/falkcorp/mediacore/secrets` (L2; `secrets.Ref` and `secrets.Resolver` only), `github.com/falkcorp/mediacore/storage` (L1; only for `*storage.Batch` in
`OverrideStore.Stage` and `Importer.Stage`; config opens nothing and runs no storage call itself) and `github.com/falkcorp/mediacore/metrics` (L0; its own
`*metrics.Scope`). It does **not** import `log`, `auth`, `rbac`, `audit`, `tasks`, `integrations` or any
module (§1.9).

#### 1.3.1 Declaring a section (module side)

```go
package config

// Path is a dotted key path: "core.log.level", "modules.books.scan.on_startup".
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
// A Validator never formats a sensitive field's value into its error: these errors reach logs,
// AttachReport, KeyState and API responses. Config does not trust it to: in a section that holds a
// sensitive key, no message a Validator writes is kept (fail closed). A FieldError on one of the
// section's keys, sensitive or plain, keeps its path and gets a fixed message in place of its own
// (in a map section, only a key of an entry the checked candidate holds: the path is Validator text
// too), and any other part of the error is replaced by a fixed message on the section. A section
// with no sensitive key keeps its Validator's messages as written.
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
func ProtectionWaivers(w map[Path]string) SectionOption          // key -> reason it needs no class despite its shape (§1.3.7)

// EnvBinding gives key p one more env name, for an ecosystem name only (OTEL_EXPORTER_OTLP_ENDPOINT and its
// siblings, 05-metrics §10). The canonical name keeps working; the two in one layer follow §1.3.5's conflict
// rule. A name bound twice, or equal to any key's canonical name, fails Register with ErrDuplicate.
func EnvBinding(p Path, name string) SectionOption

// FailClosed marks a bool or enum key whose more restrictive value wins between the operator layers and the DB
// layer (§1.3.5). order lists every allowed value as JSON, most restrictive first; Register fails with ErrInvalid
// when the key is not a bool or enum, when a value is missing from order, or for a secret key. 05-authn declares it
// for core.auth.mode (["\"required\"","\"disabled\""]) and 05-http-api for core.http.basic_gate.enabled
// (["true","false"]).
func FailClosed(p Path, order ...json.RawMessage) SectionOption

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

There is no alias type and no deprecation option. A key is renamed in the PR that renames it, together with every
caller, deploy file and dashboard (D10); a stored value under the old path is moved by a migration step of the
key's owner, exactly as M2 moves ABO's (§1.12.2), and the old path is unknown from then on.

Field metadata is struct tags on `T` (reflected once at `Register`):

| Tag | Meaning | Example |
|---|---|---|
| `json:"name"` | key segment; decoding uses `encoding/json`, so there is **one** decoder (today: dual `json` + `mapstructure` tags, e.g. `config.go:1636,1773`) | `json:"interval"` |
| `reload:"restart\|boot"` | `ReloadRestart` / `ReloadBoot`; default live | `reload:"restart"` |
| `protect:"interactive\|admin\|owner\|operator"` | `Protection`; default none | `protect:"admin"` |
| `schema:"…"` | JSON-Schema keywords: `minimum=`, `maximum=`, `enum=a\|b`, `format=duration\|uri\|cidr\|path\|email`, `pattern=` | `schema:"minimum=1,maximum=64"` |
| `sensitive:"true"` | not a secret, but redacted in diagnostics bundles and logs (emails, allow-lists) | owner e-mail |
| field type `secrets.Ref` | secret; schema `writeOnly: true`; value lives in `secrets` (Part 2); never `reload:"boot"` (the boot record holds no secret; `Register` refuses the combination) | `APIKey secrets.Ref \`json:"api_key"\`` |

Example (books module, ABO repo; the paths are the rename table's, §1.12.3):

```go
type ScanSettings struct {
	OnStartup        bool     `json:"on_startup"`
	ConcurrentScans  int      `json:"concurrent_scans" schema:"minimum=1,maximum=64"`
	ProgressEvery    int      `json:"progress_every" schema:"minimum=1"`
	ExcludePatterns  []string `json:"exclude_patterns"`
}

func (s ScanSettings) Validate() error { /* cross-field checks */ return nil }

var Scan = config.Define("modules.books.scan", ScanSettings{ /* HEAD's defaults, registerConfigDefaults */ })

type AcoustIDSettings struct {
	APIKey secrets.Ref `json:"api_key" protect:"interactive"`
}

var AcoustID = config.Define("modules.books.acoustid", AcoustIDSettings{})
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
	// Passthrough reports the operator-layer paths config reads raw and does not own: the
	// integrations.<kind>.<instance>.<field> paths of 05-metadata M1 (C11).
	Passthrough func(Path) bool
	// EnvBindings are the app's ecosystem env names for passthrough paths (ABO: OPENAI_API_KEY and
	// OPENAI_BASE_URL onto integrations.ai.openai.{api_key, base_url}), from app.Options.ConfigEnvBindings. Core
	// and module keys declare theirs with EnvBinding (§1.3.1).
	EnvBindings []EnvName
	// Retired is the app's list of retired env names and file keys (ABO: generated from its rename table,
	// §1.12.3), from app.Options.RetiredConfigNames; core holds no app name (03 R1). Boot and Load act on it
	// (§1.3.6).
	Retired     []RetiredName
	Logger      *slog.Logger
	Metrics     *metrics.Scope    // core scope "mediacore.config" (05-metrics)
	Secrets     secrets.Resolver  // may be nil until secrets opens; Refs resolve after SetSecrets
	Now         func() time.Time
}

type EnvName struct {
	Name string
	Path Path
}

// RetiredName is one env name or top-level operator-file key an app read before core (§1.3.6).
type RetiredName struct {
	Name      string // the env name, or the flat top-level file key (its nested keys included)
	File      bool   // true: an operator-file key; false: an env name
	Canonical Path   // the key it became; "" when the key was dropped (its use only warns)
	// Refuse is read only when Canonical is a passthrough path: config cannot see an instance field's class, so
	// the app states it (ABO's generator sets it for secret and destination fields, 05-metadata M1.3).
	Refuse    bool
	Message   string // optional reason (ABO: HEAD's removed-key messages, internal/config/removed_keys.go)
}

func NewStore(o Options) (*Store, error)

// ResolveStateDir is the one pre-boot rule for core.paths.state_dir (§1.7, C15, OPS3-1). The kernel runs it
// once per process, before 06 P0a, and hands the result to P0a, Boot and every consumer, so all agree
// (HEAD memoises for the same reason, internal/config/state_dir.go:57-79). explicit is the env or flag value
// ("" if unset); def is app.Options.StateDir; dbPath is core.storage.path from the pre-boot layers (compiled
// default, the operator file Options.FilePath, env, flags, as HEAD's fallback reads AppConfig.DatabasePath with
// its YAML file, OPS5-13); mkdir is the kernel's MkdirAll(dir, 0o700). It is a pure function of those inputs.
func ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error)

func (s *Store) Registry() Registry
func (s *Store) Boot(ctx context.Context) (BootView, error)              // core sections only; operator layers + boot record
func (s *Store) Load(ctx context.Context) error                          // seals Registry; defaults+file+env+flags+boot record
func (s *Store) Importer() Importer                                      // for the app's cutover steps (§1.12.2); after Load
func (s *Store) SetSecrets(r secrets.Resolver)
func (s *Store) Attach(ctx context.Context, o OverrideStore, c Committer) (AttachReport, error) // read-only; once per boot
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error)
func (s *Store) Lookup(p Path) (KeyState, bool)
func (s *Store) Keys() iter.Seq2[Path, KeyState]                         // sorted; secret values never included
func (s *Store) RetiredSet() []RetiredUse                                // retired names set at this boot (config check, §1.3.6)
func (s *Store) PassthroughValues() iter.Seq2[Path, OperatorValue]       // raw; handed to integrations by the kernel
func (s *Store) Schema(include func(section Path) bool) (json.RawMessage, error)
func (s *Store) Generation() uint64
func (s *Store) Close(ctx context.Context) error

// ConfigFileHasSecrets reports whether the operator file supplies a value for any secret key or for any
// Passthrough path (integration-bound values may be credentials, so they count). 05-backup then archives the
// file as class Secret (DATA2-17).
func (s *Store) ConfigFileHasSecrets() bool
// RedactedConfigFile renders the operator file as YAML without those values, for unencrypted archives.
func (s *Store) RedactedConfigFile() ([]byte, error)

// RetiredUse is one retired name found set (§1.3.6).
type RetiredUse struct {
	Name         string
	File         bool
	Canonical    Path   // "" for a dropped key
	CanonicalEnv string // the canonical env name, "" for a dropped key
	Refused      bool   // a protected or secret key: this boot was refused
}

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
	By    string // "file <name>", "env ABO_…", "env OPENAI_API_KEY"
}

// FromFlagSet converts a stdlib FlagSet to Options.Flags using Visit (explicitly-set flags only).
// Apps on cobra/pflag write the same 10-line adapter over pflag.FlagSet.Visit; core does not import pflag.
func FromFlagSet(fs *flag.FlagSet, bind map[string]Path) map[Path]string

// OverrideStore is the store interface of the DB layer (R4-1). It is SDK and frozen by the API-stability
// gate. Core ships its one implementation, core/internal/configstore over keyspace c/config/; no app supplies
// one (an app's pre-core settings are migrated into it once, through Importer, §1.12.2). Paths and values are
// canonical. configtest.OverrideStoreSuite is its contract (§1.11).
type OverrideStore interface {
	// Load returns every stored DB-layer value. Read-only: it never writes, renders or repairs.
	Load(ctx context.Context) iter.Seq2[Override, error]
	// Stage joins b with the writes for set and unset: only those keys change. A read-modify-write reads with
	// GetForUpdate on b, so a concurrent writer fails the commit with storage.ErrConflict and the Committer retries.
	Stage(ctx context.Context, b *storage.Batch, set []Override, unset []Path) error
}

// Importer is config's write side for a one-way cutover step (03 §3.3 "importers"; §1.12.2). An app's
// migration step decodes its own pre-core rows and hands canonical values here, into the step's batch; the
// importer reads no app row and names no app key (03 rule R1). The kernel hands it to the app's steps as
// module.Importers.Config (06 §6.4.2); it is valid after Load, when every enabled section is registered, and
// Stage is refused once Attach has run: cutover steps run before P4d, and every later write goes through Apply,
// with its protection checks and its audit record.
type Importer interface {
	// Stage validates v against p's registered schema and stages p's c/config/ row into b, binding c/config/ to
	// b (participant binding, 05-storage D-S10). A value equal to p's default stages nothing (C4). It refuses with
	// ErrUnknownKey a path that is not registered or is a passthrough path, with ErrInvalid a secrets.Ref key
	// (secrets go through secrets.Importer, §2.3) and a ProtectOperator key (operator layers only), and returns
	// *FieldError for an invalid value, which the step quarantines (§1.12.2). Cross-field Validators run at
	// Attach, as for every stored row (§1.4 step 6).
	Stage(b *storage.Batch, p Path, v json.RawMessage, by string) error
	// Default returns p's registered default, so a step can report what a quarantined key falls back to.
	Default(p Path) (json.RawMessage, error)
}

// Committer is implemented by the kernel (core's core/internal/configstore). Commit opens ONE storage batch and
// stages into it: OverrideStore.Stage for c.Set and c.Unset, secrets.(*Store).Stage for each of c.Secrets, and the
// audit record core.settings.update (05-rbac §7.1: section and changed key names, never secret values). The record's
// actor is the request's audit actor on ctx, whole (method, credential, client address), when its UserID is c.By, and
// {UserID: c.By} when ctx carries none; a ctx actor naming someone else, or an Override.By or secrets.Write.By that is
// not c.By, refuses the commit before anything is staged. c.By is set on every commit, so one that only unsets keys or
// only changes boot keys still names its writer. It retries with storage.RetryOnConflict, each attempt a fresh batch.
// config never imports audit (L4).
// audit.Store and the action table (core.settings.update among its rows) land dark in F2, the foundation PRs, so
// the record has its store from the config flip F3 on (IMPL-3).
type Committer interface {
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
	Boot    []Path // boot-record keys this Apply changed: written to config.boot.json, named in the audit record
	By      string // Actor.Subject of the Apply: the audit actor; every Override.By and secrets.Write.By equals it
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
	FailClosed  bool          // the key is FailClosed (§1.3.5)
	Pending     bool          // a restart-required change is stored but not effective
	Quarantined error         // stored override ignored at boot (§1.4)
	Secret      *secrets.Meta // non-nil for secret keys
}

type AttachReport struct {
	Applied     int
	Unknown     []Path                // retained rows for keys this binary does not declare
	Quarantined map[Path]error        // rows ignored because their section failed to decode/validate with them
	Ignored     map[Path]string       // stored values shadowed by an operator lock (rows, boot-record values) -> lock reason
	// FailClosed: FailClosed keys whose stored value is more restrictive than their operator value and therefore
	// effective -> the operator source; one warning per boot and health config_fail_closed_override (§1.3.5)
	FailClosed  map[Path]string
}
```

Config's sentinel and typed errors:

```go
var (
	ErrUnknownKey    = errors.New("config: unknown key")
	ErrLocked        = errors.New("config: key is set by the operator")      // wrapped in *LockedError
	ErrProtected     = errors.New("config: key needs a stronger credential")  // wrapped in *ProtectedError
	ErrForbidden     = errors.New("config: missing section permission")
	ErrInvalid       = errors.New("config: invalid value")                    // errors.Join of *FieldError
	ErrSealed        = errors.New("config: registry sealed after Load")
	ErrDuplicate     = errors.New("config: duplicate path or env name")
	ErrRetiredName   = errors.New("config: a retired name is set")           // wrapped in *RetiredNameError
)

type LockedError struct{ Path Path; By string }
type ProtectedError struct{ Path Path; Need Protection }
type PostCommitError struct{ Section Path; Err error }
type RetiredNameError struct{ Uses []RetiredUse } // every refused use, so one boot names them all
```

#### 1.3.4 Settings API semantics (routes owned by 05-http-api)

| Route | Permission | Semantics |
|---|---|---|
| `GET /api/core/v1/settings/schema` | any section read permission | JSON Schema 2020-12 document, one property per section the caller may read. Vendor keywords: `x-mediacore-reload` (`live`\|`restart`\|`boot`), `x-mediacore-protect`, `x-mediacore-secret`, `x-mediacore-fail-closed`; standard `writeOnly: true` on secrets. |
| `GET /api/core/v1/settings` | per section read | `{path: KeyState}` for readable keys. Secrets appear as `Secret` meta only. |
| `PATCH /api/core/v1/settings` | per section write + protection | Body `{"set": {path: value}, "unset": [path]}` → `Store.Apply`. 400 unknown key, 409 locked (`locked_by` in body), 403 forbidden/protected (`need` names the protection level), 422 invalid (field errors), 500 storage. 200 with `restart_pending` and `post_commit_error` when present. |

The routes are mounted in F4, the identity flip, because core routes authenticate natively only from there
(05-http-api §8.1); the SPA's settings page and the first-party scripts switch to them in that PR. From F3 to F4
ABO's gin `GET`/`PUT /api/v1/config` serve the settings over core config, write-only for secrets from F3 (C9;
§1.12.1 "Between the config flip and the identity flip"); F4 deletes them for every `k` and `s` key, and F7 deletes
what is left of them (the class-`i` fields).

Default section permissions: `core.*` → read and write `core:settings.manage`; `modules.<id>.*` → read and
write `<id>:settings.manage` (registered by the module in its permission list; [05-rbac](05-rbac.md)). A module
that declares a section must therefore declare `<id>:settings.manage` or pass `config.Permissions(read, write)`
naming permissions it does declare; otherwise the kernel fails boot on the undeclared permission (06 §6.6,
COR2-14). ABO's `settings.manage` permission becomes both through the identity migration's atom-to-scope table
(M3, [05-rbac](05-rbac.md) §6). A section permission never suffices for a key with `ProtectAdmin` or higher:
holding `core:settings.manage` cannot be turned into `core:admin` through configuration (§1.3.7).

#### 1.3.5 Precedence, locks and the boot record (normative)

Merge order, lowest to highest: **defaults → file → env → flags → DB overrides** (for `ReloadBoot` keys the
last layer is the **boot record** instead of DB overrides). One rule constrains the last layer: if any
operator layer (file, env, flag) supplies a key, that key is **locked**: its stored value is ignored at load
(listed in `AttachReport.Ignored`, kept, not deleted, and logged once per boot naming the key and the operator
source) and `Apply` refuses it with `*LockedError{By}`. Keys with `ProtectOperator` are never read from or written
to the DB layer or the boot record at all. Consequently the stored layers only ever override defaults, which is
what an operator expects: what they put in the file, environment or command line is what runs, and the UI says so.
This replaces HEAD's rule, under which the stored blob beat most env names (`internal/config/persistence.go:1131-1170`
over `cmd/root.go:625`); R2's release note says so.

**Fail-closed keys (normative).** A key declared `FailClosed` (§1.3.1) is the one exception to the lock.
Its effective value is the **more restrictive** of its operator value and its stored value, by the declared order;
with only one of them set, that one. When the stored value wins over a weaker operator value, `AttachReport.FailClosed`
names the key and the operator source, boot logs one warning, and health raises `config_fail_closed_override` (warn,
on `core.config`, 05-health) until one of the two changes. `Apply` on such a key while an operator value is set
accepts a value at least as restrictive as the operator value (it is then effective) and an unset (the operator
value is effective again), and refuses a weaker value with `*LockedError`, because it could never take effect.
The declared keys are `core.auth.mode` ([05-authn](05-authn.md)) and `core.http.basic_gate.enabled`
([05-http-api](05-http-api.md)). So a stale `ABO_CORE_AUTH_MODE=disabled` left in a unit, or a deploy file's
`ABO_CORE_HTTP_BASIC_GATE_ENABLED=false`, never opens a gate an admin closed in Settings, while an operator can
always close one (14 E5). With authentication off the auth-disabled principal holds no `core:admin`, so it cannot
store `core.auth.mode` at all (§1.3.7): auth is turned on by an operator value and a restart.
`TestFailClosed_MoreRestrictiveWins` covers both keys and both directions. The Basic gate's password is not
FailClosed: an operator value locks `core.http.basic_gate.password` like any key, so
`ABO_CORE_HTTP_BASIC_GATE_PASSWORD` wins over any stored password, an empty one included, which is core's form of
early fix 5's recovery from a lock-out (05-authn §11.1 item 5, 05-http-api §10).

**Empty env values.** An env variable set to the empty string is **unset**, as viper treats it at HEAD (ABO never
calls `AllowEmptyEnv`). An operator who means an empty value uses the file or `<NAME>_FILE`.

Within operator layers flags beat env beat file. Within the env layer, for one key, the canonical name beats an
ecosystem binding (`EnvBinding`, §1.3.6). If both are set with different values:

- key at `ProtectAdmin` or higher: **boot fails** with an error that names both variables (OPS2-12;
  `internal/config/config.go:2519-2544` records the hazard of a drop-in that overrides one name while the base unit
  sets the other);
- any other key: the canonical wins, with a warning and health token `config_binding_conflict` (§1.5).

**Boot record.** `ReloadBoot` keys are needed before storage opens (module enablement decides which module
sections are registered and which keyspaces are claimed, 06 §6.5.1). Their UI-made values are persisted in
`<state_dir>/config.boot.json` (0600, directory 0700, written atomically through the root that
`Options.BootRecordRoot` returns: temp file, fsync, rename), a JSON object
`{"format":"mediacore-boot/1","gen":N,"values":{path: value}}`.
`Boot` and `Load` read it; `Apply` writes it. Unknown paths in it (a module this binary lacks) are retained
and reported like unknown override rows. No other component writes the file. [05-backup](05-backup.md)
archives it with the config file. HEAD has no boot-record key, so M2 writes none.

#### 1.3.6 Key grammar, env names and retired names (normative)

**Canonical env name** = `<EnvPrefix>_` + upper(path) with `.` and `-` → `_`, with the leading `MODULES_` dropped
for module paths (unambiguous because `core` is a reserved module ID, 03 §3.6):
`core.log.level` → `ABO_CORE_LOG_LEVEL`; `modules.books.scan.on_startup` → `ABO_BOOKS_SCAN_ON_STARTUP`;
`integrations.metadata.google-books.api_key` → `ABO_INTEGRATIONS_METADATA_GOOGLE_BOOKS_API_KEY`.
Any key may instead be given as `<NAME>_FILE=<path>` (contents read once at `Load`, one trailing newline
trimmed) — the supported way to hand secrets in from systemd `LoadCredential=` or container secrets.
Object and array keys take JSON (`ABO_CORE_LOG_LEVELS='{"books":"debug"}'`). `Load` fails with
`ErrDuplicate` if two paths map to the same env name. Unknown `<EnvPrefix>_*` variables produce one warning each
(fail-open, ABO's rule that a stale key must not stop startup, `config.go:2587-2590`). **Env names are
case-sensitive**: only the exact name is read.

**Canonical file form.** The operator YAML file is a mapping whose nesting follows the path segments
(`core: {log: {level: debug}}`, `modules: {books: {scan: {on_startup: true}}}`); its top-level keys are `core`,
`modules` and `integrations`. A top-level key that is a retired name is handled below; any other unknown key warns.

**Decoding is strict**, for env, file and flags alike: JSON for objects and arrays, a Go duration, `true`/`false`
for bools, the key's own type otherwise. A value that does not decode fails boot, naming the variable or file key
(§1.5). There is no per-type legacy decoder: HEAD's spellings (`yes`, a bare number of nanoseconds, a
space-separated list) are not read (R2's release note says so).

No `MEDIACORE_` variable exists: the operator of an app should not need to know the library's name, and
per-app prefixes keep two apps' variables apart on one host or, at lowest priority, in one process.

**Ecosystem bindings.** A key may carry extra env names that other software already defines, and only those:
`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_METRIC_EXPORT_INTERVAL` (a bare
integer is milliseconds, the OTel convention, which the binding's decoder keeps) and
`OTEL_EXPORTER_OTLP_METRICS_INSECURE`, declared by [05-metrics](05-metrics.md) §10 with `EnvBinding`; and the app's
`OPENAI_API_KEY` and `OPENAI_BASE_URL` onto `integrations.ai.openai.{api_key, base_url}`, passed by ABO in
`Options.EnvBindings`. A binding is a second canonical name, not an alias of an older one; it never warns.

**Retired names (normative).** The app hands config the list of every env name and top-level file key it read
before core (`Options.Retired`; ABO generates it from its rename table, §1.12.3). A retired name is never read. When
one is set, config decides from the key it became, after `Boot` for core keys and after `Load` for module and
passthrough keys, both before storage opens (§1.4 steps 2–3):

| Retired name set for… | Effect |
|---|---|
| a key at `ProtectAdmin` or higher (`ProtectOwner` and `ProtectOperator` included), a secret key (`secrets.Ref`), a passthrough path the app marks `Refuse` (a secret or destination field of an instance), or a file key whose subtree holds one of these | **boot fails** with `*RetiredNameError` listing every such name with its canonical env name and file path, so one boot names them all; nothing has opened or been written |
| any other key, or a dropped key (`Canonical` empty) | the value is **ignored**; one warning per boot names the retired name and the canonical name (or the drop message); health raises `config_retired_name_ignored` (warn, 05-health) while one is set |

A retired file key whose subtree spans several keys (`itunes:` holding a library path and plain settings) is
judged by its most protected key. `RetiredSet()` lists every use, and `<app> config check` (an offline command that
opens no store and reads only the operator layers, an entry of `app.OfflineCommands` from F3, 06 §6.4.5) prints it with the canonical names, so an operator converts a unit or compose file before deploying.
**Storage additionally refuses to create a store** at the resolved path while a retired database-path name is set
([05-storage](05-storage.md)), so even a misclassified list never opens an empty store in place of the real one.

The refusal exists because silently ignoring a renamed database-path, sign-in, proxy or path name would point the
app at a new, empty database or open a gate (OPS-5; ABO recorded the same hazard, `config.go:2519-2544`), and an
ignored secret name makes its consumer refuse to start without saying why (`ABS_JWT_SECRET`, bound at
`internal/config/abs_config.go:45-47`; absauth refuses to boot without it, `internal/server/absauth/config.go:20-22`).
Every other name only warns, because a stale tuning value must not stop startup.

#### 1.3.7 Protection classes (normative)

The class rule replaces ABO's `configFieldRules` (`protected_fields.go:53`). It is applied by every spec that
declares keys; each key table in the 05 files carries a **Protection** column.

| ABO class today | Protection | Keys (core examples; owning spec tables each key) |
|---|---|---|
| `FieldSignIn` | `ProtectAdmin` | everything else that decides who signs in and how: `core.auth.*` sign-in keys (each provider's `client_secret`, the redirect base) ([05-authn](05-authn.md)); `core.auth.oidc.providers` itself, whose entries hold the client ids and default roles, is `ProtectOwner` (owner-trust-root row below); the Basic gate's `core.http.basic_gate.*` (row below); the books ABS token sign-in switch |
| — (new, SEC-6) | `ProtectAdmin` | `core.http.{trusted_proxies, client_ip_header, peer_allowlist, cors.allowed_origins, basic_gate.*}` ([05-http-api](05-http-api.md)); `core.updater.{mode, channel, restart_*}` ([05-updater](05-updater.md)); OTLP endpoints and `core.metrics.{path, listen}` ([05-metrics](05-metrics.md)); `core.integrations.egress.lan_allow` (SEC-15; see below) |
| `FieldExecutable` | `ProtectAdmin` | a program the server runs, or the managed-copy-vs-path switch |
| `FieldServerPath` | `ProtectAdmin` | a path the server reads, writes, scans or serves (`core.log.file.path`, library and tool paths) |
| owner trust root (`ownerTrustRootFields`, `internal/config/protected_fields.go:209-227`; `update_service.go:770-800`) | `ProtectOwner` | the successors of all five HEAD owner-only fields, plus the switch that would turn the owner gate off (SEC3-6): books' `modules.books.identity.{owner_email, cf_access_team_domain, cf_access_aud}` (the identity ABO's `internal/cfaccess` proves; core ships no owner verifier, 05-authn §4.8), `core.auth.mode` (HEAD `enable_auth`), `core.auth.oidc.providers` as a whole (HEAD `oauth_allowed_emails`; the list is one key, so every field of every entry is owner, not only its `allowed_emails` and `allowed_domains`: an admin who could repoint an entry's `issuer` or `client_id` could mint the owner's identity) and `core.auth.owner.required` ([05-authn](05-authn.md) §8). HEAD's threat holds unchanged: a second admin, or a stolen admin session, must not repoint Access or the admitted set and mint the owner's identity |
| `FieldDatabase` | `ProtectOperator` | `core.storage.path`, `core.paths.*` |

HEAD's file-only keys (`json:"-"`: the peer allow-list, the tunnel connector addresses, the login-saturation bucket,
`public_base_url`, the Cloudflare guest and invite keys, `bootstrap_key_ttl*`, the ABS JWT secret;
`internal/config/config.go:1636-1759`) become ordinary keys with their declared class in R2, with two exceptions
that stay operator-only (SEC-6): the ABS JWT signing secret `modules.books.abs.jwt_secret` and the Cloudflare
policy token `modules.books.cfguests.policy_api_token` are `ProtectOperator` secrets, class `o` in §1.12.3. They
are read only from the operator layers (`SourceEnv`, `SourceFile`), never stored: `Apply` refuses them, and
`secrets.Importer` and a module's `secrets.Scope.Put` refuse their names. This keeps HEAD's property that a database
copy alone never yields a usable phone-app credential (`internal/database/pebble_store_abssession.go:43-46`), and
no `books:settings.manage` holder can re-sign every ABS token or edit the Access policy behind owner assurance.
In particular the peer
allow-list `core.http.peer_allowlist` and the trusted proxies `core.http.trusted_proxies` (HEAD's
`ALLOWED_PEER_CIDRS` and `TUNNEL_CONNECTOR_ADDRS`, class `o` in §1.12.3) are **storable admin keys**: `ProtectAdmin`,
hot-reloadable and not `ProtectOperator` (05-http-api §10), so an admin may store them through Settings, which HEAD
did not allow. M2 imports nothing for them (HEAD never stored them); they are storable from F3 and editable through
the settings routes from F4. An operator value still locks them (§1.3.5), which is also the way back from a stored
allow-list that shuts the admin out, besides loopback, which stage 0 always admits (05-http-api §4.2). R2's release
note says so.

`core.integrations.egress.lan_allow` — `[]string` of CIDRs or host names, default `[]`, restart-free, owned and
enforced by `integrations` ([05-metadata](05-metadata.md) M1/M2): the LAN targets that **every instance without
`Local`** may reach; anything not on it gets public-only egress. It is a network trust list, so it is
`ProtectAdmin` as SEC-15 specifies, not `ProtectInteractive`.

`ProtectAdmin` is checked as `Actor.Can("core:admin") && Actor.Interactive`. `ProtectOwner` adds `Actor.Owner`
once an owner identity is set and an owner verifier is configured, whatever the caller's sign-in method, as
HEAD does "once `owner_email` is set" (`protected_fields.go:209-227`); before that it is checked as
`ProtectAdmin`. **With authentication off** the auth-disabled principal never holds `core:admin`
([05-rbac](05-rbac.md)), so it changes no key at `ProtectAdmin` or higher through `Apply` (SEC4-8), for every app,
ABO included: `core.auth.mode`, the Basic gate's keys, server paths, executables, perimeter, updater and
sign-in keys, and the protected fields of integration instances. An auth-off operator sets them through their
canonical operator names (`ABO_CORE_AUTH_MODE=required`, `ABO_CORE_HTTP_BASIC_GATE_*`) and restarts. This replaces
HEAD's `!prior.EnableAuth` exemption (`internal/config/update_service.go:781`), under which any request could change
a protected field while auth was off; R2's release note says "With authentication off, Settings can no longer turn
it on: set `ABO_CORE_AUTH_MODE=required` and restart." The owner-trust-root check is never skipped
(`update_service.go:772-779`).

**Integration instances** follow one rule, stated identically in 05-metadata M1.3 (SEC2-7): any
`core:integrations.manage` holder writes instance URL and credential fields; creating an instance, changing a
field that selects which secret is sent (an AI endpoint's `auth_ref`, a metadata source's credential keys), setting
an instance's `Local` flag (which admits every loopback, private and CGNAT address), or keeping it across a URL
change, needs the `ProtectAdmin` check, and so does `Registry.Test` with `Local=true`; every instance without
`Local` reaches public addresses plus `lan_allow` only. A secret field whose effective value is operator-sourced is
bound to its origin, as 05-metadata M1.4 step 2 states (SEC7-3). From R2 this holds for ABO's migrated instances
too: M6 writes them as native instances, so a non-admin `core:settings.manage` key can no longer repoint a URL and
have the stored credential follow it, which HEAD allowed (`internal/config/protected_fields.go:137-165`). R2's
release note: "API keys can no longer change integration URLs or credentials; use an interactive admin session."

`TestEveryURLPathOrSignInKeyClassified` (port of `protected_fields_test.go`) fails when a registered key whose
name or format looks like a URL (`format=uri`, `*_url`, `*endpoint*`), a path (`format=path`, `*_dir`, `*_path`,
`*_file`), an executable (`*_bin`, `*_command`, `*_exe`), a network trust list (`format=cidr`, `*proxies*`,
`*origins*`, `*allowlist*`, `*client_ip*`), key material or encryption policy (`*recipient*`, `*encryption*`,
`*keyring*`, or a non-secret field named `*_key` / `*_key_file`; SEC2-8: `core.backup.recipients` and
`core.backup.encryption` have none of the other shapes), a signing key or credential token (`*jwt*`, `*signing*`,
`*_secret`, `*_token`; SEC-6) or a sign-in setting (`core.auth.*`) has a Protection below
`ProtectAdmin` and no `ProtectionWaivers` entry with a reason. Its cases include the two operator-only secrets
above, which must read `ProtectOperator`. It runs over every row of ABO's rename table
too, so a books key HEAD classed `FieldSignIn`, `FieldExecutable` or `FieldServerPath` keeps that class.

### 1.4 Lifecycle

Inside `app.New` (03 §3.7). The phase names and the order of every boot step, config's included, are owned
by the one boot-phase table, [06](06-app-sdk.md) §6.5.1 (R2-8); this list gives config's and secrets' part of
it and the constraints it must keep. Normative constraint (C13): **nothing in steps 0–4 writes the store before
the pre-migration checkpoint**; step 5 runs after the checkpoint and **before** the migration list (step 5b),
because the cutover step M2 encrypts through it; step 6 runs after the migration list, before handles are issued to
services and before any listener opens.

0. Before 06 P0a the kernel runs `config.ResolveStateDir` once (§1.7, C15) and passes the result to P0a,
   `Options.BootRecordRoot` and every later consumer.
1. `config.NewStore(opts)`; the kernel registers **core** sections.
2. `Store.Boot` (06 P0; it reads no storage): takes the state directory from step 0 (a config file that sets
   `core.paths.state_dir` fails boot, §1.7), calls `Options.BootRecordRoot`, then reads the file (parse error =
   boot error), env, flags and the boot record for core sections and validates operator values. The returned
   `BootView` carries module enablement (`core.modules.<id>.enabled`, `ReloadBoot`; COR-14) and the
   operator-only paths `core.paths.state_dir` and `core.storage.path`, which 06's storage-open step (P2) reads
   from it (COR2-8). Step 0 already read `core.storage.path` from every operator layer, the file included
   (OPS5-13), so `Boot` only asserts that the effective path is the one step 0 used (a mismatch is a kernel
   bug and fails boot), and the keyring never lands beside a database that is not the one opened. A retired name
   of a protected or secret **core** key fails boot here (§1.3.6), as does a conflict on a protected key (§1.3.5).
3. The kernel registers the sections of every **enabled** module (from manifests), then `Store.Load` (06 P3)
   seals the registry and builds every section from defaults, operator layers and the boot record.
   Registration after `Load` → `ErrSealed`, boot fails. A section whose operator values fail decode or
   `Validate` fails boot (the operator must fix their input). A retired name of a protected or secret module or
   passthrough key fails boot here, still before storage opens.
4. The kernel builds logging and metrics from `core.log` / `core.metrics`. Storage opens at `core.storage.path`
   (`ProtectOperator`), checks disk headroom and takes its pre-migration checkpoint (05-storage §5S.3.12).
5. `secrets.Open` (§2.4; at the start of 06 P4b, 05-storage §5S.4 step 8, after the checkpoint and before the
   migration DAG) loads or creates the keyring and writes `c/secrets/meta` on a new store (its only store write);
   it binds no `Ref` yet (`Store.SetSecrets` is step 6's first call). There is no app-supplied config store to open: HEAD's settings
   load, encryption init and key guard, which ran in ABO's `serve` before the server (`cmd/root.go:280,345-369`),
   are replaced by `Load`, `Attach` and M2 (C17).
5b. The migration list runs (05-storage §5S.3.13; 06 §6.5.1). On the first R2 boot it includes M2 (§1.12.2): the
   kernel hands books' steps `module.Importers` (06 §6.4.2), whose `Config` is `Store.Importer()` and whose
   `Secrets` is `secrets.(*Store).Importer()` (§2.3).
6. At 06 P4d step 1 (05-storage §5S.4 step 11 (a)): `Store.SetSecrets` binds `Ref`s over the keyring step 5
   opened, then `Store.Attach` with core's native store and the kernel's committer (`core/internal/configstore`):
   `OverrideStore.Load` reads, nothing is written. Rows are decoded per section; if a section fails decode or
   `Validate` with its overrides but passes without them, **all of that section's overrides are quarantined**
   (kept, ignored, reported, health token `config_overrides_quarantined`) and boot continues. An `Apply` that sets a
   quarantined key, or a quarantined boot-record entry, to its stored bytes is never skipped as unchanged: the value is
   written and validated again, so the quarantine clears or `Apply` refuses with `ErrInvalid`. FailClosed keys whose
   stored value wins are reported in `AttachReport.FailClosed` with health token `config_fail_closed_override`
   (§1.3.5). Rationale: an operator cannot repair the DB without booting; ABO's equivalent path is fatal
   (`cmd/root.go:385-391`). Services built after this step (06 P5) see the DB layer; nothing built before it reads
   a DB-layer value.
7. The kernel hands `Store.PassthroughValues()` to the integrations registry, which overlays them in memory
   (05-metadata M1.4; C11). In 06 P5, once the effective config is known, the kernel calls
   `secrets.(*Store).RetireUnused` with the backup service's in-use check (§2.3; COR3-2).
8. Runtime: `Apply` = authorize (permission, then protection) → decode candidate per touched section →
   `Validate` → write the boot record if boot keys changed → `Committer.Commit` (one batch: `OverrideStore.Stage`,
   staged secrets, the audit record) → publish snapshots for live keys (atomic swap) → bump generation →
   `AfterCommit` hooks → wake `OnChange` subscribers. If `Commit` fails after the boot record was written, the
   previous boot record is restored before `Apply` returns (boot keys are restart-class, so nothing was
   published). A value equal to this binary's default is staged as an unset (C4).
9. `Close` cancels subscriber contexts and waits up to the drain timeout.

Restart-required and boot keys: `Apply` commits them and returns them in `RestartPending`; `Get` keeps the boot
value; `Pending` shows the stored value; `KeyState.Pending` drives a "restart required" badge in the shell.

**Factory reset.** A reset is 05-storage's staged, restart-bound swap (`KindReset`, §5S.3.15), staged by the offline
`storage reset` in release R1 and also by the core route from F4.
For config and secrets it means: the boot record `<state_dir>/config.boot.json` is set aside with the database
(so module enablement returns to its defaults); the fresh store has no `c/config/` row, so every key is at its
default or its operator value; the keyring is **kept** (14 Q-J's recommendation (a); the owner may override it), and
only the reset boot rewrites the keyring header's `install_id` to the fresh install's (`secrets.Options.ResetBoot`,
§2.4). No settings are written with defaults in the request, and there is no gap-fill file left to rewrite (M2b
removed HEAD's keys from it).

### 1.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Config file exists but unreadable or unparsable | fail-closed (boot error) | operator intent unknown; ABO ignores viper read errors (`cmd/root.go:627`) — behaviour change, release-noted |
| Unknown key in file / unknown prefixed env var | fail-open, warn once | stale keys must not stop startup (`config.go:2587-2590`) |
| Retired name set, key `ProtectAdmin` or higher, or a secret key | fail-closed (boot, before storage opens); `*RetiredNameError` names every such name and its canonical env name and file path | a silent fallback opens a wrong database, disables sign-in or starves a consumer of its credential (OPS-5, OPS2-7) |
| Retired name set, other key or dropped key | fail-open: ignored, one warning per boot naming the canonical name, health `config_retired_name_ignored` | harmless leftovers must not stop startup |
| Canonical name and ecosystem binding both set, different, key `ProtectAdmin` or higher | fail-closed (boot), message names both | a drop-in overriding one name would open an empty store (OPS2-12) |
| Canonical name and ecosystem binding both set, different, other key | fail-open, canonical wins, warn, health `config_binding_conflict` | an obvious mistake, but not worth an outage |
| Operator value fails strict decode or `Validate` | fail-closed, message names the variable or file key | operator must fix input; HEAD's lenient spellings are not read (§1.3.6) |
| FailClosed key: stored value more restrictive than the operator value | fail-open toward the stricter value: stored value effective, warn, health `config_fail_closed_override` | a stale operator value must not weaken a security switch (§1.3.5) |
| `<NAME>_FILE` unreadable | fail-closed | a missing credential file is a deployment error |
| Boot record unreadable (I/O) | fail-closed | never boot with a module silently re-enabled or disabled |
| Boot record unparsable | fail-open: file renamed `config.boot.json.bad`, ignored, health `config_boot_record_quarantined` | the operator can repair a file, but the app must boot to show why |
| Override rows unreadable (I/O) | fail-closed | running on defaults would let the next save overwrite stored config (`cmd/root.go:360-364`) |
| State directory: default not creatable, no explicit value, database path relative or empty | fail-closed (boot), message names `<APP>_CORE_PATHS_STATE_DIR` | HEAD's fallback needs an absolute database path (`state_dir.go:144-147`) |
| State directory: explicit value not usable | fail-closed (boot); never falls back | the operator named it (`state_dir.go:140-142`) |
| Override row fails decode/validate | fail-open per section: quarantine (§1.4 step 6) | DB cannot be fixed without booting |
| Override row for unknown key | fail-open: retained, ignored, reported | a module this binary lacks may own it |
| `Importer.Stage` refuses a value (unknown, operator-only, secret, invalid) | the cutover step quarantines the value and goes on; the key keeps its default (§1.12.2) | data is never dropped silently, and one bad field must not block the cutover |
| M2 finds encrypted rows and no HEAD key file, no override | fail-closed (boot, nothing written), message names the file, the directories searched and `ABO_CORE_SECRETS_ALLOW_KEY_REGEN` | HEAD's guard (`cmd/root.go:726-783`): a missing key is usually a wrong mount, not a lost key |
| M2 cannot decrypt a row (override set, or wrong or corrupt key or row) | fail-open per secret: ciphertext quarantined, unreadable marker written, "must be re-entered", health `secrets_undecryptable` | the app keeps working; the secret is never dropped silently (14 E4) |
| `Apply` unknown key (incl. a passthrough path) | refuse `ErrUnknownKey`, nothing persisted | typos must not silently no-op; instances are written through 05-metadata |
| `Apply` locked key | refuse `*LockedError` | the value would be discarded at next boot (`config.go:2224-2229` rationale) |
| `Apply` missing permission / protection | refuse `ErrForbidden` / `*ProtectedError` | fail-closed security |
| `Apply` validation failure | refuse `ErrInvalid` with `*FieldError`s | nothing partially applied |
| `Committer.Commit` fails (incl. a conflict after `storage.RetryOnConflict`'s attempts) | fail-closed: nothing published, generation unchanged, boot record restored | durable-before-visible |
| `AfterCommit` hook error | committed; `Result.PostCommit` set | hooks cannot veto; caller must know (ABO contract `update_service.go:30-48`) |
| `OnChange` fn panics | fail-open: recovered, logged, metric; others continue | one subscriber must not break config |
| `Ref.Resolve` on unset secret | `secrets.ErrNotSet` to the consumer | consumer decides (e.g. provider disabled) |
| Duplicate path / env-name collision / invalid path / bad `FailClosed` order | fail-closed at registration | programming error |
| `ReloadBoot` key in a module section read before that module is registered | fail-closed at registration | only core sections are visible to `Boot` |

### 1.6 Concurrency contract

- `Handle.Get`, `Generation`, `Lookup`, `Keys` are lock-free (one `atomic.Pointer` load per section; `Keys`
  iterates an immutable published index).
- `Apply` calls are serialized by core config's own store mutex, held from candidate build through publish; it
  replaces ABO's in-memory `mu` (the `AppConfig` lock) and HEAD's save mutex `configBlobSaveMu`
  (`persistence.go:1890-1897`), which has nothing left to guard once the blob is gone. The boot-record write and
  `Commit` happen under it; publish happens after `Commit` returns. The native store is the only writer of
  `c/config/` (the importer writes only inside the migration list, before any `Apply`), and `Stage` reads with
  `GetForUpdate`, so a conflicting commit fails with `storage.ErrConflict` and is retried.
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
outside the registering owner's namespace (05-tasks §5T.3.8). A HEAD interval or enable key is converted
onto `ScheduleConfig` once, by its rename-table row's `Convert` in M2 (`{"enabled":true,"every":<duration>}`; a
HEAD `0` that meant "off" becomes `{"enabled":false}`; §1.12.3).

### 1.7 Config keys (core-owned sections)

This file owns only `core.config.*`, `core.paths.*` and `core.secrets.*` (§2.7); other `core.*` sections are
owned and tabled by their specs ([05-logging](05-logging.md), [05-metrics](05-metrics.md),
[05-storage](05-storage.md), [05-http-api](05-http-api.md), [05-authn](05-authn.md), [05-backup](05-backup.md),
[05-updater](05-updater.md), [05-tasks](05-tasks.md), [05-ai](05-ai.md)); integration instances are not config
keys at all (C11).

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.paths.state_dir` | path | the app's compiled default, passed through `app.Options.StateDir` (06; ABO: `internal/config/state_dir.go:54`), with HEAD's fallback below | no — **pre-boot**: read from env and flags; a config file that sets it fails boot at P0, because 06 P0a (and, once built, the deferred P−1, A-6) reads it before any config file (06 §6.5.1) | operator |
| `core.config.file` | path | `""` (also `--config`, `<APP>_CONFIG`) | no | operator |
| `core.config.subscriber_drain_timeout` | duration | `5s` | no | none |

**Pre-boot resolution (C15, R3-4, OPS3-1)** — `ResolveStateDir`, run once by the kernel (§1.4 step 0):

1. an explicit env or flag value is used as given; if it cannot be created or used, boot fails (never a
   fallback, HEAD `state_dir.go:140-142`);
2. otherwise the compiled default, created `0700` if absent;
3. if the default cannot be created, the directory of `core.storage.path` as resolved from the pre-boot
   layers (compiled default, the operator file, env, flags; the file is itself a pre-boot input, `--config` /
   `<APP>_CONFIG`, and reading this one key from it does not conflict with refusing a file that sets
   `core.paths.state_dir`), when that path is absolute, with HEAD's warning naming both
   directories and the variable that chooses one deliberately (`state_dir.go:118-155`). Otherwise boot fails.
   The updater marker's `db_path` is not an input: the marker lives inside the directory being resolved, and
   its `db_path` is only `ReaderCheck`'s DB path (05-updater; OPS4-8).

**`core.storage.path` is a kernel pre-boot key (COR4-20).** Step 3 reads it before storage opens; 05-storage's
`storage` section declares it without changing its name or default. **One resolver (OPS4-8, OPS5-13).** HEAD's
`EnsureSecureStateDir` (`cmd/root.go:336`) resolves from `AppConfig.DatabasePath`, which at that point holds viper's
defaults, YAML file, env and flags (`internal/config/state_dir.go:143-153`), and `ABK_STATE_DIR`. From R1, when the
kernel opens the store, the kernel's `ResolveStateDir` is the only resolver, over the same inputs (in R1 ABO passes
the database path and its state-directory value as plain options; from R2 they are `core.storage.path` and
`core.paths.state_dir`), so the boot record, a restore intent, HEAD's `.encryption_key` and the keyring are always
looked for in one directory. Because step 3 also reads the operator file, a database path given only in the file
with an uncreatable default falls back to its directory exactly as at HEAD. `TestStateDir_OneResolverYAMLDBPath`
checks it, in the image as well as as a unit test.

On Docker and launchd installs step 3 is where `.encryption_key` lives today (the image runs as an
unprivileged user whose only persistent path is the data volume, `Dockerfile:93-96,104`; neither compose nor
the plist sets a state directory), so HEAD's key is found there and the keyring lands on the volume. The
shipped Dockerfile and plist set **no** state-directory variable: the fallback already yields the database's
directory, while an explicit value never falls back (step 1), so an image `ENV` would move the keyring of a
container whose `--db` is on another volume into an anonymous volume that a recreate discards, after which boot
fails with `ErrKeyMissing` (OPS4-2). If an explicit value is wanted for discoverability, only
`docker-compose.yml` sets it, next to the named volume and a `command:` that names the same directory. The image
job's second run checks it (09, R1; OPS5-7): it prepares the second named volume as a user must (the image
creates and chowns only `/data` and runs as uid 1000, `Dockerfile:93-96`, so a fresh volume is chowned to that
uid first), runs `serve --db` on it, and asserts that `.encryption_key` sits beside the database on that volume
in R1 and that, from R2, `secrets.keyring` sits there and a secret set through the settings route still decrypts
after the container is recreated.

There is no `core.paths.data_dir`: no component read it, and the database location is `core.storage.path`
alone (05-storage), so a second data-path key was one of three sources for one fact (COR2-8). The state
directory holds the boot record, the keyring and the log file. No key in this file changes persisted state shape;
such changes are release-bound migration steps, not keys. Keys that only tune
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
| `mediacore_config_retired_names_set` | gauge | — (warn-class retired names set at this boot, §1.3.6; refused ones never reach a running process) |
| `mediacore_config_fail_closed_overrides` | gauge | — (FailClosed keys whose stored value beats the operator value, §1.3.5) |

Names are shown under the default namespace; 05-metrics owns the naming rule.

### 1.9 Breaking config's upward imports

| Import today | What it is used for | Where it goes |
|---|---|---|
| `internal/database` (`SettingsStore`, `Setting`, `DecryptValue`, `MaskSecret`, `IsEchoedMask`, `LooksLikeMask`) | blob/row persistence, secret decrypt, masks (`persistence.go:883-1260`, `update_service.go:91-373`) | persistence → core's native `OverrideStore`; HEAD's blob codec and row cipher → a frozen copy inside books' cutover step M2 (§1.12.2), deleted in R3; secrets → `secrets`; masks → deleted at F3, when `/api/v1/config` becomes write-only for secrets (C9, §1.12.1) |
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
| `TestFailClosed_MoreRestrictiveWins` | for `core.auth.mode` and `core.http.basic_gate.enabled`: stored stricter + operator weaker → stored effective, `AttachReport.FailClosed`, one warning, health `config_fail_closed_override`; operator stricter → operator effective and locked; `Apply` of a value at least as strict as the operator's succeeds and is effective, a weaker one → `*LockedError`; `FailClosed` on a string key or with a missing value fails `Register` (14 E5) |
| `TestEnv_EmptyValueIsUnset` | `ABO_X=""` supplies nothing: no lock, no value (as viper treats it at HEAD) |
| `TestEnv_StrictDecode` | `ABO_CORE_AUTH_MODE=yes`, a bare integer for a duration, a space-separated list for an array → boot error naming the variable; the same through the file key |
| `TestEnvBinding_EcosystemNames` | `OTEL_EXPORTER_OTLP_ENDPOINT` sets `core.tracing.otlp.endpoint`; with the canonical name also set and different: canonical wins on a `ProtectNone` key with `config_binding_conflict`, boot fails on a `ProtectAdmin` key naming both; a binding equal to a canonical name fails `Register` |
| `TestRetiredName_ProtectedOrSecretRefusesBoot` | a fixture retired list: a set retired env name or file key of a `ProtectAdmin`, `ProtectOwner`, `ProtectOperator` or secret key → `*RetiredNameError` listing every such use with its canonical env name and file path, before storage opens (a recording fake storage sees no open); a file key whose subtree holds one protected key refuses too |
| `TestRetiredName_OtherWarns` | a set retired name of a `ProtectNone` key or of a dropped key → ignored, one warning naming the canonical name or the drop message, `RetiredSet()` lists it, health `config_retired_name_ignored`, gauge 1 |
| `TestOverride_IgnoredWhenOperatorLocked` | DB row kept, not applied, reported in `AttachReport.Ignored` and logged once |
| `TestApply_RefusesLocked` / `_Protected` / `_Forbidden` / `_UnknownKey` / `_Invalid` | error type, nothing persisted, generation unchanged |
| `TestApply_SettingsManageCannotChangeAdminKeys` | actor with `core:settings.manage`, interactive, no `core:admin` → `*ProtectedError{Need: ProtectAdmin}` on a sign-in, proxy and updater key |
| `TestAuthOff_ProtectedKeysRefused` | auth off: the auth-disabled principal's `Apply` of `core.auth.mode=required`, of `core.http.basic_gate.enabled` and of an integration URL → 403, nothing stored, on hello and on an ABO fixture alike (SEC4-8) |
| `TestEveryURLPathOrSignInKeyClassified` | §1.3.7 shape rule over every registered core and fixture key, and over every row of ABO's rename table |
| `TestApply_RestartKeyNotPublished` | `Get` unchanged, `Pending` changed, `RestartPending` returned |
| `TestApply_CommitFailureNothingPublished` | fake `Committer` error; boot record restored |
| `TestApply_SecretAndOverrideOneBatch` | `Commit.Secrets` and `Commit.Set` arrive together, with the audit record |
| `TestOwnerTrustRootKeysAreOwnerProtected` | port of HEAD's owner-field test: the §1.3.7 owner row's keys are `ProtectOwner`; with an owner set, an interactive `core:admin` actor without owner proof gets `*ProtectedError{Need: ProtectOwner}`; with no owner set the same actor succeeds (SEC3-6) |
| `TestBoot_ModuleEnablementFromBootRecord` | a UI-written disable is honoured by `Boot` at the next start; operator env beats it |
| `TestBootRecord_AtomicAndQuarantine` | crash between temp write and rename keeps the old file; garbage → `.bad`, boot continues |
| `TestAttach_QuarantineInvalidSection` | boot continues, section on operator view, health token set |
| `TestOnChange_CoalescesUnderSynctest` | `testing/synctest`: 100 applies, slow subscriber sees ≤2 calls, last = latest |
| `TestAfterCommit_ErrorReportedNotRolledBack` | `Result.PostCommit` non-nil, value committed |
| `TestEnvName_Derivation` / `TestEnvName_CollisionRejected` | naming rule (`.` and `-` → `_`, `MODULES_` dropped); `ErrDuplicate` |
| `TestEnvFileSuffix` | `_FILE` read once, newline trimmed, unreadable = boot error |
| `TestEnvBindingConflictOnProtectedKeyRefusesBoot` | `ABO_CORE_TRACING_OTLP_ENDPOINT` and `OTEL_EXPORTER_OTLP_ENDPOINT` set to different values → boot error naming both (the key is `ProtectAdmin`); the same on a `ProtectNone` key with a binding → canonical wins, `config_binding_conflict` |
| `TestBoot_ResolvesPathsWithoutStorage` | `BootView` yields `core.paths.state_dir` from env and flags (a file value fails boot) and `core.storage.path` from file, env and flags; `BootRecordRoot` receives the resolved state dir; no storage handle exists |
| `TestStateDirFallbackMatchesHEAD` | table over (explicit set or not, default creatable or not, DB path absolute / relative / empty): explicit never falls back; uncreatable default → `Dir(core.storage.path)` with the warning; relative or empty DB path → boot error; a fallback whose DB path the config file later moves → boot error naming both remedies; P0a and `Boot` receive the same directory (OPS3-1) |
| `TestStateDir_OneResolverYAMLDBPath` | a database path given only in the YAML file plus an uncreatable default: the kernel resolves the boot record, a restore intent, HEAD's key file and the keyring to that path's directory, as HEAD does; the updater marker is not an input (OPS4-8, OPS5-13) |
| `TestNoStoreWriteBeforeCheckpoint` | a recording fake store sees no write before the checkpoint marker, none at all from `Attach`, and from `secrets.Open` only `c/secrets/meta` on a new store; `secrets.Open` runs before the first migration step (C13) |
| `TestNativeOverrideStore_RoundTrip` | the native store over `c/config/`: set, unset and reload of scalar, map and boot-adjacent keys; one row per explicitly set key; passes `configtest.OverrideStoreSuite` |
| `configtest.OverrideStoreSuite` (exported contract) | `Load` is read-only; `Stage` changes exactly the staged keys (others byte-equal after a JSON round trip); an unset removes the row; two concurrent `Stage`s on one row both survive through the `Committer`'s retry; an unparsable row quarantines its section and is never overwritten |
| `TestImporter_StagesOnlyValidNonDefault` | `Importer.Stage` writes a `c/config/` row into the caller's batch for a valid non-default value, nothing for a default value, `ErrUnknownKey` for an unregistered or passthrough path, `ErrInvalid` for a secret or `ProtectOperator` key, `*FieldError` for a bad value; nothing commits until the caller commits |
| `TestSectionPath_NestedNoOverlap` | `core.log` + `core.log.journal` + `core.log.schedules` register; a parent field named `journal` → `ErrDuplicate` |
| `TestConfigFileHasSecrets_Redacted` | a file with a secret key or a passthrough value → `true`; `RedactedConfigFile` contains neither |
| `TestPassthrough_NotOwnedNotWritable` | passthrough values returned raw with origin, an ecosystem binding (`OPENAI_API_KEY`) included; `Apply` on them → `ErrUnknownKey` |
| `TestSchema_Golden` | schema of a fixture section equals `testdata/schema.golden.json` (guards the jsonschema-go pin) |
| `TestSchema_SameOverridesAsOpenAPI` | `config.Duration`, `secrets.Ref` render identically in the settings schema and an `httpapi` fixture built from `jsonschemax.Base().With(config.Overrides())` without a config store |
| `TestSchema_OverrideSetsDisjoint` | `Base().With(config.Overrides()).With(<httpapi's set>)` does not panic; `time.Time` is in `Base()` only (SIMP3-1) |
| `TestSchema_SecretIsWriteOnly` / `TestKeys_NeverContainSecretValues` | secret never in `Effective` or schema defaults |
| `TestSnapshot_ImmutableUnderRace` | `-race`; `coretest` fingerprint check |
| `TestRegistry_SealedAfterLoad` | `ErrSealed` |
| `TestConfigImportsOnlyAllowed` | `go list -deps` allow-list for `core/config`: stdlib, yaml, jsonschema-go, `internal/jsonschemax`, `secrets`, `storage`, `metrics` (and their own allowed imports); `jsonschemax` imports only jsonschema-go |
| `FuzzEnvDecode`, `FuzzPatchDecode` | no panic, typed errors only |

ABO's cutover and rename-table tests are in §1.12.2–§1.12.4.

### 1.12 Migration from ABO

#### 1.12.1 Code: façade required, call-site migration optional

R5 binds core only (SIMP-3). ABO's globals survive as books-module façades over core handles; removing them is
an optional ratchet, not an extraction step.

1. **Stage 0 (required; R1, in ABO, no core needed):** convert the 8 bypassing writes (§1.2) to `Mutate`, and add
   `TestNoDirectAppConfigWrites` (AST: no assignment to `config.AppConfig` outside `internal/config`). Without
   this, the façade's projection would silently drop those writes.
2. **Stage 1 (required; the R2 config flip, F3) — façade.** ABO's `internal/config` keeps its exported surface and
   becomes a façade. The books module constructs one **`Facade` value** per App (the type lives in ABO's
   `internal/config` itself, so it adds no package path; MOD6-11) from the handles it registered and the core
   handles it reads (`core.storage`, `core.auth`, …), and installs it as the target of ABO's package-level
   `AppConfig`, `Snapshot`, `Mutate`, `Generation`, `SettingLocks`, `EnvLockedSettings`. `AppConfig` is a
   **projection** rebuilt under the façade's lock on every core generation change (one `OnChange` per section),
   mapped field by field through the rename table (§1.12.3); secret fields in the projection are resolved from
   `secrets` in memory only. `Mutate(fn)` diffs the before/after `Config` and turns it into one `Store.Apply`
   through the same table. All 215 files and 341 test files compile unchanged; tests use a `coretest` store behind
   the façade. viper, `configFromViper`, `LoadConfigFromDatabase`, `SaveConfigToDatabase`, `SaveConfigToFile`,
   `UpdateService` and the masks leave ABO's boot and write paths in the same flip: core `Load`, `Attach` and
   `Apply` replace them, and M2 replaces their stored data (§1.12.2). The façade's done-check is the cutover oracle
   `TestConfigCutover_EffectiveConfigUnchanged` (§1.12.2). Multi-app is not ruled out: the façade is a per-App
   value; only ABO's delegation from package-level names is process-wide.
3. **Optional ratchet — accessor.** `TestAppConfigRatchet` is added with stage 1: a per-package ceiling of
   `config.AppConfig`/`Snapshot`/`Mutate` uses that may only go down, like
   `internal/logger/slog_guard_ratchet_test.go`. Packages move to an injected `booksettings.Accessor` (ABO repo,
   `internal/books/settings`) when they are touched anyway; largest first if anyone works it deliberately
   (`internal/server` 71 files, `internal/plugins` 33, `internal/metafetch` 15, `internal/maintenance` 11,
   `internal/scanner` 9, `cmd` 9, `internal/audiobooks` 8, then 28 packages with ≤6 files). When the ratchet
   reaches zero, the façade, `AppConfig`, `Mutate` and `Snapshot` are deleted. This work is listed in the
   11 §11.5 checklist ("books: remove `config.AppConfig` façade and package-level loggers", size L) because it is
   a prerequisite only for sharing a binary with other modules. **Trade:** until then ABO tests mutate a façade
   global and cannot use `t.Parallel()` on config.

**Every core section exists from F2 (normative, IMPL-1).** M2 stages HEAD's values under core keys whose owners are
wired by later flips (`core.backup.dir`, `core.updater.*`, `core.tasks.history_retention`,
`core.tasks.windows.nightly.*`, `core.auth.mode`), and `Importer.Stage` refuses an unregistered path. So F2, the
foundation PRs that open R2 dark before F3 (03 §3.8), puts on the kernel's core-section list **every** core section
that a rename-table row targets (http, auth, tasks, backup, updater, ai, integrations, library, notify, health,
metrics and the rest), each with the key table its owner spec states, whether or not that
subsystem is wired yet. Registering a section changes no behaviour: a key is read only by its owner, and an owner
that is still HEAD's code reads it through the façade (item 2). `TestRenameTable_EveryKeyRegistered` (§1.12.3) runs
from F3 against the build under test, so F3's M2 never quarantines a value for want of a section.

**Release R1: how ABO's stored listener and perimeter values reach `app.New` (normative).** In release R1 the kernel
owns the one Pebble open (06 §6.5.1 P2) and core config has no DB layer (06 P3), so ABO's `main` cannot read HEAD's
settings blob before `app.New`, and core config cannot supply a value the blob holds. HEAD's listener values are not
stored: `--host`, `--port`, `--tls-cert` and `--tls-key` are flags (`cmd/root.go:424-445,526-532`), and the peer
allow-list and the trusted proxies are env or file only (`ALLOWED_PEER_CIDRS`, `TUNNEL_CONNECTOR_ADDRS`, `json:"-"`,
`internal/config/config.go:1701-1711`). The DB-stored perimeter values are the rate limit (`enable_rate_limit`,
`api_rate_limit_per_minute`; stage 10) and the body limits (`json_body_limit_mb`, `upload_body_limit_mb`; stage 11),
which HEAD's blob beats env for. They reach the kernel through **one R1-only `app.Options` field** (06 owns the
field; name coined here), a func of the kind `UIOptions` already uses for values that exist only after boot (06
§6.4.5):

```go
// R1Values: release R1 only. F3's PR deletes the field and the type; an archtest fails if either is still
// present after that PR, as for LegacyAuthGinUntil. nil = every key at core's default (hello).
R1Values func() app.R1Values

type R1Values struct {
	Listen                      string        // HEAD's --host and --port → core.http.listen
	TLSCertFile, TLSKeyFile     string        // HEAD's --tls-cert, --tls-key
	PeerAllowlist               []string      // ALLOWED_PEER_CIDRS → core.http.peer_allowlist
	TrustedProxies              []string      // TUNNEL_CONNECTOR_ADDRS → core.http.trusted_proxies
	RateLimitEnabled            bool          // enable_rate_limit → core.http.rate_limit.enabled
	RateLimitPerMinute          int           // api_rate_limit_per_minute → core.http.rate_limit.per_minute
	JSONMaxBytes, UploadMaxBytes int64        // json_body_limit_mb, upload_body_limit_mb (MiB → bytes)
	ShutdownBudget              time.Duration // ABO_CORE_SHUTDOWN_BUDGET; 0 = core's default (05-health §5H.4)
}
```

The kernel calls it **once per boot**, at 06 §6.5.2 start step (7), after the module start hooks of step (2) and
before the first listener opens. Books' start hook has by then run HEAD's settings load over the kernel's store
handle (06 §6.5.1 "Releases", R1), which fails boot where HEAD's step is Fatal, so the func never sees defaults in
place of a stored value; ABO's func reads `config.AppConfig`, i.e. HEAD's own effective values under HEAD's own
precedence (a UI-saved blob value beats most env names), plus the flags. Stages 0, 10 and 11, built at P8, read the
returned values through one pointer the kernel sets before step (7); nothing is served before it. Release R1 therefore
changes no effective listener or perimeter value (`TestPerimeterHonoursBlobOnlyValues`, 05-http-api §12), and in R1
the kernel reads these keys from nowhere else, so no canonical `ABO_CORE_HTTP_*` name is consulted beside HEAD's. An
edit through `PUT /api/v1/config` takes effect at the next restart, an R1 release-note line (05-http-api §8.1). The
Basic gate is not among them: it stays in gin, reading HEAD's settings itself, until F3. From F3 these keys are core
keys read through core config, hot-reloadable as 05-http-api §10 declares, their stored values moved by M2 and their
operator values under canonical names (§1.12.4); `core.shutdown.budget` is read by core config from F3 too.

**Between the config flip and the identity flip: `/api/v1/config` (normative).** From F3 core config holds every
`k` and `s` key, but the native settings routes mount only at F4 (§1.3.4), because before F4 no core route
authenticates natively and the gin auth chain still decides every request (05-http-api §8.1). So from F3 to F4 the
SPA's settings page keeps calling ABO's gin `GET`/`PUT /api/v1/config`, behind the gin chain with HEAD's permission
(`settings.manage`), and F3's PR rewrites the two handlers onto core config, deleting `UpdateService`:

- **Read.** `GET` renders HEAD's flat response keys from the façade's projection (item 2 above), which maps every
  rename-table row field by field, with each key's lock from `KeyState.LockedBy`. A secret key is write-only from
  F3 (C9): the response carries its `secrets.Meta` (`set`, `source`, `updated_at`, `fingerprint`), never a value or a
  mask, and F3's PR switches the settings page to that form and deletes the masks and the echo detection.
- **Write.** `PUT` turns the body into one `Store.Apply` through the rename table, so a key's protection, lock,
  validation and audit are core's from F3. The handler fills `config.Actor` from the gin context: `Subject` is
  HEAD's user ID, `Interactive` is `auth.Method.MayChangeCredentials()`, `Owner` is HEAD's owner proof
  (`internal/cfaccess`), and `Can(perm)` asks whether the principal's HEAD atoms map to `perm` under books' atom
  table (05-rbac §6.1), the table M3 applies at F4, so F4 changes nobody's reach over a key. With auth off the
  auth-disabled principal holds no `core:admin` (§1.3.7), so HEAD's `!prior.EnableAuth` exemption ends at F3; R2's
  release note already says so.
- **Class-`i` fields.** M2 leaves them in the blob for M6 (§1.12.2), and HEAD's integration code reads them through
  the façade until F7. Until F7 the handlers read and write them in that remaining blob row with books' frozen codec
  (the frozen codec), and a write needs `ProtectAdmin` when §1.3.7's shape rule classes the row's canonical path (a URL,
  path or credential field), else the section permission. F4 deletes the handlers for every `k` and `s` key, as the
  native routes replace them; for the class-`i` fields they stay until F7, whose instance routes (05-metadata M1)
  replace them, and F7's PR deletes them with M6.

#### 1.12.2 The config and secrets cutover (R2, migration step M2)

ABO's stored settings and secrets move into core **once, one-way**, at the first R2 boot. This section is the one
statement of that step: PC-21-03 builds it, the R2 config flip (PC-09-06) ships it, and 09's R2 migration table
lists it as M2. The step is **books code**, because core holds no ABO key name or row format (03 R1); it reads
HEAD's rows itself and writes only through core's importers (`config.Importer`, §1.3.3, and
`secrets.Importer`, §2.3). It follows 05-storage's contract for the R2 migration list (§5S.3.13): BootBlocking,
behind the disk headroom check and the pre-migration checkpoint, resumable, covered by the crash matrix, and any
input it cannot decode is quarantined and reported, never dropped. It runs after books' HEAD-format steps M1 and the level step M7 and
before the identity step M3 (execution order M1, M7, M2–M6, M8): M7 commits before M2's first chunk, so every older
build already refuses the store before any HEAD row is deleted (05-storage §5S.3.13).

**Inputs (all HEAD's).**

| Input | Where | Read as |
|---|---|---|
| the settings blob | `setting:config_blob` | HEAD's codec: `LoadConfigFromDatabase`'s decode with the ten `migrate*Blob` read transforms, and the three marker-guarded one-shots applied where their markers are absent (`internal/config/persistence.go:359-824,1088-1129`); env and file are not inputs |
| the four encrypted rows | `setting:openai_api_key`, `setting:google_books_api_key`, `setting:hardcover_api_token`, `setting:basic_auth_password` (`persistence.go:1955-1958`) | HEAD's cipher (`internal/database/settings.go:189-235`) with HEAD's key |
| HEAD's key | `.encryption_key` in the state directory (§1.7, C15's fallback), else HEAD's fallback `<root_dir>/.appdata/` (`settings.go:110-140`) | 32 raw bytes |
| the gap-fill file | `<db parent>/config.yaml` (`persistence.go:29-38`) | HEAD's gap fill: a file value applies only where the stored value is empty, for the twelve settings and three secrets HEAD's `SaveConfigToFile` manages (`persistence.go:42-104,150-178`) |

HEAD's `Config` struct, codec and cipher are **frozen** into the step's package (files of `internal/books/cutover`,
not a sub-package, so 03 §3.5.1's five new paths hold; 04 §8.1), so later changes to ABO's façade cannot change what the step reads. R3 deletes it
with the other HEAD-format readers.

**M2a — one batch.** From HEAD's effective stored value of each rename-table row (§1.12.3: blob value, gap-filled):

| Row class | Effect |
|---|---|
| `k` key | the value through the row's `Convert`, staged with `Importer.Stage` under the canonical path; a value equal to the R2 default stages nothing, so the key follows later defaults (C4); a `ProtectOperator` key (`core.storage.path`) stages nothing, because the operator layers already chose it |
| `s` secret | a non-empty value, through `secrets.Importer` under the canonical name, encrypted with the keyring's active data key (key ID, AAD); an empty value (an empty encrypted `basic_auth_password` row included) leaves the secret unset |
| `i` integrations or library input | left in the blob for the integrations step M6 ([05-metadata](05-metadata.md) M1.10, [05-library](05-library.md) §5L.10), which converts it and deletes the blob row |
| `o` operator-only at HEAD | nothing: HEAD never stored it |
| `x` dropped | nothing; one log line names a field that holds a non-default value |

In the **same batch**: every blob field M2 consumed is removed from the blob, which M2 rewrites in place, so the
blob keeps only class-`i` fields, and the secrets of class-`i` families (the Deluge passwords, the metadata-source
credentials, the webhook secret) are moved out of them, sealed under their canonical names, which M6 resolves by
name; the four encrypted rows are deleted. `setting:config_blob` therefore stays an **active** books claim through
M2 (books may read and rewrite it) and is retired only by M6, which reads the residual blob, converts it and deletes
the row (05-storage §5S.9.1 row 7; DATA-1). M2's `Source` is the four encrypted rows, and M2's `Verify` checks that
those rows and every field M2 moved are absent from the blob, not that the blob's range is empty. Also in that
batch, a blob field that no row names (a key HEAD never had or already removed, `internal/config/removed_keys.go`) and a value that `Importer.Stage` refuses are
moved into a quarantine record (05-storage's quarantine, reported in health), the key keeping its default. M2's
sources are all `Sensitive` (the settings blob and the secret rows), so every M2 quarantine record is **sealed with
the keyring**, never kept raw: its bytes are an encrypted secret record and the quarantine record holds none
(05-storage §5S.3.13 "Quarantine"). No plaintext credential lands in quarantine: an undecryptable secret's HEAD
ciphertext is sealed the same way, and a
value that may hold a credential but has no key (userinfo in a stored URL, a retired plugin's free-form settings) is
kept as an encrypted `c/secrets/` record with no key and reported with the quarantine records (§2.2). One batch
means a crash leaves either HEAD's rows or the finished result.

**M2b — the gap-fill file.** M2b copies nothing: the pre-migration checkpoint already holds the file, one of the
state files it copies and hashes before any step runs (05-storage §5S.3.12; §2.4's state-file table), so the
rollback restore puts it back (DATA-13). After M2a committed, an idempotent step
removes the fifteen managed keys from the file (HEAD's merge: read, drop the keys, temp file, fsync, rename, mode
0600) and keeps every other key, because the file may also be the operator's `--config` (`persistence.go:110-112`);
a file left empty is deleted. M2b reruns at every boot until the file holds none of the fifteen keys. Core never
reads or writes the file otherwise, and core writes no gap-fill file.

**HEAD's key.** M2 is the only reader of `.encryption_key`. It keeps HEAD's guard: when any of the four rows is
non-empty and no key file is found, the boot fails **before M2a**, with nothing written, naming the file, the
directories searched and the override `ABO_CORE_SECRETS_ALLOW_KEY_REGEN` (§2.7), exactly where HEAD's
`guardAgainstKeyRegeneration` refuses (`cmd/root.go:726-783`), because a missing key is usually a wrong mount. With
the override, and for any row the key does not decrypt (a regenerated key, a corrupt row), the ciphertext is
quarantined and M2 writes an *unreadable* marker under the secret's name (§2.3), so `Meta.Err` is
`ErrKeyUnavailable` or `ErrCorrupt`, Settings shows "must be re-entered" and health raises `secrets_undecryptable`
until the secret is set again (14 E4). M2 leaves the key file untouched: the rollback restore reads it. R3 deletes it,
at the path §2.4's state-file table resolves, through books' crash-labelled start hook `legacy-file-cleanup`
(03 §3.8, 09; 06 §6.5.1's writer table labels it), which also deletes `library.bleve`. While
`secrets_keyring_not_exported` is raised the hook still deletes `library.bleve` and skips only `.encryption_key`
(05-storage §5S.3.8 owns this behaviour), and retries the key at the next boot; neither deletion is ever an untracked write.

**Operator values at the cutover.** M2 migrates stored values only. A retired HEAD name of a protected or secret key
has already refused the boot at step 2 or 3 of §1.4, before storage opened, so M2 never runs under a unit that
still sets `DATABASE_PATH` or `ENABLE_AUTH`. Once the operator has renamed them, canonical values lock as C3 says,
which changes one HEAD behaviour: a variable now beats a setting saved in the UI (the next boot logs each such key,
§1.3.5). For the sign-in switch and the Basic gate the fail-closed rule keeps the stricter value (§1.3.5), and
compose passes the Basic variables only when they are set (§1.12.4), so no stale value opens what a stored setting
closed.

**Defaults.** HEAD's blob holds every field (whole-config save, `persistence.go:1932-1977`). A value equal to R2's
default is not stored, so such keys follow later defaults; a key whose HEAD default differs from R2's keeps HEAD's
value, because that value is stored (for example `core.backup.compression` stays `gzip`, 05-backup).

**Rollback.** Rollback is the offline restore of the pre-migration checkpoint, or of the off-host archive taken
before the deploy together with `.encryption_key` and the gap-fill file, with **the pre-freeze release**'s binary: the
last production release before F3 merged, which is also `.prev` after R2 deploys (05-storage, 09 §9.2.1;
CONFORM-1). The keyring export is taken **after** R2's first boot, because the keyring exists only from then
(`secrets.Open`, §2.4); a rollback to the pre-freeze release does not need it, since that release reads HEAD's rows
with `.encryption_key`. It restores the blob, the four rows and the gap-fill file, and loses every setting changed
since the upgrade. The pre-freeze release, or any older binary, refuses a store M7 has raised.

**A pre-R2 archive migrated later (DATA-15).** M2 gap-fills from the `config.yaml` beside the database, which M2b
has already stripped on a host that once ran R2. So staging or offline restore of a `legacy-abo/0` archive
(05-backup B7) takes `--gap-fill <file>`, the gap-fill file kept off-host with the archive, and installs it at
`<db parent>/config.yaml` (mode 0600) before the migrating boot; without the flag the restore warns that a field
HEAD filled from that file migrates as empty, names the fifteen managed keys, and goes on. The archive's off-host
`.encryption_key` is installed the same way as for any pre-R2 archive (§2.4 "Host-loss restore").
`TestLegacyArchiveRestoreThenMigrate` (05-backup) carries a gap-filled field and asserts its value after M2, and the
warning without the flag.

**Oracle and tests (PC-09-02, 14 E1, 14 E18).** The fixture corpus has two committed stages (09 §9.3 item 1;
IMPL-8). **Stage 1** is written by the **R0 binary itself** (a script drives R0's own settings API and loader). It
covers a default install; every key set to a non-default; gap fill for empty fields; the four encrypted rows;
plaintext secrets of every class; an empty Basic password row; a missing key file with and without the override;
and a production-shaped store sized from the P1 counts. **Stage 2** is a separate, labelled, hand-built overlay
for the cases R0's API cannot write, applied to a copy of the stage-1 store by a program built at the R0 tag
against HEAD's own `internal/database`. Its config cases are exactly: a corrupt encrypted secret row (ciphertext the
key does not decrypt); a blob field no row names; and a blob value that R0's own validation refuses, which
`Importer.Stage` must refuse too. The overlay's identity cases, an unconvertible user, key or identity row and an
unfinished account-merge journal, belong to M3 and are listed with the cutover preflight that reports them (09
§9.2.3). Each overlay
case carries its label in the corpus manifest, and the expected answers are still those of the R0 binary booted on
the result.

| Test | Asserts |
|---|---|
| `TestConfigCutover_EffectiveConfigUnchanged` | for every rename-table row, R2's effective value with empty operator layers equals the row's `Convert` of R0's effective stored value (R0's own `GET /api/v1/config` of the same store, env cleared); with canonical operator values set, they win, except that a FailClosed key keeps the stricter value |
| `TestConfigCutover_SecretsReadable` | every secret the fixture set reads back through `secrets.(*Store).Get` under its canonical name with `SourceStored` and the active key ID; no `c/config/` row holds a secret |
| `TestConfigCutover_RowsGone` | after M2a the four rows are gone and the blob row, still an active books claim, holds only class-`i` fields, with no moved field and no class-`i` secret left in it; after M6 no `setting:config_blob` remains; every key range outside the listed keys is byte-identical before and after (14 E3) |
| `TestConfigCutover_KeyGuard` | rows present and no key file: boot fails naming the file and the override, nothing written; with the override the rows are quarantined and read "must be re-entered" |
| `TestConfigCutover_QuarantineNotDrop` | the unknown blob field and an invalid value are quarantined and reported; every M2 quarantine record is sealed with the keyring, and no quarantine record holds a plaintext or raw value |
| `TestConfigCutover_GapFillStripped` | after M2b the file holds no managed key and keeps every other key; the checkpoint's copy holds the original; a crash between M2a and M2b strips it at the next boot |
| `TestConfigCutover_Idempotent` | a second boot over a finished store runs nothing; the crash matrix over M2a and M2b leaves HEAD's rows or the finished result |
| `TestConfigCutover_NoPlaintextSecretOnDisk` | a canary secret planted in every HEAD form before the upgrade appears in no file under the live DB directory, the state directory or the gap-fill file after M2, M6 and the forced compaction M8; the retained checkpoint is the one exception, until the sweeper deletes it 7 days after it stops being needed (05-storage §5S.3.12) or it is pruned (D12 (d)) |
| `TestConfigCutover_RestoreDrill` | restoring the checkpoint offline and booting the pre-freeze release's binary reads the original blob, rows and gap-fill file, each back at the path the checkpoint record names |

**Release note (R2, config and secrets).** "Settings and secrets move into a new store at the first start of this
release, which takes longer. Old environment-variable and configuration-file names are no longer read: a start
that finds an old name for the database path, a sign-in, proxy, path or updater setting, or a secret stops and
names the new one; other old names are ignored with a warning, and `<app> config check` lists them all. A setting
given in the environment or the configuration file now overrides the value saved in Settings, except that the
stricter value of the sign-in switch and of the Basic gate always wins. With authentication off, Settings can no
longer turn it on: set `ABO_CORE_AUTH_MODE=required` and restart. Secrets are now encrypted on disk, and the
`config.yaml` beside the database no longer holds copies of the OpenAI, Google Books and Hardcover keys. After the
first start, export the keyring (`<app> secrets export-keyring`) and keep it with your backups. Backups made before this release still hold
those secrets in plain text: delete them once you no longer need them, and rotate the secrets if one may have been
exposed."

#### 1.12.3 Key grammar and ABO's rename table (normative)

**Grammar.** A canonical key is a `Path` (§1.3.1): `seg ("." seg)*` with `seg = [a-z][a-z0-9_]{0,47}`, first segment
`core` or `modules`. An integration passthrough path is `integrations.<kind>.<instance>.<field>` (05-metadata M1;
`<instance>` follows `InstanceID` and may hold `-`). A key's env name is §1.3.6's derivation, its operator-file form
the nested YAML mapping of its segments, and a secret's name is its path (`secrets.Name`, §2.3). A map-typed key's
map keys are data. Nothing else names a key.

**The rename table** is ABO data and lives in ABO (`internal/books/cutover`, name coined here; core holds no ABO
name, 03 R1). It has one row per HEAD key and drives four things: M2 (§1.12.2), the retired-name list ABO hands
config (`app.Options.RetiredConfigNames`, §1.3.6), the deploy-file check (§1.12.4) and the R2 release note's rename
list (09). It is **generated** from HEAD's code, never maintained by hand:

1. **Source.** Every field of HEAD's `Config` struct (`internal/config/config.go:1048-1912`: 173 top-level fields;
   the step's frozen copy), in source order, and recursively the fields of each struct-typed field. A field's
   **HEAD name** is its `json` tag, or its `mapstructure` tag where the `json` tag is `-` (the 19 "env/file" rows:
   HEAD never stores them). Its **retired names** are the HEAD name as a top-level key of the operator file (its
   nested keys included), the upper-cased HEAD name (viper's prefix-less `AutomaticEnv`, `cmd/root.go:625`), every
   name HEAD binds to it with `BindEnv` (142 calls: `config.go` 132, `abs_config.go` 9, `invite_config.go` 1),
   and the two `ABK_*` names (`ABK_STATE_DIR`, `state_dir.go:25`, → `core.paths.state_dir`;
   `ABK_ALLOW_ENCRYPTION_KEY_REGEN`, `cmd/root.go:733`, → `core.secrets.allow_key_regen`). HEAD's removed keys
   (`internal/config/removed_keys.go`) join as dropped rows that carry HEAD's message.
2. **Explicit rows** win: every HEAD key of a platform family (a core key, an integration instance field or
   secret, a library root or mapping, a task window or schedule) and the few books keys with a fixed name (the
   Cloudflare identity and guest keys). The owning spec of each target names the key; a target this file names
   first is marked "(coined)" in the golden until its owner's key table lists it.
3. **Books rows** — every other field — go to `modules.books.<section>.<leaf>`. `<section>` comes from the section
   table below: an exact name (`=name`) first, then an exact key, then the longest matching prefix; two matches of
   one rank are a generator error. `<leaf>` is the HEAD name minus the section's strip prefix when the name starts
   with it, else the HEAD name. A struct-typed field the section takes as a whole (`=name`) puts its sub-keys
   directly under the section; any other struct-typed field puts them under its own path (`dedup_boilerplate` →
   `modules.books.dedup.boilerplate.*`). Sub-keys keep their `json` names.
4. **Checks.** A field no rule assigns, a path that breaks the grammar, two rows with one path, a section path that
   overlaps a field of its parent (§1.3.1), or a retired name claimed by two rows fails the generator.
   `TestRenameTable_Golden` compares the generator's output (per row: HEAD name, retired names, canonical path,
   canonical env name, blob field, class, `Convert`, and the refuse-or-warn verdict of each retired name under
   §1.3.6) with `internal/books/cutover/testdata/rename_table.golden`; `TestRenameTable_EveryKeyRegistered` asserts
   that every canonical path of class `k` or `s` is a registered key of the build under test, on every PR from F3
   (every core section is registered from F2, §1.12.1; IMPL-1).

**Section table** (books rows):

| Section (`modules.books.…`) | Takes | Strips |
|---|---|---|
| `abs` | `abs_*` | `abs_` |
| `activity` | `activity_*` | `activity_` |
| `ai` | `enable_ai_parsing`, `filename_parse_model`, `cover_art_model` | — |
| `cache` | `list_warmer_heap_delta_mb`, `list_warmer_trickle_interval_ms`, `library_counts_cache_min_interval_seconds`, `cache_*` | `cache_` |
| `catalog` | `=catalog` | — |
| `cfguests` | `cf_*` (the explicit `cf_access_*` rows excepted) | `cf_` |
| `chapters` | `chapter_consolidation_threshold_min`, `repair_chapter_max_min`, `fragment_window_containment_min` | — |
| `db_census` | `db_census_*` | `db_census_` |
| `dedup` | `=dedup`, `dedup_*` | `dedup_` |
| `download` | `deluge_*` (the explicit `deluge_web_*` rows excepted) | `deluge_` |
| `embedding` | `=embedding` | — |
| `fingerprint` | `fingerprint_*`, `fp_parallel_workers` | `fingerprint_` |
| `itunes` | `=itunes` (`path_mappings` is an explicit row) | — |
| `master_list` | `master_list_*` | `master_list_` |
| `metadata` | `metadata_*` (the explicit `metadata_sources` row excepted), `google_books_*` limits, `authority_evidence_enabled`, `identification_advance_enabled` | `metadata_` |
| `openlibrary` | `openlibrary_*` | `openlibrary_` |
| `organize` | `organization_strategy`, `auto_organize`, `folder_naming_pattern`, `file_naming_pattern`, `auto_rename_on_apply` | — |
| `playlist` | `playlist_*` | `playlist_` |
| `purge` | `purge_*` | `purge_` |
| `quota` | `enable_disk_quota`, `disk_quota_percent`, `enable_user_quotas` (display-only at HEAD, `internal/server/handlers/system/handler.go:389-391`) | — |
| `review` | `review_apply_enabled`, `bulk_apply_max_items` | — |
| `scan` | `scan_*`, `auto_scan_enabled`, `auto_scan_debounce_seconds`, `concurrent_scans`, `min_book_size_bytes`, `min_rescan_age_hours`, `supported_extensions`, `exclude_patterns`, `coalesce_shattered_siblings` | `scan_` |
| `search` | `=search`, `bleve_description_max_chars`, `disable_per_user_search_filters`, `enabled_sort_indexes` | — |
| `setup` | `setup_*` | `setup_` |
| `tags` | `write_back_metadata`, `embed_cover_art`, `auto_write_tags_on_apply`, `verify_after_write`, `create_backups` (HEAD's switch for a `.bak-*` copy before a tag write, not a database backup) | — |
| `tools` | `=tools` | — |
| `versions` | `retire_versions_apply_enabled`, `kept_sweep_apply_enabled` | — |
| `whisper` | `whisper_*` | `whisper_` |

Two further books sections come from explicit rows: `identity` and `scheduler` (below), and `maintenance` from
`maintenance`'s sub-key rows.

**The table** (generated; HEAD's source order). *Stored*: where HEAD keeps the value (`blob` = the settings blob,
`row` = an encrypted row, `env/file` = never stored). *Class*: `k` key and `s` secret (migrated by M2), `i` input
of M6, `o` operator-only at HEAD (nothing to migrate; its retired names follow §1.3.6), `x` dropped. A path ending
`.*` holds the struct's sub-keys under their `json` names. Counts: 134 `k`, 11 `s`, 9 `i`, 16 `o`, 3 `x` (173 top-level rows; two of them, `oauth_allowed_emails` and the
`scheduled.db_optimize` sub-key, have two targets).

| HEAD name | Stored | Canonical path or name | Class | Note |
|---|---|---|---|---|
| `root_dir` | blob | `core.library.roots` entry `books-library` (`Path`) | i | 05-library §5L.10 |
| `path_aliases` | blob | `core.library.roots` entry `books-library` (`Aliases`) | i | 05-library §5L.10 |
| `database_path` | blob | `core.storage.path` | k | `ProtectOperator`: never stored |
| `database_type` | blob | — | x | Pebble only |
| `activity_backend` | blob | `modules.books.activity.backend` | k | |
| `playlist_dir` | blob | `modules.books.playlist.dir` | k | |
| `setup_complete` | blob | `modules.books.setup.complete` | k | |
| `organization_strategy` | blob | `modules.books.organize.organization_strategy` | k | |
| `scan_on_startup` | blob | `modules.books.scan.on_startup` | k | |
| `auto_organize` | blob | `modules.books.organize.auto_organize` | k | |
| `auto_scan_enabled` | blob | `modules.books.scan.auto_scan_enabled` | k | |
| `auto_scan_debounce_seconds` | blob | `modules.books.scan.auto_scan_debounce_seconds` | k | |
| `folder_naming_pattern` | blob | `modules.books.organize.folder_naming_pattern` | k | |
| `file_naming_pattern` | blob | `modules.books.organize.file_naming_pattern` | k | |
| `create_backups` | blob | `modules.books.tags.create_backups` | k | |
| `backup_dir` | blob | `core.backup.dir` | k | |
| `backup_max_total_bytes` | blob | `core.backup.retention.max_total_bytes` | k | |
| `backup_compression` | blob | `core.backup.compression` | k | |
| `backup_compression_level` | blob | `core.backup.compression_level` | k | |
| `enable_disk_quota` | blob | `modules.books.quota.enable_disk_quota` | k | |
| `disk_quota_percent` | blob | `modules.books.quota.disk_quota_percent` | k | |
| `enable_user_quotas` | blob | `modules.books.quota.enable_user_quotas` | k | |
| `write_back_metadata` | blob | `modules.books.tags.write_back_metadata` | k | |
| `embed_cover_art` | blob | `modules.books.tags.embed_cover_art` | k | |
| `metadata_sources` | blob | `metadata/<source id>` instances | i | credentials: `s` rows below |
| `openlibrary_dump_enabled` | blob | `modules.books.openlibrary.dump_enabled` | k | |
| `openlibrary_dump_dir` | blob | `modules.books.openlibrary.dump_dir` | k | |
| `hardcover_api_token` | row | `integrations.metadata.hardcover.api_token` | s | encrypted row |
| `google_books_api_key` | row | `integrations.metadata.google-books.api_key` | s | encrypted row |
| `enable_ai_parsing` | blob | `modules.books.ai.enable_ai_parsing` | k | |
| `openai_api_key` | row | `integrations.ai.openai.api_key` | s | encrypted row |
| `acoustid_api_key` | blob | `modules.books.acoustid.api_key` | s | plaintext in the blob |
| `fp_parallel_workers` | blob | `modules.books.fingerprint.fp_parallel_workers` | k | |
| `fingerprint_remote_workers_enabled` | blob | `modules.books.fingerprint.remote_workers_enabled` | k | |
| `fingerprint_worker_tool_versions` | blob | `modules.books.fingerprint.worker_tool_versions` | k | |
| `fingerprint_window_reference_tools` | blob | `modules.books.fingerprint.window_reference_tools` | k | |
| `fingerprint_length_sec` | blob | `modules.books.fingerprint.length_sec` | k | |
| `whisper_clip_cache_dir` | blob | `modules.books.whisper.clip_cache_dir` | k | |
| `whisper_batch_sleep_ms` | blob | `modules.books.whisper.batch_sleep_ms` | k | |
| `whisper_batch_size` | blob | `modules.books.whisper.batch_size` | k | |
| `whisper_max_in_flight` | blob | `modules.books.whisper.max_in_flight` | k | |
| `openai_base_url` | blob | `integrations.ai.openai.base_url` | i | |
| `abs_auth_probe_enabled` | blob | `modules.books.abs.auth_probe_enabled` | k | |
| `authority_evidence_enabled` | blob | `modules.books.metadata.authority_evidence_enabled` | k | |
| `retire_versions_apply_enabled` | blob | `modules.books.versions.retire_versions_apply_enabled` | k | |
| `kept_sweep_apply_enabled` | blob | `modules.books.versions.kept_sweep_apply_enabled` | k | |
| `abs_itunes_position_backfill_user_id` | blob | `modules.books.abs.itunes_position_backfill_user_id` | k | |
| `otel_exporter_otlp_endpoint` | blob | `core.tracing.otlp.endpoint` | k | |
| `otel_metrics_otlp_endpoint` | blob | `core.metrics.otlp.endpoint` | k | |
| `otel_metrics_otlp_interval` | blob | `core.metrics.otlp.interval` | k | bare integer = ms |
| `otel_metrics_otlp_insecure` | blob | `core.metrics.otlp.insecure` | k | |
| `telemetry_environment` | blob | `core.metrics.environment` | k | |
| `list_warmer_heap_delta_mb` | blob | `modules.books.cache.list_warmer_heap_delta_mb` | k | |
| `list_warmer_trickle_interval_ms` | blob | `modules.books.cache.list_warmer_trickle_interval_ms` | k | |
| `library_counts_cache_min_interval_seconds` | blob | `modules.books.cache.library_counts_cache_min_interval_seconds` | k | |
| `bleve_description_max_chars` | blob | `modules.books.search.bleve_description_max_chars` | k | |
| `metadata_review_model` | blob | `modules.books.metadata.review_model` | k | |
| `filename_parse_model` | blob | `modules.books.ai.filename_parse_model` | k | |
| `cover_art_model` | blob | `modules.books.ai.cover_art_model` | k | |
| `whisper_remote_url` | blob | `modules.books.whisper.remote_url` | k | |
| `whisper_endpoints` | blob | `modules.books.whisper.endpoints` | k | |
| `whisper_requires` | blob | `modules.books.whisper.requires` | k | |
| `ai_endpoints` | blob | `ai/<row id>` instances | i | 05-ai §10 |
| `ai_endpoints_routing` | blob | — | x | not a router input (05-ai §10) |
| `concurrent_scans` | blob | `modules.books.scan.concurrent_scans` | k | |
| `scan_progress_every` | blob | `modules.books.scan.progress_every` | k | |
| `scan_standdown_grace_seconds` | blob | `modules.books.scan.standdown_grace_seconds` | k | |
| `chapter_consolidation_threshold_min` | blob | `modules.books.chapters.chapter_consolidation_threshold_min` | k | |
| `repair_chapter_max_min` | blob | `modules.books.chapters.repair_chapter_max_min` | k | |
| `fragment_window_containment_min` | blob | `modules.books.chapters.fragment_window_containment_min` | k | |
| `metadata_candidate_fetch_workers` | blob | `modules.books.metadata.candidate_fetch_workers` | k | |
| `identification_advance_enabled` | blob | `modules.books.metadata.identification_advance_enabled` | k | |
| `google_books_daily_limit` | blob | `modules.books.metadata.google_books_daily_limit` | k | |
| `google_books_background_daily_limit` | blob | `modules.books.metadata.google_books_background_daily_limit` | k | |
| `google_books_fallback_daily_limit` | blob | `modules.books.metadata.google_books_fallback_daily_limit` | k | |
| `coalesce_shattered_siblings` | blob | `modules.books.scan.coalesce_shattered_siblings` | k | |
| `operation_timeout_minutes` | blob | `modules.books.operations.stale_threshold_minutes` | k | read at HEAD only as the staleness threshold of books' `GET /api/v1/operations/stale` |
| `db_census_exact_read_mb_per_sec` | blob | `modules.books.db_census.exact_read_mb_per_sec` | k | |
| `db_census_exact_cooldown_hours` | blob | `modules.books.db_census.exact_cooldown_hours` | k | |
| `min_book_size_bytes` | blob | `modules.books.scan.min_book_size_bytes` | k | |
| `min_rescan_age_hours` | blob | `modules.books.scan.min_rescan_age_hours` | k | |
| `log_retention_days` | blob | `core.log.journal.retention_days` | k | `0` → 90, negative → 0 (keep) |
| `operation_log_retention_days` | blob | `core.tasks.history_retention` | k | days → duration |
| `activity_log_retention_change_days` | blob | `modules.books.activity.log_retention_change_days` | k | |
| `activity_log_retention_debug_days` | blob | `modules.books.activity.log_retention_debug_days` | k | |
| `activity_log_compaction_days` | blob | `modules.books.activity.log_compaction_days` | k | |
| `activity_log_nightly_compaction_enabled` | blob | `modules.books.activity.log_nightly_compaction_enabled` | k | |
| `activity_log_full_detail_days` | blob | `modules.books.activity.log_full_detail_days` | k | |
| `ai_journal_retention_days` | blob | `core.ai.journal_retention` (coined) | k | days → duration |
| `embedding` | blob | `modules.books.embedding.*` | k | |
| `dedup` | blob | `modules.books.dedup.*` | k | |
| `dedup_boilerplate` | blob | `modules.books.dedup.boilerplate.*` | k | |
| `metadata_scoring` | blob | `modules.books.metadata.scoring.*` | k | |
| `catalog` | blob | `modules.books.catalog.*` | k | |
| `ai_backend` | blob | — | x | its scalars are already folded into `ai_endpoints` by HEAD's codec (05-ai §10) |
| `api_rate_limit_per_minute` | blob | `core.http.rate_limit.per_minute` | k | |
| `json_body_limit_mb` | blob | `core.http.body.json_max_bytes` | k | MiB → bytes |
| `upload_body_limit_mb` | blob | `core.http.body.upload_max_bytes` | k | MiB → bytes |
| `master_list_retention` | blob | `modules.books.master_list.retention` | k | |
| `enable_auth` | blob | `core.auth.mode` | k | `true` → `required`, `false` → `disabled`; fail-closed key |
| `enable_rate_limit` | blob | `core.http.rate_limit.enabled` | k | |
| `review_apply_enabled` | blob | `modules.books.review.review_apply_enabled` | k | |
| `bulk_apply_max_items` | blob | `modules.books.review.bulk_apply_max_items` | k | |
| `basic_auth_enabled` | blob | `core.http.basic_gate.enabled` | k | fail-closed key |
| `basic_auth_username` | blob | `core.http.basic_gate.username` | k | |
| `basic_auth_password` | row | `core.http.basic_gate.password` | s | encrypted row; empty = unset |
| `oauth_enabled` | blob | `core.auth.oidc.providers` | k | fold (05-authn) |
| `oauth_github_client_id` | blob | `core.auth.oidc.providers` entry `github` | k | fold (05-authn) |
| `oauth_github_client_secret` | blob | `core.auth.oidc.github.client_secret` (coined), the secret the `github` entry's `client_secret` ref names | s | plaintext in the blob |
| `oauth_google_client_id` | blob | `core.auth.oidc.providers` entry `google` | k | fold (05-authn) |
| `oauth_google_client_secret` | blob | `core.auth.oidc.google.client_secret` (coined), the secret the `google` entry's `client_secret` ref names | s | plaintext in the blob |
| `oauth_redirect_base_url` | blob | `core.auth.oidc.redirect_base_url` | k | |
| `oauth_allowed_emails` | blob | `core.auth.oidc.providers[].allowed_emails` and `modules.books.cfaccess.allowed_emails` | k | fold (05-authn); M2 also writes the value to the books key, because HEAD's `internal/cfaccess` reads it as the Cloudflare Access allow-list |
| `oauth_default_role` | blob | `core.auth.oidc.providers[].default_role` | k | fold (05-authn) |
| `cf_access_team_domain` | blob | `modules.books.identity.cf_access_team_domain` | k | |
| `cf_access_aud` | blob | `modules.books.identity.cf_access_aud` | k | |
| `owner_email` | blob | `modules.books.identity.owner_email` | k | |
| `abs_api_enabled` | blob | `modules.books.abs.api_enabled` | k | |
| `abs_auth_modes` | blob | `modules.books.abs.auth_modes` | k | |
| `abs_jwt_secret` | env/file | `modules.books.abs.jwt_secret` | o | `ProtectOperator` secret, never stored (SEC-6, §1.3.7); value kept (D12 (c)) |
| `abs_access_token_ttl` | blob | `modules.books.abs.access_token_ttl` | k | |
| `abs_refresh_token_ttl` | blob | `modules.books.abs.refresh_token_ttl` | k | |
| `abs_session_max_ttl` | blob | `modules.books.abs.session_max_ttl` | k | |
| `abs_refresh_grace` | blob | `modules.books.abs.refresh_grace` | k | |
| `abs_server_version` | blob | `modules.books.abs.server_version` | k | |
| `abs_default_library_id` | blob | `modules.books.abs.default_library_id` | k | |
| `smtp_host` | env/file | `integrations.notify.email.host` | o | |
| `smtp_port` | env/file | `integrations.notify.email.port` | o | |
| `smtp_username` | env/file | `integrations.notify.email.username` | o | |
| `smtp_password` | env/file | `integrations.notify.email.password` | s | |
| `smtp_from` | env/file | `integrations.notify.email.from` | o | |
| `public_base_url` | env/file | `core.http.public_base_url` (coined) | o | |
| `cf_account_id` | env/file | `modules.books.cfguests.account_id` | o | |
| `cf_guests_policy_id` | env/file | `modules.books.cfguests.guests_policy_id` | o | |
| `cf_policy_api_token` | env/file | `modules.books.cfguests.policy_api_token` | o | `ProtectOperator` secret, never stored (SEC-6, §1.3.7) |
| `cf_guest_service_tokens` | env/file | `modules.books.cfguests.guest_service_tokens` | o | |
| `cf_service_auth_policy_id` | env/file | `modules.books.cfguests.service_auth_policy_id` | o | |
| `invite_email_service_token` | env/file | `modules.books.cfguests.invite_email_service_token` | o | |
| `tunnel_connector_addrs` | env/file | `core.http.trusted_proxies` | o | comma list → list; a storable admin key from F3 (§1.3.7) |
| `allowed_peer_cidrs` | env/file | `core.http.peer_allowlist` | o | comma list → list; a storable admin key from F3 (§1.3.7) |
| `login_saturated_rate` | env/file | `core.auth.login.saturated_rate` (coined) | o | |
| `login_saturated_burst` | env/file | `core.auth.login.saturated_burst` (coined) | o | |
| `cache_invalidate_on_book_update` | blob | `modules.books.cache.invalidate_on_book_update` | k | |
| `metadata_fetch_cache_ttl_days` | blob | `modules.books.metadata.fetch_cache_ttl_days` | k | |
| `metadata_fetch` | blob | `modules.books.metadata.fetch.*` | k | |
| `bootstrap_key_ttl` | env/file | `core.auth.bootstrap.key_ttl` | o | |
| `bootstrap_key_ttl_days` | env/file | `core.auth.bootstrap.key_ttl` | o | days → duration |
| `purge_soft_deleted_after_days` | blob | `modules.books.purge.soft_deleted_after_days` | k | |
| `purge_soft_deleted_delete_files` | blob | `modules.books.purge.soft_deleted_delete_files` | k | |
| `backup_retention_days` | blob | `core.backup.retention.max_age` | k | days → duration |
| `log_level` | blob | `core.log.level` | k | |
| `itunes` | blob | `modules.books.itunes.*` | k | |
| `deluge_web_url` | blob | `integrations.download.deluge.url` | i | |
| `deluge_web_password` | blob | `integrations.download.deluge.password` | s | plaintext in the blob |
| `deluge_discovery_label` | blob | `modules.books.download.discovery_label` | k | |
| `deluge_discovery_enabled` | blob | `modules.books.download.discovery_enabled` | k | |
| `deluge_move_enabled` | blob | `modules.books.download.move_enabled` | k | |
| `protected_paths` | blob | `core.library.protected_paths` | i | 05-library §5L.10 |
| `auto_update` | blob | `core.updater.*` | k | sub-key rows below |
| `maintenance` | blob | `core.tasks.windows.nightly.*`, `modules.books.maintenance.*` | k | sub-key rows below |
| `download_client` | blob | `download/deluge` instance | i | sub-key rows below |
| `auto_rename_on_apply` | blob | `modules.books.organize.auto_rename_on_apply` | k | |
| `auto_write_tags_on_apply` | blob | `modules.books.tags.auto_write_tags_on_apply` | k | |
| `verify_after_write` | blob | `modules.books.tags.verify_after_write` | k | |
| `scheduled` | blob | `modules.books.scheduler.schedules.<entry>` | k | sub-key rows below |
| `plugins` | blob | `notify/webhook-<n>` instances | i | sub-key rows below |
| `supported_extensions` | blob | `modules.books.scan.supported_extensions` | k | |
| `exclude_patterns` | blob | `modules.books.scan.exclude_patterns` | k | |
| `tools` | blob | `modules.books.tools.*` | k | |
| `write_startup_readonly_key` | blob | `core.auth.bootstrap.readonly_key.enabled` | k | |
| `disable_per_user_search_filters` | blob | `modules.books.search.disable_per_user_search_filters` | k | |
| `search` | blob | `modules.books.search.*` | k | |
| `enabled_sort_indexes` | blob | `modules.books.search.enabled_sort_indexes` | k | boot-only at HEAD, stripped from the blob (`persistence.go:2126-2139`), so usually absent |

**Sub-key rows** (the struct-typed fields above whose sub-keys do not simply follow their parent):

| HEAD name | Stored | Canonical path or name | Class | Note |
|---|---|---|---|---|
| `itunes.path_mappings` | blob | `core.library.mappings` entries, `Source: "itunes"` | i | 05-library §5L.10 |
| `maintenance.enabled`, `maintenance.window_start`, `maintenance.window_end` | blob | `core.tasks.windows.nightly.{enabled, start, end}` | k | 05-tasks |
| every other `maintenance.<field>` (15: the window's job switches and `acoustid_nightly_limit`) | blob | `modules.books.maintenance.<field>` | k | |
| `auto_update.enabled` | blob | `core.updater.mode` | k | `true` → `check` (unattended apply stays check-only, 05-updater), `false` → `off` |
| `auto_update.channel` | blob | `core.updater.channel` | k | `develop` → `prerelease` |
| `auto_update.check_minutes` | blob | `core.updater.schedules.check` | k | `{enabled: true, every: <minutes>}` |
| `auto_update.window_start`, `auto_update.window_end` | blob | — | x | read and ignored since HEAD's updater hotfix (05-updater) |
| `scheduled.<entry>` (16 entries, `config.go:991-1046`) | blob | `modules.books.scheduler.schedules.<entry>` | k | a `tasks.ScheduleConfig` by HEAD's own reading of the entry (an interval-only entry is enabled iff its interval is above 0); `TestScheduleParity_AllDefinitions` (05-tasks) |
| `scheduled.db_optimize` | blob | `modules.books.scheduler.schedules.db_optimize` and `core.storage.schedules.compact` | k | the row above plus a second target: the same `ScheduleConfig` also seeds the core kind `core.storage.compact`'s key, while the books key keeps driving `books.maintenance.aux-store-compact` (05-tasks §5T.3.8) |
| `download_client.torrent.type`, `download_client.torrent.deluge.{host, port, username}` | blob | `download/deluge` instance fields | i | 05-download §10 |
| `download_client.torrent.deluge.password` | blob | `integrations.download.deluge.daemon_password` (coined) | s | plaintext in the blob |
| `metadata_sources[].credentials.<k>` | blob | `integrations.metadata.<source id>.credentials.<k>` | s | plaintext in the blob |
| `plugins.webhook` (`enabled`, `settings.urls`, `settings.events`) | blob | `notify/webhook-<n>` instances | i | 05-notify §10 |
| `plugins.webhook.settings.secret` | blob | `integrations.notify.webhook-<n>.secret` | s | plaintext; one record per instance `n`, the same value |
| any other `plugins.<id>` | blob | `modules.books.plugins.<id>.settings` (a secret record, no key) | s | `internal/plugin` is retired (02 N4); the free-form map may hold credentials, so M2 keeps it as one encrypted record and reports it with the quarantine records instead of dropping it or quarantining it in plain text |
| `ai_endpoints[]` rows | blob | `ai/<row id>` instances | i | no secret of their own (05-metadata M1.10) |

#### 1.12.4 Env, file and flag names after the cutover

This section is the **single list** of env-name classes for ABO; 09's "changed in R2" list points to it (OPS2-21).
Counts come from the rename table's golden, not from hand maintenance.

- **Canonical names:** `ABO_<PATH>` for every key (§1.3.6), the only names R2 reads besides the bindings below.
- **Ecosystem bindings (kept, not retired):** `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`,
  `OTEL_METRIC_EXPORT_INTERVAL` and `OTEL_EXPORTER_OTLP_METRICS_INSECURE` (`config.go:2746-2749`; targets in
  [05-metrics](05-metrics.md) §10), and `OPENAI_API_KEY`, `OPENAI_BASE_URL` onto the `ai/openai` instance's
  passthrough paths (C11). Other software defines them, so renaming them would buy nothing.
- **Retired names:** every other name the rename table lists (the `AutomaticEnv` and `BindEnv` names, the flat
  file keys and `ABK_*`), handled by §1.3.6: a protected or secret key's name refuses boot naming the canonical name,
  any other one warns. Examples of each: `DATABASE_PATH` (→ `ABO_CORE_STORAGE_PATH`, `ProtectOperator`),
  `ENABLE_AUTH` (→ `ABO_CORE_AUTH_MODE`, `ProtectOwner`), `ABS_JWT_SECRET` (→ `ABO_BOOKS_ABS_JWT_SECRET`, a `ProtectOperator` secret,
  whose **value** the operator keeps, because the ABS phone apps' tokens are signed with it, D12 (c)),
  `BASIC_AUTH_PASSWORD` (→ `ABO_CORE_HTTP_BASIC_GATE_PASSWORD`, a secret), `ABK_STATE_DIR`
  (→ `ABO_CORE_PATHS_STATE_DIR`, `ProtectOperator`) refuse; `FP_PARALLEL_WORKERS`
  (→ `ABO_BOOKS_FINGERPRINT_FP_PARALLEL_WORKERS`) and `LOGIN_SATURATED_RATE` warn. 05-storage additionally refuses
  to **create** a store while a retired database-path name is set.
- **Deploy files use canonical names only.** F3's PR rewrites `deploy/audiobook-organizer.service`,
  `deploy/com.audiobook-organizer.plist`, `deploy/local.conf.example` (its commented-out lines included),
  `docker-compose.yml` and the `Dockerfile`s to canonical names (PC-09-07). Compose passes the Basic gate's
  variables **only when they are set** (bare-name entries that pass a host variable through only when it is set, or
an `env_file`), so a default compose install sets no
  `ABO_CORE_HTTP_BASIC_GATE_ENABLED=false` (14 E5). The names a shipped file sets that HEAD never read
  (`AUDIOBOOK_ROOT_DIR`, unit `:97`, plist `:38`; `AO_DIR`, `AO_DB`, `docker-compose.yml:18-19`;
  `OLLAMA_BASE_URL`, `deploy/local.conf.example:40`) are removed from those files (out-of-scope defect 5).
  `TestDeployFilesUseCanonicalNamesOnly` scans those files: every variable found is a canonical name, an
  ecosystem binding, or on the runtime list (`HOME`, `GOMEMLIMIT`, `GOGC`, `TZ`, `NOTIFY_SOCKET`, read by the Go
  runtime, the systemd notify helper or the OS, never by config); a retired name or anything else fails. The
  repo's agent skills and first-party scripts that set env names are updated in the same PR, F3's, never in a later
  one (09 §9.2.5).
- **Not config at all:** env names read by `os.Getenv` outside `internal/config` (for example `WHISPER_PATH_MAP`,
  `internal/transcribe/path.go:28-40`) are listed by the generator and either become a key in the config flip F3 or
  stay their reader's own documented variable; `TestRenameTable_Golden` fails on an unlisted literal.

**Flags** are kept (T9, zero cost): ABO's cobra adapter maps `--dir`, `--db`, `--playlists`, `--log-level`,
`--config` and the other shipped flags of `serve` and `fp-worker` (09's kept-contracts list) onto canonical paths
with `FromFlagSet`'s rule. The no-op `--db-type` and the SQLite flags are deleted in R3.

### 1.13 Open items

1. Whether `encoding/json/v2` is GA in go1.27.3; if so, decode sections with it (strict unknown-field
   handling and `omitzero` are better fits). Decide in R1 with the toolchain spec ([13-toolchain](13-toolchain.md)).
2. Exact per-key reload class for ABO keys: default every books key to `ReloadLive` only where the consumer
   reads the handle per use; keys captured at construction (e.g. worker counts) are `ReloadRestart` until the
   consumer subscribes. The per-key audit is a column of the rename table's golden (§1.12.3), filled in the R2 config
   flip.
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
`internal/database/pebble_store_auth.go:1656-1663`: core's stores hash them, and the identity step M3 migrates
ABO's, 05-authn). 09's R2 tables reference this keyring instead of describing their own.

### 2.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Cipher | AES-256-GCM, random nonce, base64; **no key id, no AAD** (a row's ciphertext can be copied to another key's row and decrypts) | `internal/database/settings.go:189-235` |
| Key | 32 raw bytes in `.encryption_key`, 0600, in the secure state dir (`ABK_STATE_DIR`, compiled default), legacy fallback `<root_dir>/.appdata/`; package-global `encryptionKey` | `settings.go:42-52,110-175`, `internal/config/state_dir.go:25,54,133` |
| Encrypted fields | exactly 4: `openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`, as `IsSecret` rows; zeroed in the blob | `persistence.go:1955-1958,1989-1994` |
| **Same 4 also in plaintext on disk** | `SaveConfigToFile` writes `openai_api_key`, `google_books_api_key`, `hardcover_api_token` in clear to `<db parent>/config.yaml` ("file permissions protect them") on every save | `persistence.go:107-108,168-178` |
| Key loss | `guardAgainstKeyRegeneration` refuses boot if no key and encrypted rows exist (override `ABK_ALLOW_ENCRYPTION_KEY_REGEN`); if a key does regenerate, `LoadConfigFromDatabase` re-encrypts what the YAML mirror can recover and **deletes** every other secret row | `cmd/root.go:726-783`, `persistence.go:1191-1260` |
| Rotation | none | `raw/10` §7 |
| API exposure | `MaskSecrets` returns `********` + last 2 chars for secrets ≥16 runes; PUT restores echoed masks | `settings.go:250-277`, `update_service.go:88-150,353-373` |
| Log redaction | query-string token masking in access log/recovery; invite path redaction; `cfservicetoken.Created.LogValue` | `internal/server/server.go:695-704`, `internal/logging/redact_path.go:107`, `internal/cfservicetoken/client.go:191` |

**Secret-bearing fields** (verified: the blob builder zeroes only the 4 above, `persistence.go:1955-1958`, and
`marshalConfigBlob` strips only boot-only keys, `persistence.go:2126-2139`). The last column says what the R2
cutover step M2 does with each (§1.12.2). "—" means HEAD reads the field only from env or file (a `json:"-"`
field): nothing is stored, so nothing migrates, and its retired env name refuses boot (§1.3.6) until the operator
renames it. From R2 the SMTP password may also be stored, encrypted, under its canonical name; the ABS JWT secret
and the Cloudflare policy token may not: they are `ProtectOperator` and stay operator-only (§1.3.7; SEC-6).

| Field (JSON key) | Anchor | At rest today | Canonical name (secret) | M2 |
|---|---|---|---|---|
| `openai_api_key` | `persistence.go:1955` | encrypted row | `integrations.ai.openai.api_key` | row decrypted, re-encrypted into `c/secrets/`, row deleted; gap-fill copy stripped (M2b) |
| `google_books_api_key` | `persistence.go:1956` | encrypted row | `integrations.metadata.google-books.api_key` | the same |
| `hardcover_api_token` | `persistence.go:1957` | encrypted row | `integrations.metadata.hardcover.api_token` | the same |
| `basic_auth_password` | `persistence.go:1958` | encrypted row | `core.http.basic_gate.password` (SEC4-10) | the same; an empty row leaves it unset (no gap-fill copy) |
| `acoustid_api_key` | `config.go:1141` | plaintext `config_blob` (masked in API, `update_service.go:94`) | `modules.books.acoustid.api_key` | encrypted into `c/secrets/`, blob field removed |
| `oauth_github_client_secret` | `config.go:1588` | plaintext `config_blob` (value re-applied from env each boot, still written) | `core.auth.oidc.github.client_secret` | the same |
| `oauth_google_client_secret` | `config.go:1590` | plaintext `config_blob` | `core.auth.oidc.google.client_secret` | the same |
| `deluge_web_password` | `config.go:1777` | plaintext `config_blob` (carried forward when env-locked, `persistence.go:1965-1971`) | `integrations.download.deluge.password` | the same |
| `download_client.torrent.deluge.password` | `config.go:240` | plaintext `config_blob` | `integrations.download.deluge.daemon_password` (coined; 05-download names it) | the same |
| `metadata_sources[].credentials` (map) | `config.go:154` | plaintext `config_blob` | `integrations.metadata.<source id>.credentials.<k>` (one secret per entry) | the same, per entry |
| `plugins.webhook` `secret` (HMAC signing key) | `internal/plugins/webhook/plugin.go:58` | plaintext `config_blob` | `integrations.notify.webhook-<n>.secret` (the one family-level value, written under every instance's name, 05-notify §10) | the same, one record per instance |
| other `plugins[].settings` (free-form map) | `config.go:244-247` | plaintext `config_blob`; may hold credentials (unknowable) | `modules.books.plugins.<id>.settings` (one record, no key) | encrypted as one record and reported; `internal/plugin` is retired (02 N4) |
| `abs_jwt_secret` | `config.go:1636` (`json:"-"`) | env / viper file only | `modules.books.abs.jwt_secret` | — (its value must stay the same: it signs the ABS apps' tokens, D12 (c)); `ProtectOperator`, never stored (SEC-6) |
| `smtp_password` | `config.go:1678` (`json:"-"`) | env / viper file only | `integrations.notify.email.password` | — |
| `cf_policy_api_token` | `config.go:1688` (`json:"-"`) | env / viper file only | `modules.books.cfguests.policy_api_token` | —; `ProtectOperator`, never stored (SEC-6) |
| userinfo in URLs (`openai_base_url`, `whisper_remote_url`, `ai_endpoints[].url`, `embedding.base_url`) | `config.go:1222,1319`, `ai_endpoints.go:40` | plaintext if an operator embeds `user:pass@` | `schema:"format=uri"` validation **rejects** userinfo; credentials go in a sibling `secrets.Ref` | M2 stages the URL without its userinfo and keeps the userinfo as one encrypted record `<key path>.userinfo` (no key), reported with the quarantine records, so the operator moves it into the instance's credential field; no plaintext credential stays in a URL or in quarantine |

The `<instance>` of each migrated singleton is fixed by the integrations table ([05-metadata](05-metadata.md)
M1.10, with [05-download](05-download.md), [05-ai](05-ai.md), [05-notify](05-notify.md)), so each secret has one
final name from its first core write on.

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
	SourceEnv                      // operator env (incl. <NAME>_FILE and an ecosystem binding); in memory only, never persisted
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
	Err         error     // ErrKeyUnavailable / ErrCorrupt when the record cannot be decrypted, or carries an
	                      // unreadable marker from the cutover (§1.12.2)
}

type Write struct { // staged by config.Store.Apply into Committer.Commit
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
	AllowKeyRegen bool              // core.secrets.allow_key_regen (operator only); M2 reads it for HEAD's key guard too
	// ResetBoot is set by the kernel only on the reset boot of a core build's factory reset (the KindReset swap
	// journal reads set_aside, 05-storage §5S.3.15): Open then rewrites the keyring header's install_id to the fresh
	// store's (§2.4, DATA7-17). Every other Open leaves that field alone.
	ResetBoot     bool
	Logger        *slog.Logger
	Metrics       *metrics.Scope    // core scope "mediacore.secrets"
	Now           func() time.Time
}

func Open(ctx context.Context, o Options) (*Store, error)

func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
// Stage is the same-batch write used by core/internal/configstore, integrations and, through Importer, the app's
// cutover steps. COR2-12: a pointer, so Bind and the writes land on the caller's batch.
func (s *Store) Stage(b *storage.Batch, w Write) error
// StageUnreadable stages the unreadable marker for n (a pre-core secret the cutover could not decrypt, §1.12.2):
// Meta.Err is ErrKeyUnavailable (reason "key") or ErrCorrupt (reason "corrupt") until n is set again.
func (s *Store) StageUnreadable(b *storage.Batch, n Name, reason string, by string) error

// Importer is secrets' write side for a one-way cutover step (03 §3.3 "Store interfaces and importers"; §1.12.2),
// handed to the app's steps as module.Importers.Secrets (06 §6.4.2) and to storage's quarantine sealing (05-storage
// §5S.3.13). It follows 05-storage's importer rules: it seals every value with the keyring's active data key (key
// ID, AAD) and stages it into the CALLER's batch, never commits, and is create-only (an existing record with other
// bytes fails with storage.ErrImportConflict). It is a thin layer over Stage and StageUnreadable.
type Importer interface {
	ImportSecret(ctx context.Context, b *storage.Batch, w Write) error
	ImportUnreadable(ctx context.Context, b *storage.Batch, n Name, reason, by string) error
}

// Importer returns the store's importer; valid after Open, which runs before the migration list (§2.4).
func (s *Store) Importer() Importer
func (s *Store) Ephemeral(n Name, v Value, src Source)            // operator-supplied value; locks the key
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]   // never values
// KeyIDsReferenced scans the records readable through r (the live store, or a staged restore) and reports
// every key ID they need (DATA3-12). Restore reports and key pruning use it.
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
)
```

**Formats (normative; the format carries key IDs from v1 so it never changes when rotation lands).**

- Keyring file `<state_dir>/secrets.keyring`, 0600, directory 0700, written atomically via `os.Root`
  (temp + fsync + rename):
  `{"format":"mediacore-keyring/1","keys":[{"id":"k1","use":"data","created":"…","status":"active","material":"<base64 32 bytes>"},{"id":"d1","use":"derive",…}]}`.
  Exactly one `active` key per `use` (`data`, `derive`). `use: "data"` keys encrypt records. The active
  `use: "derive"` key feeds
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
  AAD = `"mediacore/secrets/v1\x00" + AppID + "\x00" + name`, so a ciphertext moved to another name or app fails with
  `ErrCorrupt`.
- Cleared marker at the same key, `{"v":1,"cleared":true,"at":"…","by":"…"}`: written by `Delete` and by a clearing
  `Stage`. Unreadable marker, `{"v":1,"unreadable":"key"|"corrupt","at":"…","by":"…"}`: written only by
  `StageUnreadable` (the cutover, §1.12.2); a later `Put` replaces it.
- **Read order (`Get`, `Meta`).** (1) an operator value (`Ephemeral`; locks the key); (2) the record
  (`SourceStored`; `ErrNotSet` for a cleared marker; `Meta.Err` for an unreadable marker); (3) `ErrNotSet`.
- Store metadata at `c/secrets/meta`: `{"app_id":"abo","keyring_format":"mediacore-keyring/1","created":"…"}`.
  `Open` compares it with `Options.AppID`; a mismatch is `ErrAppIDMismatch` (boot error naming both IDs), so an
  accidental AppID change fails loudly instead of turning every secret into `ErrCorrupt` (SEC-28). AppID is a
  compiled constant of the app's `main` (`app.Options.AppID`), never configuration.

### 2.4 Lifecycle

1. After storage opens and after the pre-migration checkpoint, **before** the migration list (§1.4 step 5, C13; the
   order 06 §6.5.1 states): `Open` checks `c/secrets/meta` (written on first open) and resolves the keyring:
   - keyring present → load; mode looser than 0600 → try `chmod 0600`; if that fails → `ErrKeyfileInsecure`
     (fail-closed);
   - keyring absent, no stored records → generate `k1` (data) and `d1` (derive), write keyring (first run; on the
     first R2 boot this is the keyring M2 encrypts with);
   - keyring absent, stored records exist → `ErrKeyMissing` (boot refused, message names the file, the exported
     copy (§2.4 Backups) and the override). With `core.secrets.allow_key_regen=true` new keys are generated **and
     existing records are kept** under their old key id (`Meta.Err = ErrKeyUnavailable`); putting the old keyring
     back later recovers them. Nothing is ever deleted automatically (contrast `persistence.go:1232-1260`).
     HEAD's own key file is M2's concern, not `Open`'s (§1.12.2);
   - `Options.ResetBoot` → the keyring header's `install_id` is rewritten to the fresh store's
     `c/storage/install` marker (the reset boot of a factory reset, §1.4; the kernel marks the `KindReset` journal
     `applied` only after this, DATA7-17); otherwise the header's `install_id` is only written when absent (below);
   - **the dry run never touches the state directory's keyring.** `storage migrate --dry-run` (05-storage §5S.3.6)
     runs M2 on a copy of the store, and M2 seals with a **throwaway keyring**: the kernel gives `Open` a `KeyFile`
     inside the dry run's directory in the scratch area `.mediacore-checkpoints/scratch/` (05-storage §5S.3.12
     "Scratch copies": mode 0700, the file 0600), `Open` creates it there as in the "keyring absent, no stored
     records" case, and it is deleted with the dry run's directory when the dry run ends, whatever the outcome (an
     orphan directory, and the throwaway keyring in it, is deleted by 05-storage's checkpoint sweeper 7 days after its
     mtime). The state directory's `secrets.keyring`, its `install_id` and the `secrets.keyring.exported` record are
     neither read nor written, so a dry run cannot create the production keyring before the first real R2 boot (the
     first-run row above and §1.12.2's keyring-export timing stay true). A `--into` rehearsal copy
     (05-storage §5S.3.12 "Into an empty path") runs no M2 and gets no throwaway keyring: the rehearsal boots the
     binary normally in a container, with the copy's own `<dir>.state/`, never the live state directory.
2. `config.Load` registered `Ephemeral` values for secret keys supplied by env or file.
3. Runtime: `Get` decrypts on demand (no plaintext cache beyond the call; callers hold `Value` briefly).
   `Put`/`Stage`/`Delete` encrypt with the active data key. No background goroutine and no timer.

**The state-file table (normative; DATA-10).** These are the state files 05-storage §5S.3.12's pre-migration
checkpoint copies. The checkpoint copies each one that exists from its **resolved** path and records its label,
that path and its SHA-256 in the record's `state_files`. An offline restore of the checkpoint puts each file back
at the recorded path, and it never removes a state file the checkpoint lacks (the keyring a later boot created
stays).

| Label | Resolved path | Read or written by | Releases |
|---|---|---|---|
| `encryption_key` | HEAD's `.encryption_key`, searched in HEAD's order: the state directory (§1.7, C15's fallback included), else HEAD's fallback `<root_dir>/.appdata/.encryption_key` (`settings.go:110-140`); books resolves the path (it reads `root_dir` from HEAD's blob with the frozen codec) and declares it to the checkpoint | read by HEAD's code (R0, R1) and by M2 only; never written by core | R0–R2; R3 deletes it at its resolved path through `legacy-file-cleanup` (§1.12.2) |
| `keyring` | `<state_dir>/secrets.keyring` (`Options.KeyFile`) | created and read by `secrets.Open` (step 1) | from R2; the first R2 boot creates it after its checkpoint, so that checkpoint holds none |
| `gap_fill` | `<db parent>/config.yaml` (`persistence.go:29-38`) | read by M2, stripped by M2b (§1.12.2) | R0–R2; after M2b only an operator `--config` remainder, if any |

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
  material, so their secrets stay ciphertext (05-backup §5B.3.1; SEC10-8). Archives taken before R2 still hold HEAD's
  plaintext secrets and its gap-fill file's copies; R2's release note says to delete them once no longer needed
  (§1.12.2). No archive carries a copy of the keyring as its recovery path.
- Recovery is an explicit operator action: `<app> secrets export-keyring --out <file>` (CLI, filesystem access
  required, refuses to write a file looser than 0600). **It opens no store (SEC10-9):** it reads the keyring file
  and the `install_id` in the keyring header from the state dir only, takes no store lock and is not a foreign
  open, so it works while the server runs. The exported file carries, next to the keys, the install's `install_id` (the `c/storage/install`
  marker's, 05-storage §5S.3.4 rule 4; `Open` writes it into the keyring file's header when it creates the
  keyring or finds the field absent, so the export, which reads only the state dir, has it), so a host-loss
  restore of a marker-less (pre-R1) archive keeps the install's lineage when no revocation journal header is
  available (05-backup §5B.3.4 step 8, DATA3-12). It records the exported key IDs and time in
  `<state_dir>/secrets.keyring.exported` (no key material). Health warning `secrets_keyring_not_exported` stays raised while any key in the keyring is missing
  from that record.
- **Setting the header's `install_id` back (DATA8-12 (b), R8-1).** `<app> secrets set-install-id --install-id <id>`
  (CLI, offline: refuses while the server holds the store lock, as `rotate-derive` does) rewrites only the keyring
  header's `install_id` (temp file, fsync, rename, mode 0600; the keys are untouched, so the export record stays
  valid) and prints the old and the new value. It is the step of 05-storage §5S.3.15's manual factory-reset undo that
  restores the old install's lineage once the `previous/` directories are moved back: `<id>` is the one the swap
  journal's `set_aside` record names. It refuses an `<id>` that differs from the `c/storage/install` marker of the
  store at the resolved `core.storage.path` (read offline), so the header can only be set to the lineage of the store
  beside it, and refuses with no keyring file. ABO ships it from R2, when the keyring lands, as an entry of
  `app.OfflineCommands` (06 §6.4.5); before R2 the state directory has no keyring, so the command has nothing to
  set and the undo has no such step.
- **How an app's CLI reaches these commands (COR9-6, R9-9).** `secrets export-keyring`, `secrets rotate-derive` and
  `secrets set-install-id` are core commands an app cannot build itself. `export-keyring` opens no store (above).
  `rotate-derive` and `set-install-id` go through `kernel.OpenOffline`, which refuses while the server holds the
  store lock; `set-install-id` reads the install marker read-only (SEC10-9; 05-storage's `OfflineCommands` list
  names all three, cross-cluster). `app.OfflineCommands(opts, mods...)` (06 §6.4.5) returns them, with core's
  other offline commands, as `app.Command` values, and ABO wraps each in its cobra tree, as it does
  `storage restore-offline` and `users reset-password` (`TestCLIExposesCoreOfflineCommands`, ABO); 06 §6.4.5's
  list names all three secrets commands, each from R2.
- **Host-loss restore** ([05-backup](05-backup.md) §5B.3.3 "Host-loss restore"): before the first start the
  operator runs `<app> storage restore-offline --archive <file> --keyring <export>`, which puts the exported keyring
  back as `<state_dir>/secrets.keyring` (mode 0600) with the store; there is no separate import command, and the
  next `Open` loads it as the keyring-present case above. A pre-R2 archive (format `legacy-abo/0`) restores, and the
  next boot migrates it with M2, which decrypts HEAD's four encrypted rows with that install's `.encryption_key`,
  copied into the state directory beside the archive (09's R2 checklist takes it off-host with the archive), and with
  the gap-fill values of the archive's off-host `config.yaml`, which the restore installs from `--gap-fill <file>`
  (§1.12.2, DATA-15); the keyring holds no HEAD key. After R3 the R3 floor refuses such a store, so the owner
  installs R2 first, restores the archive with its off-host `.encryption_key` and gap-fill file, lets R2 migrate it
  once, then installs R3 again (09 §9.2.2).
  An archive encrypted only to the install identity needs the keyring to be
  decrypted at all; `--identity` remains only for operator-recipient identities. For an unencrypted archive, or
  one also encrypted to an operator recipient, copying the keyring back is **optional**: without it the restore
  still runs (05-backup `AcceptUnreadableSecrets`), the data restores, and the secrets it cannot decrypt show
  "must be re-entered". The archive's MAC is then unverifiable, so the restore is foreign (05-backup B5).
- Losing the state directory without an exported keyring means re-entering secrets (each affected secret shows
  "must be re-entered"); the data restores. Restore reports key IDs referenced by restored records
  (`KeyIDsReferenced`, computed by scanning, not from a manifest) that the current keyring lacks, before it
  starts; names whose trial decryption fails are reported as `ErrUnreadableSecrets`, which needs
  `AcceptUnreadableSecrets` (05-backup §5B.3.4 step 5; DATA4-22).

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
| Unreadable marker from the cutover (HEAD's row could not be decrypted, §1.12.2) | fail-open per secret: `Meta.Err = ErrKeyUnavailable` or `ErrCorrupt`, "must be re-entered", health `secrets_undecryptable`, the ciphertext in quarantine, boot continues | HEAD's guard without HEAD's deletion (`persistence.go:1253-1260`; DATA3-12) |
| `Ref.Resolve` with no value | `ErrNotSet` | consumer decides |
| `Value` JSON/text marshal | error `ErrNotSerializable` | accidental exposure fails the response instead of leaking |

### 2.6 Concurrency contract

`Store` is safe for concurrent use. Reads take a read lock on the in-memory keyring only; storage reads are
lock-free snapshots. `Put`/`Delete`/`Stage` serialize per name via a keyed mutex from the core-internal copy
`core/internal/keyedmutex` (04 §9; ABO keeps its own `internal/keyedmutex`, which core cannot import, COR2-16). The keyring changes only at `Open`. The store starts
no goroutine.

### 2.7 Config keys

| Key | Type | Default | Hot reload? | Protection |
|---|---|---|---|---|
| `core.secrets.key_file` | path (relative to state dir) | `secrets.keyring` | no | operator |
| `core.secrets.allow_key_regen` | bool | `false` (HEAD's `ABK_ALLOW_ENCRYPTION_KEY_REGEN` is a retired name and refuses boot, §1.3.6); also M2's override of HEAD's key guard (§1.12.2) | no | operator |

### 2.8 Metrics

All on scope `mediacore.secrets`.

| Name | Type | Labels |
|---|---|---|
| `mediacore_secrets_operations_total` | counter | `op` ∈ get, put, delete; `outcome` ∈ ok, not_set, key_unavailable, corrupt, out_of_scope, error |
| `mediacore_secrets_undecryptable` | gauge | — |
| `mediacore_secrets_keyring_keys` | gauge | `use` ∈ data, derive; `status` ∈ active, verify, retired |

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
| `TestGet_ReadOrder` | operator > record (cleared marker = `ErrNotSet`, unreadable marker = `Meta.Err`) > `ErrNotSet` |
| `TestStageUnreadable_ReadsReenter` | a staged unreadable marker reads `ErrKeyUnavailable` or `ErrCorrupt` with health `secrets_undecryptable`; a `Put` replaces it and clears the token |
| `TestExportKeyring_OpensNoStore` | with the server holding the store lock, `secrets export-keyring` succeeds, reads only the state dir and the keyring header's `install_id`, and the next boot sees no foreign open; `rotate-derive` and `set-install-id` are refused while the lock is held (SEC10-9) |
| `TestEphemeral_NeverPersisted` | env secret never reaches storage |
| `TestFingerprint_StableNotReversible` | same value same fingerprint; differs per keyring |
| `TestExportKeyring_ClearsHealthWarning` | export record covering every key ID clears `secrets_keyring_not_exported` |
| `TestRotateDerive_VerifyThenRetire` | after `rotate-derive`: `DeriveKey` uses `d2`; `DeriveKeyFor("d1", …)` works until `retire_after`, then `ErrKeyUnavailable`; data records still decrypt; refused while the store is locked; export warning raised |
| `TestRotateDerive_NotRetiredWhileArchiveUsesIt` | a `verify` key past `retire_after` stays `verify` while the in-use check reports a retained archive under it (health `derive_key_retire_blocked`); after the rewrap it retires |
| `TestRotateDerive_JournalStaysVerifiable` | after `rotate-derive` and the rewrap, `d1` stays `verify` while a retained revocation-journal line (or the header) names it; `Open` retires nothing; `RetireUnused` in P5 sees the overridden `core.backup.dir` (DATA3-13, SEC3-10, COR3-2) |
| `TestOpen_ResetBootRewritesInstallID` | with `ResetBoot` the header's `install_id` becomes the fresh store's; without it an existing header field is never changed (DATA7-17) |
| `TestSetInstallID_RestoresLineageOffline` | after a reset and a manual undo, `secrets set-install-id` with the `set_aside` record's ID rewrites only the header's `install_id` (keys and export record unchanged); it refuses while the server holds the lock, an ID that differs from the store's marker, and a missing keyring (DATA8-12) |
| `TestStage_TakesBatchPointer` | `Stage` writes and its in-place `Write` commit with the caller's batch and are refused outside its bound keyspaces (COR2-12) |

ABO's cutover tests are in §1.12.2.

### 2.10 Migration from ABO

The data half (HEAD's four encrypted rows and every plaintext blob secret re-encrypted into `c/secrets/`, the
gap-fill copies stripped) is specified once, in §1.12.2. Code replacement:

1. Every secret uses `secrets` (`Put`/`Stage`, `Ref` fields). ABO's `database.EncryptValue/DecryptValue/
   InitEncryption` and `guardAgainstKeyRegeneration` (`settings.go:110,189,214`, `cmd/root.go:735`) leave ABO's boot
   path in the R2 config flip; a frozen copy of the decrypt half and of the guard lives on inside the cutover step
   M2 only, with HEAD's key search order (state dir, then `<root_dir>/.appdata/`, `settings.go:110-140`), and R3
   deletes it with the other HEAD-format readers.
2. `MaskSecret/IsEchoedMask/LooksLikeMask` (`settings.go:271,285,293`) are deleted in the config flip F3, when
   ABO's `/api/v1/config` becomes write-only for secrets (§1.12.1); the core settings API is write-only (C9) and
   returns `Meta`, never a mask.
3. HEAD's file-only secrets (ABS JWT secret, Cloudflare policy token, SMTP password) were never stored, so nothing
   migrates. Their HEAD env names are retired secret names, so a unit that still sets one refuses to boot naming
   the canonical name (§1.3.6, §1.12.4); the operator moves the **same value** to it. For the ABS JWT secret the
   value must not change, because it signs the tokens the ABS phone apps hold (D12 (c)). From R2 the SMTP password
   may also be stored, encrypted, through Settings. The ABS JWT secret and the Cloudflare policy token may not: they
   are `ProtectOperator` and are set only through their canonical operator names, as at HEAD (§1.3.7; SEC-6).

### 2.11 Open items

1. **Rotation and prune (deferred, SIMP-19).** Not built in v1: after a suspected key leak the operator re-enters
   secrets. Constraints already fixed for when it is built: records name their `kid`, so rotation is "new active
   data key + re-encrypt"; the derive key is rotated by its own command, which ships in v1 (`rotate-derive`,
   §2.3), because rotating it invalidates derived artifacts; pruning a retired key is safe only when
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

None open. 03 §3.3's store-interface rows for `config` and `secrets` name their importers (`config.Importer`,
`secrets.Importer`, §1.3.3, §2.3). The round-0 items (an internal order inside the old shared
layer; the §3.3 `config` row not saying that operator-set keys are locked) are resolved by 03's R4 L1/L2/L3 and its
§3.3 `config` row; 03 R5 also states that ABO's façades are allowed.

## Out-of-scope defects found

1. Eight writes to `config.AppConfig` bypass `Mutate` (§1.2 anchors): unsynchronized, and invisible to
   `config.Generation()` consumers (`internal/ai/ai_metrics.go:169`). Fixed in migration stage 0 (R1) because the
   façade depends on it.
2. Three encrypted secrets are also persisted in plaintext to `<db parent>/config.yaml` on every settings save
   (`persistence.go:168-178`), which defeats their encryption. Fixed by the cutover: M2b strips the copies and
   core never writes the file (§1.12.2).
3. Secret ciphertext has no associated data (`settings.go:189-208`): rows are swappable between keys. Fixed by
   the cutover: every secret is re-encrypted with an AAD of app and name.
4. `LoadConfigFromDatabase` deletes secret rows it cannot decrypt or recover (`persistence.go:1253-1260`,
   `DeleteSetting` at `:1258`), turning a temporarily missing key file into permanent loss when the guard is
   overridden. M2 quarantines such a row instead (§1.12.2).
5. The shipped `docker-compose.yml` sets `AO_DIR` and `AO_DB` (`:18-19`) and `deploy/local.conf.example` sets
   `OLLAMA_BASE_URL` (`:40`), but no Go code reads any of them at `748fd787a`: a compose user's database is not
   where the file says. R2's deploy-file rewrite removes them and uses canonical names (§1.12.4).
6. HEAD's `POST /system/reset` (`ResetSystem`, `internal/server/handlers/system/handler.go:463-491`) wipes the store
   but leaves the gap-fill file `<db parent>/config.yaml`, so after the next restart HEAD's gap fill brings the old
   install's OpenAI, Google Books and Hardcover keys back (`persistence.go:42-104`). After R2 there is no gap-fill
   reader and no such copy (M2b), so a core reset cannot bring them back.

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

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): §1.12.2: from S7 the legacy `PUT /api/v1/config` hands integration-bound fields to `integrations.Registry.Put`; §2.4: the keyring file and its export carry `install_id` (DATA3-12).

Round 4:

- R4-1 / SIMP4-2 / SIMP4-3 — C4, C11, C12, §1.3 imports, §1.3.3: `config.OverrideStore` becomes the SDK store interface (with `Open`, `OpenEnv`, `Confirm`, `Secrets`, `Suspended`) built from `Manifest.LegacyStores.Config` or natively; kernel `Committer`; `config.Legacy` shrinks to the aliases of core keys; `LegacyBlob`, `LegacySecret` and the core-side legacy-format adapter deleted; native store over `c/config/` for apps without legacy settings; §1.12.2 rewritten as the contract of the books legacy config store (ABO code over ABO keys; native-only keys in a books-owned extension family with a basis hash that suspends a value an older build invalidated; no core grant); `configtest.OverrideStoreSuite`, `TestNativeOverrideStore_RoundTrip`.
- SIMP4-11 — `Legacy.Rows` and `LegacyRows` deleted (the S2 bridge is 06's `app.Options.LegacyConfigStore`, deleted at S2b).
- COR4-2 — `OverrideStore.Changed` is the change source at S2 and S2b (§1.4 step 8, §1.12.2); `TestOverrideStore_ChangedRepublishesAtS2AndS2b`.
- COR4-3 — `Legacy` (aliases) and the books store stay until the settings cutover; nothing in this file drops it at S3 (09's sentence is a cross-cluster request).
- R4-2 / SEC4-1 / DATA4-1 / OPS4-1 / DATA4-3 — C16; §1.12.2 secrets rule: every legacy copy (row or blob field, and the `config.yaml` copy) gets a fresh never-matching sentinel, never an empty value; roll-forward re-import when the legacy copy differs; effects on older builds (Basic gate, webhook secret, gap-fill keys) listed; release note and R-test cells; §2.2 webhook secret row; §2.3 record `sentinel`, `LegacyStore.Supersede`, read order; §2.4 reconcile; §2.5 rows; tests.
- R4-6 / COR4-1 — C17; `OverrideStore.Open`; §1.4 step 5; §1.12.2 "ABO's boot steps in `Open`": ABO's key guard, encryption init and (S2b–S3) settings load run inside the books store's `Open` at P4d step 1 over books' own handles; failure is fatal; `TestS2bAppConfigFromBlob` named as an S2b done-check.
- COR4-10 — §1.4: `core.backup.dir` is declared from S4 by `backup/journal`; `RetireUnused` is skipped before S8.
- COR4-20 — §1.3.6: the ledger-row check starts at S2b; §1.7: `core.storage.path` is a kernel pre-boot key from S2; `TestStoragePathPreBootFromS2`.
- DATA4-4 — §1.12.2 "Exclusive writer from S3": ABO archtest plus `set-internal-flag` refusing the settings rows, instead of a grant mode.
- DATA4-22 — §2.3 `LegacyStore.Check`; §2.4 restore staging trial-decrypts with `k0-legacy`; `TestLegacyCheck_TrialDecryptForeignArchive`.
- OPS4-2 — §1.7: no state-directory `ENV` in the Dockerfile or plist; optional compose-only value; S0 image job's second run.
- OPS4-6 — §1.12.2 and §2.9: the rollback-matrix tests run in root `make ci` against `prev` built from `PREV_REF`.
- OPS4-8 — §1.3.3 and §1.7: the updater-marker clause deleted; one resolver from S2 (`serve` calls `config.ResolveStateDir`); `TestStateDir_OneResolverYAMLDBPath`.
- OPS4-11 — §1.12.2 "Stated consequences" for 09 §9.2.1's table, including the older build's reset.
- SEC4-7 — §1.3.7: integration-bound blob fields are `ProtectAdmin` on the façade from S3 to S7, and a URL-origin change clears the stored secret unless re-supplied; `TestPreS7Facade_IntegrationURLIsProtectAdmin`.
- SEC4-8 — §1.3.7: with auth off, protected keys change only through operator layers (turning auth on by env), recorded for 09 §9.2.6.
- SEC4-10 — §2.2 and §1.12.3: one name, `core.http.basic_gate.password`; `basic_auth_*` moved to its own row (05-http-api).
- SIMP4-10 — §1.3.6: core's ledger fragment holds only permanent `OTEL_*` rows; `TestCoreLedgerFragmentPermanentOnly`.
- MOD4-1 — §1.3.6: ABO embeds a generated, CI-checked copy of its ledger kept under the books module (R4-4).
- SIMP4-12 — C11: integrations is the registry; ABO's instances are legacy-backed through books' own instance store sharing this store's render (03's row is another cluster's).

Integration pass (round 4):

- integration pass (round 4): §1.7 names `app.ResolveStateDir(opts)` (06 §6.4.5) for ABO's S2 serve; §1.12.2: `set-internal-flag` refuses every 05-storage row-7 key, the extension family and every store-interface key; `TestOnlyLegacyConfigStoreWritesSettingsRows` is an instance of 09's `TestLegacyFamiliesWrittenOnlyByLegacyStores`.

Round 5:

- R5-1(a) / OPS5-1 — C3, §1.3.1 `Alias.BelowStored`, §1.3.5 "HEAD precedence for legacy names" and "Empty env values", §1.12.2 reading, §1.12.4: a legacy env name or file key HEAD lays the blob over feeds the default layer and locks nothing until cutover (generated from HEAD's lists; `BASIC_AUTH_*` marked); `""` is unset; legacy flags' change recorded for 09; `TestAlias_BelowStoredFeedsDefaultLayer`, `TestEnv_EmptyValueIsUnset`, `TestComposeEnvKeepsUIBasicGate`, `TestAliasBelowStoredMatchesHEAD`; secrets `EphemeralDefault`.
- R5-1(b) / OPS5-2 — C8, C11 (the OpenAI, Google Books and Hardcover secrets stay in place, no sentinel or re-import), C16, §1.12.2 value table, secrets rule, effects, rollback matrix, consequences, R-test cells, release note, cutover, §2.2, §2.3 (`LegacyStore.Kept`/`Write`, read order, record rule), §2.4, §2.5, §2.9, §2.10, §2.11, out-of-scope defect 2: the four HEAD-encrypted secrets are kept and written in place (row plus gap-fill file); only plaintext blob secrets move and get a sentinel; the secondary-copy sentinel machinery is gone.
- R5-1(c) / SEC5-4 / DATA5-12 — §1.12.2 effects and roll-forward: an enabled gate with an unset, empty, unreadable or cleared password stays closed (05-http-api stage 8); R-test cell `TestRollback_BasicGateClearedStaysClosed`.
- COR5-2 — C17, §1.3.3 `LegacyOpener`, §1.4 step 5, §1.12.2, §2.4 step 1: `LegacyOpener.Open` (loads ABO's key) before `secrets.Open` (keyring, `k0-legacy`, re-import; no derive-key retirement), aligned with 06 §6.5.1; `TestP4dStep1_LegacyOpenBeforeSecrets`.
- R5-3 / COR5-1 / DATA5-11 — §1.6, §1.12.2: the config rows refuse unchecked writes only from S3; `Open` runs HEAD's load without its corrupt-secret repair; `TestLegacyOpen_NoCorruptSecretRepair`.
- COR5-14 — §1.3.3: `OverrideStore` lands at S2 with `Load`; native store and `LegacyStores.Config` at S2b; `Stage` and the S3 interfaces at S3.
- SIMP5-4 — §1.3.3: `OverrideStore` keeps only `Load`/`Stage`; `LegacyOpener`, `ExternalWriter`, `LegacySecrets`, `Suspender`, `CommitObserver` are optional interfaces removed at cutover; `configtest.LegacyOverrideStoreSuite`; references updated.
- SIMP5-5 — §1.3.3, §1.4 steps 3 and 8, §1.12.2: `OpenEnv.Stage` deleted (constructor option at S2); `Changed` is `ExternalWriter`, deleted in the S3 PR.
- SIMP5-8 — §1.3.3 `LegacySecrets`: `secrets.LegacyStore` reached only through it, under 03 §3.3's config row.
- SIMP5-10 — §1.3.3: `app.Options.LegacyConfigStore` named as the sole exception to 03 §3.3.
- MOD5-2 / R5-2 — §1.12.2 "Package": the books store, legacy instance store and books-root source live in `internal/books/configstore`.
- DATA5-10 — §1.12.2 consequence (b) corrected for factory reset; §2.3 recognisable `mc-sentinel:` prefix read as unset; §2.5 row; `TestOrphanSentinelReadsUnset`.
- DATA5-16 — §1.12.2: `legacy_hash` → `legacy_ref.fp`.
- DATA5-20 — §1.12.2: gap-fill file rewrites serialised under one mutex, re-read under the lock; `TestGapFillFileRewriteSerialised`.
- OPS5-7 — §1.7: the image job's second run checks `.encryption_key` from S0 (resolver changes at S2), `secrets.keyring` from S3, on a volume chowned to the image's uid.
- OPS5-13 — §1.3.3, §1.4 step 2, §1.7, §1.11: `ResolveStateDir` reads the database path from the operator file too (HEAD parity, no behaviour change); one test name.
- SEC5-15 — §1.12.1 stage 1: the S3 façade's actor (seed `admin` role or auth off; HEAD's interactive rule); native rule from S4 r2; S3 and S4 variants of `TestPreS7Facade_IntegrationURLIsProtectAdmin`.

Round 6:

- SIMP6-1 — §1.3.1, §1.3.3, §1.4 step 6, §1.5, §1.8, §1.11, §1.12.2 (value table, native-only keys, reading, rollback matrix, consequences): the config basis mechanism (`config.Basis`, `BasisClass`, `BasisDerived`, `BasisWidening`, `Store.Confirm`, `AttachReport.Suspended`, the suspender capability, `core.settings.confirm`, `config_extension_suspended`, the suspended gauge, the suite case) moved to the new §1.12.5 "Deferred-build members"; every native-only key is a standalone record.
- R6-3 (SIMP6-2, SIMP6-3) — §1.3, §1.3.3: the config capabilities move to package `legacy` (03 §3.3) as `legacy.ConfigOpener`, `ConfigOpenEnv`, `ConfigExternalWriter`, `ConfigSecrets`, `ConfigCommitObserver`, and `secrets.LegacyStore` becomes `legacy.SecretsStore`. The kernel drives `Open` and `Secrets`; config, its committer and `secrets` consume the others through unexported mirrors checked by `export_test.go`. Config no longer imports `secrets.LegacyStore`. C17, §1.4, §1.5, §1.11, §1.12.2, §2.2–§2.5, §2.9 renamed; cutover removal is non-breaking. The value types `LegacyCopies` and `LegacyCheck`, and `ReadLegacyKey`, stay in `secrets` (new objection to 03).
- SIMP6-2 — §1.3.3 and §1.12.2 cutover: removing the capabilities is not a v1 break because `legacy` is outside the API-stability gate.
- SIMP6-4 — §1.12.5: when built, config confirmation uses `storage.ExtConfirmer`, and `legacy.ConfigSuspender` keeps only `Suspended()`.
- SIMP6-6 — §1.11: one legacy suite per store interface, `legacytest.ConfigOverrideStoreSuite`, with a case per capability.
- SEC6-1 / R6-6 — C3, §1.3.1 `config.SafeLegacyValue` and `Alias.BelowStored` comment, §1.3.5, §1.3.7, §1.5, §1.12.4: a legacy value that enables auth is an operator-layer value (beats a stored `false`, locks the key); a disabling one stays `BelowStored`; `AttachReport.Shadowed` and health `config_legacy_name_shadowed` for shadowed `ProtectAdmin`+ legacy names; `TestSafeLegacyValue_EnableBeatsStored`, `TestAttach_LegacyNameShadowedReported`, `TestAuthOn_LegacyEnableAuthBeatsStoredOff`.
- SEC6-5 — C11, §1.3.3 `OperatorValue.BelowStored`, §1.12.4: Passthrough names follow `BelowStored` (fill only a field with no stored value); private-network trust only when the effective field's `Source` is an operator layer; `TestPassthroughValues_ReportBelowStored`.
- COR6-5 — §1.12.2 "`Open` writes nothing": reshaping `migrate*Blob` are pure read transforms; the three marker-guarded one-shots become books versioned migrations that write HEAD's markers; read transforms consult marker or applied record; `TestFreshInstall_EmbedCoverArtFalseSurvivesRestart`.
- COR6-8 — §1.3.3, §1.4 step 3, §1.12.2: `legacy.ConfigOpener` lands at S2b; at S2 the kernel calls no `Open`; the S2 constructor option is gone.
- DATA6-11 — §1.12.2: the corrupt-secret repair never runs on a core build; it is not a writer a later stage converts.
- DATA6-6 — §1.12.2 "A core build's factory reset": storage's staged reset, boot record set aside, keyring and legacy key kept (owner decision for 14), header `install_id` rewritten only by the reset boot, gap-fill file reset by books' handler; `TestFactoryResetClearsGapFillSecrets`.
- DATA6-12 — §1.12.2: the S3 `CheckedWrites` flag starts with 05-storage's count-only release (consistency).
- MOD6-11 — §1.3.3, §1.4 step 6, §1.12.1, §1.12.2, §2.3: core's `core/internal/configstore` and ABO's `internal/books/configstore` always written in full; the façade is a `Facade` type in ABO's `internal/config` (no `legacyconfig` package).
- OPS6-4 / R6-7 — §2.2: one generated secret-class list in `abo_keys.golden` for 09's staging scrub; `TestSecretClassListMatchesLegacyStore`.
- OPS6-7 — §1.3.5: `BelowStored` applies wherever core config attaches the legacy store (S2 on), to env names and file keys, never flags.
- integration pass (round 6): key-map row: `basic_auth_username`/`BASIC_AUTH_USERNAME` are `BelowStored` aliases and the empty-username default keeps HEAD's behaviour (SEC6-9, accepted deviation); factory reset: keeping the keyring is the lead's adoption of 14 Q-J (a); `legacy.Handles`, `legacy.ExtConfirmer`, `legacy.Extension` and `legacy.RootSource` replace stale `storage.*`/`library.*` spellings, and books' configstore imports `legacy`; the auth-on test is `TestAuthOn_LegacyAliasEnablesOverStoredOff` (05-authn owns it); Objection 1 resolved by 03 v1.9. Already applied: `maintenance_window_migrated` among the marker one-shots that become books migrations (COR6-5).

Round 7:

- SEC7-1 / R7-1 — C3, C13, §1.3.1 `config.NarrowStored`, §1.3.3 `Store.NarrowStored`, §1.3.5 "Operator tightening survives a rollback", §1.3.7, §1.4 (constraint, step 6b), §1.5 rows, §1.11, §1.12.2 (writing, release note, R-test cell), §2.9: an operator value narrower than the stored legacy field of `core.auth.mode` (from S4 r1, per R-D: r1's `AppConfig.EnableAuth` follows the effective key) or `core.http.basic_gate.enabled` (from S3, only with a stored password) is written into that field at P4d step 1, audited `core.config.legacy_narrowed`; `TestNarrowStored_WritesOnceAudited`, `TestRollback_OperatorEnabledAuthStaysOn`.
- SEC7-2 / R7-1 — C3, C4, §1.3.5, §1.4 step 9, §1.5, §1.11, §1.12.2 reading and writing: a stored value equal to the default still beats a `BelowStored` value and reports `SourceDefault` only when no operator or `BelowStored` value is set; the shadow check compares with it; an unset of a legacy field writes the default explicitly and never removes the field; `TestBelowStored_StoredDefaultStillWins`, `TestLegacyUnsetWritesDefault`.
- SEC7-3 — §1.3.7, §1.12.4: operator-sourced secrets are withheld when a destination field (URL, host or port) is not operator-sourced or names another origin (`ErrOperatorSecretWithheld`, `operator_secret_withheld`), citing 05-metadata M1.4 step 2 (cross-cluster request from R-F).
- SEC7-12 — §1.3.5: `config_legacy_name_shadowed` is carried by HEAD's degraded token list until S8.
- R7-2 (COR7-1, SEC7-4, SEC7-5, DATA7-6, DATA7-17) — §1.3.3 `legacy.ConfigOpenEnv.ResetUnfinished`, §1.12.2 "A core build's factory reset", §2.3 `secrets.Options.ResetBoot`, §2.4, §2.9: books' gin handler only calls `legacy.Maintenance.StageFactoryReset` until S4 r2, the core route lands in `internal/storagehttp` at S4 r2; no `ResetToDefaults`/`SaveConfigToDatabase` before the restart; the gap-fill file reset, OL data deletion and `RootDir` wipe (Q-K) run in books' post-reset hook after `applied`; no gap-fill copy is served until the hook completes; the reset boot rewrites the keyring `install_id` before the journal is marked `applied`; undo claim reworded; `TestOpen_ResetBootRewritesInstallID`.
- DATA7-13 — §1.12.2 item 2, §2.9: the three marker one-shots are `Seed` books migrations numbered from `LegacyCap+1`, so a fresh install writes HEAD's markers; `TestFreshInstallWritesHEADMarkers`.
- R7-7 (SIMP7-1, COR7-2, SIMP7-4) — §1.3.3, §1.12.2 cutover, §2.3, §2.4, §2.5, §2.9: every "`export_test.go` assertion" is the mirror test (03 §3.3); `secrets.Options.LegacyStore` is typed `any`, asserted in `Open`, `ErrLegacyStoreType` on a mismatch; the cutover wording names `legacy` only; `TestOpen_LegacyStoreTypeMismatchFails`.
- COR7-3 / SIMP7-5 — §1.3.3: the kernel asserts only the two capabilities it drives; config detects `ConfigExternalWriter` itself (P3 at S2, P4d step 1 at S2b), the committer `ConfigCommitObserver`, secrets its mirror.
- R7-6 (OPS7-4, SEC7-10) — §2.2, §2.9: `scripts/staging/secret_fields.txt`, written by hand from §2.2 at S0, read by ABO's `staging-scrub`; from S3 `TestSecretClassListMatchesLegacyStore` asserts it equals the generated list.
- SIMP7-9 — verified: this file cites the basis deferral as §1.12.5 everywhere; the wrong citations are in 03 and README (cross-cluster request).
- Header — 03 reference v1.9.1.
- SIMP7-8 — §1.12.2 package paragraph: `legacy.RootSource` → `legacy.LibraryRootSource` (initialism naming rule, cross-cluster note from R-D).
- integration pass (round 7): §1.3.3 `legacy.ConfigOpenEnv.Reset *legacy.ResetRun` beside `ResetUnfinished`, books' legacy config store implements `legacy.ConfigResetArgs` (R-B request); secrets defines no sentinel of its own: `storage.ErrLegacyStoreType` throughout §2.3–§2.9 (R-G request); factory-reset paragraph: `reset_pending`/`reset_inflight`, the set-aside path `<db parent>/.mediacore-reset/<id>/previous/`, test name `TestFactoryResetRejectedLeavesStoreAndSettings` (09's); §1.12.2 package paragraph drops the former name.

Round 8:

- R8-1 / SEC8-5 — §1.12.2 "The reset boot stages HEAD's factory defaults", C13, §2.9: "settings are the build's defaults without any write" is replaced; books' `ResetArgs` renders HEAD's `factoryDefaultConfig` blob (`root_dir` empty, `setup_complete` false), the intent records it and the reset boot's fresh-install batch writes it, for both kinds; `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`.
- R8-1 / SIMP8-1, COR8-1 — §1.12.2: the reset is `KindReset` of the one swap, kinds `ResetStore` (HEAD's `POST /system/reset`) and `ResetFactory`; one intent slot `restore/pending.json`, tokens `restore_pending`/`restore_inflight`, one rejection rule; both legacy routes stage, and both become `Alias`es at S4 r2; consequence (b) cites both HEAD callers.
- R8-1 / COR8-2, COR8-5 — §1.3.3 `ConfigResetArgs`, §1.12.2 "Who stages": the kernel's one staging function asserts and calls `ResetArgs(kind)` and passes the result in, so no L1 code names `legacy` and the S4 r2 route records the same arguments.
- R8-1 / COR8-3, DATA8-1 — §1.12.2: the boot record is set aside beside itself; a failed set-aside moves back, journals `rejected` and boots `degraded`.
- R8-1 / COR8-4, OPS8-5 — §1.3.3 `ConfigOpenEnv.Reset`, §1.12.2: kernel-owned `hook_done` for `ResetStore` and hookless apps; a dangerous `RootDir` answers 400 at staging; the hook never fails boot (`factory_reset_cleanup_incomplete`).
- SIMP8-7 — §1.3.3, §1.12.2, cutover paragraph: `ResetUnfinished` deleted (`Reset != nil`); `ResetRun` → `ConfigResetRun`; the cutover must re-home or end the Open Library and `RootDir` wipes.
- SIMP8-3 — §1.3.3 (two places): "the kernel asserts only the capabilities 03 §3.3 lists", naming `ConfigResetArgs` as asserted at staging.
- R8-4 / SEC8-1 — §1.3.1, §1.3.5, §1.12.2 R-test cell and release note: the Basic-gate narrowing has no `when` and narrows whenever the value is `true`, with the fail-closed sentence; the false rationale is deleted.
- R8-4 / SEC8-6 — C3, §1.3.1, §1.3.3 `Store.WriteOperatorNames`, §1.3.5 "Canonical-only operator values", §1.3.7, §1.4 step 6c, §1.5, §1.11 `TestOperatorNames_NamesOnly`, §1.12.2: `core.http.rate_limit.enabled` narrows; every boot from S3 writes `<state_dir>/operator-names.json` (names only), reported as `canonical_only_operator_values`.
- SIMP8-6 — §1.3.1, §1.3.5, §1.11: a nil `narrow` means the key's `SafeLegacyValue` predicate; `core.auth.mode` declares it once. With no `when` left in use, the `when` parameter is deleted (03 §3.4).
- SEC8-9 — §1.3.3, §1.3.5, §1.4 step 6b, §1.5, §1.11: an owed narrowing raises `config_narrowing_owed` and is listed in `operator-names.json`.
- SEC8-12 — §1.3.1, §1.3.5, §1.5, §1.11, release note: an absent legacy field counts as the legacy default; an absent blob is never created.
- R8-5 / SEC8-2, SEC8-3 — §1.3.7, §1.12.2, §1.12.4: origin-plus-provenance (`OperatorSecretOrigin`) replaces withholding by source; 403 `ErrOperatorSecretBound` for a non-admin destination change of a legacy-backed instance until the integrations cutover, the legacy path included (R-F request).
- COR8-9 / DATA8-13 — every body occurrence: the gap-fill file is `<db parent>/config.yaml`.
- Out-of-scope defect 6 — HEAD's `ResetSystem` leaves the gap-fill file, kept by `ResetStore`.
- Header — 03 reference v1.10.1.
- integration pass (round 8): §1.3.3 `ConfigResetArgs` is called for both reset kinds and is declared, with `legacy.ConfigResetPlan{Hook, Rows}` and `legacy.ConfigResetRun` (`Done(ctx, failed)`) mirrored from 05-storage §5S.3.15 (R-B request); §2.4 defines `<app> secrets set-install-id` (offline, header `install_id` only, checked against the store's marker), used by 05-storage's manual reset undo and cited from §1.12.2, with `TestSetInstallID_RestoresLineageOffline` (R-B request); §1.3.7 and §1.12.4: the `ErrOperatorSecretBound` wording matches 05-metadata M1.4 step 2 (sweep (e)); §2.2 staging paragraph: `staging-scrub` clears the legacy integration URLs and runs with the archive's binary (sweep (f)).

Advisor:

- A-2 (ADV-S1, ADV-3) — C3, C4, C8, C11, C13, C16, §1.3.1, §1.3.3, §1.3.5, §1.3.7, §1.4, §1.5, §1.11, §1.12.1, §1.12.2, §1.12.4, §2.1–§2.10: books' legacy config store keeps HEAD's representation through S10. Plaintext blob secrets are written in place, the four encrypted secrets stay in their rows, and `secrets` encrypts only native data. HEAD's auth-enable precedence is kept (a stored value beats `ENABLE_AUTH` either way). Deleted: moved-secret sentinels, the re-import rule and roll-forward reconcile, `legacy.SecretsStore`'s `Has`/`Supersede`, the record and cleared-marker sentinel fields, `config.SafeLegacyValue` (R6-6's exemption), `config.NarrowStored`, `Store.NarrowStored` and the narrowing writes (R7-1, R8-4), `Store.WriteOperatorNames` and `operator-names.json` with its tokens, boot steps 6b and 6c, `OperatorSecretOrigin`/`ErrOperatorSecretBound` for legacy-backed instances (their secrets go to the effective destination as at HEAD; the façade's integration destination and secret fields stay `ProtectAdmin`), the books extension family for config, and the related tests, release notes, R-test cells and error rows. Added: one storage rule (a key with a legacy name only in HEAD's row and format; a key without one natively in `c/config/` through the kernel's routing store, lead decision) with `TestRoutedStore_OneStorageRule`, a canonical-only rollback consequence for 09, `TestLegacyAuthEnablePrecedenceMatchesHEAD`, `TestRollback_PlaintextSecretSavedOnCurWorksOnPrev`, and the §1.12.5 "Secrets and precedence cutover (post-S10)" section, which replaces the deferred basis members (`config.Basis*`, `Store.Confirm`, `AttachReport.Suspended`, `legacy.ConfigSuspender`, `legacy.ExtConfirmer` use), deleted with the extension family.
- A-7 (ADV-2) — §1.3.1 `Alias.Decode` and `LegacyDecode`, §1.3.6 "Legacy decoding", §1.5, §1.11, §1.12.1, §1.12.2 release note: legacy names decode with HEAD's per-type semantics (`ParseBool` spellings, whitespace-split slices, Go durations with bare nanoseconds, cast integers), canonical names strictly; a silent-false legacy bool on a `core.auth.*` or `ProtectAdmin`+ key is a boot error; `TestLegacyEnvDecodeMatchesHEAD` (HEAD's `InitConfig` from `PREV_REF`, frozen as a golden by the S3 PR; in `extraction-gates` from S3) and `TestLegacyBool_SilentFalseOnAuthKeyFailsBoot`.
- A-3 (ADV-S2) — §1.6, §1.12.2: no storage-enforced `CheckedWrites` on the blob and secret rows during the extraction; HEAD's save mutex and the single-writer ratchet stay; the count-only release and strict switch for these rows deleted.
- A-14 (ADV-S8) — §1.3.3 `OnLegacyUse`, §1.3.6: alias uses feed compat's process-local counter only; the per-boot presence record and use record deleted; removal follows W's dates and the owner's check.
- A-10 (ADV-6) — §2.2: `staging-scrub` refuses a store without 09 §9.3's staging marker.
- ADV-8 (A-16) — §1.12.2, §2.9: no moved secret and no core-written key in `<db parent>/config.yaml`; the merge writes only HEAD's `SaveConfigToFile` keys; `TestGapFillFileWritesOnlyHEADManagedKeys`.
- Header — 03 reference v1.11.1.
- integration pass (advisor): §1.3.7 auth-off rule restated per the lead decision (ABO's `core.auth.mode` keeps HEAD's `!prior.EnableAuth` rule, `internal/config/update_service.go:781`; other protected keys refused with auth off; native apps keep SEC4-8) and §1.12.1's S3 actor note aligned; §1.3.6 states env names are case-sensitive (ADV-2); §1.12.5 names H2 and H3 as 09 §9.13 does and the precedence cutover is H3 throughout; `TestCoreWritesOnlyHEADGapFillKeys` is the gap-fill test's name (09's); 09 §9.2.1 row 3 cited; `TestLegacyEnvDecodeMatchesHEAD` confirmed.

Round 9:

- R9-5 / SEC9-1 — §1.3.1 `Alias.HEADAuthOffWritable`, §1.3.7, §1.12.1, §1.12.2 R-test cells, §2.9: on ABO the Basic gate keys keep HEAD's auth-off write rule like `enable_auth` (stored into HEAD's `basic_auth_*` fields and encrypted row); "keep the legacy name set" replaced by §1.3.5's wording; `TestRollback_AuthOffBasicGateStaysOnPrev`.
- R9-5 / SEC9-2 — C3, §1.3.1 `BelowStored`, §1.3.5, §1.12.5: H3 moves only enabling `enable_auth` precedence; every other `BelowStored` flag keeps HEAD's precedence until a separate, owner-decided cutover, bound by "a name a shipped deploy file always sets stays `BelowStored`" and "a weakening legacy value never locks"; H3's done-check includes `TestComposeEnvKeepsUIBasicGate`.
- R9-5 / DATA9-6 — C4, C16, §1.3.1 (`Alias.Legacy` split into `LegacyYAML` and `LegacyBlob`; derived `FileOnly()`), §1.3.3 routing, §1.3.5, §1.3.6, §1.3.7, §1.12.2 (value table, storage rule, perimeter-edit sentence, release note, R-test cell), §1.12.4, §1.11, §2.2, §2.9, §2.10 item 3: HEAD's `json:"-"` keys (peer allow-list, tunnel connectors, ABS JWT secret, …) keep HEAD's storage, file and env only, never stored, operator-only while the legacy store is attached; `TestFileOnlyLegacyKeysNeverDBStored`, `TestFileOnlyKey_OperatorWhileLegacyStoreAttached`; the peer allow-list removed from the UI-edit examples.
- R9-6 / SEC9-4 / COR9-4 — §1.3.3 `ConfigResetArgs.ResetArgs(ctx, storage.ResetRequest)`, `ConfigResetPlan.LibraryRoot/LibraryFiles`, `ConfigResetRun.DeleteLibraryFiles`; count of `RootDir`'s top-level entries, exact; mismatch → 422 `library_files_confirm_required` with nothing staged; dangerous-root check only with the wipe; legacy paths keep HEAD's wipe; §1.12.2 "Who stages" and the hook's wipe follow A-17; the Q-K sentence deleted.
- R9-1 / DATA9-15 / R-F request — §1.3.3 `Committer`, §1.6: HEAD's `configBlobSaveMu` stays, exposed by the books config, instance and notify-rule stores through `legacy.WriteLock` and held across `storage.CommitLocked`; core config's `Apply` mutex is separate and taken first; `GetForUpdate` kept.
- R9-11 / SEC9-3 / DATA9-8 — §1.12.5: H2 and H3 name the owners they raise (`books`, plus `core` for H2) and `TestHardeningCutoverRefusedByEveryOlderRelease`; this file decides that H2 moves the four encrypted rows into `c/secrets/`; H2 deletes its checkpoint and compacts, says archives keep the old copies, `TestH2_NoPlaintextSecretOnDisk`, release note on old backups and key rotation.
- SEC9-7 — §1.3.7, §1.12.5: one H3 statement; H3 ends HEAD's auth-off write rule for `enable_auth` only (release-noted), and the Basic gate keys keep it until their settings cutover.
- SEC9-13 — §1.3.1, §1.3.7, §1.11: the generated `HEADAuthOffWritable` flag selects HEAD's auth-off rule, honoured only while the key is routed to the app's legacy store; `TestAuthOff_NativeAppRefusesAuthModeWrite`.
- SEC9-11 — §1.3.7: the deleted `Local` grant sentence replaced ("the hosts a legacy-backed instance reaches with `Local=true` were all written by an admin"); `ABO_CORE_AUTH_MODE` is first read at S4 r1.
- SEC9-10 — §1.3.7: the façade's `ProtectAdmin` on integration-bound fields is recorded as a tightening against HEAD's `FieldUnprotected`, with the 09 release note and test cell.
- SEC9-6 / R9-4 — §1.3.7: creating a legacy-backed instance and changing `auth_ref` or another secret-selecting field need `ProtectAdmin`, on the façade and after S7, matching 05-metadata M1.3.
- COR9-6 / R9-9 — §2.4: `secrets export-keyring`, `rotate-derive` and `set-install-id` reach ABO's CLI through 06's `app` offline commands; `TestCLIExposesCoreOfflineCommands`.
- COR9-7 — §1.3.3 `ResolveStateDir`, §1.4 step 0, §1.7, §1.11: P−1 marked deferred-build (A-6); the resolver runs before P0a.
- Header — 03 reference v1.12.1.
- R9-7 / COR9-5 (R-G request) — §1.3.3 `legacy.ConfigOpenEnv.Files` and `legacy.StateFiles`: the labelled state filesystem through which books' key-file, gap-fill and post-reset-hook writes go, so `coretest/crashmatrix` sees them.
- R-D requests — §1.3.1: `FileOnly()` is derived (a `LegacyYAML` key with no `LegacyBlob`), not a declared flag; `HEADAuthOffWritable` comment, §1.3.7 and §1.12.5: H3 clears it on `enable_auth` only (release note), the Basic gate keys keep it to their settings cutover; §1.3.3 and §1.6: the lock is 05-storage's `WriteLocks(names []string) []storage.Lock` under the config lock name `legacy.LockConfigSettings`, committed inside `storage.CommitLocked(ctx, b, d, stage, locks...)`.
- OPS9-7 (R-D request) — §1.3.5, §1.12.2, §1.11: an empty stored Basic password counts as no stored value, so `BASIC_AUTH_PASSWORD` is effective in memory only; `TestBasicGate_EmptyStoredPasswordDoesNotShadowEnv`.
- integration pass (round 9): §1.3.3 `ConfigResetArgs`: `ResetArgs` runs the preflight (top-level entries the hook would remove, one directory read) and compares nothing; `kernel.stageReset` compares, skipping the check only for `LegacyConfirm`; §1.12.2 likewise; §1.3.7: the `json:"-"` SMTP fields are file and env only, never stored (05-notify); §2.4: `secrets set-install-id` ships at S3 with the keyring (09 §9.4 row 17), and the cross-cluster note on 06's offline-command list is resolved.

Round 10:

- R10-5 / DATA10-2 — §1.3.3 `Committer` and `legacy` block (`ConfigCommitObserver` deleted; the gap-fill write is a post-commit step the store registers on the batch, `storage.OnCommitted`, which `CommitLocked` runs inside the hold), §1.4 step 9, §1.6, §1.12.2 (rendered from the rows inside the hold; every writer path HEAD covered: `Apply`, `Registry.Put`, notify, `secrets.Put`/`Delete` of a kept name), §2.3 `SecretsStore.Write` and `Options.SettingsLocks`, §2.5, §2.9 `TestGapFillClearNotUndoneByLateSet`, `TestGapFillStepOnEveryWriterPath`.
- R10-10 / SEC10-3 (lead ruling) — §1.3.1 `HEADAuthOffWritable` extended to every integration-bound blob field (AI endpoints, `openai_*`, metadata sources, webhooks, Deluge, `download_client`), §1.3.7 new bullet (façade, alias, `Registry.Put`/`Test`; no `ABO_CORE_*` promise; ends at the earlier of H2 and the family's integrations cutover), SEC4-7 paragraph (tightening is auth-on only), §1.12.1, §1.12.2, §1.11 `TestAuthOff_IntegrationFieldsWritableAsHEAD`.
- R10-4 / COR10-2 / DATA10-1 — §1.3.3 `LockConfigSettings`, §1.5 new row, §1.6, §2.3: books' stores and `SecretsStore.Write` refuse a write staged into a batch without the hold (`storage.LocksHeld(b, l)`); legacy suite case for a second batch inside a hold.
- R10-4 / DATA10-3 — §1.6: `configBlobSaveMu` at its place in 05-storage's total rank table; core-side mutexes (`Apply`, Registry writer, rule store) taken before `CommitLocked`, secrets' keyed mutex the one exception inside a `stage`; no `stage` calls a books method that takes a HEAD mutex; the alias split stated as the resolution of the one-batch contradiction; §2.3 read-order paragraph, §2.6.
- COR10-5 — §1.3.3 `Committer`, `LockConfigSettings`, §1.6, §2.3: the kernel resolves the lock at P4c and hands `SettingsLocks`; no owner calls `WriteLocks`.
- COR10-4 — §1.3.3 `StateFiles` (confinement defined once in 05-storage §5S.3.15: state dir, `<db parent>`, intent `Delete` and `HookPaths`, rule (4) kept paths), `ConfigResetArgs`, `ConfigResetPlan.Paths` (recorded as `hook_paths`).
- COR10-10 — §1.6: one store instance per App; `configBlobSaveMu` is one mutex per process because it is a package variable.
- DATA10-6 / R10-11 — §1.12.5 H2: deletes every checkpoint of the store; `ErrCutoverResidue` while a swap set-aside exists unless swept; the canary is planted before a fixture upgrade, restore and reset and every location is scanned.
- DATA10-7 — §1.12.5: the first cutover that raises `core` lifts `{core, 0}`; H3 lifts nothing.
- DATA10-9 (b) — §1.3.3 `ConfigResetArgs`: the reset rows are rendered by the staging binary and written as given by a resuming build (consistency with 05-storage).
- SEC10-5 — §1.3.1, §1.3.7, §1.11: one predicate (legacy store supplied and the value in a `LegacyBlob` field or a `legacy.SecretsStore` row); each group loses the flag together at the earlier of H2 and its own cutover; the password in the legacy test half.
- SEC10-6 — §1.3.1, §1.3.7, §1.12.1 ("through S10" replaced), §1.12.5: the gate keys keep the rule until the settings cutover, which precedes or ships with H3.
- SEC10-7 — §1.12.5: H2's release note is 09 §9.13's one text, not restated.
- SEC10-8 — §2.4: an unencrypted archive holds ABO's plaintext blob secrets until H2.
- SEC10-9 — §2.4, §2.9: `export-keyring` opens no store; `rotate-derive` and `set-install-id` use `kernel.OpenOffline`; `TestExportKeyring_OpensNoStore`.
- SEC10-11 — §1.3.3 `AttachReport.DeployDefaults`, §1.3.5, §1.11: a shipped deploy file's always-set name at that file's default is reported at info, not as a shadow.
- OPS10-11 — §2.4: `set-install-id` lands at 09 S3 (the dangling "§9.4 row 17" citation).
- Header — 03 reference v1.14.0.
- integration pass (round 10): §1.12.5 quotes 09 §9.13's H1–H3 release notes verbatim (R-H) and the H2 bullet cites them instead of declining to restate; `SecretsStore.Write` and the §1.5 not-locked row wrap `storage.ErrNotLocked` (R-B). Already as requested: kept-name `secrets.(*Store)` `Put`/`Delete` commit through `CommitLocked` with `Options.SettingsLocks`; the post-commit hook is `storage.OnCommitted`. Not renamed: `config.Handle.AfterCommit` is config's own subscriber hook, not storage's post-commit hook, which the LEAD decision names.

Plan-phase consistency:

- PC-4 — §1.12.5 H2: a swap set-aside defers H2 (05-storage §5S.3.13 item 3) instead of "refuses to start".
- PC-17 — §1.3.3 `Committer`, §1.4 step 9 and §1.12.2 "Writing": at S3 the committer logs the change (section, key names, actor) and stages no audit record; from S4, when `audit` lands, it stages `core.settings.update`.
- D10–D12 revision (2026-11-04): no backward compatibility, no soaks; ABO's settings and secrets move once, in R2.
  **Deleted:** the books legacy config store and the old §1.12.2 (blob in place, plaintext blob secrets, HEAD's
  four rows in place, `configBlobSaveMu`, the gap-fill write inside the hold, `Changed`, the rollback matrix and its
  R-test cells, the HEAD factory-default blob on reset); §1.12.5 (H2/H3 as post-S10 cutovers; their designs land in
  R2); `Alias` with `LegacyYAML`/`LegacyBlob`/`BelowStored`/`HEADAuthOffWritable`/`Decode`/`Ledger`, `FileOnly`,
  `LegacyDecode` and the A-7 decode table, `config.Aliases`, `config.Deprecated`, `Options.Retired func`,
  `OnLegacyUse`, `Legacy`, `Surface`, `Store.Aliases`, `OperatorValue.BelowStored`, `KeyState.Alias`,
  `AttachReport.Shadowed`/`DeployDefaults`, every `legacy.*` config and secrets capability (`ConfigOpener`,
  `ConfigOpenEnv`, `StateFiles`, `ConfigResetArgs`/`Plan`/`Run`, `LockConfigSettings`, `ConfigExternalWriter`,
  `ConfigSecrets`, `SecretsStore`), `app.Options.LegacyConfigStore`/`LegacyConfig`, the compat-ledger and window
  text, HEAD precedence for legacy names, the shadow and deploy-default reports, the canonical-only rollback
  consequence, `secrets.Options.LegacyStore`/`SettingsLocks`, the `legacyStore` mirror, `LegacyCopies`,
  `LegacyCheck`, `ReadLegacyKey`, `k0-legacy`, `SourceLegacy`, `LegacyForm`, `EphemeralDefault`, the
  plaintext-secret and legacy-read metrics, the staging secret-class list, and every test of those.
  **Rewritten:** the intro; C3 (operator lock plus the fail-closed rule for auth and Basic-gate keys), C4 (native
  store only, `config.Importer`), C6, C7 (canonical names and ecosystem bindings; retired names refuse or warn),
  C8, C11, C12, C13, C16 (HEAD's secrets migrate once), C17 (no app boot step in config); §1.3.1 (new
  `EnvBinding`, `FailClosed`; no alias or deprecation option), §1.3.3 (new `EnvName`, `RetiredName`, `RetiredUse`,
  `Importer`, `RetiredSet`; `ErrRetiredName`/`RetiredNameError` replace `ErrRetiredAlias`/`RetiredAliasError`),
  §1.3.4, §1.3.5, §1.3.6 (key grammar, strict decoding, ecosystem bindings, the retired-name rule), §1.3.7 (one
  native auth-off rule; HEAD's file-only keys become ordinary keys; integration instances native for ABO too),
  §1.4 (`secrets.Open` after the checkpoint and before the migration list; step numbers 0–7 kept; the reset
  paragraph), §1.5, §1.6, §1.7, §1.8, §1.9, §1.11, §1.12.1 (stage 0 in R1, the façade in the R2 flip), new §1.12.2
  "The config and secrets cutover (R2, migration step M2)", §1.12.3 "Key grammar and ABO's rename table" (the
  generator rule from HEAD's `Config` struct, the section table and the full 173-row table with sub-key rows),
  §1.12.4 "Env, file and flag names after the cutover", §1.13; Part 2 §2.1, §2.2 (the M2 column), §2.3
  (`StageUnreadable` and the unreadable marker; read order operator > record), §2.4–§2.11, objections and
  out-of-scope defects 2–6 (fixed by the cutover).
  Consistency pass: added §1.12.1's release R1 delivery of the listener and perimeter values (`app.Options.R1Values`,
  called once at start step 7) and the F3-to-F4 serving of `/api/v1/config` over core config (class-`i` fields until
  F7), confirmed the peer allow-list and trusted proxies as storable admin keys, declared `secrets.Importer`, applied
  the M1, M7, M2–M6, M8 order, sealed M2's quarantine, moved the keyring export after the first boot, remapped
  `operation_timeout_minutes` and the second targets of `oauth_allowed_emails` and `scheduled.db_optimize`, renamed
  `config_alias_conflict` to `config_binding_conflict`, and re-pointed 14, D12, 06 and 05-storage citations. Final consistency sweep: `Store.SetSecrets` moved from step 5 to step 6 (P4d step 1, before `Attach`), `secrets.Open` placed at the start of P4b, and the archive-contents citation pointed at 05-backup §5B.3.1.

Round 13 (2026-11-04): IMPL-1, IMPL-3, IMPL-8, DATA-1, DATA-10, DATA-13, DATA-15, SEC-6, CONFORM-1, CONFORM-3 and
XFILE-3 (M2b kept as the owner text), CONFORM-4 and DATA-16 (as ruled), CONFORM-2 and DATA-8 (as ruled). Every core section registered from F2
and `TestRenameTable_EveryKeyRegistered` run from F3 (§1.12.1, §1.12.3); the audit store landing at F2 (§1.3.3);
the blob kept an active books claim until M6, M2's `Verify` checking moved fields absent (§1.12.2); M2b's
checkpoint copy deleted (the checkpoint already holds the file); the state-file table with the fallback key path
(§2.4); `--gap-fill` for a migrated pre-R2 archive; the ABS JWT secret and the Cloudflare policy token made
`ProtectOperator`, class `o`, never stored, with the classifier's signing-key and token patterns; the fixture
corpus's labelled hand-built overlay; the pre-freeze release as the rollback binary; R3's deletion of
`.encryption_key` through the crash-labelled `legacy-file-cleanup` step (03 and 09's name; it refuses while
`secrets_keyring_not_exported` is raised); the overlay's identity cases cited to the cutover preflight (09 §9.2.3); the checkpoint's deletion 7 days after
it stops being needed.

Round 14 (2026-11-05): V5 (§2.4 lifecycle: the dry run's and the `--into` rehearsal's M2 seals with a throwaway keyring inside the scratch directory and never reads or writes the state directory's keyring), V16 (§1.12.2: while `secrets_keyring_not_exported` is raised, the `legacy-file-cleanup` start hook deletes `library.bleve` and skips only `.encryption_key`, as 05-storage §5S.3.8; named a start hook), V23 (checked: no bare round-13 ruling citation in this file).
Round 15 (2026-11-05): W5 (§2.4 lifecycle: the dry run's throw-away keyring lives in the dry run's directory in `.mediacore-checkpoints/scratch/` (05-storage §5S.3.12 "Scratch copies"), not under `c/storage/scratch/`; only `storage migrate --dry-run` gets it, and a `--into` rehearsal copy runs no M2 and boots the binary normally with the copy's own `<dir>.state/`). W2-W4, W6-W10 checked: this file states none of them.
