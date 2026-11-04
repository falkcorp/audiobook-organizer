<!-- file: docs/specs/platform-core/05-config.md -->
<!-- version: 1.12.1 -->
<!-- guid: fb637cbc-bbfa-4aea-807c-71f02534b3c9 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Config and secrets (`core/config`, `core/secrets`)

Evidence commit `748fd787a` (all `path:line` anchors). Raw inputs:
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §2, §9;
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7, G12;
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md) finding 3;
[`raw/03-layering-guard.md`](../../plans/platform-core/raw/03-layering-guard.md) `allowed` rows 1–5;
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md). Normative names come from
[03-architecture](03-architecture.md) v1.14.0: R4 layers **L1** `storage` → **L2** `secrets` → **L3** `config`
(`metrics` and `log` are L0; `compat` and `integrations` are L4, above config); R5 (no globals) binds **core**
only; §3.6 `core.*` / `modules.<id>.*`. The compatibility window is defined **once**, in
[09-abo-on-core](09-abo-on-core.md) §9.2.1, and implemented once in `compat`; config aliases reference it and
carry no window logic of their own.

## Decisions at a glance

| # | Decision | Why (short) |
|---|---|---|
| C1 | One `config.Store` per `App`; no package-level config value anywhere in **core** (R5). ABO keeps `config.AppConfig` as a books-module façade over core handles (§1.12.1). | `var AppConfig Config` (`internal/config/config.go:1939`) cannot host two apps and is read racily by design (`config.go:1930-1938`). R5 governs core only (03 §3.5), so ABO's façade is allowed. |
| C2 | **Config owns the mechanism, never a section's type.** Each section struct lives in the package that consumes it and is registered through the manifest as `config.Section[T]`. | This is what breaks all upward imports (§1.9): config imports `aidispatch`, `dedup/unified`, `backup`, `auth`, `database` today only because it owns their types. |
| C3 | Merge order defaults → file → env → flags → DB overrides, with **operator lock**: a key supplied by file, env or flag cannot be overridden from the DB layer and `Apply` refuses it with the lock reason. **One exception, HEAD precedence for legacy names (R5-1(a)):** a legacy env name or legacy file key whose value HEAD lays the stored blob over (`Alias.BelowStored`, generated from HEAD's lists, §1.3.5) feeds the **default** layer, below the DB layer, and locks nothing. The auth-precedence cutover H3 (§1.12.5) changes only an *enabling* `enable_auth` value; every other `BelowStored` name keeps HEAD's precedence until a separate, owner-decided cutover, and a name a shipped deploy file sets unconditionally (compose's `BASIC_AUTH_*`) never locks (SEC9-2); canonical `<APP>_*` names always lock. The auth switch is no exception: a stored `enable_auth` beats `ENABLE_AUTH` in both directions, as at HEAD (A-2). A stored value beats a `BelowStored` value even when it equals the default (SEC7-2). Legacy names decode with HEAD's per-type semantics and canonical names strictly (§1.3.6, A-7). An env variable set to `""` is unset (HEAD parity). | Generalizes ABO's per-key `envLockedSettings`/`flagLockedSettings` (`config.go:2241,2353`) to every key; today a DB blob silently beats env for all other keys (`persistence.go:915` then `config.go:2435`). Keeping that for legacy names means a shipped compose file that always sets `BASIC_AUTH_ENABLED=false` (`docker-compose.yml:20`) cannot switch off a gate turned on in the UI (OPS5-1), and every older build reads the same effective auth switch after a rollback (A-2). |
| C4 | **The DB layer is a store interface, `config.OverrideStore` (R4-1).** Core ships the **native** store over its own keyspace `c/config/` (one row per explicitly set key), which `hello` and every app without legacy settings use. An app with legacy settings supplies its **own** implementation through its module's `Manifest.LegacyStores.Config` (§1.3.3; 06), written in the app's code over the app's keys and row formats; for ABO that is the books legacy config store, which reads and writes keys that have a legacy blob name **in place** in ABO's settings blob, in the legacy JSON shape, and only those. **One storage rule (A-2, R9-5):** a key that has a legacy blob field (`Alias.LegacyBlob`) is stored only in HEAD's legacy row and format; a key HEAD never stores (`Alias.FileOnly()`: HEAD's `json:"-"` fields, the peer allow-list among them) keeps HEAD's storage, the operator file and env only, and is never stored while the app's legacy config store is attached; a key with no legacy name is stored natively in core's `c/config/` keyspace, which older builds never read, so it costs nothing on a rollback (the kernel routes each key, §1.3.3; §1.12.2). Core holds no app key name or row format and no storage grant for config. Nothing is copied, mirrored or adopted, and no level step exists for config. **Cutover** to the native store is deferred and out of the extraction (§1.12.2). | Core-resident legacy adapters put ABO's on-disk formats into `mediacore` (SIMP4-2, against 03 R1); one interface with an app-supplied implementation keeps them in the app. One stored format per family (R3-1) still holds: every round's dual-format fix produced new cross-stage defects. A stored value equal to the shipped default reads as default while no operator or `BelowStored` value is set for the key, so the blob's frozen defaults (`persistence.go:1180-1188` measured 33 keys inheriting stale defaults) stop pinning future defaults; against a `BelowStored` value it is still the stored value, as at HEAD (SEC7-2). |
| C5 | Every key declares `Reload` (live / restart / boot) and `Protection` (none / interactive / admin / owner / operator); both appear in the generated JSON Schema. | ABO has exactly one boot-only key handled ad hoc (`persistence.go:2111`) and a 92-entry protection map (`protected_fields.go`) separate from the schema. Roles-as-data can hand `core:settings.manage` to non-admins, so sign-in, proxy and updater keys need an admin level (§1.3.7). |
| C6 | Drop **viper** from core and, by the end of migration, from ABO. Stdlib + `go.yaml.in/yaml/v3` + `github.com/google/jsonschema-go` only. | §1.10. |
| C7 | Env names are `<APP>_<PATH>`; no `MEDIACORE_` prefix. ABO's existing names are aliases; each alias names its 09 §9.2.9 ledger row, which alone says whether it is a window, permanent or retired name. **Permanent** rows: ecosystem names (`OTEL_*`, `OPENAI_*`) and every name ABO reads today that a shipped deploy file sets (e.g. `DATABASE_PATH`). | §1.3.6, §1.12.4. Dropping `DATABASE_PATH` after the window would open a new, empty database (OPS-5). |
| C8 | Every recoverable secret a core build writes **as native data** is encrypted at rest by `core/secrets` with **one keyring design**; a secret HEAD already stores keeps HEAD's representation until the secrets cutover (the four HEAD encrypts in their legacy rows in ABO's cipher, the plaintext ones in their blob fields: C16, A-2) (AES-256-GCM, key IDs in every record, AAD = app + name, a separate derivation key); config fields are `secrets.Ref`, never strings. The keyring is the install's **only** key artifact: backup's archive identity is derived from it, so one export recovers both (§2.4). Data-key rotation and prune tooling are **deferred**; the format already carries key IDs so it never changes. The derive key has an offline rotation command in v1 (§2.3). Verifiers (passwords, API keys, session ids) are hashed by `auth`, never stored in `secrets`. | `raw/10` G12: 4 of at least 10 secret-bearing fields are encrypted, and 3 of those 4 are also written in plaintext to a mirror YAML file (`persistence.go:168-178`). Rotation does not exist today (`raw/10` §7). A second key file would need a second export (SIMP2-2). |
| C9 | Secrets are **write-only** over the API: the UI receives `{set, source, updated_at, fingerprint}`, never a mask. | Masks reveal the last 2 characters (`internal/database/settings.go:250-277`) and force echo-detection logic (`update_service.go:353-373`). |
| C10 | **One JSON-Schema reflector**: `github.com/google/jsonschema-go`, held at v0.4.3 (Dependabot `ignore` entry plus `check_module_skew.py --pin`, 13), wrapped by the L0 leaf `core/internal/jsonschemax`, which owns the tag pass and an extensible override table. Each package contributes the entries for its own types: config adds `config.Duration` and `secrets.Ref`; [05-http-api](05-http-api.md) adds `rbac.Permission` for OpenAPI. | §1.10.2; verified 2026-11-04. A config-owned table could not name an L4 type (SIMP2-3). |
| C11 | **Config holds no integration instances.** Instance records (kind, instance, settings, `secrets.Ref`s) live in the `integrations` registry ([05-metadata](05-metadata.md) M1; native instances in `c/integrations/instances/`); ABO's legacy singletons that today live in the blob stay there until cutover, and integrations reaches their blob fields through its own legacy instance store, which books implements beside its config store and which shares that store's blob render (§1.6, §1.12.2), never through config keys. Config only reads the operator-layer values (file, env, flag) of the legacy integration-bound keys raw, at their `integrations.<kind>.<instance>.<field>` paths, and hands them over (§1.3.3 `Passthrough`). integrations applies them as an **in-memory overlay** on the instance's stored row, never persisted (05-metadata M1.4), under **one precedence rule (SEC6-5)**. A value given under a `BelowStored` legacy name fills only a field with **no stored value** and locks nothing, as at HEAD; every integration-bound name HEAD lays the blob over is `BelowStored`, `OPENAI_BASE_URL` among them (§1.12.4). Only a value under a canonical spelling overlays a stored field and locks it. Private-network trust attaches only when the **effective** field's `Source` is an operator layer, never merely because an operator value exists for the path (`OperatorValue.BelowStored`, §1.3.3). Removing the operator value reverts to the stored row, as HEAD does (`persistence.go:1959-1971`). There is no `core.integrations.<kind>.instances.<id>` config form. Every integration secret HEAD stores (the OpenAI, Google Books and Hardcover keys in their encrypted rows, the plaintext ones in their blob fields) stays where HEAD keeps it during the extraction, whichever route writes it (C16, A-2). | One store for one fact (SIMP-4): the Deluge password had four destinations across the specs. One lock model, config's operator-layer lock, for config keys and instance fields alike (SIMP2-5). |
| C12 | This file owns `config.OverrideStore`, the native store, the **contract** the books legacy config store must meet (§1.12.2, tested by `configtest.OverrideStoreSuite`), the secrets legacy-copy rule (C16) and the deferred cutover; 09 S2–S3 sequence them and reference them. | Specified twice with contradictory semantics before (SIMP-15, OPS-9). |
| C13 | Config and secrets write nothing to the store at boot except `c/secrets/meta` on a new store, and that only after the boot's pre-migration checkpoint and the storage DAG; attaching the DB layer is read-only. One boot write is not config's: the factory defaults a factory reset's fresh install starts with, which storage's fresh-install batch writes from the reset intent (§1.12.2, SEC8-5). Its boot step is placed by the one boot-phase table, [06](06-app-sdk.md) §6.5.1 (R2-8). | A write before the checkpoint puts post-upgrade rows into the restore point (COR2-7). |
| C16 | **Secrets HEAD stores keep HEAD's representation (A-2).** Every secret with a legacy copy is read and written **in place** by the books store, in HEAD's form, until the secrets cutover (§1.12.5): the four HEAD encrypts (OpenAI key, Google Books key, Hardcover token, Basic password, `persistence.go:1955-1958,1989-2006`) in their encrypted rows in ABO's cipher, with HEAD's gap-fill file copies of three of them; the plaintext ones in their blob fields. `secrets` encrypts only native data: secrets with no legacy name (new names) in `c/secrets/`. HEAD's file-only secrets (§2.2 "—" rows) keep HEAD's storage, operator layers only, never stored (R9-5). There is no sentinel and no re-import. | Every build reads the current value of every secret, so a rollback strands, opens or resurrects nothing: HEAD's Basic gate compares the presented password with the stored one (`internal/server/middleware/basicauth.go:81-87`), so a password an older build could not read would lock it out or open it (SEC4-1, DATA4-1, OPS4-1, OPS5-2). Encrypting the plaintext ones is a one-way, floored cutover (N15), not a step of the extraction. |
| C17 | **ABO's settings load, encryption init and key-regeneration guard run inside the books legacy config store (R4-6)**, in its `Open` (the optional capability `legacy.ConfigOpener`, §1.3.3; R6-3), which the kernel calls over books' own handles at 06 P4d step 1 from S2b, before any module code runs and before `secrets.Open` (COR5-2): never a second Pebble open, never defaults written over the stored blob (§1.12.2). | From S2b `serve` no longer opens the store, so HEAD's pre-server steps had no home (COR4-1); running them in ABO's code over ABO's keys keeps them out of core (R4-1). |
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
`OverrideStore.Stage`; config opens nothing and runs no storage call itself) and `github.com/falkcorp/mediacore/metrics` (L0; its own
`*metrics.Scope`). It does **not** import `log`, `auth`, `rbac`, `audit`, `tasks`, `compat`, `integrations` or any
module (§1.9).

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
// Convert and Revert are also what an app's legacy config store uses to map its stored fields onto canonical
// paths and back (§1.12.2), so there is one alias table.
type Alias struct {
	// LegacyYAML is the flat key in the operator YAML file (an operator surface, governed by the ledger row like the
	// env names), e.g. "root_dir"; "" if HEAD reads the key from env only. LegacyBlob is the field in the app's stored
	// settings (ABO: config_blob), e.g. "root_dir" (the storage format, kept until the family's cutover whatever the
	// row's state); "" when HEAD never stores the key. They are two fields because they differ: HEAD's json:"-"
	// keys have a YAML key and no blob field (internal/config/config.go:1636-1759 @748fd787a; DATA9-6, R9-5).
	// Routing follows LegacyBlob only (§1.3.3).
	LegacyYAML string
	LegacyBlob string
	LegacyEnv  []string             // env names read today (AutomaticEnv upper-case key + BindEnv names)
	Canonical  Path
	Convert    func(json.RawMessage) (json.RawMessage, error) // optional legacy → canonical unit/shape change
	Revert     func(json.RawMessage) (json.RawMessage, error) // canonical → legacy; required when Convert is set and
	                                                          // LegacyBlob is non-empty (the legacy config store writes it, §1.12.2)
	// HEADAuthOffWritable keeps HEAD's auth-off write rule for this key (§1.3.7; SEC9-1, SEC9-13): with auth off the
	// auth-disabled principal may store it after the owner-trust-root check, as HEAD's !prior.EnableAuth allows
	// (update_service.go:770-786 @748fd787a). ABO generates it for enable_auth, the three basic_auth_* keys and every
	// integration-bound blob field of §1.12.3 (the rows marked integrations: AI endpoint rows and the openai_* pair,
	// metadata sources, webhooks, Deluge and download_client, list rows and singletons alike; all FieldUnprotected
	// at HEAD, protected_fields.go:137-165; R10-10, SEC10-3).
	// One predicate decides it (SEC10-5): it is honoured while the app supplies a legacy config store AND the key's
	// value lives in a legacy row, either its LegacyBlob field (for an integration-bound field, through the legacy
	// instance store that shares the blob, 05-metadata M1.4 step 2a) or a legacy.SecretsStore row (the encrypted
	// basic_auth_password and the three kept integration keys). A native app, which has no legacy store, never
	// inherits it. The keys of one group lose it together, at the earlier of the secrets cutover H2 (which moves the
	// group's secrets into c/secrets/) and the group's own cutover: the three Basic gate keys at the settings cutover,
	// which precedes or ships with H3; an integration instance's fields at its family's integrations cutover
	// (05-metadata M1.10); one release note per group. H3 clears it on enable_auth only (§1.12.5, R9-5, SEC10-6).
	HEADAuthOffWritable bool
	Ledger     string              // ledger row ID: the app's ledger (app.Options.CompatLedger) or core's own fragment
	                               // (09 §9.2.9, SIMP3-3); TestCompatLedgerMatchesCode joins them from S2b, when
	                               // the ledger lands (COR4-20)
	// BelowStored keeps HEAD's precedence for this alias's operator spellings (R5-1(a), §1.3.5; OPS5-1 called it
	// EnvIsDefault): a value given under LegacyEnv or the LegacyYAML file key feeds the default layer, below the DB
	// layer, and locks nothing. The app generates it from its own lists of the keys it re-applies over its stored
	// settings; it is ignored for a ProtectOperator key (no DB layer, FileOnly keys included). It has no exemption:
	// the auth switch keeps it too (A-2). The auth-precedence cutover H3 lifts it only for an enabling enable_auth
	// value; every other flag stays until a separate, owner-decided cutover (§1.12.5, R9-5).
	BelowStored bool
	// Decode is the HEAD type a value given under LegacyEnv or the LegacyYAML file key is decoded as, before Convert
	// (A-7, §1.3.6). The app generates it from the getter HEAD's loader uses for that key (ABO: configFromViper's
	// viper.Get<Type> call); zero means the canonical key's own type, decoded as HEAD's cast rules decode it.
	Decode LegacyDecode
}

// FileOnly reports a key HEAD never stores: an alias with a LegacyYAML key and no LegacyBlob field (ABO: a json:"-"
// field, never in GET /config or the blob, internal/config/config.go:1668-1670; the env-only refusal,
// update_service.go:632-635,720-726 @748fd787a). It is derived, not declared, so it cannot drift from the two names.
// While a built legacy config store is attached, config treats such a key as ProtectOperator whatever its declared
// class: no DB-layer value is read or staged for it and Apply refuses it, so it keeps HEAD's storage, the operator
// file and env only, and a rollback loses no stored value (R9-5, DATA9-6; §1.3.7;
// TestFileOnlyLegacyKeysNeverDBStored). A native app has no legacy store, so its declared class applies; the
// family's settings cutover ends the rule.
func (a Alias) FileOnly() bool // a.LegacyYAML != "" && a.LegacyBlob == ""

// LegacyDecode names one of HEAD's per-type decoders for legacy spellings (A-7, §1.3.6). Canonical names never use it.
type LegacyDecode uint8

const (
	DecodeCanonical   LegacyDecode = iota // the canonical key's type, with the rule of that type below
	DecodeBool                            // strconv.ParseBool spellings; any other spelling is HEAD's silent false
	DecodeInt                             // cast's integer rule: strconv.ParseInt(s, 0, 0) after trimming a decimal part; an error is 0
	DecodeFloat                           // strconv.ParseFloat; an error is 0
	DecodeDuration                        // a Go duration; a bare number is nanoseconds (cast appends "ns"); an error is 0
	DecodeStringSlice                     // a string split on whitespace (strings.Fields)
	DecodeString                          // the raw string
)

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
		LegacyYAML: "concurrent_scans", LegacyBlob: "concurrent_scans", LegacyEnv: []string{"CONCURRENT_SCANS"},
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
	// OnLegacyUse reports one use of an alias. The kernel buffers reports made before compat is built and hands
	// them over once it is (06 §6.5.1); compat counts them in its process-local counter and warns once for a
	// window row. Nothing is persisted (A-14; §1.3.6, OPS2-7).
	OnLegacyUse func(a Alias, s Surface)
	// Legacy is the app's legacy names of core keys (app.Options.LegacyConfig; nil for an app without one),
	// registered with the core sections they name. Needed at P0, before any manifest is read (06 P1).
	Legacy      *Legacy
	Logger      *slog.Logger
	Metrics     *metrics.Scope    // core scope "mediacore.config" (05-metrics)
	Secrets     secrets.Resolver  // may be nil until secrets opens; Refs resolve after SetSecrets
	Now         func() time.Time
}

func NewStore(o Options) (*Store, error)

// ResolveStateDir is the one pre-boot rule for core.paths.state_dir (§1.7, C15, OPS3-1). The kernel runs it
// once per process, before 06 P0a, and hands the result to P0a, Boot and every consumer (and, once the deferred
// P−1 is built, to P−1 first; A-6, COR9-7), so all agree
// (HEAD memoises for the same reason, internal/config/state_dir.go:57-79). explicit is the env or flag value
// ("" if unset); def is app.Options.StateDir; dbPath is core.storage.path from the pre-boot layers (compiled
// default, the operator file Options.FilePath, env, flags, as HEAD's fallback reads AppConfig.DatabasePath with
// its YAML file, OPS5-13; registered as a kernel pre-boot key from S2, §1.7); mkdir is the kernel's
// MkdirAll(dir, 0o700). It is a pure function of those inputs, so an app that must know the directory before
// app.New (ABO's serve at S2, §1.7) calls it with the same inputs and gets the same answer.
func ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error)

func (s *Store) Registry() Registry
func (s *Store) Boot(ctx context.Context) (BootView, error)              // core sections only; operator layers + boot record
func (s *Store) Load(ctx context.Context) error                          // seals Registry; defaults+file+env+flags+boot record
func (s *Store) SetSecrets(r secrets.Resolver)
func (s *Store) Attach(ctx context.Context, o OverrideStore, c Committer) (AttachReport, error) // read-only; once per boot
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
	// BelowStored: the value came under a BelowStored legacy name (§1.3.5), so it fills the field only while the
	// instance stores no value there and locks nothing (C11, SEC6-5); false for a canonical spelling, which locks.
	BelowStored bool
}

// FromFlagSet converts a stdlib FlagSet to Options.Flags using Visit (explicitly-set flags only).
// Apps on cobra/pflag write the same 10-line adapter over pflag.FlagSet.Visit; core does not import pflag.
func FromFlagSet(fs *flag.FlagSet, bind map[string]Path) map[Path]string

// OverrideStore is the store interface of the DB layer (R4-1). It is SDK and frozen by the API-stability
// gate, and it holds only the native contract (SIMP5-4): every legacy-only capability is an optional interface
// of package legacy (below; R6-3), which the native store does not implement. The kernel asserts only the
// capabilities 03 §3.3 lists (SIMP8-3); config and its committer detect the others through their mirrors
// (SIMP7-5). Core ships the native implementation (core's
// core/internal/configstore, keyspace c/config/; not ABO's internal/books/configstore, §1.12.2); an app with legacy settings supplies its own on its module's Manifest.LegacyStores.Config (06:
// func(legacy.Handles) (config.OverrideStore, error), built at 06 P4c from that module's own handles),
// written in the app's code over the app's keys (§1.12.2); it serves only keys that have a legacy blob name. For an
// app that supplies one, the kernel attaches a routing store (core/internal/configstore): a key with a legacy blob
// field (Alias.LegacyBlob) goes to the app's store, a FileOnly key to neither (it has no DB layer while the app's
// store is attached, R9-5), every other key to the native store over c/config/, and both stores stage into the one
// commit batch (A-2, the one storage rule of C4). At S2 only, before kernel storage exists, ABO's
// main passes the same implementation over the store its serve opened as app.Options.LegacyConfigStore (06),
// deleted in the S2b PR: the sole exception to 03 §3.3's "never through app.Options" (SIMP5-10). Paths and
// values are canonical: an implementation that stores legacy names maps them with the alias table
// (Alias.Convert on read, Alias.Revert on write). configtest.OverrideStoreSuite is the contract every
// implementation runs, and legacytest.ConfigOverrideStoreSuite the contract of the legacy capabilities (§1.11).
// Landing (COR5-14): the interface with Load lands at S2 (no storage or secrets type in it); the native
// implementation and LegacyStores.Config at S2b; Stage at S3, when core config first writes (v0, 12 §12.4.1).
type OverrideStore interface {
	// Load returns every stored DB-layer value. Read-only: it never writes, renders or repairs.
	Load(ctx context.Context) iter.Seq2[Override, error]
	// Stage joins b with the writes for set and unset: only those keys change, nothing else is rendered or
	// absorbed (no defaults, no operator-layer values). A read-modify-write reads with GetForUpdate on b, so a
	// concurrent writer fails the commit with storage.ErrConflict and the Committer retries.
	Stage(ctx context.Context, b *storage.Batch, set []Override, unset []Path) error
}

// Legacy-only capabilities (SIMP5-4) live in package legacy, never in config (R6-3; listed after this block).

// Committer is implemented by the kernel (core's core/internal/configstore). Commit opens ONE storage batch and
// stages into it: OverrideStore.Stage for c.Set and c.Unset, secrets.(*Store).Stage for each of c.Secrets (a
// secret with a legacy copy is written in place, a native one to c/secrets/, §2.3), and the audit record
// core.settings.update (05-rbac §7.1: section and changed key names, never secret values; actor = Override.By).
// When the store (or any store that staged into the batch) has legacy.WriteLock, it builds and commits the batch
// inside storage.CommitLocked(ctx, b, d, stage, locks...) with its SettingsLocks: the []storage.Lock the kernel
// resolved at 06 P4c by calling WriteLocks with legacy.LockConfigSettings and handed it with the store. The
// Committer never calls WriteLocks and names no lock string (it cannot import legacy, L5; COR10-5). CommitLocked
// marks b with the hold (storage.LocksHeld(b, l), R10-4) and holds the locks from the first staged read through the
// commit and through every post-commit step a store registered on b while staging (the books store's gap-fill
// file write, §1.12.2), so the file step is inside the lock as HEAD's SaveConfigToDatabase had it (R9-1, R10-5,
// §1.6); it retries with storage.RetryOnConflict, each attempt a fresh batch and a fresh hold. config never
// imports audit (L4).
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
}

// Legacy is what config needs from an app's main before any manifest is read: the app's legacy names of CORE
// keys (module sections carry their own, §1.3.1), supplied once as app.Options.LegacyConfig. It stays until
// the settings cutover (COR4-3). The app's override store is not here: it travels with the other
// R4-1 legacy stores on the owning module's Manifest.LegacyStores.Config (06). Core holds none of the app's
// names or formats (R1).
type Legacy struct {
	Aliases []Alias
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
	// Shadowed: keys at ProtectAdmin or higher whose BelowStored legacy name is set to a value that differs from the
	// stored effective value -> the legacy name; health config_legacy_name_shadowed (§1.3.5, SEC6-1)
	Shadowed    map[Path]string
	// DeployDefaults: the same condition for a name on §1.12.5's list of names a shipped deploy file sets
	// unconditionally, whose value equals that file's default (compose's BASIC_AUTH_ENABLED=false,
	// BASIC_AUTH_USERNAME=admin) -> the legacy name; logged once at info, never a health token (SEC10-11)
	DeployDefaults map[Path]string
}
```

**The legacy capabilities of the config store (R6-3, SIMP5-4).** They live in package
`github.com/falkcorp/mediacore/legacy` (03 §3.3), the one core package that holds every identifier existing only
to serve an app's pre-core stores. Like `kernel/` it is outside the API-stability gate (12 §12.4), so removing
a capability at its family's cutover is not a breaking change (SIMP6-2). `legacy` (L5) imports `config`,
`secrets` and `storage`, which never import it. The kernel asserts only the capabilities 03 §3.3 lists (SIMP8-3).
Of this store's, those are the two whose signatures name a `legacy` type, which it asserts on the store it built
at 06 P4c and drives: it calls `ConfigOpener.Open` at P4d step 1 and passes `ConfigSecrets.Secrets()` to
`secrets.Open` (COR7-3, SIMP7-5); and `ConfigResetArgs`, which the kernel's staging function asserts and calls
when a reset of either kind is staged, with the one `storage.ResetRequest` (COR8-2, R9-6, §1.12.2). Every other capability is detected once,
by its consumer, through an **unexported mirror interface** with the same method set: config detects
`ConfigExternalWriter` itself (`externalWriter`) when it attaches the store, at P3 through
`app.Options.LegacyConfigStore` at S2 and at P4d step 1 at S2b; and `secrets` asserts its `SecretsStore` mirror on
the value it is handed (§2.3). The former `ConfigCommitObserver` is gone (R10-5): its `Committed` ran after the
lock was released and only on the config `Committer`'s path, so the gap-fill write is now a post-commit step the
store registers on the batch it stages into, which `storage.CommitLocked` runs inside the hold on every writer's
path (§1.12.2). Each consumer's mirror is checked by **the mirror test (03 §3.3)**: the consumer's `export_test.go`
only exports a test alias of the mirror, and `legacy_mirror_test.go` in the consumer's external test package
(`package <consumer>_test`, which may import `legacy`) asserts mutual assignability in both directions (R7-7,
SIMP7-1, COR7-2). Names follow 03's rule
(`config.LegacyOpener` → `legacy.ConfigOpener`, `secrets.LegacyStore` → `legacy.SecretsStore`). 05-storage
§5S.3.11 "Legacy capabilities" lists them with the other owners' capabilities. The settings cutover removes them
(§1.12.2).

```go
package legacy

// ConfigOpener: Open runs once per boot, from S2b (COR6-8), when the kernel attaches the store at 06 P4d step 1,
// before OverrideStore.Load and before secrets.Open. It is where an app's legacy store runs the app's own boot
// steps over its one handle (R4-6, §1.12.2), including loading the app's legacy key, so ConfigSecrets'
// KeyMaterial is set when secrets.Open captures it (COR5-2). It never writes the store; an error fails boot.
// At S2 the kernel does not call it: ABO's serve still runs those steps before app.New.
type ConfigOpener interface {
	Open(ctx context.Context, env ConfigOpenEnv) error
}

// ConfigOpenEnv is what the kernel hands ConfigOpener.Open. It carries no extraction-stage value: a legacy
// store takes its stage behaviour from its own build (SIMP5-5).
type ConfigOpenEnv struct {
	StateDir *os.Root // core.paths.state_dir as config.ResolveStateDir resolved it (05-config §1.7)
	// KeyringLegacy returns the legacy key material recorded in the keyring (secrets.ReadLegacyKey, 05-config
	// §2.4), so a keyring copied back after host loss also restores the app's legacy decryption; nil before S3.
	KeyringLegacy func() ([]byte, bool, error)
	// Reset is the applied ResetFactory swap (a KindReset swap of restoreswap, 05-storage §5S.3.15; R8-1) whose
	// post-reset hook has not yet recorded hook_done, and nil otherwise. The kernel hands it on the reset boot
	// (the swap journal reads set_aside at Open and applied once P4d step 1 has run) and on every later boot whose
	// journal reads applied without hook_done. While it is non-nil the store serves no copy from a file the reset
	// has not yet rewritten (ABO: the gap-fill file), so no secret of the old install is effective; a separate flag
	// would always equal Reset != nil (SIMP8-7). The app's start hook runs its post-reset hook with Reset.Args at
	// Run step 2, after applied, and calls Reset.Done, which journals hook_done; until then every boot hands it
	// again, so the hook is idempotent (R7-2, DATA7-17). The hook never fails boot: a per-entry error is logged,
	// audited and reported as factory_reset_cleanup_incomplete, and the hook still calls Done, so only a crash
	// reruns it (COR8-4, OPS8-5). The kernel hands no Reset, and journals hook_done itself right after applied,
	// for a ResetStore swap (no file effects) and whenever the built store does not implement both ConfigOpener
	// and ConfigResetArgs (no app hook: hello, a native app, ABO after its settings cutover), so such a reset
	// never leaves restore_inflight behind (COR8-4).
	Reset  *ConfigResetRun
	// Files is the labelled state filesystem (06 §6.4.8, R9-7, COR9-5; R-G request): every write, rename, fsync and
	// removal the app's legacy store makes outside Pebble goes through it, with the label of its row in 06 §6.5.1's
	// boot-writer table: ABO's legacy key file .encryption_key (InitEncryption in Open), the gap-fill file
	// <db parent>/config.yaml (the store's post-commit step, §1.12.2, and the post-reset hook) and the post-reset
	// hook's deletions (the delete entries, the Open Library dump files and, only with DeleteLibraryFiles, RootDir's
	// entries). The store keeps it for its later writers. In production it is the plain OS calls;
	// coretest/crashmatrix injects faults through it, so ABO's TestBootCrashMatrix_ABO breaks books' boot at every
	// such write.
	Files  StateFiles
	Logger *slog.Logger
}

// StateFiles is the labelled filesystem of ConfigOpenEnv.Files. Paths are absolute; label names the writer-table
// row. Its confinement is defined once, in 05-storage §5S.3.15 (COR10-4): the state directory, <db parent>, the
// reset intent's Delete entries and its HookPaths (the directories ConfigResetPlan.Paths named at staging: the
// Open Library dump directory and, only with DeleteLibraryFiles, the confirmed library root), so the post-reset
// hook reaches exactly HEAD's wipe targets without the kernel parsing hook_args; and StateFiles itself refuses the
// kept paths of 05-storage's rule (4), so a library root that holds the store cannot reach it. S2b.
type StateFiles interface {
	WriteFile(label, path string, data []byte, perm fs.FileMode) error // temp file, fsync, rename, directory fsync
	Rename(label, from, to string) error
	Remove(label, path string) error
	RemoveAll(label, path string) error
}

// ConfigResetArgs (05-storage §5S.3.15 owns it) is implemented by books' legacy config store. When a reset of
// either kind (ResetStore or ResetFactory) is staged, the kernel's one staging function (kernel.stageReset, behind
// legacy.Maintenance.StageFactoryReset and internal/storagehttp's Stager and its Aliases) asserts it on the built
// config store and calls ResetArgs with the request, before anything is staged, so no L1 code names legacy and
// every path records the same arguments (COR8-2, COR8-5). The request is the one struct storage.ResetRequest
// (05-storage owns it; R9-6): the reset kind, the confirm token, delete_library_files and the echoed library_root
// and library_file_count of 05-rbac §6.2, the one confirmation contract; the HEAD-shaped legacy paths mark it as
// such (05-rbac's library_confirm=legacy). ResetArgs:
//   - for ResetFactory with delete_library_files set, runs the preflight first: it resolves RootDir and counts the
//     top-level entries the hook would remove, as HEAD removes each with everything under it
//     (internal/server/handlers/system/handler.go:571-577 @748fd787a), without the hook's kept paths, so the count is
//     one directory read, exact, and a live import changes it only when it creates a new top-level entry; it returns
//     them as ConfigResetPlan.LibraryRoot and LibraryFiles. A RootDir that HEAD's IsDangerousRoot refuses is an error
//     wrapping storage.ErrResetRefused (400, nothing staged; handler.go:563-568). ResetArgs compares nothing:
//     kernel.stageReset compares the preflight's values with req and, unless req.LegacyConfirm is set (HEAD's wipe
//     with no count check), answers a library_root or library_file_count that differs with 05-storage's
//     *storage.LibraryConfirmError carrying the current root and count (422 library_files_confirm_required, the
//     values in the problem body), and nothing is staged (05-storage §5S.3.15; SEC9-4, COR9-4);
//   - with delete_library_files unset (the native route's default), records no library root, counts nothing,
//     runs no dangerous-root check, and the hook touches no file under RootDir (A-17);
//   - for ResetFactory, returns the hook arguments: the Open Library dump directory, the gap-fill file's path and,
//     only when delete_library_files is set, the confirmed library root, so the post-reset hook acts on the
//     recorded paths, never on what the fresh settings say (COR8-4); and, separately in Paths, the directories the
//     hook deletes under (the dump directory and, only with delete_library_files, the library root), which the
//     intent records as hook_paths so the kernel bounds StateFiles without reading hook_args (COR10-4);
//   - for both kinds, returns the settings rows the fresh install starts with (books: HEAD's factory-default blob,
//     §1.12.2 "The reset boot stages HEAD's factory defaults"), which storage's fresh-install batch writes at the
//     reset boot (SEC8-5).
// The rows are rendered by the staging binary, and a build that resumes the reset writes them as given. They are
// factory defaults, so a key they lack takes the resuming build's default (DATA10-9 (b), as 05-storage states). S2b.
type ConfigResetArgs interface {
	ResetArgs(ctx context.Context, req storage.ResetRequest) (ConfigResetPlan, error)
}

// ConfigResetPlan and ConfigResetRun mirror 05-storage §5S.3.15, which wins on spelling (06 declares the same).
// The plan is recorded in the intent: Hook as hook_args (ResetFactory only, nil for ResetStore), Rows as fresh_rows,
// which the reset boot's fresh-install batch writes inside the declaring module's claims (books: HEAD's
// factory-default blob, the same for both kinds, §1.12.2), and LibraryRoot/LibraryFiles, the root and count
// the preflight found, which kernel.stageReset compares with the request (only with delete_library_files; "" and
// 0 otherwise), which the route's 200 answer and its
// audit record name; and Paths as hook_paths (restorejournal.Intent.HookPaths, COR10-4), the only input from
// which the kernel extends StateFiles beyond the state directory and <db parent>. The run is the applied ResetFactory swap handed as ConfigOpenEnv.Reset: Args is the plan's
// Hook, Delete the Recreatable auxiliary DBs outside the DB's filesystem that the hook removes,
// DeleteLibraryFiles the intent's delete_library_files (the hook wipes the recorded root only when it is set),
// and Done journals hook_done with the entries the hook could not remove; the hook calls it once every action has
// been attempted, whatever failed, so only a crash reruns the hook (COR8-4).
type ConfigResetPlan struct {
	Hook         []byte
	Rows         []storage.KV
	LibraryRoot  string
	LibraryFiles int
	Paths        []string // hook_paths: the dump directory and, only with DeleteLibraryFiles, LibraryRoot (COR10-4)
}
type ConfigResetRun struct {
	ID                 string
	Args               []byte
	Delete             []string
	DeleteLibraryFiles bool
	Done               func(ctx context.Context, failed []string) error
}

// LockConfigSettings is the config lock name of legacy.WriteLock (R9-1; WriteLock itself, WriteLocks(names
// []string) []storage.Lock, is declared once in 05-storage §5S.3.11). It names what a write touches, the app's
// stored settings and its legacy secret copies, never an app mutex or key (03 R1): books' config, instance and
// notify-rule stores return HEAD's configBlobSaveMu for it, at its one place in 05-storage §5S.3.2's total rank
// table (R10-4). Only the kernel names it: at 06 P4c it calls WriteLocks([]string{LockConfigSettings}) on the
// built store and hands the result as SettingsLocks to the config Committer, the integrations Registry, notify's
// rule store and secrets (§2.3), none of which can import legacy (L2–L4) or ever calls WriteLocks (COR10-5).
// Each of books' three stores refuses a write staged into a batch that does not carry a CommitLocked hold of this
// lock (storage.LocksHeld(b, l) false; R10-4): OverrideStore.Stage, the instance and rule stores' writes and
// SecretsStore.Write alike. S3.
const LockConfigSettings = "config.settings"

// ConfigExternalWriter: Changed fires when a writer other than config.Store changed stored values (the app's own
// legacy config path, 05-config §1.12.2); config then re-runs OverrideStore.Load (05-config §1.4 step 8). A stage
// bridge (SIMP5-5): it exists from S2 and is deleted in the S3 PR, when the façade makes Store.Apply the only
// writer. config detects it through an unexported mirror.
type ConfigExternalWriter interface {
	Changed() <-chan struct{}
}

// ConfigSecrets: Secrets returns the app's legacy secret copies, which the kernel passes to secrets.Open as
// secrets.Options.LegacyStore (05-config §2.3). It is not a separate store-interface row: SecretsStore is reached
// only through it, under 03 §3.3's config row (SIMP5-8). From S3.
type ConfigSecrets interface {
	Secrets() SecretsStore
}

// SecretsStore (formerly secrets.LegacyStore) is the app's storage of secrets written before core existed.
// Its contract is secrets' unexported mirror legacyStore (05-config §2.3), which has the same method set. The
// kernel hands it to secrets as secrets.Options.LegacyStore, a field typed any, so no SDK type names this method
// set and it may change in a minor like every legacy identifier (R7-7, SIMP7-4).
type SecretsStore interface {
	Read(ctx context.Context, n secrets.Name) (secrets.LegacyCopies, error)
	Kept(n secrets.Name) bool
	Write(b *storage.Batch, n secrets.Name, v secrets.Value, clear bool) error
	KeyMaterial() []byte
	Check(ctx context.Context, r storage.Reader, key []byte) (secrets.LegacyCheck, error)
}

// There is no ConfigCommitObserver (R10-5, DATA10-2). A side effect outside the store that HEAD ran under
// configBlobSaveMu (books: the gap-fill file write) is a post-commit step the store registers on the batch it stages
// into, through 05-storage's batch hook (storage.OnCommitted(b, step)); storage.CommitLocked runs every registered
// step after Commit succeeded and before it releases the locks, so every writer of the blob and the four rows gets
// it inside the hold without calling anything (§1.12.2).
```

The value types `SecretsStore`'s methods carry (`secrets.LegacyCopies`, `secrets.LegacyCheck`), and
`secrets.ReadLegacyKey`, stay in `secrets`: mutual assignability needs result types that both `secrets` and
`legacy` can name, and `secrets` cannot name a `legacy` type. They stay in v1 until a major, inert for an app
without legacy secrets (03 §3.3 "Legacy-only identifiers").

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

**HEAD precedence for legacy names (R5-1(a), normative until each family's cutover).** At HEAD most env names
and file keys are only a base that the stored blob overrides: viper's prefix-less `AutomaticEnv` and `BindEnv`
feed the base config (`cmd/root.go:625`), `LoadConfigFromDatabase` lays every field the blob holds over it
(`internal/config/persistence.go:1131-1170`) and the encrypted secret rows over that (`:1186-1199`), and only the
keys in `envLockedSettings`, `absEnvOnlySettings`, `applyEnvAuthoritativeConfig` (`config.go:2241-2254,2270-2279,
2435-2576`), the config-file Deluge locks (`configFileLockedSettings`, `config.go:2315`) and `SyncConfigFromEnv`'s six (`persistence.go:2023-2057`) are
re-applied over it. An alias marked `BelowStored` (§1.3.1) keeps that: a value set under its `LegacyEnv` names
or its `LegacyYAML` file key enters the **default** layer (`Source` reports `SourceEnv`/`SourceFile` while no stored
value overrides it), a stored value beats it, it locks nothing, and `Apply` writes the key. **A stored value equal to
the default still beats it (SEC7-2):** every HEAD blob holds every field (whole-config save,
`persistence.go:1932-1977`), and HEAD's stored `enable_auth: true` beats `ENABLE_AUTH=false`, so §1.12.2's
"reported as `SourceDefault`" applies only when no operator or `BelowStored` value is set for the key
(`TestBelowStored_StoredDefaultStillWins`). Its canonical
`<APP>_*` name keeps C3's lock. The flag is generated from HEAD's lists by the app (ABO:
`TestAliasBelowStoredMatchesHEAD`), so it cannot drift, and it never applies to a `ProtectOperator` key (a
`FileOnly` key on ABO included: HEAD stores no value for it, so its legacy names are plain operator values that
lock, as at HEAD). The auth-precedence cutover H3 lifts it only for an enabling `enable_auth` value (§1.12.5,
release-noted then); every other flag keeps HEAD's precedence until a separate, owner-decided cutover (R9-5). ABO's legacy flags keep C3: HEAD's
`--playlists` and `--log-level` lose to a stored value (`cmd/root.go:648-651` writes the flag before the blob is
laid over it), and from the S3 façade a typed flag wins; this is recorded in 09 §9.2.6. Example: compose's always-set
`BASIC_AUTH_ENABLED=false` (`docker-compose.yml:20-22`) is a default, so a Basic gate turned on in the UI stays
on (`TestComposeEnvKeepsUIBasicGate`, OPS5-1). **Where it applies (OPS6-7):** `BelowStored` takes effect wherever
core config attaches the app's legacy config store, that is from S2 (P3, through `app.Options.LegacyConfigStore`)
and from S2b at P4d step 1, for legacy env names and legacy file keys only, never for flags.

**The auth switch keeps HEAD's precedence (A-2).** `ENABLE_AUTH` and the file key `enable_auth` are `BelowStored`
like every name HEAD lays the blob over (`enable_auth` is in none of HEAD's re-applied lists,
`internal/config/config.go:2241-2254,2435-2576`, `persistence.go:2023-2057`): a stored `enable_auth` beats them in
both directions, as at HEAD, and the shadow report below names the canonical variable when they differ. The
canonical `<APP>_CORE_AUTH_MODE` locks in both directions, as every canonical name does, so an operator can always
turn auth on. No boot writes a legacy field to carry an operator value across a rollback. The hardening that would
(an enabling legacy value beating a stored one, kept across a rollback) is the post-S10 auth-precedence cutover H3
(§1.12.5), which moves nothing else.

**Rollback consequence: canonical-only values (A-2).** A canonical `<APP>_*` name exists only from the stage that
registers its key, and every older build reads only the legacy names. A value given only under a canonical name is
therefore not read on a rollback below that stage: for example `<APP>_CORE_AUTH_MODE=required` over a stored
`enable_auth: false`, or a canonical-only peer allow-list (env-only at HEAD, where empty admits every peer,
`internal/config/config.go:1709-1719` @748fd787a). 09 §9.2.1 row 3 carries the cost and the release note: while you may
roll back below that stage, keep the stored setting (or, for a key HEAD re-applies from env, its legacy name) at the
same value. Nothing records the names, and no file or health token lists them.

**An empty stored Basic password never shadows `BASIC_AUTH_PASSWORD` (OPS9-7, R-D request).** A UI save with no
password writes an **empty** encrypted `basic_auth_password` row when none exists, and HEAD applies every decrypted
row, the empty one included (`internal/config/persistence.go:1193-1205,1996-2003` @748fd787a), so under the
stored-beats-env rule an install with the gate on and an empty saved password would be locked out by S0's early
fix 5 with no setting or variable to reach Settings. So for `core.http.basic_gate.password` alone, an empty stored
value counts as **no stored value**: a non-empty `BASIC_AUTH_PASSWORD` (its `BelowStored` legacy name) is then
effective, in memory only, never written to the row, and one warning per boot names the variable; with neither, the
gate stays fail-closed (§1.12.2). A non-empty stored password still beats the variable, as at HEAD.
`TestBasicGate_EmptyStoredPasswordDoesNotShadowEnv` covers both. 09's S0 release note gives the recovery: "If you
are locked out, set `BASIC_AUTH_PASSWORD` and restart" (cross-cluster).

**Shadowed legacy names (SEC6-1).** At attach, a `BelowStored` legacy name of a key at `ProtectAdmin` or higher that
is set and differs from the stored value (a stored value equal to the default included, SEC7-2) is listed in
`AttachReport.Shadowed`. Boot logs one warning naming the key and its canonical variable, and health raises
`config_legacy_name_shadowed` (warn); until 05-health's component snapshot lands at S8, HEAD's degraded token list
carries the token, as it carries `basic_gate_no_password` (05-health, SEC7-12). Secret values are compared in
constant time and never logged. An operator who expected the legacy name to win, for example rotating
the Basic password through `BASIC_AUTH_PASSWORD` with auth off from S4 r2, is told which name does.
**Deploy-file defaults are not shadows (SEC10-11).** A name on §1.12.5's list of names a shipped deploy file sets
unconditionally (compose's `BASIC_AUTH_*`, `docker-compose.yml:20-22` @748fd787a) whose value equals that file's
default (`BASIC_AUTH_ENABLED=false`, `BASIC_AUTH_USERNAME=admin`) is listed in `AttachReport.DeployDefaults`
instead and logged once at info; it raises no token. Every default compose install sets them and HEAD's
whole-config save stores `basic_auth_username: ""` (`persistence.go:1932-1977`), so reporting them would raise
`config_legacy_name_shadowed` on every such install for ever and hide a real stale override. A value that differs
from the file's default is still a shadow.

**Empty env values.** An env variable set to the empty string is **unset**, for canonical and legacy names
alike, as viper treats it at HEAD (ABO never calls `AllowEmptyEnv`): compose's `BASIC_AUTH_PASSWORD=` supplies
nothing and cannot lock a password to empty. An operator who means an empty value uses the file or `<NAME>_FILE`.

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

**Legacy decoding (A-7, normative).** Canonical names keep the strict decoder (JSON for objects and arrays, a Go
duration, `true`/`false`). A **legacy** env name or legacy file key decodes with HEAD's per-type semantics, so a
value an operator set for HEAD reads the same after S3, when viper leaves the boot path (§1.12.1). At HEAD every
legacy name goes through viper's typed getters (`internal/config/config.go:3346,3355,3358` `GetBool` for
`enable_auth`, `enable_rate_limit` and `basic_auth_enabled`; `:3226,3229,3276,3298` `GetStringSlice`); env lookup
is prefix-less, by the upper-cased key (`cmd/root.go:625`, `viper.AutomaticEnv`), plus the explicit `BindEnv`
names, and **env variable names are case-sensitive** (ADV-2): `AutomaticEnv` looks up only the upper-cased name, so
`ENABLE_AUTH` is read and `enable_auth` set in the environment is not, and core matches env names exactly too; and the getters convert through `spf13/cast` v1.10.0 (`go.mod`, indirect). The decoder named by
`Alias.Decode` (§1.3.1, generated from HEAD's getter for the key) applies before `Convert`:

| `Decode` | HEAD rule | Anchor (`<gomodcache>/github.com/spf13/cast@v1.10.0`) |
|---|---|---|
| `DecodeBool` | `strconv.ParseBool`: `1`, `t`, `T`, `TRUE`, `true`, `True` and `0`, `f`, `F`, `FALSE`, `false`, `False`; a parse error reads as `false` | `basic.go:51-52`, `zz_generated.go:8-10`; HEAD warns for locked keys only, `internal/config/config.go:2425-2433` |
| `DecodeInt`, `DecodeFloat` | `strconv.ParseInt(s, 0, 0)` after trimming a decimal part (so `0x10`, `1_000`, `8.0`), or `strconv.ParseFloat`; an error reads as `0` | `number.go:407-413,527-546` |
| `DecodeDuration` | a Go duration; a value with no unit letter is nanoseconds; an error reads as `0` | `time.go:86-91` |
| `DecodeStringSlice` | the string split on whitespace (`strings.Fields`), so `".m4b .mp3"` is two elements | `slice.go:94-95` |
| `DecodeString` | the raw string | — |

A legacy file key is matched case-insensitively and its YAML value, once decoded, goes through the same rule.
**One exception, fail-closed (A-7):** a legacy bool that `strconv.ParseBool` rejects (for example `ENABLE_AUTH=yes`,
which HEAD silently reads as `false`, turning auth off) is a **boot error** when its canonical key is under
`core.auth.` or has `ProtectAdmin` or higher; the error names the variable, the spelling and the canonical name, and
the S3 release note says so (§1.12.2). On any other key it reads as `false`, as at HEAD, with one warning per boot.
`TestLegacyEnvDecodeMatchesHEAD` (§1.11) checks the rule against HEAD itself.

**Aliases.** Every legacy name is an `Alias` naming its ledger row. Config itself holds no window and no
alias class: the kernel registers `Store.Aliases()` with `compat` (L4, which may import config; config may not
import compat), and the row's gate (window or permanent), its dates and its `Retired` state live in the ledger
(09 §9.2.9, SIMP2-12). Config's only lifetime input is `Options.Retired`. Every use calls
`Options.OnLegacyUse` with its surface (OPS2-7):

- **`SurfaceEnv`**: an env var or operator-file key that is set. Config reports it once per boot (at `Boot`
  or `Load`, before compat exists); the kernel buffers the report and hands it to compat once compat is built.
- **`SurfaceConfig`**: a legacy API path written through the ABO compat handler. Reported per use. Reading
  or writing the blob is **not** a legacy use: until cutover the blob is the storage format (§1.12.2).

**Blob names outlive the window.** An alias's `LegacyYAML` key is an operator-file key (a user surface, governed
by the ledger like env names); its `LegacyBlob` field is the storage format. They are separate fields (R9-5,
DATA9-6) because they differ: HEAD's `json:"-"` keys have a file key and no blob field. Retiring the row stops the
operator-file and env spellings; the app's legacy config store keeps reading and writing the blob field until that
family's cutover, whatever the row's state.

**Where the rows come from (SIMP3-3, SIMP4-10).** The ledger the kernel hands to `compat` is the app's
(`app.Options.CompatLedger`; ABO embeds a generated copy of `docs/compat-surfaces.md` kept under the books
module, with a CI freshness check, because `.dockerignore` excludes `docs`; R4-4, MOD4-1) merged with core's
own fragment, embedded by `compat`. Core's fragment holds **only names every app shares**: the permanent
`OTEL_*` ecosystem env aliases of [05-metrics](05-metrics.md) §10 (C7). Every window alias, and every alias that
carries an app's name, is a row of the app's ledger (R1). A row ID in both is a boot error (09 §9.2.9);
`TestCoreLedgerFragmentPermanentOnly` asserts that every core-fragment row has gate `permanent`. From **S2b**,
when the ledger lands, a config alias whose row is in neither fails registration (`ErrNoLedgerRow`); at S2 there
is no ledger and no kernel storage, so aliases register unchecked and their uses are logged, not recorded
(COR4-20).

compat emits the one-time deprecation warning for window rows and feeds
`<namespace>_compat_legacy_use_total`. When a window alias may be removed is decided by 09 §9.2.1 (W's dates,
release notes and the owner's check of the process-local counter on the reference host, A-14) and done by a contract PR checked by `TestNoContractBeforeSunset`; nothing is removed or refused
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
| `FieldSignIn` | `ProtectAdmin` | everything else that decides who signs in and how: `core.auth.*` sign-in keys (OAuth/OIDC providers, client ids and secrets, redirect base, default role) ([05-authn](05-authn.md)); the Basic gate's `core.http.basic_gate.*` (row below); the books ABS token sign-in switch |
| — (new, SEC-6) | `ProtectAdmin` | `core.http.{trusted_proxies, client_ip_header, peer_allowlist, cors.allowed_origins, basic_gate.*}` ([05-http-api](05-http-api.md); on a legacy-backed install `peer_allowlist` and `trusted_proxies` are HEAD file-only keys and therefore operator-only, below); `core.updater.{mode, channel, restart_*}` ([05-updater](05-updater.md)); OTLP endpoints and `core.metrics.{path, listen}` ([05-metrics](05-metrics.md)); `core.integrations.egress.lan_allow` (SEC-15; see below) |
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
`core:admin` ([05-rbac](05-rbac.md)). HEAD's rule for every protected field is
`!prior.EnableAuth || auth.MethodFromContext(ctx).MayChangeCredentials()` (`internal/config/update_service.go:781`
@748fd787a), applied after the owner-trust-root check, which is not skipped with auth off (`:772-779`). With auth
off:

- **The keys whose alias is `HEADAuthOffWritable` keep HEAD's rule exactly on a legacy-backed install** (A-2, lead
  decision; R9-5, SEC9-1): `core.auth.mode` (HEAD `enable_auth`) and the Basic gate's
  `core.http.basic_gate.{enabled, username, password}` (HEAD `basic_auth_*`, all four `FieldSignIn` at HEAD,
  `internal/config/protected_fields.go:55-58` @748fd787a). The auth-disabled principal may store them through the
  settings page or the `/api/v1/config` alias, after the owner-trust-root check; each is written into HEAD's own
  field in place (`enable_auth`, `basic_auth_enabled`, `basic_auth_username`, and the password into HEAD's encrypted
  `basic_auth_password` row), so every build reads it after a rollback (05-authn §4.11, `TestAuthEnable_MatchesHEAD`,
  `TestRollback_AuthOffBasicGateStaysOnPrev`). **What selects HEAD's rule (SEC9-13):** the generated per-key
  `Alias.HEADAuthOffWritable` flag (§1.3.1), honoured under §1.3.1's one predicate (the app supplies a legacy
  config store and the key's value lives in a legacy row: its `LegacyBlob` field or a `legacy.SecretsStore` row;
  SEC10-5), so a native app never inherits it (`TestAuthOff_NativeAppRefusesAuthModeWrite`). H3 clears it on
  `enable_auth` only; the three Basic gate keys keep it together until the earlier of H2 and the settings cutover,
  which precedes or ships with H3, with one release note (§1.12.5, SEC9-7, R9-5, SEC10-6). The canonical
  `<APP>_CORE_AUTH_MODE=required` is an additional operator name that locks the key (C3) and that no build before
  S4 r1 reads (S4 r1's gin auth follows the effective key; 05-authn §4.11, 09 §9.2.1 row 3; SEC9-11); ABO's legacy
  `ENABLE_AUTH=true` / `enable_auth: true` keeps HEAD's precedence and does not beat a stored `false` (§1.3.5) until
  the auth-precedence cutover H3 (§1.12.5). The Basic gate's legacy names keep HEAD's precedence through H3 and
  after it (§1.12.5).
- **Integration-bound fields keep HEAD's rule too on a legacy-backed install (R10-10, SEC10-3; lead ruling).** Every
  integration-bound blob field of §1.12.3 (AI endpoint rows and the `openai_*` pair, metadata sources, webhooks,
  Deluge and `download_client`; URLs, hosts, credentials, headers, `auth_ref`, and the creation of a list row) is
  `FieldUnprotected` at HEAD (`internal/config/protected_fields.go:137-165` @748fd787a), so with auth off every
  write passes there (`update_service.go:781`). Their aliases are `HEADAuthOffWritable` (§1.3.1): with auth off
  the auth-disabled principal passes their `ProtectAdmin` check after the owner-trust-root check, on the S3
  façade, on the `/api/v1/config` alias and, from S7, through `integrations.Registry.Put` and `Test`, which apply
  the same rule (05-metadata M1.3). So an auth-off ABO operator keeps creating and editing AI endpoints, metadata
  sources, webhooks and the legacy singletons in Settings, as at HEAD, and every value is written in place in
  HEAD's row, which every older build reads. A list-family row has no operator layer at all, and no spec
  promises an `ABO_CORE_*` name for these fields. The flag ends for an instance's fields together, at the earlier
  of H2 and its family's integrations cutover (05-metadata M1.10).
- **Every other `ProtectAdmin`/`ProtectOwner` key on ABO** (server paths, executables, perimeter, updater and
  sign-in keys other than the four above) is refused through `Apply` with auth off (403, the auth-off half of
  09's `TestLegacyRoleReachUnchanged`), a tightening recorded in 09 §9.2.6 (SEC4-8). An auth-off operator changes
  such a key through its **canonical** name where the key has one, which locks: its legacy names stay
  `BelowStored`, and `config_legacy_name_shadowed` says so. A value given only under a canonical name is not read by an older build, so
  while a rollback below that release is possible the operator keeps the stored setting (or, for a key HEAD
  re-applies from env, its legacy name) at the same value (§1.3.5; 09 §9.2.1 row 3). With authentication off, the
  Basic gate is set in Settings, not only through `<APP>_CORE_HTTP_BASIC_GATE_*`, so that it stays on after a
  rollback (the bullet above; 09 §9.2.1 row 3's release note, cross-cluster).
- **`FileOnly` keys on a legacy-backed install** (HEAD's `json:"-"` fields: the peer allow-list
  `core.http.peer_allowlist`, the tunnel connector addresses `core.http.trusted_proxies`, the login-saturation
  bucket, `public_base_url`, the Cloudflare guest and invite keys, `bootstrap_key_ttl*`, the ABS JWT secret;
  `internal/config/config.go:1636-1759` @748fd787a; the `json:"-"` SMTP fields are integration-bound passthrough
  values, file and env only and never stored, as at HEAD: they are `notify/email`'s in-memory overlay, 05-notify §10) are `ProtectOperator` whatever their declared class, with auth
  on or off: no caller stores them, as at HEAD, so nothing an older build would not read is ever stored for them and
  a rollback never empties a perimeter list (R9-5, DATA9-6; `TestFileOnlyLegacyKeysNeverDBStored`). A native app
  keeps each key's declared class (05-http-api tables `peer_allowlist` and `trusted_proxies` as `admin`).
- **Native apps (hello, TV, Movies) keep SEC4-8 for every protected key, `core.auth.mode` included:** the
  auth-disabled principal changes none through `Apply`, and auth is turned on by setting `core.auth.mode=required`
  through its canonical env name or the operator file's canonical path and restarting. Integration instances follow one rule, stated identically in 05-metadata M1.3 (SEC2-7): any
`core:integrations.manage` holder writes instance URL and credential fields; setting an instance's `Local` flag
(which admits every loopback, private and CGNAT address), or keeping it across a URL change, needs the `ProtectAdmin`
check, and so does `Registry.Test` with `Local=true`; every instance without `Local` reaches public addresses plus
`lan_allow` only. On a **native** instance a secret field whose effective value is operator-sourced (a canonical
overlay or a `BelowStored` fill) is bound to its origin, as 05-metadata M1.4 step 2 states (SEC7-3). On a
**legacy-backed** instance (ABO's singletons until the integrations cutover) that binding reduces to HEAD's
behaviour, which is no regression: the secret goes to the instance's effective destination, as at HEAD (A-2). What
keeps a non-admin from repointing a stored or operator credential there is the `ProtectAdmin` class of the
integration-bound destination and secret fields on the façade from S3 to S7 (below, SEC4-7) and, from S7, 05-metadata M1.3's rules on every route.
On a legacy-backed instance, **creating** an instance (an `ai_endpoints` row, a webhook, a metadata source) and
changing any field that selects which secret is sent (an `ai_endpoints` row's `auth_ref`, a metadata source's
credential keys, the family-level webhook secret) count as a destination and secret change and need the
`ProtectAdmin` check, else 403 with nothing written (R9-4, SEC9-6; 05-metadata M1.3 states the same). With auth
off on a legacy-backed install the auth-disabled principal passes that check (the integration-bound bullet above,
R10-10).

**Integration-bound blob fields before S7 (SEC4-7).** Until 09 S7 hands ABO's legacy singletons to
`integrations.Registry.Put`, their blob fields (URL, host, credentials, headers of the rows marked
*integrations* in §1.12.3) are written through the S3 façade and, from S4, through the `/api/v1/config` alias, where
a custom role holding `core:settings.manage` could otherwise point a URL at a host it controls and have the stored
credential follow it. From S3 every such field is therefore `ProtectAdmin` on the façade (a new row of a list family
and an `ai_endpoints` row's `auth_ref` included, SEC9-6), and a change of a URL's
origin before S7 clears that instance's stored secret (in place: the blob field or the encrypted row is cleared, and a kept
secret's gap-fill file copy removed) unless the same request re-supplies it, judged on the raw payload (05-metadata M1.3's rule), so
the hosts a legacy-backed instance reaches with `Local=true` were all written by an admin (SEC9-11).
`TestPreS7Facade_IntegrationURLIsProtectAdmin` covers both. **This is a tightening, not parity (SEC9-10):** at HEAD
these fields are `FieldUnprotected` (`internal/config/protected_fields.go:137-161` @748fd787a), so an admin's
`settings.manage` API key could change them through `PUT /api/v1/config`; from S3 `ProtectAdmin` needs an interactive
admin session. 09 §9.2.6 records it under "Recorded changes" with the release note "API keys can no longer change
integration URLs or credentials in Settings; use an interactive admin session" (cross-cluster), and
`TestLegacyRoleReachUnchanged` carries the cell. The tightening applies with authentication on only: with auth off
these fields keep HEAD's rule (`HEADAuthOffWritable`, above; R10-10), so 09 records no auth-off loss for them and
its auth-off release note promises no `ABO_CORE_*` path for them (cross-cluster).

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
it and the constraints it must keep. Normative constraint (C13): **nothing in steps 0–4b writes the store, and
steps 5–6 run strictly after the boot's pre-migration checkpoint and after the storage DAG**, as the **first**
every-boot reconcilers of 06 P4d (step 1), before the revocation-journal re-apply (which needs the keyring and the
effective `core.backup.dir`, declared from S4 by `backup/journal`, 05-backup; COR3-2, DATA3-5, COR4-10), before
handles are issued to services and before any listener opens. Config's boot writes nothing apart from
`c/secrets/meta` on a new store.

0. Before 06 P0a the kernel runs `config.ResolveStateDir` once (§1.7, C15) and passes the result to P0a,
   `Options.BootRecordRoot` and every later consumer; once the deferred P−1 is built (A-6), it runs before P−1 and
   hands P−1 the same result (COR9-7).
1. `config.NewStore(opts)`; the kernel registers **core** sections, with the app's `Legacy.Aliases`.
2. `Store.Boot` (06 P0; it reads no storage): takes the state directory from step 0 (a config file that sets
   `core.paths.state_dir` fails boot, §1.7), calls `Options.BootRecordRoot`, then reads the file (parse error =
   boot error), env, flags and the boot record for core sections and validates operator values. The returned
   `BootView` carries module enablement (`core.modules.<id>.enabled`, `ReloadBoot`; COR-14) and the
   operator-only paths `core.paths.state_dir` and `core.storage.path`, which 06's storage-open step (P2) reads
   from it (COR2-8). Step 0 already read `core.storage.path` from every operator layer, the file included
   (OPS5-13), so `Boot` only asserts that the effective path is the one step 0 used (a mismatch is a kernel
   bug and fails boot), and the keyring never lands beside a database that is not the one opened. A retired protected or secret alias set in env or file
   fails boot here (§1.3.6), as does a conflict on a protected key (§1.3.5).
3. The kernel registers the sections of every **enabled** module (from manifests), then `Store.Load` (06 P3)
   seals the registry and builds every section from defaults, operator layers and the boot record.
   Registration after `Load` → `ErrSealed`, boot fails. A section whose operator values fail decode or
   `Validate` fails boot (the operator must fix their input). From S2b the DB layer is attached at step 6.
   **At S2**, before core storage exists, the kernel attaches `app.Options.LegacyConfigStore` here instead (ABO's
   legacy store over the store that `serve` opened before `app.New`, HEAD's order), read-only: a read before any
   write, so C13 holds (COR3-1). The kernel calls no `Open` at S2 (`legacy.ConfigOpener` lands at S2b, COR6-8):
   `serve` already ran ABO's boot steps. The bridge field is deleted in the S2b PR.
4. The kernel builds logging and metrics from `core.log` / `core.metrics` (operator layers and defaults).
   Storage opens at `core.storage.path` (`ProtectOperator`: needed before the DB layer exists), takes its
   pre-migration checkpoint and runs the DAG (05-storage §5S.4).
4b. **Store construction (from S2b; R4-1).** At 06 P4c the kernel builds the DB layer's store: the
   native store over `c/config/` and, when the books manifest supplies `LegacyStores.Config(legacy.Handles{…})`,
   that store over books' own handles, joined by the routing store (§1.3.3, C4). A constructor error fails boot.
5. At 06 P4d step 1, in this order, which 06 §6.5.1's one boot table states identically (COR5-2): (a) the
   kernel calls the store's `Open` when it has `legacy.ConfigOpener` (for ABO, its key-regeneration guard, its encryption init, which loads ABO's
   legacy key, and, from S2b until the S3 façade, its settings load, R4-6, §1.12.2: over the one handle, never a
   second Pebble open, never a write; this is before books' `Register` at 06 P6, the first module code that reads
   `AppConfig`); then (b) `secrets.Open` (from S3) with the store's `legacy.ConfigSecrets` `Secrets()` as its
   `legacy.SecretsStore` (§2.3):
   the keyring and the `k0-legacy` capture of the key (a) loaded; its only store write is `c/secrets/meta` on a
   new store; it retires no derive key
   (that is P5); then (c) `Store.SetSecrets`; then step 6. Run the other way round, `KeyMaterial()` would be nil,
   `k0-legacy` never recorded and every encrypted legacy row unreadable
   (`TestP4dStep1_LegacyOpenBeforeSecrets`). The order is therefore books store `Open` → `secrets.Open` → `SetSecrets` → `Attach`, as 06 v1.6 §6.5.1 states.
6. `Store.Attach` with the store and the kernel's committer (core's `core/internal/configstore`, §1.12.2):
   `OverrideStore.Load` reads,
   nothing is written. Rows are decoded per section; if a section fails
   decode or `Validate` with its overrides but passes without them, **all of that section's overrides are
   quarantined** (kept, ignored, reported, health token `config_overrides_quarantined`) and boot continues.
   Shadowed legacy names are reported in `AttachReport.Shadowed` with health token `config_legacy_name_shadowed`
   (§1.3.5).
   Rationale: an operator cannot repair the DB without booting; ABO's equivalent path is fatal
   (`cmd/root.go:385-391`). Services built after this step (06 P5) see the DB layer; nothing built before it
   reads a DB-layer value.
7. The kernel hands `Store.PassthroughValues()` to the integrations registry, which overlays them in memory
   (05-metadata M1.4; C11). In 06 P5, once the effective config is known, the kernel calls
   `secrets.(*Store).RetireUnused` with the backup service's in-use check (§2.3; COR3-2); before S8 there is no
   backup service, the call is skipped and `verify` derive keys stay usable (fail-safe).
8. The store's `legacy.ConfigExternalWriter.Changed` fires when a writer other than the Store changed stored
   values: from S2 until the S3 PR deletes the capability, ABO's legacy config path after every blob write
   (§1.12.2). Config, which detects the capability at `Attach` through its unexported mirror, re-runs
   `OverrideStore.Load`,
   republishes changed live keys of core sections and marks restart keys pending, so a UI edit of a perimeter
   key reaches the running perimeter without a restart at S2 **and** S2b, as at HEAD (COR3-1, COR4-2).
9. Runtime (from S3): `Apply` = authorize (permission, then protection) → decode candidate per touched section →
   `Validate` → write the boot record if boot keys changed → `Committer.Commit` (one batch: `OverrideStore.Stage`,
   staged secrets (legacy copies in place, native ones in `c/secrets/`), audit; for a legacy store, inside its
   `CommitLocked` hold with the store's post-commit step, §1.3.3) → publish snapshots for live keys (atomic swap) → bump
   generation → `AfterCommit` hooks → wake `OnChange` subscribers. If `Commit` fails after the boot record was
   written, the previous boot record is restored before `Apply` returns (boot keys are restart-class, so nothing
   was published). A value equal to this binary's default is staged as an unset (C4). For a key with a legacy blob
field an unset **writes the default value explicitly and never removes the field** (SEC7-2): an older build lays
only the fields the blob holds over its env (`persistence.go:1131-1170`), so a removed `enable_auth` would let a
left-over `ENABLE_AUTH=false` win there (`TestLegacyUnsetWritesDefault`).
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
| Legacy bool that `strconv.ParseBool` rejects (HEAD reads it as `false`) on a `core.auth.*` key or a key at `ProtectAdmin` or higher | fail-closed (boot), message names the variable, the spelling and the canonical name; release-noted | HEAD would silently turn the switch off, which can open a gate (A-7) |
| Same, on any other key | fail-open: `false`, as at HEAD; one warning per boot | decode parity (A-7) |
| `<NAME>_FILE` unreadable | fail-closed | a missing credential file is a deployment error |
| Boot record unreadable (I/O) | fail-closed | never boot with a module silently re-enabled or disabled |
| Boot record unparsable | fail-open: file renamed `config.boot.json.bad`, ignored, health `config_boot_record_quarantined` | the operator can repair a file, but the app must boot to show why |
| Override rows or the blob unreadable (I/O) | fail-closed | running on defaults would let the next save overwrite stored config (`cmd/root.go:360-364`, comment at `:361`) |
| Stored legacy row unparsable (the books store cannot decode the blob) | fail-open: the store's `Load` yields an error for every key it stores there, so they are quarantined (health `config_overrides_quarantined`), and its `Stage` refuses them with a storage error until the row is repaired; the bytes are never overwritten | a render from bytes the store could not read would destroy them; the operator must be able to boot to see why |
| Store constructor or the store's `Open` (`legacy.ConfigOpener`) fails (incl. ABO's key-regeneration guard, encryption init or, before S3, its settings load, §1.12.2) | fail-closed (boot) | HEAD's class for those steps is Fatal (`cmd/root.go:345-369`): running on defaults would let the next save overwrite stored config |
| `BelowStored` legacy name of a `ProtectAdmin`+ key set and different from the stored value (one equal to the default included, SEC7-2) | fail-open: the stored value stays effective; warn naming the canonical variable, `AttachReport.Shadowed`, health `config_legacy_name_shadowed` | HEAD precedence holds, but an operator who expected the legacy name to win must learn the name that does (SEC6-1) |
| State directory: default not creatable, no explicit value, database path relative or empty | fail-closed (boot), message names `<APP>_CORE_PATHS_STATE_DIR` | HEAD's fallback needs an absolute database path (`state_dir.go:144-147`) |
| State directory: explicit value not usable | fail-closed (boot); never falls back | the operator named it (`state_dir.go:140-142`) |
| Override row fails decode/validate | fail-open per section: quarantine (§1.4 step 6) | DB cannot be fixed without booting |
| Override row for unknown key | fail-open: retained, ignored, reported | downgrade safety (gate G5, 02 §2.4) |
| `Apply` unknown key (incl. a passthrough path) | refuse `ErrUnknownKey`, nothing persisted | typos must not silently no-op; instances are written through 05-metadata |
| `Apply` locked key | refuse `*LockedError` | the value would be discarded at next boot (`config.go:2224-2229` rationale) |
| `Apply` missing permission / protection | refuse `ErrForbidden` / `*ProtectedError` | fail-closed security |
| `Apply` validation failure | refuse `ErrInvalid` with `*FieldError`s | nothing partially applied |
| `Committer.Commit` fails (incl. a render conflict after `storage.RetryOnConflict`'s attempts) | fail-closed: nothing published, generation unchanged, boot record restored | durable-before-visible; the blob is never half-written |
| A books legacy store write (`OverrideStore.Stage`, `SecretsStore.Write`, an instance or rule write) staged into a batch that carries no `CommitLocked` hold of `LockConfigSettings` (`storage.LocksHeld(b, l)` false) | fail-closed: refused with an error wrapping `storage.ErrNotLocked`, nothing staged | a commit outside `configBlobSaveMu` is the race R9-1 closed; the check is on the batch, so a write staged into another batch inside a hold is refused too (R10-4) |
| `AfterCommit` hook error | committed; `Result.PostCommit` set | hooks cannot veto; caller must know (ABO contract `update_service.go:30-48`) |
| `OnChange` fn panics | fail-open: recovered, logged, metric; others continue | one subscriber must not break config |
| `Ref.Resolve` on unset secret | `secrets.ErrNotSet` to the consumer | consumer decides (e.g. provider disabled) |
| Duplicate path / env-name collision / invalid path | fail-closed at registration | programming error |
| `ReloadBoot` key in a module section read before that module is registered | fail-closed at registration | only core sections are visible to `Boot` |

### 1.6 Concurrency contract

- `Handle.Get`, `Generation`, `Lookup`, `Keys` are lock-free (one `atomic.Pointer` load per section; `Keys`
  iterates an immutable published index).
- `Apply` calls are serialized by core config's own store mutex, held from candidate build through publish; it
  replaces ABO's in-memory `mu` (the `AppConfig` lock), never ABO's save mutex. The boot-record write and `Commit`
  happen under it; publish happens after `Commit` returns.
- **Two locks, both kept (DATA9-15, R9-1).** A store may have several writers inside one process. For the books
  legacy config store the blob is written by config's `Apply`, by integrations' legacy instance store and by the
  notify-rule store, which books implements beside it and which share its render (C11). HEAD's process-wide save
  mutex `configBlobSaveMu` (`persistence.go:1890-1897`) **stays**: the three books stores expose it through the
  one capability `legacy.WriteLock` (`WriteLocks(names []string) []storage.Lock`, declared once in 05-storage
  §5S.3.11; this file's lock name is `legacy.LockConfigSettings`, the blob and the four secret rows, for which each
  of the three stores returns the one `configBlobSaveMu` lock at its one rank in 05-storage §5S.3.2's total lock
  order, R10-4). The kernel resolves it at 06 P4c and hands it as `SettingsLocks` to every writer; no writer calls
  `WriteLocks` (COR10-5). **Every writer path HEAD covered goes through `storage.CommitLocked` with it (R10-5,
  DATA10-2):** the config `Committer` (`Apply`), `integrations.Registry.Put`/`Delete` (05-metadata M1.4 step 2a),
  notify's rule store (05-notify) and `secrets.(*Store).Put`/`Delete` of a kept name (§2.3), which before R10-5
  committed outside it. `CommitLocked` takes the lock once and holds it from staging through the commit **and
  through the store's post-commit step**, the gap-fill file write (§1.12.2), so HEAD's serialisation of a save
  from its snapshot through its file step (`persistence.go:1940-1944` @748fd787a) spans core's commit as it spanned
  HEAD's direct one. The hold is marked on the batch, and each books store refuses a write staged into a batch
  without it (`storage.LocksHeld(b, l)`, R10-4; `OverrideStore.Stage` and `SecretsStore.Write` are checked), so a
  path that forgets `CommitLocked`, or stages into a batch captured from outside the hold, fails its test.
  **Core-side mutexes (R10-4, DATA10-3):** core config's `Apply` mutex, like the integrations Registry's writer
  mutex and the rule store's, is taken **before** `CommitLocked`, never inside a `stage` function, so none of them
  inverts with `configBlobSaveMu`; secrets' per-name keyed mutex (§2.6) is the one exception, taken only inside a
  `stage`, after the config lock. A `stage` function never calls a books method that takes a HEAD mutex of a family
  the hold covers (`configBlobSaveMu` is not re-entrant). The legacy `PUT /api/v1/config` alias **splits** each
  request into `Registry.Put` and `Store.Apply` (§1.12.2), each committing its own batch in its own hold, so no
  batch carries both and neither takes the other's core-side mutex inside a hold. The store also reads the stored
  bytes with `GetForUpdate`, so a conflicting render (none can be staged outside the lock from S3, by the
  exclusive-writer rule of §1.12.2 and the batch check above) fails the later commit with `storage.ErrConflict` and
  the writer re-reads and retries (`storage.RetryOnConflict`, 3 attempts, each a fresh hold). That read check is
  books' own code: the blob carries no storage-enforced `CheckedWrites` during the extraction (A-3). The store
  keeps one pending render per batch, so a key change and a plaintext secret's in-place write in one batch are one
  blob write. `TestLegacyStoresAreSingletons` (R9-1) covers the one store instance per App; `configBlobSaveMu` is a
  package variable at HEAD, so it is one mutex per process anyway (COR10-10).
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

**`core.storage.path` is a kernel pre-boot key from S2 (COR4-20).** Step 3 reads it, but its owning section
(`core.storage`, 05-storage) lands at S2b. From S2 the kernel therefore registers `core.storage.path` itself as a
pre-boot core key (`ProtectOperator`, the `--db` flag and the app's aliases such as `DATABASE_PATH`); `storage`
adopts the declaration at S2b without a change of name, alias or default. Without it a Docker or launchd install
that sets no state-directory variable would fail boot at S2.

**One resolver from S2 (OPS4-8, OPS5-13).** HEAD's `EnsureSecureStateDir` (`cmd/root.go:336`) resolves from
`AppConfig.DatabasePath`, which at that point holds viper's defaults, YAML file, env and flags
(`internal/config/state_dir.go:143-153`), and `ABK_STATE_DIR`. From S2 ABO's `serve`, which still opens the store
before `app.New`, calls `app.ResolveStateDir(opts)` (06 §6.4.5), which runs `config.ResolveStateDir` over the same
inputs the kernel uses, instead of `EnsureSecureStateDir`, so the boot record, a restore intent and
`.encryption_key` are always looked for in one directory. Because step 3 also reads the operator file, a database
path given only in the YAML file with an uncreatable default falls back to its directory exactly as at HEAD (ABO
passes HEAD's viper file, `--config` or `<home>/.audiobook-organizer.yaml`, as `Options.FilePath`), so S2 records
no behaviour change here. One test, one name in this file and in 06: `TestStateDir_OneResolverYAMLDBPath`, an S2
done-check (09), run in the image as well as as a unit test. The resolver changes at S2, so the image job's
second run checks the key location from S0 on (below; OPS5-7).

On Docker and launchd installs step 3 is where `.encryption_key` lives today (the image runs as an
unprivileged user whose only persistent path is the data volume, `Dockerfile:93-96,104`; neither compose nor
the plist sets a state directory), so the legacy key is found there and the keyring lands on the volume. The
shipped Dockerfile and plist set **no** state-directory variable: the fallback already yields the database's
directory, while an explicit value never falls back (step 1), so an image `ENV` would move the keyring of a
container whose `--db` is on another volume into an anonymous volume that a recreate discards, after which boot
fails with `ErrKeyMissing` (OPS4-2). If an explicit value is wanted for discoverability, only
`docker-compose.yml` sets it, next to the named volume and a `command:` that names the same directory. The S0
image job's second run checks it (09 S0; OPS5-7): it prepares the second named volume as a user must (the image
creates and chowns only `/data` and runs as uid 1000, `Dockerfile:93-96`, so a fresh volume is chowned to that
uid first), runs `serve --db` on it, and asserts **from S0 on** that `.encryption_key` sits beside the database on
that volume and that a secret set through `PUT /api/v1/config` still decrypts after the container is recreated;
**from S3** it also asserts `secrets.keyring` there.

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
| `mediacore_config_legacy_plaintext_secrets` | gauge | — (secret keys whose value is read from a plaintext legacy copy, `secrets.Meta.LegacyForm == LegacyPlain`; they stay plaintext until the secrets cutover, §1.12.5) |

Legacy-name use is counted once, by `compat` (`<namespace>_compat_legacy_use_total{surface="config"}`), not by a
second config counter.

### 1.9 Breaking config's upward imports

| Import today | What it is used for | Where it goes |
|---|---|---|
| `internal/database` (`SettingsStore`, `Setting`, `DecryptValue`, `MaskSecret`, `IsEchoedMask`, `LooksLikeMask`) | blob/row persistence, secret decrypt, masks (`persistence.go:883-1260`, `update_service.go:91-373`) | persistence → the `OverrideStore` interface: core's native store, or books' legacy config store (ABO code over ABO's keys, §1.12.2); decrypt of legacy rows stays ABO's cipher inside that store, new secrets → `secrets`; masks → deleted (write-only, C9); the ABO compat handler keeps mask echo logic while its ledger row is live (§2.10) |
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
| `TestAlias_BelowStoredFeedsDefaultLayer` | a `BelowStored` alias set in env or file: a stored value beats it, it is effective while nothing is stored, `LockedBy` is empty and `Apply` succeeds; the canonical name of the same key still locks; ignored on a `ProtectOperator` key (R5-1(a), OPS5-1) |
| `TestBelowStored_StoredDefaultStillWins` | a fixture key whose `BelowStored` alias is set to a non-default value while the stored value equals the default → the stored value is effective with `Source` `SourceOverride`, and the shadow token is raised; with nothing set under any operator or `BelowStored` name the same stored value reports `SourceDefault`. ABO's instances: blob `enable_auth: true`, env `ENABLE_AUTH=false` → auth `required`; blob `enable_auth: false`, env `ENABLE_AUTH=true` → auth off, as at HEAD; both raise `config_legacy_name_shadowed` (SEC7-2, A-2) |
| `TestAttach_LegacyNameShadowedReported` | a `BelowStored` alias of a `ProtectAdmin` key (and of a secret key) set to a value that differs from the stored one → `AttachReport.Shadowed` names it, one warning naming the canonical variable, health `config_legacy_name_shadowed`; no secret value in the log; a `ProtectNone` key is not reported (SEC6-1). Compose cell: compose's default environment (`BASIC_AUTH_ENABLED=false`, `BASIC_AUTH_USERNAME=admin`) over HEAD's stored blob with no UI change → `AttachReport.DeployDefaults` lists them, one info line, no health token; `BASIC_AUTH_USERNAME=bob` is still a shadow (SEC10-11) |
| `TestBasicGate_EmptyStoredPasswordDoesNotShadowEnv` | gate enabled, an empty stored `basic_auth_password` row and `BASIC_AUTH_PASSWORD=p`: the effective password is `p` with `Source` `SourceEnv`, nothing is written to the row, one warning names the variable; with a non-empty stored row the stored value wins; with neither, every non-exempt request gets 401 (OPS9-7) |
| `TestAuthOff_NativeAppRefusesAuthModeWrite` | hello (no legacy config store) with auth off: the auth-disabled principal's `Apply` of `core.auth.mode=required` and of `core.http.basic_gate.enabled` → 403, nothing stored; the same writes on a fixture with a legacy store whose aliases are `HEADAuthOffWritable` succeed after the owner-trust-root check and land in the legacy fields, the Basic password included (its `legacy.SecretsStore` row, SEC10-5); an integration-bound URL field of the fixture lands in its blob field (R10-10); a fixture whose password lives in `c/secrets/` (after H2) refuses the password write (SEC9-13, SEC9-1) |
| `TestAuthOff_IntegrationFieldsWritableAsHEAD` | (ABO, from S4 r2) auth off: `PUT /api/v1/config` adding an `ai_endpoints` row with a URL and `auth_ref`, adding a metadata source, editing the webhook URL and the Deluge host → 200, each written in place in HEAD's blob field, and a HEAD-built `prev` reads them; the same requests with auth on from a non-admin `core:settings.manage` key → 403 (R10-10, SEC10-3) |
| `TestFileOnlyKey_OperatorWhileLegacyStoreAttached` | a `FileOnly` alias's key declared `ProtectAdmin`: with a legacy store attached, `KeyState.Protection` is operator, `Apply` → `*ProtectedError{Need: ProtectOperator}`, a planted `c/config/` row is ignored and reported; without one (native app) the declared class applies; `FileOnly()` is true exactly for a `LegacyYAML` key with no `LegacyBlob` (R9-5) |
| `TestPassthroughValues_ReportBelowStored` | a passthrough value given under a `BelowStored` legacy name carries `OperatorValue.BelowStored=true`; under a canonical spelling, `false` (C11, SEC6-5) |
| `TestEnv_EmptyValueIsUnset` | `ABO_X=""` and a legacy name set to `""` supply nothing: no lock, no value (HEAD parity with viper) |
| `TestOverride_IgnoredWhenOperatorLocked` | DB row kept, not applied, reported in `AttachReport.Ignored` |
| `TestApply_RefusesLocked` / `_Protected` / `_Forbidden` / `_UnknownKey` / `_Invalid` | error type, nothing persisted, generation unchanged |
| `TestApply_SettingsManageCannotChangeAdminKeys` | actor with `core:settings.manage`, interactive, no `core:admin` → `*ProtectedError{Need: ProtectAdmin}` on a sign-in, proxy and updater key |
| `TestEveryURLPathOrSignInKeyClassified` | §1.3.7 shape rule over every registered core and fixture key |
| `TestApply_RestartKeyNotPublished` | `Get` unchanged, `Pending` changed, `RestartPending` returned |
| `TestApply_CommitFailureNothingPublished` | fake `Committer` error; boot record restored |
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
| `TestStateDirFallbackMatchesHEAD` | table over (explicit set or not, default creatable or not, DB path absolute / relative / empty): explicit never falls back; uncreatable default → `Dir(core.storage.path)` with the warning; relative or empty DB path → boot error; a fallback whose DB path the config file later moves → boot error naming both remedies; P0a and `Boot` (and the deferred P−1 once built, A-6) receive the same directory (OPS3-1) |
| `TestStateDir_OneResolverYAMLDBPath` | a database path given only in the YAML file plus an uncreatable default: the kernel and ABO's `serve` resolve the boot record, a restore intent and the legacy key file to that path's directory, as HEAD does; the updater marker is not an input (OPS4-8, OPS5-13; the one name 06 also uses) |
| `TestStoragePathPreBootFromS2` | an S2 kernel (no `storage` section) resolves `core.storage.path` from `--db` and the app alias, and falls back to its directory (COR4-20) |
| `TestP4dStep1_LegacyOpenBeforeSecrets` | first S3 boot with ABO's legacy key file: the store's `legacy.ConfigOpener` `Open` runs before `secrets.Open`, `KeyMaterial()` is non-nil there, `k0-legacy` is recorded and the encrypted legacy rows read (COR5-2) |
| `TestNoStoreWriteBeforeCheckpoint` | a recording fake store sees no write from the store constructor, its `Open` (`legacy.ConfigOpener`), `secrets.Open` or `Attach` before the checkpoint marker, none at all from `Attach`, and from `secrets.Open` only `c/secrets/meta` on a new store (C13) |
| `TestRoutedStore_OneStorageRule` | with a fake legacy store: a key with a legacy blob name is loaded from and staged into the legacy store only, a key with none into `c/config/` only; one `Commit` touching both stages one batch; an older-build fixture over the same store reads the blob unchanged and never sees the `c/config/` rows (A-2, C4) |
| `TestLegacyEnvDecodeMatchesHEAD` | (ABO, in `extraction-gates` from S3) a generated corpus of every alias × representative spellings of its `Decode` type (bools: every `strconv.ParseBool` spelling plus `yes`, `on`, ` true`, garbage; slices: one, two and comma-separated words; ints: `08`, `0x10`, `1.0`, `abc`; durations: `90`, `90s`, `1h`, `abc`), through env and through the legacy file key, decoded by HEAD's `InitConfig` and by core `Load`, with every effective value diffed; the cases the fail-closed rule refuses are listed and must fail boot. HEAD's side is built from `PREV_REF` (09 §9.2.1) while `PREV_REF` predates S3; the S3 PR commits its output as `testdata/legacy_decode.golden`, which later stages compare against, because no later `PREV_REF` has HEAD's viper loader (A-7, ADV-2) |
| `TestLegacyBool_SilentFalseOnAuthKeyFailsBoot` | `ENABLE_AUTH=yes`, `BASIC_AUTH_ENABLED=on` and the same spellings as legacy file keys → boot error naming the variable, the spelling and the canonical name; the same spelling on a `ProtectNone` bool → `false` with one warning (A-7) |
| `TestNativeOverrideStore_RoundTrip` | the native store over `c/config/`: set, unset and reload of scalar, map and boot-adjacent keys; one row per explicitly set key; passes `configtest.OverrideStoreSuite`; implements none of package `legacy`'s config capabilities (SIMP4-3, SIMP5-4, R6-3) |
| `configtest.OverrideStoreSuite` (exported contract) | run by every implementation, ABO's with HEAD fixtures: `Load` is read-only; `Stage` changes exactly the staged keys (others byte-equal after a JSON round trip); an unset writes the default, and for a key with a legacy field writes it explicitly and never removes the field (`TestLegacyUnsetWritesDefault`, SEC7-2); `Alias.Revert` applies to converted keys; two concurrent `Stage`s on one row both survive through the `Committer`'s retry; an unparsable row quarantines its keys and is never overwritten (R4-1) |
| `legacytest.ConfigOverrideStoreSuite` (exported contract, in package `legacy`'s test package, outside the API-stability gate with it, R6-3) | the one legacy suite of `config.OverrideStore` (SIMP6-6: one legacy suite per store interface, running a case for each capability the implementation has, SIMP5-4): `ConfigOpener.Open` writes nothing; `ConfigExternalWriter.Changed` after an outside write republishes a live key without a restart; `ConfigSecrets.Secrets` returns a `legacy.SecretsStore` that passes the secrets read-order cases; a write staged into a batch without a `CommitLocked` hold of the store's lock is refused, also when the write is staged inside a hold into a second batch (R10-4); the post-commit step a write registers runs once per committed batch, inside the hold (a probe lock-holder blocked on the lock does not run until the step returned), and not at all for a batch that failed to commit (R10-5) |
| `TestOverrideStore_ChangedRepublishesAtS2AndS2b` | a fake legacy config store at the S2 shape (attached at P3 through `app.Options.LegacyConfigStore`) and the S2b shape (built at P4c, attached at P4d step 1): a core key set only in the stored row (the rate-limiter switch, the CORS origins) is effective after attach; an outside write signalled on `legacy.ConfigExternalWriter.Changed` reaches the live key without a restart (COR3-1, COR4-2) |
| `TestPreS7Facade_IntegrationURLIsProtectAdmin` | from S3 to S7, a `core:settings.manage` actor without `core:admin` gets `*ProtectedError` on an integration-bound URL, host, credential or header field; an admin's URL-origin change without the credential clears the stored secret in place (the blob field or the encrypted row, and a kept one's file copy) (SEC4-7). An **S3 variant** fills the actor by §1.12.1's S3 rule (seed `admin` role or auth off holds `core:admin`); an **S4 variant** uses the native rule from S4 r2 (SEC5-15) |
| `TestCoreLedgerFragmentPermanentOnly` | every row of core's ledger fragment has gate `permanent` and names no app key (SIMP4-10) |
| `TestSectionPath_NestedNoOverlap` | `core.log` + `core.log.journal` + `core.log.schedules` register; a parent field named `journal` → `ErrDuplicate` |
| `TestConfigFileHasSecrets_Redacted` | a file with a secret key or a passthrough value → `true`; `RedactedConfigFile` contains neither |
| `TestPassthrough_NotOwnedNotWritable` | passthrough values returned raw with origin; `Apply` on them → `ErrUnknownKey` |
| `TestSchema_Golden` | schema of a fixture section equals `testdata/schema.golden.json` (guards the jsonschema-go pin) |
| `TestSchema_SameOverridesAsOpenAPI` | `config.Duration`, `secrets.Ref` render identically in the settings schema and an `httpapi` fixture built from `jsonschemax.Base().With(config.Overrides())` without a config store |
| `TestSchema_OverrideSetsDisjoint` | `Base().With(config.Overrides()).With(<httpapi's set>)` does not panic; `time.Time` is in `Base()` only (SIMP3-1) |
| `TestSchema_SecretIsWriteOnly` / `TestKeys_NeverContainSecretValues` | secret never in `Effective` or schema defaults |
| `TestSnapshot_ImmutableUnderRace` | `-race`; `coretest` fingerprint check |
| `TestRegistry_SealedAfterLoad` | `ErrSealed` |
| `TestConfigImportsOnlyAllowed` | `go list -deps` allow-list for `core/config`: stdlib, yaml, jsonschema-go, `internal/jsonschemax`, `secrets`, `storage`, `metrics` (and their own allowed imports); `jsonschemax` imports only jsonschema-go |
| `FuzzEnvDecode`, `FuzzPatchDecode` | no panic, typed errors only |

### 1.12 Migration from ABO

#### 1.12.1 Code: façade required, call-site migration optional

R5 binds core only (SIMP-3). ABO's globals survive as books-module façades over core handles; removing them is
an optional ratchet, not an extraction stage.

1. **Stage 0 (required; in ABO, no core needed):** convert the 8 bypassing writes (§1.2) to `Mutate`, and add
   `TestNoDirectAppConfigWrites` (AST: no assignment to `config.AppConfig` outside `internal/config`). Without
   this, the façade's projection would silently drop those writes.
2. **Stage 1 (required; 09 S3) — façade.** Its done-check is that the effective config of the production env,
   YAML file and blob equals HEAD's (not the previous stage's, COR3-1); it brings no
   stored-data change: everything it writes is in HEAD's representation (§1.12.2, A-2). ABO's `internal/config` keeps its exported surface and becomes a
   façade. The books module constructs one **`Facade` value** per App (the type lives in ABO's `internal/config` itself, so
   it adds no package path; MOD6-11) from the handles it registered
   and the core handles it reads (`core.storage`, `core.auth`, …), and installs it as the target of ABO's
   package-level `AppConfig`, `Snapshot`, `Mutate`, `Generation`, `SettingLocks`, `EnvLockedSettings`. `AppConfig`
   is a **projection** rebuilt under the façade's lock on every core generation change (one `OnChange` per
   section); secret fields in the projection are resolved from `secrets` in memory only. `Mutate(fn)` diffs the
   before/after `Config` and turns it into one `Store.Apply` through the alias table. All 215 files and 341 test
   files compile unchanged; tests use a `coretest` store behind the façade. viper is removed from the boot path
   here (`configFromViper` is replaced by core `Load` + aliases, whose legacy names decode with HEAD's semantics,
   §1.3.6, checked by `TestLegacyEnvDecodeMatchesHEAD`), and ABO's settings load leaves the books
   store's `Open` (§1.12.2). Multi-app is not ruled out: the façade is a per-App value; only ABO's
   delegation from package-level names is process-wide.
   **The S3 actor (SEC5-15).** At S3 `rbac` and `auth` do not exist yet and `PUT /api/v1/config` still runs gin's
   `RequireAuth`, so the façade fills `config.Actor` from gin's request context with HEAD's rule
   (`internal/config/update_service.go:781`): `Can("core:admin")` holds iff the request's permission set came
   from the seed `admin` role, or auth is off; `Interactive` is `auth.Method.MayChangeCredentials()`, or auth
   is off; `Owner` is HEAD's owner proof. From S4 r2 the auth-off principal never holds `core:admin`, and §1.3.7's
   auth-off rule applies: on ABO `core.auth.mode`, the Basic gate's three keys and every integration-bound field
   keep HEAD's `!prior.EnableAuth` rule (`HEADAuthOffWritable`, R9-5, R10-10): `core.auth.mode` until H3, the gate
   keys until the earlier of H2 and the settings cutover, the integration-bound fields until the earlier of H2 and
   their family's integrations cutover (§1.3.1; SEC10-6). Every other protected key is refused with auth off, the
   tightening 09 §9.2.6 records for SEC4-8; never earlier.
   `TestPreS7Facade_IntegrationURLIsProtectAdmin` has an S3 and an S4 variant.
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

#### 1.12.2 Data: the books legacy config store (single mechanism; cutover deferred)

This is the only specification of how core config and secrets use ABO's stored settings during the extraction
(R3-1 as refined by R4-1; 02 decision 13). 09 S2–S3 schedule it, the R-test (09 §9.10) exercises its rollback
rows, and it defers 14 Q-E to the secrets cutover (§1.12.5, A-2). There is no import, no re-import, no mirror, no Teach build, no adopt step, no
level step and no contract for config or secrets: **one stored format per family**, read and written in place.

**Who implements what (R4-1).** Core defines `config.OverrideStore` and `legacy.SecretsStore` and ships the native
store. The books module supplies the **books legacy config store** through `Manifest.LegacyStores.Config`
(06; beside the other R4-1 legacy stores): ABO code, in the ABO repo, over ABO's existing keys and
row formats, which keeps HEAD's blob codec (the ten `migrate*Blob` read transforms, `persistence.go:359-824`),
its secret-row cipher and its gap-fill file. Core holds none of those names or formats (R1) and no storage grant
for them; books' runtime handle over its own claims is the one handle involved.

**Package (MOD5-2, R5-2).** The store needs `internal/config` (the load, the codec, `AppConfig`), and
`internal/config` imports the residue `internal/database` from four files (`ai_endpoints.go`,
`persistence.go:915`, `register.go`, `update_service.go`), so it cannot live in the residue or in
`internal/books/store` without closing a cycle. It lives in **`internal/books/configstore`** (a new ABO path,
layer 2 in ABO's `layerOf`; 04 §2.4 owns the name. It shares only its last element with core's
`core/internal/configstore`, the native store and the kernel's committer, so this file always writes both paths in
full, MOD6-11), with the legacy `integrations.InstanceStore` and the books-root half of
`legacy.LibraryRootSource` (SIMP7-8) that render the same blob (C11): it imports `internal/config`, the residue and core
`config`, `secrets`, `integrations`, `library` and `legacy`, and is imported only by `internal/books` (the
`Manifest.LegacyStores` constructors) and, at S2 only, ABO's `main` (`app.Options.LegacyConfigStore`). Nothing
imports it back. Only the row cipher (`internal/database/settings.go:110-235`) stays in the residue. 04 §2.4
lists the path and 03 §3.5.1 counts it. The rest of this section is the
**contract** that implementation must meet; `configtest.OverrideStoreSuite` and the ABO tests listed below check it.

**Where each value lives (ABO).**

| Value | Stored | Read by |
|---|---|---|
| a key that has a legacy blob field (`Alias.LegacyBlob`) | ABO's settings blob, in the legacy JSON shape, written in place | every build |
| a key HEAD never stores (`Alias.FileOnly()`: HEAD's `json:"-"` fields, the peer allow-list and the ABS JWT secret among them) | nowhere: the operator file and env only, as at HEAD; `ProtectOperator` while the books store is attached (§1.3.7; R9-5, DATA9-6) | every build, from the same file and env |
| a key with no legacy name (new core and module keys) | core's native store, `c/config/` (one row per explicitly set key), through the routing store (§1.3.3); older builds never read it | builds that declare the key |
| a secret HEAD keeps in **plaintext** in the blob | its blob field, in place, plaintext, as HEAD writes it, until the secrets cutover (§1.12.5) | every build |
| a secret HEAD keeps **encrypted** (the four of §2.2) | its encrypted legacy row in ABO's cipher, written in place, plus HEAD's gap-fill file copy `<db parent>/config.yaml` for three of them; never in `c/secrets/` before the cutover | every build |
| a secret with no legacy name (new names) | `c/secrets/v/<name>` (§2.3), encrypted | core builds |
| `ReloadBoot` keys (module enablement) | boot record `<state_dir>/config.boot.json` (§1.3.5) | core builds |

**The one storage rule (A-2, R9-5).** A key that has a legacy blob field is stored only in HEAD's legacy row and
format; a `FileOnly` key is stored nowhere, as at HEAD; a key with no legacy name is stored natively under
`c/config/`, which older builds never read, so it costs nothing on a rollback. Routing is fixed per key: a key never moves between the two before its family's cutover. Native-only values are never stored as additional members of
a legacy row (R4-1; an extra member is what an older build would misread or drop, SEC4-3).

**ABO's boot steps in `Open` (R4-6, C17, COR4-1).** HEAD runs `guardAgainstKeyRegeneration`,
`initEncryption` and `loadConfigFromDB` (then `syncConfigFromEnv` and `config.Validate`) after opening the store
and before constructing the server (`cmd/root.go:280,345-352,364-369`), and `loadConfigFromDB` is Fatal because a
server on defaults would write them over the stored blob on its first save (`cmd/root.go:360-363`). From S2b these
steps run **inside the books legacy config store's `Open`** (`legacy.ConfigOpener`, which lands at S2b, COR6-8),
which the kernel calls at 06 P4d step 1 (§1.4 step 5: after storage opened, after books' P4b migrations and after
the store was built at P4c, before core `secrets.Open`, so the legacy key is loaded when secrets captures
`k0-legacy`, COR5-2, and before books' `Register` at P6 constructs `*server.Server`), over books' own handles the
store was built from — never a second Pebble open:

- the key-regeneration guard and ABO's settings-encryption init (file reads of the legacy key under the
  resolved state directory; when the file is absent but `legacy.ConfigOpenEnv.KeyringLegacy` returns the material,
  `Open` uses it and may restore the file for older builds, so a keyring copied back after host loss also
  recovers the legacy rows);
- **from S2b until the S3 façade only**, ABO's settings load into `config.AppConfig` from the blob (with
  `syncConfigFromEnv` and `config.Validate`), because until the façade ABO's code reads `AppConfig` directly;
- a failure of any of them fails `Open` and so the boot, exactly where HEAD's step class is Fatal.

**`Open` writes nothing (COR6-5, DATA5-11, DATA6-11).** This is the one statement of the rule. HEAD's load writes
in three ways, and each has a home outside `Open`:

1. **Reshaping blob migrations.** HEAD persists each `migrate*Blob` reshape it applies with `saveRawBlob`
   (`internal/config/persistence.go:1043-1058` @748fd787a). These functions are idempotent, so in `Open`, as in
   `OverrideStore.Load`, they are **pure read transforms**: the stored bytes keep their old shape until the next
   core write re-renders the fields it changes, and every read in between reshapes them the same way.
2. **Marker-guarded one-shots.** `log_retention_zero_migrated`, `embed_cover_art_default_on_migrated` and
   `maintenance_window_migrated` (`persistence.go:1088-1129`, `:649`, `:1274`, `:1314-1338` @748fd787a) guard
   rewrites that must run **once**: dropping a stored `log_retention_days=0`, dropping a stale
   `embed_cover_art=false` (`migrateEmbedCoverArtBlob`, `:659-674`), and moving the auto-update window. Run on
   every read instead, they would undo an operator's later choice on every boot: an `embed_cover_art=false` set
   on purpose would come back on, and the organizer would write cover art into audio files again. Each becomes a
   **books versioned `Migration`** (`Seed: true`, `Additive`, P4b, over books' `LegacyRange`, 05-storage §5S.3.4;
   numbered from `LegacyCap+1`, R7-2) that performs HEAD's rewrite and writes HEAD's marker in the same batch, so an
   older build also skips it. Because the steps are `Seed`, they also **run on a fresh install**, where the blob
   holds no stale value and they only write the three markers (DATA7-13): recorded applied without running, a fresh
   S2b+ store would lack HEAD's markers, and after a rollback HEAD's load would drop a stored
   `embed_cover_art=false` and rewrite `log_retention_days=0` (`persistence.go:1088-1129` @748fd787a), a loss that
   outlives the roll-forward (`TestFreshInstallWritesHEADMarkers`). The read transform of each applies only while
   neither HEAD's marker nor that step's applied record exists, so it never runs on a store the step has passed.
3. **The corrupt-secret repair.** HEAD's load re-encrypts what the gap-fill file holds for a secret row it cannot
   decrypt and deletes the rest (`persistence.go:1233-1262`). `Open` runs the load **without that repair**: an
   undecryptable row is kept and reported (§2.5, DATA3-12), the gap-fill file still fills the in-memory value as
   HEAD's `LoadConfigFromFile` does, and the next save re-encrypts it through HEAD's ordinary path. The repair can
   only arise after the guard was overridden (`ABK_ALLOW_ENCRYPTION_KEY_REGEN`), and its deletions are
   out-of-scope defect 4. The repair is therefore not a blind writer that a later stage converts: it does not run
   on a core build at all.

A value `Open` could not read is never replaced by a default. ABO's test
`TestFreshInstall_EmbedCoverArtFalseSurvivesRestart` (§2.9) covers item 2, and `TestLegacyOpen_NoCorruptSecretRepair`
covers item 3; `legacytest.ConfigOverrideStoreSuite`'s "`Open` writes nothing" case covers all three.

At S2 `serve` still runs these steps before `app.New` (HEAD's order, HEAD's writes included, before any claim
exists) and the kernel does not call `Open` (COR6-8). At S3 the façade replaces the settings load (§1.12.1); the
guard and the encryption init stay in `Open` until cutover, because the legacy rows stay in ABO's cipher. ABO's
test `TestS2bAppConfigFromBlob` (a value set only in the blob is effective in `AppConfig` after an S2b boot, and a
`PUT /api/v1/config` that changes one field leaves every other non-default blob field byte-identical) is an S2b
done-check (09); it passes at S2b because the config rows refuse unchecked writes only from S3 (R5-3, below).

**Reading (`OverrideStore.Load`, every boot; read-only).** The DB layer is the union of:

- the blob, decoded with ABO's read-time transforms, as HEAD's loader does, and mapped onto canonical paths
  through the aliases (`Convert`). A stored value equal to **this binary's** shipped default is reported as
  `SourceDefault`, not as an override, **only when no operator or `BelowStored` value is set for the key**: the
  effective value is the same today, and a later default change reaches keys that hold the old default
  (release-noted: a value a user set equal to the default follows it too; HEAD froze every default at save time,
  `persistence.go:1180-1188`). When a `BelowStored` value is set, the stored value, default-equal or not, beats it,
  as at HEAD (SEC7-2, §1.3.5). `ProtectOperator` keys in the blob are ignored;
- for the keys the gap-fill file fills when their blob value is empty, the gap-fill file, as HEAD does
  (`persistence.go:42-104`); it holds plaintext copies of three kept secrets, so it is never archived and the
  boot log names it (out-of-scope defect 2);
- through the routing store, the native store's `c/config/` rows for keys with no legacy name.

The operator lock (C3) then applies as for any DB-layer value, and a `BelowStored` legacy name sits below it
(§1.3.5), as at HEAD. Before S3 there is no `secrets` store, so
secret fields are not part of the layer (a core stage that needs a legacy secret before S3 stays in ABO, 09
S2). The store is attached at P3 at S2 (through `app.Options.LegacyConfigStore`) and at P4d step 1 from S2b (§1.4).

**Edits made outside core config (S2 until the S3 façade; COR4-2).** ABO's legacy config path
(`PUT /api/v1/config`, viper's loader) still writes the blob itself. It does so through the books store, whose
`legacy.ConfigExternalWriter.Changed()` fires after every such write at S2 **and** S2b (the capability is deleted in
the S3 PR); config re-runs `Load` and republishes changed live
keys of core sections (§1.4 step 8), so a UI edit of a perimeter key (the rate limiter, CORS origins, the log
level) takes effect without a restart, as at HEAD (COR3-1). The peer allow-list and the tunnel connector list are
not UI-editable at HEAD (`json:"-"`) and stay so (`FileOnly`, §1.3.7; DATA9-6). From the façade on, every write
goes through `Store.Apply`.

**Writing (`Apply` → `Committer.Commit` → `OverrideStore.Stage`, from S3).** One batch holds: (1) the blob re-rendered
from the bytes read with `GetForUpdate`, replacing only the blob fields this commit changes (an unset writes this
binary's default explicitly and never removes the field, SEC7-2; converted keys pass through `Revert`), so the blob
never absorbs default or operator-layer values it did not hold; (2) the native store's `c/config/` sets and deletes
for keys without a legacy name (routing store); (3) the staged secret writes: every secret with a legacy copy in
place (a plaintext one as its blob field, in the same render, an encrypted one as its row), a native one as its
`c/secrets/` record (below); (4) the audit record. The
books store binds its own claim to the batch (participant binding, 05-storage D-S10). Concurrent renders retry
on conflict (§1.6).

**Exclusive writer from S3 (DATA4-4).** From S3 the books legacy config store is the **only** writer of the blob and the
four legacy secret rows. With no core grant, this is enforced by package structure (the
trade R4-1 accepts): ABO's archtest `TestOnlyLegacyConfigStoreWritesSettingsRows`, the config instance of 09's
umbrella ratchet `TestLegacyFamiliesWrittenOnlyByLegacyStores` (09 §9.2.1), fails when any other ABO package
writes those keys, and ABO's `POST /operations/set-internal-flag` (which writes any settings-family key from
the request body, `internal/server/handlers/operations/handler.go:378-397`) refuses every key of 05-storage
§5S.9.1 row 7 (the blob and the four legacy secret rows) and every other key a
store interface serves (09 §9.8) (`TestSetInternalFlagRefusesConfigRows`), because an AST check cannot see a
dynamic key. No storage-enforced `CheckedWrites` applies to these rows during
the extraction (A-3): HEAD's save mutex and this single-writer ratchet protect them, as at HEAD.

**Secrets: every legacy copy is written in place (A-2, C16).** Where HEAD keeps a secret is fixed per name (§2.2),
and the books store reports it (`SecretsStore.Kept`, §2.3): every name with a legacy copy stays in that copy until
the secrets cutover (§1.12.5).

- **Plaintext in the blob** (AcoustID key, the OAuth client secrets, the Deluge and download-client passwords, the
  metadata-source credentials, the webhook signing secret; §2.2): core builds read the blob field and write it in
  place through `SecretsStore.Write`, plaintext, in the blob render of the same batch, exactly as HEAD's
  `SaveConfigToDatabase` does. None of these has a gap-fill file copy (HEAD's `SaveConfigToFile` writes only the
  three encrypted keys below, `persistence.go:168-178`), and no core build gives one any.
- **Encrypted** (`openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`;
  `persistence.go:1955-1958,1989-2006`): core builds read them from HEAD's encrypted rows, in ABO's cipher, and write
  them **in place** exactly as HEAD's `SaveConfigToDatabase` does: a set rewrites the encrypted row in the batch and
  HEAD's gap-fill file copy (`<db parent>/config.yaml`, the three that have one) is rewritten by the store's
  post-commit step, inside the same `configBlobSaveMu` hold (below; R10-5); a clear deletes the row in the batch and
  removes the file key during staging, inside the hold and **before** the batch commits, so a failed file rewrite
  fails the write instead of leaving a value HEAD's gap fill would bring back (`persistence.go:42-92`). Until the cutover they keep
  HEAD's cipher (no key ID, no AAD, out-of-scope defect 3) and the file keeps HEAD's plaintext copies (out-of-scope
  defect 2); `k0-legacy` in the keyring still recovers the rows (§2.4).

There is no record in `c/secrets/`, no sentinel and no roll-forward compare for any of them: the legacy copy is the
one stored value on every build, so an older build after a rollback reads the current value, a rotated key cannot
come back (DATA4-3), and nothing has to be re-entered. Plaintext copies are counted by
`mediacore_config_legacy_plaintext_secrets` and shown with `Source: legacy` in the settings UI. A secret with no
legacy name is native data: its encrypted record in `c/secrets/` is its only stored copy, and no older build knows
the name. A HEAD file-only secret (the "—" rows of §2.2) is never stored while the books store is attached, so an
older build, which reads it from env or file only, reads the same value as the current one (`FileOnly`, R9-5). An undecryptable encrypted row
(ABO's key missing) reads as "must be re-entered" (`Meta.Err = ErrKeyUnavailable`), the boot continues and the row
is kept (DATA3-12).

**The gap-fill write is a post-commit step inside the lock (R10-5, DATA10-2).** HEAD's `SaveConfigToDatabase` holds
`configBlobSaveMu` from before its snapshot until its `config.yaml` step is done (`persistence.go:1940-1944`
@748fd787a), so the file always carries the latest save. Core keeps that: whenever a books store stages a write of
the blob or of a kept secret into a batch (`OverrideStore.Stage`, the legacy instance and rule stores,
`SecretsStore.Write`), it registers its post-commit step on that batch (`storage.OnCommitted`, 05-storage), and
`storage.CommitLocked` runs the step after the commit succeeded and before it releases `configBlobSaveMu`. So
**every** writer path HEAD covered gets it, whichever owner commits: the config `Committer`, `integrations.Registry`,
notify's rule store and `secrets.(*Store).Put`/`Delete` of a kept name (§1.6). The step renders HEAD's managed keys
from the rows and blob it reads **inside the hold**, never from the commit's own values, so a late set can never put
back a key that a clear on another route removed after it, and a `Registry.Put` set rewrites the file copy as an
`Apply` set does (`TestGapFillClearNotUndoneByLateSet`, `TestRegistryPutRewritesGapFillCopy`). A batch that failed to
commit runs no step. A failed step is fail-open (§2.5): the row holds the value, so HEAD's gap fill never consults the
file while it is set, and the next commit's step repairs it.

**The gap-fill file holds only what HEAD manages (ADV-8).** The step writes the file by HEAD's merge
(`SaveConfigToFile`: read, merge, temp file, fsync, rename) under the books store's file mutex, a leaf lock taken only
inside the step and inside the post-reset hook and never held while another lock is taken, re-reading the file under
it, so the hook and a commit can never leave one secret's stale copy behind (DATA5-20); between commits
`configBlobSaveMu` already serialises it. The merge writes only the keys HEAD's `SaveConfigToFile` manages (twelve settings and the three
encrypted secrets, `persistence.go:150-178`) and keeps every other key as it found it, because the same file may be
the operator's `--config` (`:110-112`); no core build writes any other key into it
(`TestCoreWritesOnlyHEADGapFillKeys`).

Effects on older builds after a rollback: none for secrets. The Basic gate compares with the password last saved on
any build, so it is never stranded or opened (OPS5-2; SEC4-1, DATA4-1, OPS4-1); provider keys and the webhook
signing secret keep working. A gate that reads enabled with no usable password on a core build (unset, empty with no
non-empty `BASIC_AUTH_PASSWORD`, §1.3.5, unreadable, or removed on an older build, e.g. by HEAD's key-regeneration path, `persistence.go:1233-1262`) refuses
every non-exempt request: core's stage 8 is fail-closed ([05-http-api](05-http-api.md) §4.2; R5-1(c), SEC5-4,
DATA5-12).

**Integration-bound keys (C11).** ABO's legacy singletons (Deluge web, `download_client`, AI endpoints and the
`openai_*` pair, `metadata_sources`, the Google Books and Hardcover keys, SMTP, webhooks; §1.12.3 rows marked
*integrations*) stay in the blob. Config does not own or interpret them. integrations reaches their blob fields
through its own legacy instance store ([05-metadata](05-metadata.md) M1), which books implements in the same
package as this store (`internal/books/configstore`, `Manifest.LegacyStores.Integrations`, 06), joins the
caller's `*storage.Batch` and shares this store's pending render (§1.6).
Their secrets follow the secret rule above by name: each is written in place, in HEAD's form, whichever route writes
it (the façade from S3, `Registry.Put` from S7, or the clear that a URL-origin change causes, §1.3.7). From 09 S7, when `integrations` lands, the legacy
`PUT /api/v1/config` path splits each request: the integration-bound fields go to `integrations.Registry.Put` (so
M1.3's trust rules, the `Local` reset on a URL change and the change-URL audit apply once, whichever route wrote
them) and the rest to `Store.Apply` (05-metadata M1.4 step 2a); from S3 to S7 the façade writes them through
`Store.Apply` as `ProtectAdmin` blob fields (§1.3.7, SEC4-7), which with auth off keep HEAD's rule on every route
(`HEADAuthOffWritable`, §1.3.7; R10-10). The blob is owned by books, written from S3 only by
the books store, and outlives every stage of the extraction (DATA3-3).

**Rollback matrix** (inputs to 09's R-test and to 09 §9.2.1's one table of rollback costs and consequences):

| Move | Config the target reads | Secrets the target reads |
|---|---|---|
| S3+ → S0–S2b or HEAD | the blob, current (written in place); keys with no legacy name (`c/config/` rows) do not exist there | every secret with a legacy copy: its current value, written in place. Native secrets (no legacy copy) do not exist there; the older build reads those names from env or file, as before |
| S0–S2b or HEAD → S3+ again | the blob read in place: every change made on the older build is effective at the next boot; the `c/config/` rows are effective again unchanged | the legacy copies as the older build left them; native records unchanged |
| S2b → S2, S2 → HEAD | the blob (core config never writes it before S3) | unchanged |
| S3+ → S3+ | the blob and the `c/config/` rows | the legacy copies and `c/secrets/` |

**Stated consequences for 09 §9.2.1's table (OPS4-11):** (a) no secret needs re-entry after a rollback, and a value
given only under a canonical name is not read below the stage that registers it (§1.3.5); (b) an older build's
**settings** reset does not touch values that live only in core records — `c/config/` rows and `c/secrets/` records
of names with no legacy copy — so they are effective again after roll-forward, but its **factory** reset
(`PebbleStore.Reset`, `internal/database/pebble_store.go:6310-6331`, behind `POST /system/factory-reset` and
`POST /system/reset`, `internal/server/handlers/system/handler.go:538-560,463-491`) deletes every key, core records
included, so nothing written on S3+ survives it.

**A core build's factory reset (DATA6-6, R7-2, R8-1).** From S2b HEAD's `Reset` cannot run: strict claims refuse its
whole-keyspace `DeleteRange`, and in the shadow release it would wipe `c/` under a live kernel. HEAD has two callers
of it, `POST /system/factory-reset` (`internal/server/handlers/system/handler.go:538-589` @748fd787a) and
`POST /system/reset` (`ResetSystem`, `:463-491`, which wipes the store and resets settings but deletes no file). A
core build's reset is therefore a kind of the one staged, restart-bound swap, `KindReset` of `restoreswap`
(05-storage §5S.3.15 owns it; SIMP8-1), and the next boot is a fresh install. It has two kinds: **`ResetStore`**,
for HEAD's `POST /system/reset` (store and settings only, no file effects), and **`ResetFactory`**, which adds the
post-reset hook (COR8-1). **The request only stages; nothing destructive runs before the restart** (R7-2; COR7-1,
SEC7-4, SEC7-5, DATA7-6):

- **Who stages.** From S2b until S4 r2, books' gin handlers for both legacy routes, behind HEAD's chain unchanged
  (`credRoute`, the always-authenticated admin group and the owner gate,
  `internal/server/wire_system_routes.go:45-48` @748fd787a), call `legacy.Maintenance.StageFactoryReset` with the
  kind, the narrow kernel-built handle that the module declaring legacy claims receives as
  `module.StorageHandles.Maintenance` (06, 05-storage). There is no core route before S4 r2. At S4 r2
  `POST /api/core/v1/system/factory-reset` lands in `internal/storagehttp` with its guards and audit, and both legacy
  paths become its `Alias`es. Every path reaches the kernel's one staging function, which calls
  `legacy.ConfigResetArgs.ResetArgs` on the built config store and passes the result into the intent (§1.3.3,
  COR8-2), so the core route and its aliases record the same arguments as the S2b handlers. They all pass the one
  request struct `storage.ResetRequest` (05-storage; R9-6), whose confirmation contract is 05-rbac §6.2's: with
  `delete_library_files` `kernel.stageReset` compares the root and count with the preflight's before anything is staged (422
  `library_files_confirm_required` on a mismatch, §1.3.3), and the HEAD-shaped legacy paths keep HEAD's wipe with no
  count. Every target is pre-validated at staging; a dangerous `RootDir` answers 400 and nothing is staged only
  when the reset will wipe the library (COR8-4, SEC9-4).
- **The live settings are never written with defaults.** Neither the handlers nor the route calls
  `config.ResetToDefaults` or `SaveConfigToDatabase` in the request (HEAD's `handler.go:485,584-589` do). An intent
  that the next boot rejects (one rule for every swap kind: `expires_at` passed, another binary, the live store's
  open stamp moved) therefore leaves the store and every setting, the Basic gate's `basic_auth_enabled` among them,
  exactly as they were (`TestFactoryResetRejectedLeavesStoreAndSettings`, 05-storage). While the swap's intent
  (`restore/pending.json` with `kind: "reset"`) or its in-flight file exists, from the set-aside until `hook_done`,
  `CheckReader` refuses with `restore_pending` or `restore_inflight`, whose message names the kind, so
  `make rollback`, `deploy` and the updater refuse (05-storage `CheckReader`). A failed set-aside moves back what it
  moved, journals `rejected` and boots the untouched store `degraded` (COR8-3, DATA8-1).
- **The reset boot stages HEAD's factory defaults (SEC8-5).** HEAD's factory reset stores `factoryDefaultConfig()`,
  built from the shipped defaults alone and never from the process environment (`internal/config/config.go:4111-4155`
  @748fd787a), with `root_dir` empty and `setup_complete` false (`handler.go:584-589`), so its stored
  `enable_auth: true` and `enable_rate_limit: true` beat a stale `ENABLE_AUTH=false` or `ENABLE_RATE_LIMIT=false` at
  the next boot. A fresh store with no blob would let those `BelowStored` names win (§1.3.5): auth would come back
  **off**, with no users and remote access allowed. So books' `ResetArgs` renders that blob (HEAD's codec, the
  shipped defaults only, `root_dir` empty, `setup_complete` false) as the settings rows the fresh install starts
  with, the intent records them, and **the reset boot's fresh-install batch writes them** (05-storage §5S.3.15
  "Next boot"), before P4d step 1 opens the store, so `Open`'s settings load at S2b, the S3 façade and every later
  boot read it as the stored blob. It is a write to the fresh store after the restart, so "the live settings are
  never written with defaults" holds, and books' code renders every byte of it, so the exclusive-writer rule above
  stands. The same blob is written for both kinds: HEAD's `ResetSystem` leaves `factoryDefaultConfig` in memory and
  never persists it (`handler.go:485`), so after a restart HEAD itself has no stored `root_dir` either, and writing
  the defaults closes the same stale-env hole for it (`TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`);
- the boot record `<state_dir>/config.boot.json` is set aside with the database, beside itself in the state
  directory (each target is set aside on its own filesystem; 05-storage §5S.3.15 names the destination, COR8-3), so
  module enablement returns to its defaults, as every other stored setting does;
- the keyring (data, derive and `k0-legacy` keys) and ABO's legacy key file are **kept** unchanged, and the
  keyring export stays valid. Keeping them is the lead's decision (round 6), 14 Q-J's recommendation (a); the
  owner may override it (DATA6-6). Moving `previous/` back, with the other steps of 05-storage §5S.3.15's manual
  undo (the keyring header's `install_id` set back with `<app> secrets set-install-id`, §2.4), restores the store,
  its secrets and its settings; files deleted by the post-reset hook (below) are not restored. The reset boot, and only it, rewrites the keyring
  header's `install_id` to the fresh install's: the kernel sets `secrets.Options.ResetBoot` on the boot whose
  `KindReset` journal reads `set_aside`, `secrets.Open` rewrites the header at P4d step 1, and only then does the
  kernel mark the journal `applied` (DATA7-17, 05-storage). An ordinary `Open` never rewrites it, because a
  host-loss restore depends on that field (§2.4);
- **HEAD's side effects run in books' post-reset hook** (`ResetFactory` only), which runs only after the swap
  journal reads `applied` and is stated as not undoable (05-storage §5S.3.15 owns its phase and its completion
  record). For config it rewrites HEAD's gap-fill file `<db parent>/config.yaml` as HEAD's reset leaves it, without
  the three kept secrets' plaintext copies (`persistence.go:168-178`). That file sits beside the DB directory, so
  the set-aside does not move it, and the fresh blob's empty kept-secret fields would otherwise gap-fill the old
  install's keys. Until the hook has completed, the kernel hands the run as `legacy.ConfigOpenEnv.Reset`, and while
  it is non-nil the books store serves no gap-fill copy, so no old key is effective on the reset boot or after a
  crash before the hook (`TestFactoryResetClearsGapFillSecrets`). The same hook carries HEAD's Open Library data
  deletion and, only when the intent's `delete_library_files` is set (`ConfigResetRun.DeleteLibraryFiles`), HEAD's
  library `RootDir` wipe of the confirmed root (`handler.go:556-583`; A-17): the legacy paths set it, as HEAD always
  wiped, and the native route only on the confirmed opt-in (05-rbac §6.2). The hook never fails
  boot: an entry it cannot delete is logged, audited and reported as `factory_reset_cleanup_incomplete`, and it
  still records `hook_done` (COR8-4, OPS8-5). A `ResetStore` swap has no hook: the kernel records `hook_done`
  itself, and the gap-fill file stays as HEAD's `ResetSystem` leaves it (out-of-scope defect 6).

R-test cells: "save the Basic password on cur, roll back: the saved password gets 200 and `<username>:` with an
empty password gets 401 on prev; change it on prev, roll forward: the changed value is effective"
(`TestRollback_BasicGateNeverOpenOnPrev`); "remove the Basic password row on prev with the gate still enabled,
roll forward: `<username>:` gets 401 on cur" (`TestRollback_BasicGateClearedStaysClosed`, SEC5-4); "with
compose's environment at its defaults and the gate enabled in the UI, boot cur: the gate is on" (S3,
`TestComposeEnvKeepsUIBasicGate`, OPS5-1); "rotate a gap-fill key on cur, roll back, save an unrelated setting on
prev, roll forward: cur uses the rotated key" (`TestRollback_RotatedKeyNotResurrected`); "clear a plaintext secret on
prev that cur had written, roll forward: it is cleared on cur"; "save the AcoustID key on cur, roll back: prev
uses it" (`TestRollback_PlaintextSecretSavedOnCurWorksOnPrev`, A-2); "edit a perimeter key through
`PUT /api/v1/config` at S2 and at S2b, it takes effect without a restart"; "edit a Deluge URL on prev, roll
forward, the instance uses it"; "with the blob holding `enable_auth: false`, boot cur with `ENABLE_AUTH=true`:
auth stays off, as at HEAD, and `config_legacy_name_shadowed` names `ABO_CORE_AUTH_MODE`"
(`TestLegacyAuthEnablePrecedenceMatchesHEAD`, A-2); "with `ENABLE_AUTH=false` left in the unit after auth was
turned on in the UI, factory-reset on cur: after the restart auth is required and the stored blob holds HEAD's
factory defaults" (`TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`, SEC8-5); "auth off on S4 r2, the Basic gate enabled through
the settings page with a username and password, roll back to S2b: the gate is on, `<username>:<password>` gets 200
and no credential gets 401" (`TestRollback_AuthOffBasicGateStaysOnPrev`, SEC9-1); "set the peer allow-list in the
operator file, try to save another through `PUT /api/v1/config` and the core settings route: both refused, nothing
stored, and prev after a rollback reads the file's list" (`TestFileOnlyLegacyKeysNeverDBStored`, DATA9-6). These run in root `make ci` against a `prev` binary built from `PREV_REF`
(09 §9.2.1 owns the mechanism; OPS4-6).

**Release note (S3):** "Every setting and every secret is still stored where earlier releases store it, so a
rollback needs no re-entry. A setting given only under an `ABO_CORE_*` name is not read by a release older than the
one that introduced that name: while you may roll back below it, keep the stored setting (or, for a setting the old
release takes from its environment, the old variable) at the same value. `ENABLE_AUTH` still loses to a sign-in
setting saved in the UI, as before; use `ABO_CORE_AUTH_MODE` to override it. A legacy true/false variable for
sign-in, the Basic gate or another admin-protected setting must now be spelled `1`, `t`, `T`, `TRUE`, `true`, `True`,
`0`, `f`, `F`, `FALSE`, `false` or `False`: any other spelling stops startup instead of being read as off. Settings
that earlier releases take only from the environment or the configuration file (the allowed peer networks, the
tunnel connector addresses) can still be set only there." 09 §9.2.1 carries the canonical-name rollback row.

**Cutover (deferred; out of the extraction).** Moving the blob's keys into the native store's `c/config/`, removing
the store's capabilities from package `legacy` (R6-3; not a breaking change, and `secrets.Options.LegacyStore`,
typed `any`, stays in v1 and is left nil, so it needs no SDK change either: R7-7, SIMP7-4) and deleting the blob
and the four rows is a later, per-family, one-way, checkpointed step behind the single per-owner level guard
(05-storage §5S.3.7), decided on measured data; it is the only step that needs core to read books' rows, so it
reintroduces a read-delete grant (05-storage §5S.3.13); its floor is the cutover release itself. Its secrets and
precedence parts are §1.12.5. The post-reset hook reaches books only through `legacy.ConfigOpenEnv.Reset`
(SIMP8-7), and only its gap-fill rewrite is a config concern: the settings cutover must give the Open Library and
`RootDir` wipes another carrier, or end them, and its release note says which. Until then the blob and the four
rows are the storage format, the books store serves them, and nothing in this file deletes them.

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
| `basic_auth_enabled`, `basic_auth_username`, `basic_auth_password` | 3 | `core.http.basic_gate.{enabled, username, password}` (the stage-8 owner; one name, SEC4-10); the file keys and `BASIC_AUTH_*` env names are `BelowStored` aliases (§1.12.4), `basic_auth_username`/`BASIC_AUTH_USERNAME` included: the username's default `""` keeps HEAD's empty-username behaviour (`internal/config/config.go:2806` @748fd787a; 05-http-api §10, SEC6-9) | 05-http-api |
| `enable_auth`, `oauth_*`, `bootstrap_key_ttl*`, `write_startup_readonly_key` | 12 | `core.auth.*` | 05-authn |
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
  `AutomaticEnv`; all three `BelowStored`, because HEAD lays the blob and the encrypted password row over them,
  so compose's always-set defaults never override a gate configured in the UI, OPS5-1), `WHISPER_REMOTE_URL`, `ALLOWED_PEER_CIDRS`, `TUNNEL_CONNECTOR_ADDRS`, `LOGIN_SATURATED_RATE`,
  `LOGIN_SATURATED_BURST` (`deploy/local.conf.example:39-61`, read at `config.go:2838` and `invite_config.go`).
- **Not aliases — shipped but unread by ABO:** names a shipped file sets that ABO does **not** read today stay
  unread, because honouring them would change behaviour: `AUDIOBOOK_ROOT_DIR` (unit `:97`, plist `:38`;
  deliberately unread because the unit's generic value does not exist on the production host,
  `config.go:2569-2575`), `AO_DIR`, `AO_DB` (`docker-compose.yml:18-19`) and `OLLAMA_BASE_URL`
  (`deploy/local.conf.example:40`), none of which has a reader.
- **Not aliases — runtime and ecosystem names config never reads:** `HOME`, `GOMEMLIMIT`, `GOGC`
  (`deploy/audiobook-organizer.service:96,101,104`), `TZ` and `NOTIFY_SOCKET` (read by the Go runtime, the
  systemd notify helper or the OS, never by config).
- **Precedence (R5-1(a)):** independently of its ledger gate, every alias is `BelowStored` exactly when HEAD lays
  the stored blob over that name: every `AutomaticEnv`/`BindEnv` name and legacy file key of a key with a blob
  field, except the keys of `envLockedSettings`, `absEnvOnlySettings`, `applyEnvAuthoritativeConfig`,
  `configFileLockedSettings` and `SyncConfigFromEnv` (§1.3.5). A `FileOnly` key has no blob field, so its names
  are never `BelowStored`: they lock, as at HEAD, where they are the only source (R9-5). ABO generates the flag from those lists into the
  alias table and `abo_keys.golden`; `TestAliasBelowStoredMatchesHEAD` fails when a list and the table disagree.
  `DATABASE_PATH` (env-authoritative at HEAD) and `FP_PARALLEL_WORKERS` (a blob key HEAD does not re-apply, so
  `BelowStored`) are examples of each side. The rule covers **Passthrough** names too (SEC6-5): `OPENAI_BASE_URL`
  (`config.go:1222,2741`, re-applied by no HEAD list) is `BelowStored`, so it fills the AI instance's URL only
  while none is stored (C11), and `TestAliasBelowStoredMatchesHEAD` includes every Passthrough alias. An
  operator-sourced secret such as `OPENAI_API_KEY` goes to the legacy-backed instance's effective destination, as
  at HEAD (A-2, §1.3.7). `ENABLE_AUTH` and the file key `enable_auth` are `BelowStored` with no exemption: a stored
  value beats them, as at HEAD (§1.3.5).
- `TestShippedDeployEnvNamesArePermanent` scans `deploy/*.service`, `deploy/*.plist`, `deploy/local.conf.example`,
  `docker-compose.yml` and `Dockerfile*`, **including commented-out `Environment=` lines** (an operator uncomments
  them, e.g. `local.conf.example:50-61`): every name found is a permanent row or on one of the two not-alias
  lists above; anything else fails.
- After contract, window rows of keys with `ProtectAdmin` or higher, and of secret keys, move to state `Retired`
  and boot refuses to start if one is set (§1.3.6). [05-storage](05-storage.md) additionally refuses to
  **create** a store at the resolved path when none exists there and a retired database-path name is set.

Flags: ABO's cobra adapter maps `--dir`, `--db`, `--db-type`, `--playlists`, `--log-level` (and the other flags of
09 §9.6 row 18, kept until the next major) to paths.

#### 1.12.5 Hardening cutovers H2 and H3 (post-S10)

A-2 keeps HEAD's representation and precedence for ABO through S10. The hardening the extraction does not do is
named here as post-S10 cutovers on N15's one-way, floored machinery (02 N15; 12 and 14 list them). Each is its own
release, its floor is that release, and its release note names what an older build loses. **The owners each one
raises (DATA9-8, R9-11):** H2 raises `books` (it rewrites books' legacy blob fields and secret rows) and `core` (it
writes `c/secrets/`); H3 raises `books` (it changes how books' stored `enable_auth` is read against the legacy
names). The first cutover that raises `core` (H2, or H1 when it moves the identity families) lifts books' `{core,
0}` legacy restore bound, because 05-backup §5B.3.9 rule 3 passes the bound only when the store's `core` level
exceeds 0; H3 raises `books` only and lifts nothing (DATA10-7). Every release from S0 up to the one
before the cutover declares both owners, so every older build refuses a store a cutover stamped, through boot and
`CheckReader` (`TestHardeningCutoverRefusedByEveryOlderRelease`, 09 §9.13).

**Release notes (SEC10-7).** 09 §9.13 owns one release-note text per cutover; this file quotes them verbatim.

- H1 (identity, 05-authn §11.2, listed here for completeness): "Backups made before this release still contain sign-in tokens. They no longer work against this server, but restoring such a backup brings them back until they expire; delete those backups or take new ones."
- H2: "Backups made before this release still contain plaintext third-party secrets. Delete them or take new ones; if the backup directory may have been exposed, rotate those secrets."
- H3: "With authentication off, Settings can no longer turn it on; set `ENABLE_AUTH=true` or `ABO_CORE_AUTH_MODE=required` and restart."

- **H2 — secrets cutover** (09 §9.13). This file decides the four encrypted rows (DATA9-8): the books store's
  plaintext blob secrets **and** the four encrypted rows move into `c/secrets/`, encrypted with the keyring (key IDs,
  AAD), and their legacy copies and gap-fill file keys are removed; the `legacy.SecretsStore` capability goes with
  it, and with it every remaining `HEADAuthOffWritable` flag on a secret it moves (the Basic password and the
  kept integration secrets, so each group's flag ends together, §1.3.1; SEC10-5). An older build then has no copy
  to read, which the floor refuses. 14 Q-E is deferred to it. **What it can and cannot clean (SEC9-3, R9-11,
  R10-11):** the cutover boot rewrites the live store, then deletes **every** pre-cutover checkpoint of this store
  (its own attempt's and every retained or open one under `<db parent>/.mediacore-checkpoints/`, with their records)
  and forces a compaction of the rewritten key ranges with a WAL rotation, so no obsolete SST, WAL segment or
  checkpoint keeps a plaintext copy; the gap-fill file's three keys are removed in the same boot. A swap set-aside
  inside `core.storage.swap.keep_previous` (a restore's `.mediacore-restore/<id>/previous/` and `failed/`, a reset's
  `<state_dir>/restore/<id>/previous/` and its beside-itself destinations) is a full pre-H2 store outside the DB
  directory, so H2 refuses to start while one exists (`ErrCutoverResidue`, naming the paths) unless the operator
  passes the cutover's sweep flag, which removes them in the cutover boot first (DATA10-6). Recovery from a failed
  H2 is then the pre-cutover archive, the floor's restore point. Archives taken before H2 still hold every
  plaintext secret and the encrypted rows in ABO's cipher, and H2 cannot reach them. Its done-check says so plainly
  and is a test, not a live-key read: `TestH2_NoPlaintextSecretOnDisk` plants a canary secret **before** a fixture
  upgrade, a restore and a reset, so retained checkpoints and set-asides hold it, runs the cutover and scans the DB
  directory, `.mediacore-checkpoints/`, `.mediacore-restore/` beside the DB and every recorded beside-itself
  destination, `<state_dir>/restore/` and the gap-fill file. Its release note is 09 §9.13's H2 text, quoted
  verbatim above (SEC10-7).
- **H3 — auth-precedence cutover** (09 §9.13). One H3, stated the same in 05-authn §11.2 and 09 §9.13 (SEC9-7,
  cross-cluster): H3 moves **only auth-enable precedence**, and only in the enabling direction. An operator value
  that enables auth (`ENABLE_AUTH=true`, `enable_auth: true` in the operator file) beats a stored `false`; a value
  that disables auth keeps HEAD's precedence (a stored `true` beats it). H3 also clears `HEADAuthOffWritable` on
  `enable_auth` (R9-5): from H3 the auth-disabled principal can no longer store `core.auth.mode` through `Apply`, and
  auth is turned on by an operator value or by an admin once auth is on, as SEC4-8 states for native apps. The Basic
  gate keys keep the flag, and HEAD's auth-off write rule, until the earlier of H2 and the settings cutover, which
  precedes or ships with H3, so after H3 none of them has it; the cutover that ends it carries their one release
  note (§1.3.1; SEC10-5, SEC10-6). H3's release note: "With authentication off, Settings can no longer turn it on; set `ENABLE_AUTH=true`
  or `ABO_CORE_AUTH_MODE=required` and restart." **Every other `BelowStored` flag keeps HEAD's precedence** after H3,
  until a separate cutover the owner decides per key (R9-5, SEC9-2). That later cutover is bound by two rules, so an
  always-set deploy value can never override a UI setting: a name a shipped deploy file sets unconditionally
  (compose's `BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME` and `BASIC_AUTH_PASSWORD`, `docker-compose.yml:20-22`
  @748fd787a) stays `BelowStored` permanently, and a legacy value that weakens a security control
  (`basic_auth_enabled=false`, `enable_rate_limit=false`) never locks. So compose's `BASIC_AUTH_ENABLED=false` never
  turns off a gate enabled in the UI, at H3 or after it; H3's done-check includes `TestComposeEnvKeepsUIBasicGate`
  and `TestLegacyAuthEnablePrecedenceMatchesHEAD`'s disabling half. H3 ships with or after the settings cutover, so
  no older build has to be kept in step by a write into a legacy field (09 §9.13 copies the dependency).

Neither builds anything during S0–S10, and no stage of 09 depends on either.

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
`internal/database/pebble_store_auth.go:1656-1663`: native stores hash them, and ABO's stay as at HEAD until the
identity cutover, A-2). 09 S3 and 14 Q-E reference this keyring
instead of describing their own.

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
`marshalConfigBlob` strips only boot-only keys, `persistence.go:2126-2139`). The *legacy copies* column lists
where the previous build keeps the value. That knowledge is ABO's: the books legacy config store serves it through
`legacy.SecretsStore` (§2.3) and reads and writes every copy **in place**, in HEAD's form, until the secrets
cutover: a plaintext copy as its blob field, the four encrypted rows in ABO's cipher with their gap-fill file
copies (C16, A-2, §1.12.2). "—" means the previous build reads the field only from env or file (a `json:"-"`
field): such a secret keeps HEAD's storage on a legacy-backed install, the operator layers only, and is never
stored, because a stored value would vanish on a rollback and its consumer would then refuse to start (absauth
without `ABS_JWT_SECRET`) (`FileOnly`, R9-5, DATA9-6); it becomes native data in `c/secrets/` only at its family's
settings cutover, and on a native app, which has no legacy names. Core code never names a legacy row; the only legacy-row writes
of the extraction are the books store's in-place writes.

| Field (JSON key) | Anchor | At rest today | New name (secret) | Legacy copies |
|---|---|---|---|---|
| `openai_api_key` | `persistence.go:1955` | encrypted row | `integrations.ai.<instance>.api_key` | **kept**: row `setting:openai_api_key`; gap-fill file key |
| `google_books_api_key` | `persistence.go:1956` | encrypted row | `integrations.metadata.<instance>.api_key` | **kept**: row `setting:google_books_api_key`; gap-fill file key |
| `hardcover_api_token` | `persistence.go:1957` | encrypted row | `integrations.metadata.<instance>.api_token` | **kept**: row `setting:hardcover_api_token`; gap-fill file key |
| `basic_auth_password` | `persistence.go:1958` | encrypted row | `core.http.basic_gate.password` (SEC4-10) | **kept**: row `setting:basic_auth_password` |
| `acoustid_api_key` | `config.go:1141` | plaintext `config_blob` (masked in API, `update_service.go:94`) | `modules.books.acoustid.api_key` | blob field (in place) |
| `oauth_github_client_secret` | `config.go:1588` | plaintext `config_blob` (value re-applied from env each boot, still written) | `core.auth.oauth.github.client_secret` | blob field (in place) |
| `oauth_google_client_secret` | `config.go:1590` | plaintext `config_blob` | `core.auth.oauth.google.client_secret` | blob field (in place) |
| `deluge_web_password` | `config.go:1777` | plaintext `config_blob` (carried forward when env-locked, `persistence.go:1965-1971`) | `integrations.download.<instance>.password` | blob field (in place) |
| `download_client.torrent.deluge.password` | `config.go:240` | plaintext `config_blob` | `integrations.download.<instance>.password` | blob field (in place) |
| `metadata_sources[].credentials` (map) | `config.go:154` | plaintext `config_blob` | `integrations.metadata.<instance>.credentials.<k>` (one secret per entry) | blob field (per entry, in place) |
| `plugins.webhook` `secret` (HMAC signing key) | `internal/plugins/webhook/plugin.go:58` | plaintext `config_blob` | `integrations.notify.webhook-<n>.secret` (one family-level legacy value staged under every instance's name, 05-notify §10) | blob field (in place) |
| other `plugins[].settings` (free-form map) | `config.go:244-247` | plaintext `config_blob`; may hold credentials (unknowable) | retired with `internal/plugin` (02 N4); any surviving entry declared per adapter | — |
| `abs_jwt_secret` | `config.go:1636` (`json:"-"`) | env / viper file only | `modules.books.abs.jwt_secret` | — |
| `smtp_password` | `config.go:1678` (`json:"-"`) | env / viper file only | `integrations.notify.<instance>.password` | — |
| `cf_policy_api_token` | `config.go:1688` (`json:"-"`) | env / viper file only | `modules.books.cfguests.policy_api_token` | — |
| userinfo in URLs (`openai_base_url`, `whisper_remote_url`, `ai_endpoints[].url`, `embedding.base_url`) | `config.go:1222,1319`, `ai_endpoints.go:40` | plaintext if an operator embeds `user:pass@` | `schema:"format=uri"` validation **rejects** userinfo; credentials go in a sibling `secrets.Ref` | — |

**One generated secret-class list (OPS6-4, R6-7).** ABO generates the secret class of every field in this table
(each row, the "—" rows and the free-form `plugins[].settings` included) into `abo_keys.golden` (09), from its
alias table and the `secrets.Ref` fields; `TestSecretClassListMatchesLegacyStore` fails when that list and the
names for which the books store's `SecretsStore.Kept` is true, plus the "—" rows, disagree. 09 §9.3, the one staging procedure,
blanks every secret field of a restored copy before its first boot with ABO's offline subcommand `staging-scrub`
(R7-6), which also clears every legacy integration URL, runs with the binary of the release that wrote the
archive (R8-3), and refuses any store without 09 §9.3's staging marker (A-10). Its field list exists before the generated one does: **S0 commits `scripts/staging/secret_fields.txt`**,
written by hand from this table (every row, the "—" rows and `plugins[].settings` included), because staging is
needed from S0 while the books `SecretsStore` lands only at S3. **From S3**, `TestSecretClassListMatchesLegacyStore`
also asserts that `secret_fields.txt` equals the generated list, so the scrub cannot drift from what the store
treats as secret (OPS7-4, SEC7-10).

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

// LegacyForm says where a SourceLegacy value was read (diagnostics and the plaintext gauge, 05-config §1.8).
type LegacyForm uint8

const (
	LegacyNoForm      LegacyForm = iota
	LegacyPlain                  // a plaintext primary copy (a blob field)
	LegacyEncrypted              // an encrypted primary copy in the app's own cipher (a legacy row)
	LegacySecondary              // the secondary copy (ABO: the gap-fill file)
)

type Meta struct {
	Name        Name
	Set         bool
	Source      Source
	KeyID       string    // stored only
	UpdatedAt   time.Time
	UpdatedBy   string
	Fingerprint string    // first 8 hex of HMAC-SHA256(fingerprint subkey, value); stable per value, not reversible
	LegacyForm  LegacyForm // set when Source == SourceLegacy
	Err         error     // ErrKeyUnavailable / ErrCorrupt when the row cannot be decrypted
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
	AllowKeyRegen bool              // core.secrets.allow_key_regen (operator only)
	// LegacyStore is the app's legacy copies: a legacy.SecretsStore (the store's legacy.ConfigSecrets Secrets()),
	// set by the kernel (05-config §1.3.3); nil = none. It is typed any, never by the mirror, so no SDK type freezes
	// legacy.SecretsStore's method set (R7-7, SIMP7-4). Open asserts legacyStore on it and fails with
	// storage.ErrLegacyStoreType (boot fails) when the value does not implement it: never a silent skip. After the
	// settings cutover the field stays, inert and nil.
	LegacyStore   any
	// SettingsLocks are the locks the kernel resolved at 06 P4c for legacy.LockConfigSettings on the store that
	// supplies LegacyStore (nil without one; COR10-5). Put and Delete of a kept name build their batch inside
	// storage.CommitLocked(ctx, b, d, stage, SettingsLocks...), so the in-place write and the store's post-commit
	// gap-fill step run under configBlobSaveMu as every other writer's do (R10-5, DATA10-2); Stage is called by a
	// caller that already holds them. secrets never calls WriteLocks.
	SettingsLocks []storage.Lock
	// ResetBoot is set by the kernel only on the reset boot of a core build's factory reset (the KindReset swap
	// journal reads set_aside, 05-storage §5S.3.15): Open then rewrites the keyring header's install_id to the fresh
	// store's (§2.4, DATA7-17). Every other Open leaves that field alone.
	ResetBoot     bool
	Logger        *slog.Logger
	Metrics       *metrics.Scope    // core scope "mediacore.secrets"
	Now           func() time.Time
}

// legacyStore is secrets' unexported mirror of legacy.SecretsStore (R6-3, 03 §3.3 "Legacy-only identifiers"):
// the app's storage of secrets written before core existed (R4-1), implemented in the app's code over the app's
// keys (ABO: the books legacy config store, 05-config §1.12.2). secrets (L2) cannot import legacy (L5), so it names
// the capability only through this interface, asserted on Options.LegacyStore in Open. The mirror test (03 §3.3)
// checks it: export_test.go exports the test alias LegacyStoreMirror, and legacy_mirror_test.go in package
// secrets_test asserts that it and legacy.SecretsStore are mutually assignable, so a renamed or retyped member
// fails secrets' tests (R7-7, SIMP7-1). Core never names a legacy row or
// knows the legacy cipher. The method semantics below are the contract of legacy.SecretsStore.
type legacyStore interface {
	// Read returns n's legacy copies as the app reads them: Primary from the blob field or the legacy row
	// (decrypted with the app's own cipher), Secondary from the gap-fill copy. A zero Value means empty or absent.
	Read(ctx context.Context, n Name) (LegacyCopies, error)
	// Kept reports whether n has a legacy copy, which stays its one stored copy until the secrets cutover (A-2:
	// ABO's four rows HEAD already encrypts and its plaintext blob fields). A kept name has no c/secrets/ record:
	// Put, Stage and Delete call Write instead, and reads return its legacy copies (read order below). Names in the
	// "—" rows of 05-config §2.2 return false (they are FileOnly, never stored on a legacy-backed install, R9-5).
	Kept(n Name) bool
	// Write joins b with an in-place write of a kept name in the app's own form: a plaintext copy as its blob field,
	// an encrypted one in the app's own cipher (clear = empty the field or delete the row). It refuses, staging
	// nothing, when b carries no CommitLocked hold of the store's LockConfigSettings lock (storage.LocksHeld(b, l);
	// R10-4, COR10-2), with an error wrapping storage.ErrNotLocked. Secondary copies are rewritten by the post-commit step Write registers on b, which
	// CommitLocked runs inside the hold, rendered from the rows (R10-5); a clear also removes its secondary copy
	// before Write returns, so a failed removal fails the write (05-config §1.12.2).
	Write(b *storage.Batch, n Name, v Value, clear bool) error
	// KeyMaterial returns the key the app's legacy cipher uses (nil when it has none), recorded opaque in the
	// keyring as k0-legacy so the one keyring export also recovers the legacy rows (C8, §2.4).
	KeyMaterial() []byte
	// Check inspects the legacy copies readable through r (the live store or a staged restore): Encrypted counts
	// non-empty encrypted copies; when key is non-nil, one copy per name is trial-decrypted with it and the names
	// that fail are returned (05-backup restore staging, DATA4-22).
	Check(ctx context.Context, r storage.Reader, key []byte) (LegacyCheck, error)
}

type LegacyCopies struct {
	Primary       Value
	PrimaryForm   LegacyForm // LegacyPlain or LegacyEncrypted
	PrimaryErr    error      // ErrKeyUnavailable / ErrCorrupt when the app's cipher cannot decrypt the row
	Secondary     Value
}

type LegacyCheck struct {
	Encrypted  int
	Unreadable []Name
}

// ReadLegacyKey reads k0-legacy's material from the keyring file without opening the store (file read only),
// for legacy.ConfigOpenEnv.KeyringLegacy (05-config §1.3.3); ok is false when the keyring or the key is absent.
func ReadLegacyKey(stateDir *os.Root, keyFile string) (material []byte, ok bool, err error)

func Open(ctx context.Context, o Options) (*Store, error)

func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
func (s *Store) Stage(b *storage.Batch, w Write) error           // same-batch write used by core/internal/configstore and integrations (COR2-12: a pointer, so Bind and the writes land on the caller's batch); for a name with a legacy copy it calls SecretsStore.Write in b instead of writing c/secrets/
func (s *Store) Ephemeral(n Name, v Value, src Source)            // operator-supplied value; locks the key
func (s *Store) EphemeralDefault(n Name, v Value, src Source)     // a BelowStored legacy name's value (05-config §1.3.5): used only when nothing is stored; locks nothing; never persisted
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]   // never values
// KeyIDsReferenced scans the records readable through r (the live store, or a staged restore) and reports
// every key ID they need, including "k0-legacy" when SecretsStore.Check reports an encrypted legacy copy
// (DATA3-12). Restore reports and key pruning use it.
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
	// A non-nil Options.LegacyStore that does not implement legacyStore fails Open with
	// storage.ErrLegacyStoreType, the one sentinel for every owner's any-typed legacy input (05-storage
	// §5S.3.11; R7-7); secrets defines no sentinel of its own for it.
)
```

**Formats (normative; the format carries key IDs from v1 so it never changes when rotation lands).**

- Keyring file `<state_dir>/secrets.keyring`, 0600, directory 0700, written atomically via `os.Root`
  (temp + fsync + rename):
  `{"format":"mediacore-keyring/1","keys":[{"id":"k1","use":"data","created":"…","status":"active","material":"<base64 32 bytes>"},{"id":"d1","use":"derive",…},{"id":"k0-legacy","use":"legacy","status":"retired",…}]}`.
  Exactly one `active` key per `use` (`data`, `derive`). `use: "data"` keys encrypt records. `use: "legacy"`
  holds the app's legacy key material (`SecretsStore.KeyMaterial`) opaque: core never encrypts or decrypts with
  it; it is there so the export recovers the legacy rows and so `legacy.ConfigOpenEnv.KeyringLegacy` can hand it back
  (§2.4). The active `use: "derive"` key feeds
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
  `ErrCorrupt`. Only native names have records: a kept name (`SecretsStore.Kept`) never has one before the secrets
  cutover (A-2).
- Cleared marker at the same key, `{"v":1,"cleared":true,"at":"…","by":"…"}`: written by `Delete` and by a clearing
  `Stage` of a native name.
- **Read order (`Get`, `Meta`; R5-1, A-2).** (1) an operator value (`Ephemeral`; locks the key); (2) a kept name:
  the non-empty legacy primary copy (`SourceLegacy`, `LegacyPlain` or `LegacyEncrypted`); (3) a native name's record
  (`SourceStored`, or `ErrNotSet` for a cleared marker); (4) an `EphemeralDefault` value (a `BelowStored` legacy name,
  HEAD's base layer); (5) a non-empty secondary copy (`SourceLegacy`, `LegacySecondary`: HEAD's gap fill, which
  applies only when the field is still empty, `persistence.go:42-92`); (6) `ErrNotSet`. Every `Put`, `Stage` and
  `Delete` of a kept name calls `SecretsStore.Write` in the same batch and never writes `c/secrets/`; of a native
  name it writes the record (05-config §1.12.2). `Put` and `Delete` of a kept name commit through
  `storage.CommitLocked` with `Options.SettingsLocks`, taking the per-name keyed mutex inside the `stage` function,
  after the config lock and never before it, so they never invert with `Apply` → `Stage` (§2.6; R10-4, R10-5).
- Store metadata at `c/secrets/meta`: `{"app_id":"abo","keyring_format":"mediacore-keyring/1","created":"…"}`.
  `Open` compares it with `Options.AppID`; a mismatch is `ErrAppIDMismatch` (boot error naming both IDs), so an
  accidental AppID change fails loudly instead of turning every secret into `ErrCorrupt` (SEC-28). AppID is a
  compiled constant of the app's `main` (`app.Options.AppID`), never configuration.

### 2.4 Lifecycle

1. After storage opens and after the pre-migration checkpoint and DAG, as the first P4d reconciler (§1.4
   step 5, C13), after the books store's `Open` (`legacy.ConfigOpener`) ran ABO's own key guard and loaded ABO's
   legacy key (§1.12.2; the order 06 §6.5.1 states, COR5-2): `Open` first asserts its `legacyStore` mirror on a
   non-nil `Options.LegacyStore` (`storage.ErrLegacyStoreType` otherwise, R7-7), then checks
   `c/secrets/meta` (written on first open) and resolves the keyring:
   - keyring present → load; mode looser than 0600 → try `chmod 0600`; if that fails → `ErrKeyfileInsecure`
     (fail-closed);
   - keyring absent, no stored records → generate `k1` (data) and `d1` (derive), write keyring (first run);
   - keyring absent, stored records exist → `ErrKeyMissing` (boot refused, message names the file, the exported
     copy (§2.4 Backups) and the override). With `core.secrets.allow_key_regen=true` new keys are generated **and
     existing records are kept** under their old key id (`Meta.Err = ErrKeyUnavailable`); putting the old keyring
     back later recovers them. Nothing is ever deleted automatically (contrast `persistence.go:1232-1260`).
     Legacy rows without their key are the app's guard's case (HEAD's `guardAgainstKeyRegeneration`,
     `cmd/root.go:726-783`), which runs in the books store's `Open` before this step (R4-6);
   - `Options.ResetBoot` → the keyring header's `install_id` is rewritten to the fresh store's
     `c/storage/install` marker (the reset boot of a factory reset, §1.12.2; the kernel marks the `KindReset` journal
     `applied` only after this, DATA7-17); otherwise the header's `install_id` is only written when absent (below);
   - `SecretsStore.KeyMaterial()` non-nil → recorded in the keyring as `k0-legacy` (`use: legacy`, opaque) when
     absent; when it differs from the recorded material, the recorded one is replaced and
     `secrets_keyring_not_exported` is raised, so the export always covers the key that decrypts the rows. The
     app's own key file is left untouched: older builds keep using it (09 S3 "same key file" means this);
2. `config.Load` registered `Ephemeral` values for secret keys supplied by env/file under a locking name, and
   `EphemeralDefault` values for those supplied under a `BelowStored` legacy name (05-config §1.3.5).
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
  material, so their secrets stay as the store holds them: ciphertext for native secrets and for HEAD's four
  encrypted rows, plaintext for ABO's plaintext blob secrets until H2 (05-backup §5B; 14 K25; SEC10-8). No archive
  carries a copy of the keyring as its recovery path.
- Recovery is an explicit operator action: `<app> secrets export-keyring --out <file>` (CLI, filesystem access
  required, refuses to write a file looser than 0600). **It opens no store (SEC10-9):** it reads the keyring file
  and the `install_id` in the keyring header from the state dir only, takes no store lock and is not a foreign
  open, so it works while the server runs. The exported file carries, next to the keys, the install's `install_id` (the `c/storage/install`
  marker's, 05-storage §5S.3.4 rule 4; `Open` writes it into the keyring file's header when it creates the
  keyring or finds the field absent, so the export, which reads only the state dir, has it), so a host-loss
  restore of a marker-less (pre-S0) archive keeps the install's lineage when no revocation journal header is
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
  beside it, and refuses with no keyring file. ABO ships it from S3, when the keyring lands (09 S3), as an entry of `app.OfflineCommands` (06 §6.4.5); before S3 the
  state directory has no keyring, so the command has nothing to set and the undo has no such step.
- **How an app's CLI reaches these commands (COR9-6, R9-9).** `secrets export-keyring`, `secrets rotate-derive` and
  `secrets set-install-id` are core commands an app cannot build itself. `export-keyring` opens no store (above).
  `rotate-derive` and `set-install-id` go through `kernel.OpenOffline`, which refuses while the server holds the
  store lock; `set-install-id` reads the install marker read-only (SEC10-9; 05-storage's `OfflineCommands` list
  names all three, cross-cluster). `app.OfflineCommands(opts, mods...)` (06 §6.4.5) returns them, with core's
  other offline commands, as `app.Command` values, and ABO wraps each in its cobra tree, as it does
  `storage restore-checkpoint` and `users reset-password` (`TestCLIExposesCoreOfflineCommands`, ABO); 06 §6.4.5's
  list names all three secrets commands, each from S3.
- **Host-loss restore** ([05-backup](05-backup.md) §5B.3.3 procedure): the operator copies the exported file
  into the new state directory as `<state_dir>/secrets.keyring` (mode 0600) before the first start; there is
  no import command, and the next `Open` loads it as the keyring-present case above, then
  `<app> restore --archive <file>`. Its `k0-legacy` reaches the app's backend through `legacy.ConfigOpenEnv.KeyringLegacy`,
  so the legacy rows decrypt without the app's own key file (§1.12.2). An archive encrypted only to the install identity needs the keyring to be
  decrypted at all; `--identity` remains only for operator-recipient identities. For an unencrypted archive, or
  one also encrypted to an operator recipient, copying the keyring back is **optional**: without it the restore
  still runs (05-backup `AcceptUnreadableSecrets`), the data restores, and the secrets it cannot decrypt show
  "must be re-entered". The archive's MAC is then unverifiable, so the restore is foreign (05-backup B5).
- Losing the state directory without an exported keyring means re-entering secrets (each affected secret shows
  "must be re-entered"); the data restores. Restore reports key IDs referenced by restored records
  (`KeyIDsReferenced`, computed by scanning, not from a manifest) that the current keyring lacks, before it
  starts. A matching `k0-legacy` ID proves nothing for a foreign archive (the legacy cipher carries no key ID),
  so restore staging also calls `SecretsStore.Check` with this keyring's `k0-legacy` material; names whose
  trial decryption fails are reported as `ErrUnreadableSecrets`, which needs `AcceptUnreadableSecrets`
  (05-backup §5B.3.4 step 5; DATA4-22).

### 2.5 Error semantics

| Path | Mode | Why |
|---|---|---|
| Keyring unreadable (exists) | fail-closed (boot) | never overwrite a key we failed to read (ABO's lesson, `settings.go:83-98`) |
| Keyring too permissive and not fixable | fail-closed | secret material exposure |
| No keyring, encrypted records exist | fail-closed unless `allow_key_regen`; with override, records kept, not deleted | recovery must stay possible |
| `c/secrets/meta` app ID ≠ `Options.AppID` | fail-closed (`ErrAppIDMismatch`) | a rename would otherwise make every secret `ErrCorrupt` |
| `Options.LegacyStore` set but not implementing the mirror | fail-closed (boot, `storage.ErrLegacyStoreType`) | a silently skipped legacy store would read every kept secret as unset (R7-7) |
| Record under unknown key id | fail-open per secret: `ErrKeyUnavailable`, health `secrets_undecryptable`, UI "re-enter" | the app keeps working; the integration using it reports itself unconfigured |
| AAD/auth failure | fail-open per secret: `ErrCorrupt`, same reporting | tampering or swap; never "repaired" automatically |
| `Put` storage error | fail-closed: error to caller, nothing changed | |
| `Scope` name outside prefix | `ErrOutOfScope` | handle scoping (06 A3) |
| Legacy row undecryptable by the app's cipher (key missing or bad ciphertext), no record | fail-open per secret: `Meta.Err = ErrKeyUnavailable` or `ErrCorrupt`, "must be re-entered", health `secrets_undecryptable`, legacy row kept, boot continues | same as ABO's guard, without the deletion (DATA3-12) |
| `SecretsStore.Write` fails inside a write of a kept name (incl. the secondary-copy removal of a clear, and a batch with no `CommitLocked` hold, R10-4) | fail-closed: the batch is not committed, nothing changed | a cleared row with its file copy left would be refilled by HEAD's gap fill |
| Secondary-copy rewrite fails in the store's post-commit step, after a kept name's set committed, still inside the hold (R10-5) | fail-open: logged; the next commit's step, rendered from the rows, repairs it | the row holds the value, so HEAD's gap fill never consults the file while it is set |
| `Ref.Resolve` with no value | `ErrNotSet` | consumer decides |
| `Value` JSON/text marshal | error `ErrNotSerializable` | accidental exposure fails the response instead of leaking |

### 2.6 Concurrency contract

`Store` is safe for concurrent use. Reads take a read lock on the in-memory keyring only; storage reads are
lock-free snapshots. `Put`/`Delete`/`Stage` serialize per name via a keyed mutex, which for a kept name is taken
only inside a `CommitLocked` `stage` function, after the config lock (the one core-side mutex taken inside a hold,
§1.6; R10-4, DATA10-3). The keyed mutex comes from the core-internal copy
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
| `mediacore_secrets_keyring_keys` | gauge | `use` ∈ data, derive, legacy; `status` ∈ active, verify, retired |
| `mediacore_secrets_legacy_reads_total` | counter | `where` ∈ primary, secondary (values still read from the previous build's copies) |

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
| `TestLegacyKeyMaterial_RecordedOpaque` | a fake `legacy.SecretsStore` with key material → `k0-legacy` (`use: legacy`) in the keyring, never used to encrypt or decrypt; changed material replaces it and raises the export warning; `ReadLegacyKey` returns it without opening the store |
| `TestGet_ReadOrder` | operator > kept name's legacy primary (plaintext or encrypted) > native record (cleared marker = `ErrNotSet`) > `EphemeralDefault` > secondary > `ErrNotSet` (R5-1, A-2) |
| `TestStage_KeptNameWritesInPlace` | `Stage`, `Put` and `Delete` of a kept name, an encrypted one and a plaintext one, call `SecretsStore.Write` in the batch and never write `c/secrets/`; `Put` and `Delete` commit inside `CommitLocked` with `SettingsLocks` and the keyed mutex is taken inside the `stage`; a `Stage` into a batch with no hold is refused; a clear whose secondary removal fails commits nothing; an older-build fixture reading the row or blob field after the commit sees the new value (R5-1(b), OPS5-2, A-2, R10-4, R10-5) |
| `TestExportKeyring_OpensNoStore` | with the server holding the store lock, `secrets export-keyring` succeeds, reads only the state dir and the keyring header's `install_id`, and the next boot sees no foreign open; `rotate-derive` and `set-install-id` are refused while the lock is held (SEC10-9) |
| `TestKeyIDsReferenced_ReportsLegacyRows` | a staged pre-S3 store whose `SecretsStore.Check` reports encrypted copies and that has no `c/secrets/` reports `k0-legacy` (DATA3-12) |
| `TestLegacyCheck_TrialDecryptForeignArchive` | `Check` with this keyring's `k0-legacy` over a staged archive from another install returns its legacy names as unreadable (DATA4-22) |
| `TestEphemeral_NeverPersisted` | env secret never reaches storage |
| `TestFingerprint_StableNotReversible` | same value same fingerprint; differs per keyring |
| `TestExportKeyring_ClearsHealthWarning` | export record covering every key ID clears `secrets_keyring_not_exported` |
| `TestRotateDerive_VerifyThenRetire` | after `rotate-derive`: `DeriveKey` uses `d2`; `DeriveKeyFor("d1", …)` works until `retire_after`, then `ErrKeyUnavailable`; data records still decrypt; refused while the store is locked; export warning raised |
| `TestRotateDerive_NotRetiredWhileArchiveUsesIt` | a `verify` key past `retire_after` stays `verify` while the in-use check reports a retained archive under it (health `derive_key_retire_blocked`); after the rewrap it retires |
| `TestRotateDerive_JournalStaysVerifiable` | after `rotate-derive` and the rewrap, `d1` stays `verify` while a retained revocation-journal line (or the header) names it; `Open` retires nothing; `RetireUnused` in P5 sees the overridden `core.backup.dir` (DATA3-13, SEC3-10, COR3-2) |
| `TestOpen_LegacyStoreTypeMismatchFails` | `Options.LegacyStore` set to a value lacking one `legacyStore` method → `Open` returns `storage.ErrLegacyStoreType` and writes nothing; nil → no legacy copies; a `legacytest` fake → accepted (R7-7) |
| `TestOpen_ResetBootRewritesInstallID` | with `ResetBoot` the header's `install_id` becomes the fresh store's; without it an existing header field is never changed (DATA7-17) |
| `TestSetInstallID_RestoresLineageOffline` | after a reset and a manual undo, `secrets set-install-id` with the `set_aside` record's ID rewrites only the header's `install_id` (keys and export record unchanged); it refuses while the server holds the lock, an ID that differs from the store's marker, and a missing keyring (DATA8-12) |
| `TestStage_TakesBatchPointer` | `Stage` writes and its in-place `Write` commit with the caller's batch and are refused outside its bound keyspaces (COR2-12) |

**ABO-side tests of the books legacy config store** (ABO repo; they run `configtest.OverrideStoreSuite` with HEAD fixtures
and the §1.12.2 rollback matrix, in root `make ci` against a `prev` binary built from `PREV_REF`, OPS4-6):
`TestRollback_BasicGateNeverOpenOnPrev`, `TestRollback_BasicGateClearedStaysClosed` (SEC5-4),
`TestRollback_RotatedKeyNotResurrected`, `TestRollback_ClearedOnPrevClearedOnCur`,
`TestRollback_BlobEditOnPrevEffectiveOnCur`, `TestS2bAppConfigFromBlob` (COR4-1),
`TestComposeEnvKeepsUIBasicGate` (compose's three `BASIC_AUTH_*` at their defaults, the blob holding
`basic_auth_enabled: true` and a stored password: on S3 stage 8 is on, `<username>:` gets 401 and the stored
password 200; OPS5-1), `TestAliasBelowStoredMatchesHEAD` (R5-1(a)), `TestOnlyLegacyConfigStoreWritesSettingsRows`,
`TestSetInternalFlagRefusesConfigRows` (DATA4-4), `TestLegacyGuard_RowsWithoutKeyRefuses` (HEAD's
key-regeneration guard inside the store's `Open`, R4-6), `TestLegacyOpen_NoCorruptSecretRepair` (an
undecryptable row is kept, nothing is written, DATA5-11), `TestGapFillFileRewriteSerialised` (two concurrent
commits leave both values in the file, DATA5-20), `TestGapFillClearNotUndoneByLateSet` (a set of a kept name through
the `/api/v1/config` alias and a clear of it through the integrations route, raced: neither the row nor the file key
survives the clear, because the step renders from the rows inside the hold; R10-5, DATA10-2),
`TestGapFillStepOnEveryWriterPath` (a kept-name write through `Apply`, `Registry.Put`, the notify rule store and
`secrets.Put`/`Delete` each leaves `config.yaml` equal to the rows before `configBlobSaveMu` is released; R10-5), `TestIntegrationStoreSharesBlobRender`,
`TestFreshInstall_EmbedCoverArtFalseSurvivesRestart` (a fresh S2b+ install, and a store upgraded from a pre-marker
build, keep an operator's `embed_cover_art=false`, `log_retention_days` and 0–0 maintenance window across restarts;
the three one-shots ran once as books migrations, which wrote HEAD's markers; `Open` wrote nothing; COR6-5),
`TestLegacyAuthEnablePrecedenceMatchesHEAD` (blob `enable_auth=false`, env `ENABLE_AUTH=true` → auth off, as at HEAD, and `config_legacy_name_shadowed` raised; blob `true`, env `ENABLE_AUTH=false` → auth stays on; `ABO_CORE_AUTH_MODE=required` → on and locked; A-2), `TestRollback_PlaintextSecretSavedOnCurWorksOnPrev` (a plaintext secret saved on cur is the value prev reads, A-2), `TestCoreWritesOnlyHEADGapFillKeys` (after every kind of core write, set, clear, factory-reset hook, the gap-fill file holds no key outside HEAD's `SaveConfigToFile` set, and an operator key already in it survives unchanged; ADV-8), `TestLegacyEnvDecodeMatchesHEAD` (§1.11), `TestFreshInstallWritesHEADMarkers` (a fresh S2b+ store holds the three HEAD markers after
its first boot; DATA7-13), `TestFactoryResetClearsGapFillSecrets` (the staging request writes no setting; after the
restart the fresh store gap-fills no kept secret, also before and across a crash ahead of the post-reset hook, the
hook leaves the gap-fill file without the three kept keys, and `config.boot.json` is set aside; DATA6-6, R7-2),
`TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn` (blob `enable_auth: true` set in the UI, `ENABLE_AUTH=false` and
`ENABLE_RATE_LIMIT=false` in the environment: after a `ResetFactory` and after a `ResetStore` swap the reset boot's
fresh store holds HEAD's factory-default blob with `root_dir` empty and `setup_complete` false, auth is `required`
and the rate limiter on, and `config_legacy_name_shadowed` is raised; SEC8-5, R8-1),
`TestRollback_AuthOffBasicGateStaysOnPrev` (SEC9-1, §1.3.7), `TestFileOnlyLegacyKeysNeverDBStored` (no
`FileOnly` key is staged by `Apply`, the façade or the `/api/v1/config` alias, a `c/config/` row for one is ignored,
and the operator file's value is effective; DATA9-6) and
`TestSecretClassListMatchesLegacyStore` (the generated list equals the names `SecretsStore.Kept` reports plus the "—" rows and,
from S3, `scripts/staging/secret_fields.txt`; OPS6-4, R7-6).

### 2.10 Migration from ABO

The data half (every legacy copy read and written in place by the books store, native secrets in `c/secrets/`,
the deferred secrets cutover) is specified once, in §1.12.2 and §1.12.5. Code replacement:

1. New secrets use `secrets` (`Put`/`Stage`). ABO's `database.EncryptValue/DecryptValue/InitEncryption` and
   `guardAgainstKeyRegeneration` (`settings.go:110,189,214`, `cmd/root.go:735`) stay **ABO code inside the books
   legacy config store** until cutover, because the four kept rows stay in that cipher and are written in it
   (R4-1, R4-6, R5-1(b)); the legacy key
   search order (state dir, then legacy dir) stays that of `database.InitEncryption` (`settings.go:110-140`).
   `MaskSecret/IsEchoedMask/LooksLikeMask` (`settings.go:271,285,293`) serve only the compat handler (item 2).
2. ABO's `/api/v1/config` compat handler returns masks for as long as its ledger row is live, by composing `Meta`
   (`set`) with the old mask shape; the core settings API never does. On the way in, an empty, echoed or
   mask-shaped secret value (`IsEchoedMask`/`LooksLikeMask`, evaluated on the raw request payload) means
   "unchanged" and is never staged, so a round-tripped mask neither overwrites nor moves a secret.
3. ABS JWT secret, Cloudflare policy token, SMTP password: HEAD's file-only (`json:"-"`) secrets keep HEAD's
   storage through the extraction: env and the operator file only (aliases), never stored, and the settings UI shows
   them read-only with their lock reason (`FileOnly`, §1.3.7; R9-5). They are stored encrypted in `c/secrets/` only
   from their family's settings cutover.

### 2.11 Open items

1. **Rotation and prune (deferred, SIMP-19).** Not built in v1: after a suspected key leak the operator re-enters
   secrets. Constraints already fixed for when it is built: records name their `kid`, so rotation is "new active
   data key + re-encrypt"; the derive key is rotated by its own command, which ships in v1 (`rotate-derive`,
   §2.3), because rotating it invalidates derived artifacts; pruning a retired key, `k0-legacy` included, is safe
   only when `KeyIDsReferenced` no longer lists it (the four kept rows keep `k0-legacy` listed until the family's
   cutover) and the export record covers the current keyring
   (no coupling to backup manifests).
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

None open. The round-6 objection (the legacy-only value types `secrets.LegacyCopies` and `secrets.LegacyCheck`
and the format reader `secrets.ReadLegacyKey` cannot move to `legacy`, R6-3) is resolved: 03 v1.9 lists them among
the identifiers that cannot move (03 §3.3 "Legacy-only identifiers", 14 K23).

The round-0 items (an internal order inside the old shared layer; the §3.3 `config` row not saying
that operator-set keys are locked) are resolved by 03 v1.2.0+ (R4 L1/L2/L3; §3.3 `config` row); 03 v1.3.0 R5 also states that ABO's façades are allowed. Note for readers:
`compat` (L4) sits above `config` (L3), so config aliases are registered with `compat` by the kernel and use is
reported through `Options.OnLegacyUse` (§1.3.6); no R4 change is needed.

## Out-of-scope defects found

1. Eight writes to `config.AppConfig` bypass `Mutate` (§1.2 anchors): unsynchronized, and invisible to
   `config.Generation()` consumers (`internal/ai/ai_metrics.go:169`). Fixed in migration stage 0 because the
   façade depends on it.
2. Three encrypted secrets are also persisted in plaintext to `<db parent>/config.yaml` on every settings save
   (`persistence.go:168-178`), which defeats their encryption. Core never writes the file. These three are kept
   secrets (R5-1(b)), so until cutover the books store keeps HEAD's behaviour exactly: it reads the file as HEAD's
   gap fill and rewrites their copies with their rows (§1.12.2), because an older build after a rollback still
   gap-fills from it. While it holds a secret key, the boot log names the file as holding plaintext copies; it is
   never archived by [05-backup](05-backup.md). The cutover moves the three into `c/secrets/` and removes their
   file keys.
3. Secret ciphertext has no associated data (`settings.go:189-208`): rows are swappable between keys.
4. `LoadConfigFromDatabase` deletes secret rows it cannot decrypt or recover (`persistence.go:1253-1260`, `DeleteSetting` at `:1258`),
   turning a temporarily missing key file into permanent loss when the guard is overridden.
5. The shipped `docker-compose.yml` sets `AO_DIR` and `AO_DB` (`:18-19`) and `deploy/local.conf.example` sets
   `OLLAMA_BASE_URL` (`:40`), but no Go code reads any of them at `748fd787a`: a compose user's database is not
   where the file says. The fix (rename them in the shipped files to names ABO reads) belongs to 09 S3's
   deploy-file update; this spec keeps them unread (§1.12.4).
6. HEAD's `POST /system/reset` (`ResetSystem`, `internal/server/handlers/system/handler.go:463-491`) wipes the store
   but leaves the gap-fill file `<db parent>/config.yaml`, so after the next restart HEAD's gap fill brings the old
   install's OpenAI, Google Books and Hardcover keys back (`persistence.go:42-104`). A core `ResetStore` swap keeps
   that behaviour (no file effects, R8-1, §1.12.2). If the owner wants the old keys gone after it too, the
   `ResetStore` kind would also need a hook that rewrites the gap-fill file; that is the owner's call.

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
