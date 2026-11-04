<!-- file: docs/specs/platform-core/09-abo-on-core.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c82c917-2fc4-4385-b085-73d143a2374f -->
<!-- last-edited: 2026-11-04 -->

# 09 — ABO on core: the strangler-fig sequence

Evidence commit: `748fd787a`. Raw data: [`raw/`](../../plans/platform-core/raw/00-index.md).
Names, paths and rules come from [03-architecture](03-architecture.md) (normative). Package moves
reference `raw/01` package names and 03 §3.3 targets; [04-package-mapping](04-package-mapping.md)
is the authoritative per-package table and wins on any disagreement about a single package.

## 9.0 Purpose & scope

How audiobook-organizer (ABO) becomes **app = core + module `books`** without a flag day. Every
stage below ends with ABO shippable, deployable with `make deploy`, and reversible with
`make rollback` (D3; gate G5, [02](02-decision-record.md) §2.4). This file answers old Q5 (§9.6)
and is the single owner of ABO's compatibility windows. It does not design the core contracts
themselves (05-*, 06, 07); it says in what order ABO adopts them and what stays compatible.

Out of scope: TV/Movies (10), multi-app (11), the repo split (12). The storage-efficiency program
that owns `storage_format` 2 (`internal/database/storage_format.go:44-50`) is a separate track;
this sequence never raises `SupportedStorageFormat` (§9.2.2 explains why it needs its own guard).

## 9.1 Current state at HEAD (compatibility-relevant facts)

| Fact | Evidence |
|---|---|
| One Pebble store, one WAL, one backup unit; 258 registered key families (243 namespaced, 2 bare, 10 legacy, 3 v1) | `internal/database/keyfamilies.go:37-345`; `raw/05-storage.md` §2–§3 |
| No family starts with `c/` or `m/`; a key outside every family is counted as `(unregistered)`, never deleted | `internal/database/census_families.go:128`, `keyfamilies.go:19-26`; `raw/05-storage.md` §3 table |
| Whole-keyspace iterations: emptiness probe, census, raw diagnostics query, checkpoint backup — all read-only | `storage_format.go:240`, `census_exact.go:468`, `cmd/diagnostics.go:268`, `internal/backup/backup.go:428` |
| `idx:` is ONE registered family shared by auth (`idx:user:`, `idx:role:name:`, `idx:sess:user:`, `idx:apikey:`, `idx:invite:username:`, `idx:oauth:`) and books (`idx:bv:`, `idx:col:name:`, `idx:ubs:status:`, `idx:upl:`, `idx:sidx:dirty:`, `idx:abs_sess:`) | `keyfamilies.go:64`; `pebble_store_auth.go:67,142,966,1126,1131,1335,1670`; `oauth_identity.go:32,78`; `pebble_store.go:5376-5387` |
| Storage-format guard refuses a store **newer or older** than the build; `make rollback` asks `.prev --print-storage-format` | `pebble_store.go:670-680`; `main.go` fast-exit flags; `Makefile:892` (`rollback`); `scripts/storage_format_guard.py` |
| Precedent: opv2→opv3 dual-write in ONE batch so the previous binary (opv2-only) can be redeployed; reconcile every boot | `internal/database/pebble_store_ops_v3.go:29-42` (owner decision D25); `internal/database/store.go:1861-1870` |
| Precedent: renamed op IDs via `OperationDef.FormerIDs`, counted by `audiobook_organizer_operation_deprecated_def_id_total{alias,entry}` as the removal signal | `internal/operations/registry/aliases.go:16-52`, `types.go:29-45`; `internal/metrics/metrics.go:98-108` |
| Precedent: metric renames by dual emission + recording rules + dated soak (11-PR4) | `deploy/prometheus/recording-rules.yml:14-26`; `deploy/prometheus/README.md` "Dual-rule soak" |
| Retired routes: `gone(replacement)` → 410 `{"error":"gone","message":…}` behind the original auth/permission, one release, then deleted; 45 such stubs | `internal/server/gone.go:14-36`; `docs/decisions/2026-10-30-bulk-fetch-gate.md`; `raw/09-http-api.md` §1 |
| Unversioned `/api/*` is 301-redirected to `/api/v1/*` unless excluded (v1, v3, health, events, metrics, ABS-reserved) — so `/api/core/v1/x` and `/api/books/v1/x` would redirect today | `internal/server/server_lifecycle.go:1790-1822`; ABS lists `internal/server/wire_abs_routes.go:308-345` |
| 13 permission atoms, hard-coded list, 4 seed roles upserted every boot, admin checked by role **name** | `internal/auth/permissions.go:24-85,91,112`; `internal/auth/seed.go:33-38,86-129`; `internal/server/middleware/auth.go:522-537` |
| Config: one struct, ~251 viper defaults, flat keys; YAML file, `viper.AutomaticEnv()` with **no prefix and no key replacer**, 141 explicit `BindEnv` aliases; whole config persisted as one flat JSON `config_blob` + 4 encrypted secret rows | `internal/config/config.go:1048-1912,2621`; `cmd/root.go:625`; `internal/config/persistence.go:884,1996-2008`; `raw/12-cross-cutting.md` §2 |
| Metrics: fixed namespace `audiobook_organizer`, default Prometheus registry; OTel scope prefix `audiobook-organizer/` | `internal/metrics/metrics.go:15-60,300`; `internal/telemetry/meter.go:17` |
| Shipped runtime contracts: Docker `CMD ["serve","--host","0.0.0.0","--db","/data/audiobooks.pebble"]`, HEALTHCHECK on `/health`; systemd unit `serve --host … --port 8484` | `Dockerfile:100-104`; `deploy/audiobook-organizer.service:92-108` |
| Web client hard-codes `API_BASE = '/api/v1'`; SSE at `/api/events` and `/api/v1/operations/events` | `web/src/services/api.ts:24,733-740`; `web/src/services/eventSourceManager.ts:31` |
| Backups: tar(+codec) of the Pebble dir only, entries rooted at the DB dir's basename; restore extracts every entry under the target | `internal/backup/backup.go:283-428,777-860,1068-1108` |
| Releases: RC on every push to main, production minors by manual dispatch, irregular cadence (two minors one day apart in 2026-08) | `.github/workflows/prerelease.yml:32-58`, `release-prod.yml:43-65`; tags `v0.220.0`…`v0.222.2` |

## 9.2 Compatibility model (used by every stage)

### 9.2.1 Expand → migrate → contract, and what "a release" means

Every renamed or moved surface goes through three steps, **never two in the same deployed build**:

1. **Teach** (one deployed build *before* the flip): the old binary learns the new identifier as an
   alias of the old one (op IDs, config keys). Needed wherever a newer binary can leave the new
   spelling in shared state that an older binary later reads.
2. **Expand**: the new form becomes truth; the old form is still written (data) or still served
   (API, config, metrics). Rollback to the previous build is always safe.
3. **Contract**: after the window **W**, the old form stops being written/served (410/refusal for
   one more production minor), then is deleted.

**Window W** = the later of (a) two production minor releases after the expand shipped, (b) 30
days; and removal additionally requires (c) the surface's `mediacore_compat_legacy_use_total`
series at **zero for 14 consecutive days** in production (command P8, §9.7). (c) generalizes the
`FormerIDs` metric rationale (`aliases.go:38-52`): a counter answers "can it be deleted yet?". This
refines 02 §2.5 Q5 ("two minor releases") with a wall-clock floor and a usage gate, because minors
can be a day apart (§9.1) and production deploys from main builds, not only tags.

**Unit of compatibility = consecutive deployed builds.** `make rollback` swaps to `.prev`
(`Makefile:892`), which may be any main build. Therefore: an expand PR and its contract PR never
merge before the expand build has run in production for W; CI enforces it with a dated
`compat` ledger entry (§9.2.9).

### 9.2.2 The compat-level guard (new in S0)

`storage_format` cannot carry this: it refuses **older** stores too and needs a converter
(`pebble_store.go:676-685`; `RunCutover` does not exist, `storage_format.go:125-129`). A contract
step only needs "binaries older than level L must not serve this store". So S0 adds an
independent, monotone **minimum-reader level**:

```go
// core/storage/compat.go  (S0 ships it in internal/database; S2 moves it; the KEY never moves)
package storage

// CompatKey holds the minimum compat level a binary needs to serve this store.
// Absent means 0. Only a contract migration raises it; nothing lowers it.
const CompatKey = "c/compat/required_level" // JSON {"level":N,"by":"<release>","at":"<rfc3339>"}

// CompatSidecarSuffix names a one-line file beside the store, read by make rollback over ssh
// without opening Pebble (same reason as the storage-format sidecar).
const CompatSidecarSuffix = ".compat-level"

type CompatLevelTooNewError struct{ Required, Supported int; Source string } // "stamp" | "sidecar"

func (e *CompatLevelTooNewError) Error() string {
	return fmt.Sprintf("store requires compat level %d, this build supports %d (%s); "+
		"roll forward or restore a backup taken before the contract release", e.Required, e.Supported, e.Source)
}

// CheckCompat runs in the open phase, after the storage-format checks, before any write.
func CheckCompat(ctx context.Context, r Reader, sidecar int, supported int) error

// RaiseCompat is called only by a contract migration, in the same batch as its legacy deletes.
// It writes the stamp first in the batch and the sidecar before the batch commits (fsync),
// so a crash leaves a store already too new for older builds — never a half-contracted store
// an old build would accept (same ordering argument as storage_format.go:44-49).
func RaiseCompat(b *Batch, level int, by string) error
```

The binary reports its level with a new fast-exit `--print-compat-level` (handled in `main.go`
beside `--print-storage-format`); `scripts/storage_format_guard.py` gains `--prev-compat` and
`--compat-sidecar` and refuses a swap when sidecar > `.prev` level. A build older than S0 prints
"unknown flag" and is read as level 0. Levels: S0 = 1; each expand stage that introduces a native
source of truth that a later contract will retire bumps the build's supported level (S3 → 2,
S4 → 3; no other stage needs one); the store's
required level is raised only by contract migrations, to the level of the matching expand.

### 9.2.3 Data: legacy claims, adoption, mirror, reconcile

Decision 13 (02): ABO's families are **claimed** by `books` in place; small generic families are
**adopted** into `c/…` by one atomic, backed-up migration; large families are read in place behind
adapters; no full rename.

```go
// What books.Module declares (shape must match 05-storage; requirements in "Objections to 03").
reg.Storage().ClaimLegacy(
	storage.LegacyPrefix("book:"), /* … every books family from keyfamilies.go … */
	storage.LegacyExact("narrator_counter"), storage.LegacyExact("sync_alias_use_seed_cutoff"),
	storage.LegacyPrefix("idx:", storage.Except( // sub-prefixes adopted by core in S4
		"idx:user:", "idx:role:name:", "idx:sess:user:", "idx:apikey:", "idx:invite:username:", "idx:oauth:")),
)

// Adoption of a small family into a core keyspace, with a rollback mirror.
// core/storage/mirror.go (proposed; 05-storage owns the final API)
type Mirrored[T any] struct {
	Name    string                                  // metric label, e.g. "auth.user"
	Legacy  Codec[T]                                // u:<id> ↔ T
	Native  Codec[T]                                // c/auth/user/<id> ↔ T (+ LegacyHash field)
	Reverse func(T) (T, bool)                       // project to what the legacy binary may see; false = do not mirror
	Phase   MirrorPhase                             // compiled into the release
}
type MirrorPhase uint8
const (
	PhaseLegacy  MirrorPhase = iota // before the stage: legacy is truth, native absent
	PhaseMirror                     // native is truth; every native write stages the legacy row in the SAME batch
	PhaseNative                     // contract: no legacy writes; legacy rows deleted by the contract migration
)

// Adopt copies legacy → native in one batch (or chunked + final flip, §9.7 P1 branch B), recording
// LegacyHash = sha256(legacy value) in each native row. Idempotent: rows whose LegacyHash matches
// are skipped. Runs inside a pre-migration checkpoint (G2).
func (m Mirrored[T]) Adopt(ctx context.Context, db *DB) (AdoptReport, error)

// Reconcile runs every boot while Phase == PhaseMirror, synchronously before listeners
// (precedent ReconcileOpsV3WithV2, store.go:1861-1870). A legacy row whose hash differs from the
// native row's LegacyHash was written by an older binary after a rollback: legacy wins and is
// re-adopted. A native row whose legacy twin vanished was deleted by an older binary: deleted.
func (m Mirrored[T]) Reconcile(ctx context.Context, db *DB) (iter.Seq2[ReconcileEvent, error])
```

Why reconcile is required, not optional: after `make rollback` the older build writes only legacy
rows (password change, new key, deactivation); without reconcile the next roll-forward would serve
stale native rows — a deactivated user could log in again. Fail-closed rule: a reconcile error
halts boot (§9.8).

### 9.2.4 HTTP: aliases, deprecation headers, 410

```go
// core/httpapi (05-http-api owns the final API)
type Deprecation struct {
	Shipped   time.Time // when the canonical route reached production
	Sunset    time.Time // earliest removal = Shipped + W floor
	Successor string    // canonical pattern, e.g. "GET /api/core/v1/users/{id}"
}
// Alias serves a legacy pattern with the canonical handler, the canonical permission and the
// LEGACY wire shape (adapter optional), adding `Deprecation: @<unix>` (RFC 9745),
// `Sunset: <http-date>` (RFC 8594) and `Link: <successor>; rel="successor-version"`, and counting
// mediacore_compat_legacy_use_total{surface="http",name=<legacy pattern>,entry="request"}.
func (r *Router) Alias(legacy string, canonical RouteID, d Deprecation, opts ...AliasOption)
// Gone is the core port of internal/server/gone.go: 410, same JSON, behind the ORIGINAL permission.
func (r *Router) Gone(legacy string, perm rbac.Permission, replacement string)
```

Legacy wire shapes are preserved by a per-route `WithLegacyShape(func(canonical any) any)` adapter,
pinned by goldens derived from `internal/wirecontract` (`raw/09-http-api.md` §5). ABS-compatible
unversioned paths are **not** aliases: they are a protocol owned by `books` permanently.

### 9.2.5 Config aliases

```go
// core/config (05-config owns the final API)
type Alias struct {
	Legacy    string        // flat key as in viper / YAML / config_blob, e.g. "root_dir"
	LegacyEnv []string      // env names read today: AutomaticEnv upper-cased key ("ROOT_DIR") + BindEnv names
	Canonical string        // "modules.books.library.root_dir" | "core.http.port"
	Convert   func(any) (any, error) // optional unit/shape change
	Permanent bool          // standard third-party env names (OTEL_*, OPENAI_*): never deprecated
}
```

Resolution order per key: canonical source > legacy source at the same layer; a key set in both
forms in one layer logs a WARN and counts `{surface="config",name=<legacy>,entry="file|env|blob"}`.
The books module contributes its alias table generated from `registerConfigDefaults`
(`config.go:2621`); `TestEveryLegacyConfigKeyHasCanonical` fails on a key with no row.

### 9.2.6 Permission atoms

| Legacy atom (`permissions.go`) | Canonical grant set (expansion) | Reverse (mirror) rule |
|---|---|---|
| `library.view` (147 routes) | `books:view`, `core:tasks.view`, `core:events.subscribe` | granted iff `books:view` held |
| `library.edit_metadata` (111) | `books:edit` | iff `books:edit` |
| `library.delete` (22) | `books:delete` | iff `books:delete` |
| `library.organize` (9) | `books:organize` | iff `books:organize` |
| `scan.trigger` (15) | `books:scan`, `core:tasks.run` | iff `books:scan` |
| `integrations.manage` (12) | `core:integrations.manage`, `books:integrations.manage` | iff **both** |
| `users.manage` (20) | `core:users.manage` | iff `core:users.manage` |
| `settings.manage` (83; op-def default) | `core:settings.manage`, `books:settings.manage`, `core:tasks.manage` | iff **all three** |
| `playlists.create` (0) | `books:playlists.create` | iff held |
| `collections.manage` (6) | `books:collections.manage` | iff held |
| `requests.create` / `requests.approve` (0) | `books:request` / `books:request.approve` | iff held |
| `fingerprint.worker` (5) | `books:fingerprint.worker` | iff held |
| role-name check `"admin"` (20 routes + handlers) | `core:admin` (held by seed role `admin`) | role id `admin` unchanged |

Route counts: `raw/09-http-api.md` §1.3. Canonical names are this file's proposal; 05-rbac may
rename the `core:` atoms, but the **invariant** is fixed here: for every one of the 497 (+75 ABS)
routes and every seed role, allow/deny is identical before and after S4
(`TestLegacyRoleReachUnchanged`, built on the real-router dump technique of `raw/09` §0). Reverse
mapping is conservative (fail-closed): a canonical-only atom with no legacy equivalent is dropped
from the mirrored row, never widened. Role IDs (`admin`, `editor`, `viewer`, `fp-worker`) do not
change, so `User.Roles` and `Invite.RoleID` (`internal/database/store.go:1004-1012`) need no rewrite.

### 9.2.7 Metrics

Mechanical rename rule (03 §3.6): core series `audiobook_organizer_<x>` → `mediacore_<subsystem>_<x>`;
books series → `audiobook_organizer_books_<x>` (yes, `audiobook_organizer_books_books_total`:
predictability beats euphony). During W both names are emitted from the per-`App` registry, and
`deploy/prometheus/recording-rules.yml` gains rules mapping old → new; `alert-rules.yml` carries
both rule sets, exactly as the 11-PR4 soak did. OTel scope `audiobook-organizer/<n>` →
`mediacore/<subsystem>` / `audiobook-organizer/books/<n>`. Go/process collectors keep their
standard names.

### 9.2.8 Feature flags

**None are added.** Every switch in this sequence is release-bound (a build is either in phase
Mirror or Native), because a runtime flag would double the states rollback must handle and an
operator could flip a store into a state the previous build cannot read. Existing switches are
untouched: `abs_api_enabled`, `enable_auth` (`raw/09` §2.2 note b). The one per-entry format switch
is webhook payload `format` (S6), which is data, not a flag.

### 9.2.9 The ledger

`docs/compat-surfaces.md` (the existing shim ledger, v1.1.0) gains a section per stage; each row:
surface, legacy name, canonical name, owner PR, shipped date, earliest sunset, counter name,
removal PR. A test `TestCompatLedgerMatchesCode` (root module) compares the ledger with the alias
tables registered at boot (HTTP, config, rbac, metrics, mirrored families, op FormerIDs), and
`TestNoContractBeforeSunset` fails a build whose code removes an alias whose ledger sunset date is
in the future.

## 9.3 Stage overview

| Stage | Name | Build compat level | Native truth introduced | Contract (≥ W later) raises required to |
|---|---|---|---|---|
| S0 | Groundwork and guards | 1 | — | — |
| S1 | Core module skeleton, leaf packages | 1 | — | — |
| S2 | Composition root, `books` module wraps the server | 1 | — | — |
| S3 | Config and secrets | 2 | `c/secrets/`, `c/config/` | 2 (C3) |
| S4 | Identity: auth, RBAC, audit; first native core routes | 3 | `c/auth/`, `c/rbac/`, `c/audit/` | 3 (C4) |
| S5 | Tasks | 3 | task kinds `books.*`; core task API | — (data stays in place) |
| S6 | Events, outbox, realtime | 3 | `c/outbox/`; SSE `/api/core/v1/events` | — |
| S7 | Integrations and library roots | 3 | contracts only; state stays in place | — |
| S8 | Backup, updater, health, metrics registry | 3 | backup manifest; metric names | — |
| S9 | UI shell | 3 | `/books/...` SPA routes | — |
| S10 | Native books API, gin retired group by group | 3 | `/api/books/v1/...` | — |
| S11 | Final cleanup | 3 | — | — |

S1→S2→S3→S4 are strictly ordered (R4 layers). S5, S6, S7 may overlap only if they touch disjoint
package groups (CLAUDE.md "never launch overlapping waves"); S9 needs S4–S6 routes; S10 runs
per route group after S9.

## 9.4 Stages

### S0 — Groundwork and guards

| Aspect | Specification |
|---|---|
| Goal | Make every later stage reversible and measurable, with zero behaviour change. |
| Moves | Nothing moves. `core/` created with `go.mod` (`module github.com/falkcorp/mediacore`, 03 §3.2), `archtest` stub; root `go.mod` `require`+`replace`; Makefile `GO_MODULE_DIRS := . core`; CI jobs loop over both; `internal/arch/layering_test.go` redesigned per 03 §3.5.1 (ratio floor, new positive control, layer −1 for mediacore). |
| Data/keys | Register reserved families `c/` (core) and `m/` (modules) in `keyfamilies.go` so the census names them. Add compat-level guard (§9.2.2) with `c/compat/required_level` absent ⇒ 0. **Teach** op-ID aliases: each of the 233 registered defs declares the future kind `books.<id>` (or `core.<id>` for the 24 generic ones, `raw/06` §6.2) in `FormerIDs`, so an S4 build can read S5-written rows. |
| Config | None. |
| Endpoints | `/api/*` redirect excludes `^/api/(core|[a-z][a-z0-9]{1,15})/v[0-9]+(/|$)` (`server_lifecycle.go:1798`); `TestNativePrefixesNotRedirected`; `TestCollidingNamespacesStillRedirect` unchanged. `/api/books/<id>` (unversioned, no `/vN/`) still redirects to `/api/v1/books/<id>`. |
| Permissions / UI / flags | None. |
| Rollback | Plain binary swap; S0 writes no key (the compat stamp is only written by contracts). |
| Done when | `audiobook-organizer --print-compat-level` prints `1`; `make ci` runs Go targets in `.` and `core`; `TestLegacyToleratesNamespacedKeys` (seeds `c/x`, `m/y` into a HEAD store and runs census, retention sweeps, backup, restore, boot) passes; `scripts/test_storage_format_guard.py` covers the compat cases; `curl -s -o /dev/null -w '%{http_code}' <abo-url>/api/core/v1/ping` returns 404, not 301. |

### S1 — Core module skeleton and leaf packages

| Aspect | Specification |
|---|---|
| Goal | Prove the nested module with real code; move dependency leaves that have no data or API surface. |
| Moves (generic leaves, `raw/01`) | `internal/keyedmutex` → `core/internal/keyedmutex`; `internal/telemetry`, `internal/telemetry/contract` → `core/metrics` (+ `core/internal/otel`) taking namespace and scope as **constructor options** (ABO passes `audiobook_organizer`, `audiobook-organizer/`); `internal/logging` → `core/log`; `internal/lifecycle`, `internal/sdnotify`, `internal/startup` → `core/kernel` / `core/health`; `internal/boundedcall`, `internal/errhandling`, `internal/flight`, `internal/diag/flight` → `core/internal/…`; `internal/security/safehttp` → `core/integrations/providerhttp`; `internal/security/{safepath,pathvalidation}`, `internal/libroot` → `core/library`. `internal/logger` stays (its `Change` carries `BookID`, `raw/12` §3) as an adapter over `core/log`. |
| Data/keys, config, endpoints, permissions, UI, flags | None changed. Metric and scope names byte-identical (namespace injected). |
| Rollback | Binary swap; no state. |
| Done when | `go list -deps ./core/...` has no `github.com/falkcorp/audiobook-organizer` path (`archtest.TestCoreImportsOnlyCore`); `curl -s <abo-url>/metrics` series-name set (sorted `^[a-z_]+` names) is identical to the S0 build's (diff empty); `go build ./... && (cd core && go build ./...)` exit 0 with `go1.27.3`. |

### S2 — Composition root; `books` wraps the server

| Aspect | Specification |
|---|---|
| Goal | ABO's `main` becomes `app.New(opts, books.Module())`. Core owns process lifecycle, listeners and the service container; the entire existing server is one module handler (N7). |
| Moves | `internal/serviceregistry` (container, phases) → `core/kernel`; its 43 `Register` call sites become `module.Registrar` calls inside `books.Module()` (N4), `init()` registrations removed for moved services. Listener code (`server_lifecycle.go:903-1078`: TLS, HTTP/2, HTTP/3, port-80 redirect, peer allow-list) → `core/httpapi` server. `internal/startup` table → `core/kernel` boot steps. The gin engine (`server.go:638`) is mounted by `books` at `/` as an `http.Handler`; global gin middleware (logger, recovery, security headers, CORS, BasicAuth, compression, tracing) stays inside it. |
| Data/keys | None. Books migrations 1..66 keep their bookkeeping (`db_version`, `migration_<n>`, `migration_bookkeeping.go:21-26`) as the books module's legacy migration track; core's own track starts empty under `c/storage/migrations/`. |
| Config | Core config loader reads the **same** sources (YAML, AutomaticEnv, BindEnv, `config_blob`) through a books-provided legacy source; canonical `core.*` / `modules.books.*` keys are **taught** (accepted as aliases) but undocumented. Persisted shapes unchanged. Flags `--port --host --tls-cert --tls-key --http3-port --external-url --read-timeout --write-timeout --idle-timeout --workers` (`cmd/root.go:526-535`) feed `core.http.*` / `core.tasks.workers`. |
| Endpoints / permissions / UI / flags | None changed. |
| Rollback | Binary swap; no state written. |
| Done when | `main.go` contains no `server.NewServer`; `grep -rn 'serviceregistry.Register' --include=*.go internal | grep -v _test` count = 0 for moved services (exact list in 04); `coretest` boots core with zero modules (G7); `deploy-wait-ready.sh` sees ready within the S1 baseline + 10 %; `curl` of 20 sampled routes (one per top-level group, `raw/09` §1.1) returns byte-identical bodies to S1. |

### S3 — Config and secrets

| Aspect | Specification |
|---|---|
| Goal | `core/config` and `core/secrets` become truth; every secret encrypted at rest (N10, G12). |
| Moves | Generic loader parts of `internal/config` (layering, `Mutate`/`Generation`/`Snapshot`, lock reasons, removed/unknown-key warnings: `config.go:2011-2035,2391-2412`, `removed_keys.go`, `unknown_keys.go`) → `core/config`; `internal/database/settings.go` encryption (`:42-52,110-125,189-235`) → `core/secrets`. The ABO struct becomes typed sections: `core.*` (http, log, storage, tasks, auth, otel, backup, update) and `modules.books.*` (everything audiobook, `raw/12` §2). |
| Data/keys | **Adopt** the 4 encrypted secret rows (`openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`; `persistence.go:1996-2008`) into `c/secrets/<name>` with the **same** key file (path unchanged). **Encrypt** the plaintext-in-blob secrets (`acoustid_api_key`, OAuth client secrets, Deluge passwords, `MetadataSources[].Credentials`; `raw/10` §7) into `c/secrets/`. Phase Mirror: legacy rows and plaintext blob fields keep being written, so the previous build still finds them. Config stays persisted in the flat `config_blob`; canonical docs are written only at contract C3. |
| Config compatibility | All flat keys remain valid in YAML, env and blob (alias table §9.2.5). Canonical keys documented. Env canonical form `ABO_<SECTION>_<KEY>` (prefix from `app.Options`, grammar per 05-config); legacy unprefixed names keep working in W. |
| Endpoints | `GET/PUT /api/v1/config` unchanged (masking `update_service.go:88-150` now reads `core/secrets` references). |
| Permissions / UI / flags | None / none / none. |
| Rollback | To S2: S2 reads `config_blob` and the 4 legacy secret rows, both still current. Secrets created or rotated on S3 are mirrored, so they survive. `c/secrets/` left in place, ignored (S0 tolerance). Roll-forward: reconcile re-adopts secrets changed on S2. |
| Contract C3 (≥ W) | Stop mirror; delete the 4 legacy secret rows; zero plaintext secret fields in the blob; one-shot convert `config_blob` → canonical section docs under the core keyspace (05-config), blob deleted; `RaiseCompat(2)`. YAML files with legacy keys: refuse to start naming the canonical key (fail-closed) for one minor, then the alias table is deleted. Legacy env names: not read; one boot WARN per known legacy name set, for one minor. **Cost surfaced:** plaintext secrets stay in the blob for W; the alternative (stop writing plaintext at expand) makes a rollback lose OAuth/Deluge credentials. Owner decides (open item O1). |
| Done when | Census (P1) shows `c/secrets/` count = 4 + configured plaintext secrets; `TestEverySecretFieldIsReference` (no `json` field tagged secret holds a value in the blob after C3); `TestEveryLegacyConfigKeyHasCanonical`; rollback rehearsal R-test (§9.10) green. |

### S4 — Identity: auth, RBAC, audit

| Aspect | Specification |
|---|---|
| Goal | One core identity resolver and authorizer for every surface (web, API keys, CF Access, OAuth, ABS); permissions namespaced; admin is `core:admin`; audit trail (G13). |
| Moves | `internal/auth` → `core/rbac` (atoms as data, module-declared) + `core/auth` (context, methods, `MayChangeCredentials`); `internal/oauth` → `core/auth` providers; `internal/server/middleware/{auth,credential_guard,ratelimit,actor,basicauth}.go` → `core/auth` middleware; password hashing from `internal/server/absauth/password.go` → `core/auth`; store code `pebble_store_auth.go`, `oauth_identity.go`, `pebble_store_invite_delivery.go`, `iface_auth.go` → `core/auth` store; handlers `auth.go`, `apikeys.go`, `user.go`, `invite_delivery.go`, `oauth_login.go`, `bootstrap.go`, `auth_temp_login.go`, `auth_accept_invite.go` → core routes. **Stay in ABO** (G11, G15): `internal/auth/owner.go`, `internal/cfaccesspolicy`, `internal/cfservicetoken`, guest tokens (`guestcftoken:`), ABS JWT/sessions (`abs_sess:`), `internal/usermerge`. The 44 `database.Store` auth methods' 14 caller packages (`raw/04` §3.6) switch to `core/auth` interfaces. Gin `s.perm(atom)` becomes a thin adapter over `rbac.Require` via the §9.2.6 table. |
| Data/keys | **Adopt** (Mirrored, §9.2.3): `u:` → `c/auth/user/`, `role:` → `c/rbac/role/` (atoms translated), `apikey:` → `c/auth/apikey/` (scopes translated; legacy empty-scope keys flagged `LegacyFullScope` so semantics hold), `invite:`, `inviteallow:`, `oauthid:`, and the six `idx:` auth sub-prefixes. **Sessions are not copied**: new sessions are written to `c/auth/session/` keyed by SHA-256 of the id, and mirrored raw to `sess:` + `idx:sess:user:`; lookups fall back to legacy `sess:` for 7 days (max TTL, `handlers/auth.go:90-96`). User IDs unchanged, so every books row keyed by user (`pref:`, `ubs:`, `playp:`, `upl:`, `bookmark:` …) is untouched. New `c/audit/` (append-only, no legacy twin). Adoption runs as core migration `core.auth#1` after books track ≥ 66, in a pre-migration checkpoint. |
| Config | `oauth_*`, `cf_access_*`, `basic_auth_*`, `enable_auth`, `owner_email` aliases → `core.auth.*` (owner/CF keys → `modules.books.identity.*` because the machinery stays in ABO). |
| Endpoints | New native: `/api/core/v1/auth/*`, `/users/*`, `/roles/*` (role CRUD; none exists today, `raw/10` §3.2), `/api-keys/*`, `/audit`, `/config/*`. Aliases (W): `/api/v1/auth/*` (22), `/api/v1/users/*` (15), `/api/v1/me`, `/api/v1/setup`, `/api/v1/config` (2), `/api/v1/preferences` (3). Public routes keep public (`raw/09` §2.4). `/auth/temp-login` and `/invite/:token` SPA links: aliases with W ≥ max invite TTL (P5). |
| Permissions | §9.2.6 mapping live; stored legacy atoms accepted on input (key creation) for W with deprecation counter; output always canonical. `RequireAdmin` role-name checks (`middleware/auth.go:522-537`, `handlers/user.go:285,441,453`, `wire_master_list_routes.go:41`) → `rbac.Require(core:admin)`. Seed upsert no longer clobbers operator edits of non-permission fields (G3; 05-rbac). |
| UI | `services/api.ts` auth/user/config calls switch to `/api/core/v1/...` in the **same** build (otherwise the first-party UI keeps the legacy counter non-zero forever). Users page gains roles management. |
| Rollback | To S3: `make rollback` passes (store required level 0). S3 reads `u:`, `role:`, `apikey:`, `sess:` — all mirrored, so users stay logged in and keys created on S4 work with reverse-mapped (possibly narrower) scopes; custom roles created on S4 appear with reverse-mapped atoms; S3's seed upsert rewrites seed roles as it does today. Audit rows remain unread. Roll-forward: reconcile re-adopts rows S3 changed (e.g. a password reset bumps `CredentialEpoch`). |
| Contract C4 (≥ W) | Stop mirror; delete legacy auth rows and the six `idx:` sub-prefixes; drop `sess:` fallback; legacy atom input → 400 naming the canonical atom; HTTP aliases → `Gone` for one minor → delete; `RaiseCompat(3)`. |
| Done when | `TestLegacyRoleReachUnchanged` green over 572 routes × 4 seed roles; P1 census: `c/auth/user/` count = `u:` count; `GET /api/core/v1/audit?kind=auth.login` shows the rehearsal login; `grep -rn '"admin")' internal --include=*.go` (role-name comparisons) = 0; rollback rehearsal R-test green including "deactivate on old build, roll forward, login refused". |

### S5 — Tasks

| Aspect | Specification |
|---|---|
| Goal | One task system (N9): v3 authoring on the v2 executor becomes `core/tasks`; required parallelism (N8, R7); `/api/core/v1/tasks` is the one API. |
| Moves | `pkg/ops` (minus `Writer`'s `database.Book*` types, `pkg/ops/writer.go:46-121`), `internal/operations/{registry,state,childop,opmode,freshness}`, generic `internal/scheduler`, `internal/opsmetrics`, `internal/server/handlers/opsv3` → `core/tasks`, `core/internal/executor`, `core/internal/scheduler`. `internal/maintenance` + `internal/plugins/**` defs → books task definitions; `internal/opscatalog` → books. `pkg/plugin/sdk`, `internal/plugin` registry → retired into `module.Registrar` (aliases until no caller). |
| Data/keys | **Adapter, not migration**: core `tasks` takes a run-store interface; ABO injects `books/legacyops`, implementing it over `opv2:`/`opv3:`/`op:`/`opstate:` in place, preserving the D25 dual-write mirror (`pebble_store_ops_v3.go:29-42`) until its own 05-PR16 retirement — this sequence does not retire it. Canonical kinds flip: def `dedup.full-scan` becomes `books.dedup.full-scan` with `FormerIDs: ["dedup.full-scan"]`; S0 taught the reverse, so S4 can resume S5-written rows. Stored rows are never rewritten (`aliases.go:27-31`). Scheduler interval clocks and markers (`scheduler/interval_clock.go:60`, `full_sweep.go:76`, `registry/pause_gate.go:268`) stay under books' claimed `setting:` keys. |
| Config | `scheduled.*` (~40), `maintenance.*` → `modules.books.schedules.*`; worker count → `core.tasks.workers`. |
| Endpoints | Native `/api/core/v1/tasks/{defs,runs,runs/{id},runs/{id}/cancel,approve,timeline,counts,events}`. Aliases (W): `/api/v3/ops/*` (12), `/api/v1/operations/*` (38), `/api/v1/op-defs` (2), `/api/v1/tasks` (3); legacy op status vocabulary kept on aliased responses via `WithLegacyShape` (generated enums `web/src/generated/ops.ts`). |
| Permissions | Per-def permissions (`registry/types.go:165-166`) translated; the implicit `settings.manage` default (`operations_v2.go:781-800`) becomes an explicit `core:tasks.manage` on every def that had none (no zero-value default; R6 spirit). |
| UI | Activity/operations pages and `useOperationsStore` switch to core task API in the same build. |
| Rollback | To S4: same families, mirrored opv2/opv3; rows carrying `books.*` kinds resolve through the S0-taught aliases. Runs queued on S5 for a def S4 lacks (none planned) stay queued; listed as risk. |
| Done when | `opscatalog` registers 233 defs through `core/tasks` with declared `Parallelism`; `internal/plugins/runitems_concurrency_lint_test.go` deleted because `tasks.Parallelism` has no zero value; `mediacore_compat_legacy_use_total{surface="task_kind",entry="enqueue"}` = 0 for UI-originated enqueues; resume rehearsal (kill -9 mid `books.acoustid.backfill`, restart, checkpoint resumes). |

### S6 — Events, outbox, realtime

| Aspect | Specification |
|---|---|
| Goal | One notify-only bus, one durable outbox, one resumable permission-filtered SSE hub (G4). |
| Moves | `internal/realtime` → `core/realtime`; `internal/operations/registry/bus.go` and `internal/plugin/events.go` → `core/events` with topics `books.book.imported.v1` etc. (12 types, `internal/plugin/events.go:18-30`); `internal/plugins/webhook` → `core/integrations/notify` webhook adapter. The tag write-back outbox `pref:_system:outbox:writeback:` stays a books claim read through an adapter (not moved). |
| Data/keys | New `c/outbox/` (core-owned, no legacy twin; previous build ignores it — S0 tolerance). |
| Config | `plugins.webhook.*` (`config.go:1814`) → `modules.books.notify.webhooks[]`; each migrated entry gets `format: legacy` (keeps `X-Audiobook-Event`, `X-Audiobook-Signature-256`, legacy event names; `internal/plugins/webhook/plugin.go:101-126`); new entries default `format: v1`. |
| Endpoints | Native `/api/core/v1/events` (SSE with `id:`, `Last-Event-ID`, caps — the v3 behaviour, `raw/09` §6). Adapters (W): `/api/events` (data-only frames, `operation.*`, `books.changed`), `/api/v1/operations/events` (`op.*`), `/api/v3/ops/events` (`run`, `resync`). After W: 410 (EventSource stops on non-200). |
| UI | `eventSourceManager` default URL → core stream in the same build. |
| Rollback | To S5: outbox rows left unprocessed; anything correctness-critical put in the outbox by S6 must also be durable by the S5 mechanism (ops rows) until C6 — the stage ships no correctness path that only the outbox carries. |
| Contract C6 (≥ W) | Legacy SSE → 410; `format: legacy` webhooks refused at boot with the conversion instruction for one minor, then the format is deleted. |
| Done when | Three SSE implementations reduced to one (`grep -rln 'text/event-stream' --include=*.go internal core | grep -v _test` = 1 file plus adapters); webhook golden (legacy headers/body) byte-equal to S5. |

### S7 — Integrations and library roots

| Aspect | Specification |
|---|---|
| Goal | Capability contracts in core; ABO's adapters satisfy them; per-instance HTTP resilience (R5). |
| Moves | `internal/metadata/{providerhttp,dailyquota}` + throttle registry → `core/integrations/providerhttp` (instance-scoped, `raw/12` §9); `internal/ai/{aijobs,resultjournal}`, generic `internal/aidispatch` → `core/integrations/ai`; Deluge client (`internal/deluge`, `internal/plugins/deluge`) → `core/integrations/download/deluge` + books glue; `internal/mail` → `core/integrations/notify/mail`; generic `internal/fileops`, `internal/watcher` → `core/library`. Metadata sources (`internal/metafetch` etc.) stay books, implementing `metadata.Provider[Q,R]`. |
| Data/keys | In place: `provider_throttle:`, `provider_daily_budget:`, `aijob*`, `aijournal:` read by core through adapters over books claims (large or hot; no adoption). |
| Config | `deluge_*`, `openai_*`, `whisper_*`, `ai.*` → `core.integrations.download.deluge.*`, `core.integrations.ai.*`; metadata keys → `modules.books.metadata.*`. Standard env `OPENAI_API_KEY`, `OPENAI_BASE_URL` permanent. |
| Endpoints | Native `/api/core/v1/integrations/{ai,download,notify}/...`; aliases (W): generic parts of `/api/v1/ai` (14), `/api/v1/deluge` (6), `/api/v1/metadata-sources`. Books-specific integration routes (iTunes, Open Library) wait for S10. |
| Rollback | No data shape change; binary swap. |
| Done when | `plugin.DownloadClient` (0 implementors, `raw/11` §4) deleted; `download.Client` has 1 production implementor (Deluge) passing `coretest` conformance; no package-level throttle/quota map (`archtest.TestNoGlobals` green on moved code). |

### S8 — Backup, updater, health, metrics registry

| Aspect | Specification |
|---|---|
| Goal | Platform operations owned by core; per-App metrics registry; names follow 03 §3.6. |
| Moves | `internal/backup` → `core/backup`; `internal/updater` → `core/updater` (release source from `app.Options`, not the hard-coded slug `updater.go:72`); host parts of `internal/sysinfo` + startup health tokens → `core/health`; `internal/metrics` series split: core series in core subsystems, books series in books. |
| Data/keys | Backup format v2 = **legacy layout unchanged** (DB dir rooted at its basename) + one root entry `mediacore-manifest.json` (format, app, modules, compat levels, storage format, checksums). An older build restoring v2 extracts the manifest as a harmless stray file beside the DB dir (`backup.go:817-860`). Module artifacts (hnsw snapshot, OL store) are **not** added inside the v2 tar during W; they go in a companion archive. Restore reads every format ever written (no window). |
| Config | `backup_*` → `core.backup.*`; `auto_update*` → `core.updater.*`. |
| Endpoints | Native `/api/core/v1/{backup,update,system,health}`; aliases (W): `/api/v1/backup` (4), `/api/v1/update` (3), `/api/v1/system` (7), `/api/health`, `/api/v1/health`. **Permanent:** `/health` (Docker HEALTHCHECK, `deploy-wait-ready.sh`), `/metrics` (scrape configs). |
| Metrics | Dual emission + recording rules for every renamed series (§9.2.7; 38 named Prometheus series plus the Pebble collector and OTel `ops_*` instruments, `metrics.go:15-300`, `pipeline_metrics.go:63-97`, `opsmetrics.go:12-16`). |
| Rollback | Binary swap; v2 archives restorable by S7. A restore of a post-contract archive onto an older build is refused by the compat guard at next boot (fail-closed, message names the required level). |
| Done when | `curl -s <abo-url>/metrics | grep -c '^mediacore_'` > 0 and every legacy name still present; `promtool test rules deploy/prometheus/alert-rules.test.yml` green with both rule sets; restore matrix test (archive from S0 build × S8 build, archive from S8 × S7) green. |

### S9 — UI shell

| Aspect | Specification |
|---|---|
| Goal | `@mediacore/shell` + books UI contribution (D6, N6); generated core API client. |
| Moves | `AuthContext`, `apiFetch`, `eventSourceManager`, `useOperationsStore`, toasts, `ErrorBoundary`, `storageKeys`, theme, Login, Setup, AcceptInvite, Users, ActivityLog (task parts) → `core/web` (`raw/13` §14.2); Library, BookDetail, Authors, Series, Works, Playlists, Collections, Review, Versions → books contribution. `App.tsx` route table and `Sidebar.tsx` arrays become `createShell({ modules: [books] })`. |
| UI routes | Module pages under `/books/...` (`/books/library`, `/books/library/:id`, `/books/authors`, …); shell pages `/settings`, `/system/...`, `/users`, `/activity`. Legacy SPA paths (`/library`, `/authors`, `/series`, `/works`, `/playlists`, `/collections`, `/review`, `/versions`, `/fingerprints`, `/dedup*`) → client redirects (pattern of `web/src/utils/absRedirects.ts:17-25`); the existing `/books/dedup` redirect is re-pointed inside the new prefix. Server counts full-page loads of legacy SPA paths in the NoRoute handler (`static_embed.go:121`) as `{surface="ui_route"}`. `localStorage` keys migrated on first load. |
| Rollback | Binary swap; UI is embedded per build. Bookmarks to `/books/...` 404 on the older build's SPA (accepted, cosmetic). |
| Done when | `App.tsx` has no hard-coded module routes; a dummy second contribution renders with no edit to books UI (G6, UI half); G3 test: shell rejects a module descriptor with unknown version. |

### S10 — Native books API; gin retired

| Aspect | Specification |
|---|---|
| Goal | `/api/books/v1/...` on `core/httpapi` with OpenAPI; gin and `internal/server` dissolved, one route group per PR. |
| Moves | `internal/server/handlers/**` books groups (audiobooks 106, dedup 35, authors 19, series 16, metadata 21, playlists 10, …; `raw/09` §1.1) → books httpapi handlers; `wireHandlers`/`setupRoutes` shrink group by group; gin removed when the last group moves. ABS surface re-hosted on `httpapi` at the same unversioned paths with the same reservation lists (`wire_abs_routes.go:283-345`). |
| Endpoints | Per group: native ships, UI switches, legacy group becomes `Alias` with legacy shape for W, then `Gone` one minor, then deleted. The 45 existing `gone()` stubs are deleted when older than one release (their own rule) — not ported. |
| Rollback | Per group: binary swap. |
| Done when | `go list -deps . | grep -c gin-gonic` = 0; `archtest.RoutesHavePermissions` green over the built router; OpenAPI document covers 100 % of native routes; ABS conformance fixtures (`internal/syncapi/conformance`) unchanged. |

### S11 — Final cleanup

Delete after their windows: the `/api/*` → `/api/v1/*` redirect (W after S10's last group;
afterwards unversioned non-ABS `/api/x` answers 404); removed flags `--db-type` and the SQLite
no-op flag (`cmd/root.go:489,508`); `viper.AutomaticEnv` (replaced by declared env bindings);
`internal/serviceregistry`, `internal/plugin`, `pkg/plugin/sdk`; compat-surfaces rows closed. Done
when §9.6 has no row in state "aliased" except those marked permanent.

## 9.5 Contract track

| Contract | Earliest | Precondition | Action | Required level after |
|---|---|---|---|---|
| C3 secrets/config | R(S3) + W | P8 zero for `config` and `secrets` surfaces; S4 build in prod (reads native) | delete legacy secret rows, scrub blob, convert blob → section docs | 2 |
| C4 identity | R(S4) + W | P8 zero for `http` auth/users aliases and `rbac.atom`; no `sess:` fallback hits for 7 days | delete adopted legacy rows + `idx:` sub-prefixes | 3 |
| C5 task API | R(S5) + W | P8 zero for task aliases (`enqueue`, `http`) | aliases → Gone; stored-row alias resolution **kept** | — |
| C6 SSE/webhooks | R(S6) + W | zero legacy stream connections | 410 streams; refuse `format: legacy` | — |
| C8 metrics/health | R(S8) + W | P6 shows no dashboard/alert on legacy names | stop dual emission; delete compat recording rules | — |
| C9/C10 UI/API groups | per group + W | per-route counter zero | Gone → delete | — |

A contract migration runs in a pre-migration checkpoint, deletes in ≤ 10 000-key batches, writes
`RaiseCompat` in the first batch (stamp before deletes, §9.2.2), and is idempotent on resume.

## 9.6 Q5 — every compatibility surface, window, removal mechanism

| # | Surface | Count / examples | Canonical | Window | Removal mechanism | Evidence |
|---|---|---|---|---|---|---|
| 1 | App API `/api/v1/*` (generic) | auth 22, users 15, operations 38, system 7, backup 4, update 3, config 2, preferences 3, tasks 3, op-defs 2, ai/deluge (generic parts) | `/api/core/v1/...` | W after the native route ships (S4–S8) | `Alias` + Deprecation/Sunset/Link → `Gone` 410 one minor → delete | `raw/09` §1.1 |
| 2 | App API `/api/v1/*` (books) | ~280 routes | `/api/books/v1/...` | W per group (S10) | same | `raw/09` §1.1 |
| 3 | `/api/v3/ops/*` | 12 | `/api/core/v1/tasks/*` | W after S5 | same | `wire_ops_v3_routes.go` |
| 4 | SSE streams | `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events` | `/api/core/v1/events` | W after S6 | framing adapter → 410 | `raw/09` §6 |
| 5 | Unversioned `/api/*` → `/api/v1/*` 301 | all non-reserved | — | W after S10 | redirect deleted (S11) | `server_lifecycle.go:1790-1822` |
| 6 | ABS-compatible surface | 75 routes, unversioned | unchanged, books-owned | **permanent** | none | `wire_abs_routes.go:628-653` |
| 7 | Infra paths | `/health`, `/metrics` | unchanged | **permanent** | none | `Dockerfile:100-101` |
| 8 | Infra aliases | `/api/health`, `/api/v1/health` | `/api/core/v1/health` | W after S8 | alias → 410 → delete | `server_lifecycle.go:1746-1782` |
| 9 | Retired 410 stubs | 45 | — | their own rule (one release) | deleted, not ported | `gone.go:14-36` |
| 10 | Permission atoms in API input/output | 13 | §9.2.6 | W after S4 | input accepted + counted → 400 naming canonical | `permissions.go:24-85` |
| 11 | Permission atoms in stored roles/API keys | `role:`, `apikey:` rows | translated in `c/rbac`, `c/auth` | mirror until C4 | contract migration | `store.go:968-994` |
| 12 | Role-name admin check | `"admin"` | `core:admin` | none (internal) | replaced in S4 | `middleware/auth.go:522-537` |
| 13 | Config keys in YAML | ~251 flat keys | `core.*`, `modules.books.*` | W after S3 | refuse-to-start naming canonical (one minor) → alias row deleted | `config.go:2621` |
| 14 | Config in DB (`config_blob`) | 1 blob + 10 `migrate*Blob` | section docs | mirror until C3 | contract converts automatically | `persistence.go:359-884` |
| 15 | Env vars, legacy | 141 `BindEnv` + implicit AutomaticEnv names | `ABO_<SECTION>_<KEY>` | W after S3 | not read; one boot WARN per known name for one minor | `cmd/root.go:625`; `config.go` |
| 16 | Env vars, standard | `OTEL_EXPORTER_OTLP_*`, `OTEL_METRIC_EXPORT_INTERVAL`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `NOTIFY_SOCKET`, `HOME` | unchanged | **permanent** | none | `config.go` BindEnv; `sdnotify.go:89` |
| 17 | CLI commands | `serve scan playlist tag organize inspect-metadata seed diagnostics{cleanup-invalid,reserved-prefs,query} fp-worker dedup-bench`(bench tag) | unchanged names | **permanent** | none | `cmd/*.go` `Use:` |
| 18 | CLI flags in shipped artifacts | `--host --port --db --config --dir --log-level --playlists --tls-cert --tls-key --http3-port --external-url --workers --read-timeout --write-timeout --idle-timeout` | same flags feed canonical keys | until next **major** (1.0) | release-note at the major; not usage-gated (Docker CMD in users' compose files) | `cmd/root.go:486-535`; `Dockerfile:104` |
| 19 | Removed/no-op flags | `--db-type`, SQLite flag | — | W after S2 | deleted S11 | `cmd/root.go:489,508` |
| 20 | Fast-exit protocol | `--print-storage-format`, `--print-capabilities`, new `--print-compat-level` | unchanged | **permanent** | none (rollback protocol) | `main.go`; `Makefile:892` |
| 21 | Child-mode sentinel/env | `--operation-runner`, `registry.EnvChild*` | unchanged | none (same binary) | n/a | `cmd/child_mode.go:53-96` |
| 22 | Other binaries in `cmd/` | `itl-*`, `mtls-bridge`, `pebble-inject-skip`, `pid-census`, `verify-suite` | stay ABO | permanent (books tools) | n/a | `cmd/` |
| 23 | Metric names | 38 named Prometheus series + Pebble collector + OTel `ops_*` | §9.2.7 | W after S8 | dual emission + recording rules → legacy dropped | `metrics.go:15-300` |
| 24 | OTel scope prefix | `audiobook-organizer/` | `mediacore/…`, `audiobook-organizer/books/…` | W after S8 | same | `meter.go:17` |
| 25 | Pebble small generic families | `u: role: apikey: invite: inviteallow: oauthid: sess:` + 6 `idx:` sub-prefixes + 4 secret rows | `c/auth/`, `c/rbac/`, `c/secrets/` | mirror until C3/C4 | contract migration + `RaiseCompat` | `keyfamilies.go:63-71` |
| 26 | Pebble large/books families | the other ~240 incl. `opv2:`, `opv3:`, `act:`, `setting:`, `pref:` | legacy claims by books | **permanent** (no rename, 02 #13) | none | `keyfamilies.go` |
| 27 | Task kinds (op def IDs) | 233 | `books.<id>`, `core.<id>` | input W after S5; stored-row resolution **permanent** | `FormerIDs` + existing deprecation counter | `aliases.go:16-52` |
| 28 | Webhook payloads | 12 event names, 2 headers | `books.*.v1` topics, core notify headers | W after S6 | per-entry `format: legacy` → refused → deleted | `plugin/events.go:18-30`; `webhook/plugin.go:101-126` |
| 29 | Backup archives | tar(+codec) of DB dir | v2 = legacy layout + manifest | read: **permanent**; write legacy-restorable layout: W after S8 | companion archive for module artifacts after W | `backup.go:283-860` |
| 30 | SPA routes | 14 module paths | `/books/...` | W after S9 | client redirect, server counter → removed | `App.tsx:253-448` |
| 31 | Wire shapes of aliased routes | ~260 hand-written TS interfaces | OpenAPI-generated | = route window | `WithLegacyShape` goldens | `raw/13` §8 |

## 9.7 Production-data unknowns (read-only commands, both branches designed)

Run these against production before the stage that needs them; paste counts only, never values.
`<abo-url>`, `<redacted-token>` (an admin API key from `server-bootstrap`), `<unit>`, `<scratch>`
are placeholders. Offline commands run on a **restored backup copy** so production is never
stopped: restore the newest archive into `<scratch>` with `tar` and point `--db` at it.

| ID | Question | Command | Branch A | Branch B |
|---|---|---|---|---|
| P1 | Key counts/bytes per family (S3, S4, C*) | `curl -sS -H "Authorization: Bearer <redacted-token>" "<abo-url>/api/v1/diagnostics/db-census?fresh=true" \| jq '.families[] \| select(.prefix \| test("^(u\|role\|apikey\|invite\|inviteallow\|oauthid\|sess\|idx\|setting\|opv2\|opv3\|act):")) \| {prefix,keys,bytes}'` (estimated, read-only, `handlers/diagnostics.go:1195-1220`; field names per the JSON you get). Exact: `audiobook-organizer diagnostics query --raw --db <scratch>/audiobooks.pebble --prefix u: --limit 10000000 \| grep -c '^Key:'` per prefix | adopted families total < 50 000 keys and < 64 MiB: **one atomic batch** adoption | larger: chunked copy (5 000 rows/batch) under an `adoption_in_progress` marker, legacy stays truth until a final one-batch flip writes the marker-clear + phase; reconcile covers rows changed between chunk and flip |
| P2 | Custom (non-seed) roles exist? | `audiobook-organizer diagnostics query --raw --db <scratch>/audiobooks.pebble --prefix role: --limit 1000 \| grep -c '"is_seed":true'` vs total `^Key:` | none: adoption only seeds + translation | some: each translated; any atom outside §9.2.6 → boot refuses with the role id (fail-closed), owner maps it |
| P3 | API keys with empty scopes / legacy atoms | same with `--prefix apikey:` and `grep -c '"scopes":\[\]'`, `grep -c 'settings.manage'` | none empty: no `LegacyFullScope` rows | some: flagged `LegacyFullScope`; native API forbids new empty-scope keys (G6) |
| P4 | Env names actually set | `systemctl show <unit> -p Environment \| tr ' ' '\n' \| cut -d= -f1` and `systemctl cat <unit> \| grep -o 'Environment="[A-Z_0-9]*' `; Docker: `docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' <container> \| cut -d= -f1` | only standard/explicitly bound names: alias table suffices | an implicit AutomaticEnv name in use: add explicit alias row before S3 so S11 removal of AutomaticEnv cannot silently drop it |
| P5 | Invite TTLs outstanding; external API callers | `... --prefix invite: ... \| grep -o '"expires_at":"[^"]*"' \| sort \| tail -1`; callers: `journalctl -u <unit> --since -30d -o cat \| grep -oE '(GET\|POST\|PUT\|PATCH\|DELETE) /api/[a-z0-9/_-]*' \| sed -E 's#/[0-9A-Za-z]{20,}#/:id#g' \| sort \| uniq -c \| sort -rn \| head -50` (adapt to the access-log format, `server.go:695`) | only the SPA calls `/api/v1`: W floor applies | scripts/clients found: list them in the ledger; window stays open until P8 is zero |
| P6 | Metric consumers | operator's Grafana/Prometheus: `grep -rhoE 'audiobook_organizer_[a-z_]+' <dashboards-and-rules-dir> \| sort -u` | only repo templates (`deploy/grafana`, `deploy/prometheus`): update them in S8 | private dashboards: recording rules carry them through W |
| P7 | ABS surface in use | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.abs_api_enabled'`; `... --prefix abs_sess: ... \| grep -c '^Key:'` | off: S10 ABS re-host has no live-client risk | on with sessions: conformance fixtures + live smoke against a real client before S10 ABS group |
| P8 | Legacy use counters (removal gate) | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/metrics \| grep -E '^(mediacore_compat_legacy_use_total\|audiobook_organizer_operation_deprecated_def_id_total)'`; over 14 days in Prometheus: `sum by (surface,name) (increase(mediacore_compat_legacy_use_total[14d]))` | 0: contract may merge | > 0: window extends; the `name` label says which caller to fix |
| P9 | Webhooks configured | `curl … /api/v1/config \| jq '.plugins.webhook // empty \| keys'` | none: `format: legacy` path has no users, C6 trivial | some: entries keep `format: legacy` through W |
| P10 | Rollback inventory | `ssh <host-a> '<bin>.prev --print-storage-format; <bin>.prev --print-compat-level'`; `curl -s <abo-url>/api/v1/system/version` | `.prev` ≥ S0 build: compat guard effective | `.prev` older than S0: deploy S0 twice (two consecutive builds) before S3 so `.prev` always knows the guard |

## 9.8 Error semantics (compatibility machinery)

| Error path | Behaviour | Why |
|---|---|---|
| Store required level > build level | **fail-closed**: refuse to open, name both levels | an old build on a contracted store would read stale or missing rows |
| Adoption migration error | **fail-closed**: boot halts, checkpoint kept, legacy untouched | G2 |
| Reconcile error or undecodable legacy row | **fail-closed** boot halt naming the key; recovery via `diagnostics` | silently keeping a stale native row can re-enable a deactivated user |
| Mirror write fails | **fail-closed**: whole batch fails, request errors | a native write without its mirror breaks rollback |
| Reverse mapping has no legacy atom | drop atom from mirror (**closed**) | never widen privilege on an older build |
| Stored role holds an unknown atom (adoption) | **fail-closed** boot, names role id | P2 branch B |
| Legacy alias request | served (**open**), counted, headers | compatibility is the point |
| Legacy key in YAML after W | **fail-closed** refuse to start, names canonical | silently ignoring `root_dir` would point the library at nothing |
| Legacy env name after W | ignored + WARN (**open**) | env carries unrelated names (`HOST`, `PORT`); refusing would break hosts |
| Same key in legacy and canonical form | canonical wins + WARN (**open**) | deterministic, visible |
| Compat counter cannot increment | ignored (**open**) | telemetry must not fail requests |
| Backup manifest missing (old archive) | restore proceeds as legacy (**open**) | read support is permanent |

## 9.9 Concurrency contract

Adoption, reconcile and contract migrations run in the kernel's migration phase, before listeners
and the executor start (03 §3.7), single goroutine, under the storage migration lock. They are
**sequential by declared necessity**: correctness needs one atomic flip and the bottleneck is one
Pebble batch commit (R7 `tasks.Sequential(why)` wording reused). Adopted families are bounded by
P1 branch A/B; none is library-scale. Mirror writes are staged in the caller's batch under the
family's existing store mutex (the `opsMu` pattern, `pebble_store_ops_v3.go:33-35`). Alias
handlers inherit the canonical handler's concurrency. All migration steps take `ctx` and honour
cancellation between batches; a cancelled adoption leaves legacy as truth.

## 9.10 Config keys, metrics, tests

Config keys: **none new** (§9.2.8). Every behaviour is release-bound.

| Metric | Type | Labels |
|---|---|---|
| `mediacore_compat_legacy_use_total` | counter | `surface` (http, config, env, rbac_atom, task_kind, sse, webhook, ui_route, metric), `name` (bounded: alias-table key), `entry` |
| `mediacore_compat_reconcile_rows_total` | counter | `family`, `outcome` (unchanged, readopted, deleted) |
| `mediacore_compat_level` | gauge | `kind` (supported, required) |
| `mediacore_compat_adoption_seconds` | histogram | `family` |

| Test | Proves |
|---|---|
| `TestLegacyToleratesNamespacedKeys` (S0) | HEAD-era code paths ignore `c/`, `m/` keys |
| `TestNativePrefixesNotRedirected` (S0) | `/api/core/vN/`, `/api/<module>/vN/` bypass the 301 |
| `TestCompatGuard*` + `scripts/test_storage_format_guard.py` cases | open refusal and rollback refusal across levels |
| `TestMirroredAdoptIdempotent`, `TestReconcileLegacyWins`, `TestReconcileLegacyDeleted` | §9.2.3 under `testing/synctest` with a fake clock |
| `TestLegacyRoleReachUnchanged` | §9.2.6 invariant over 572 routes |
| `TestEveryLegacyConfigKeyHasCanonical`, `TestEverySecretFieldIsReference` | config/secrets completeness |
| `TestCompatLedgerMatchesCode`, `TestNoContractBeforeSunset` | ledger is the code |
| `TestAliasHeadersAndShape` | Deprecation/Sunset/Link + legacy-shape goldens |
| **R-test (rollback rehearsal), per stage** | in CI: build `prev` (stage N−1) and `cur`; boot `cur` on a seeded store, write through every adopted surface, stop; boot `prev` on the same store, assert reads, write (password reset, deactivate, key revoke), stop; boot `cur`, assert reconcile adopted `prev`'s writes. Make target `make compat-rehearsal`. |

## 9.11 Risk table

| Stage | Risk | L | I | Detection | Mitigation |
|---|---|---|---|---|---|
| S0 | Redirect exclusion regex too broad: some unversioned client path `/api/x/v2/...` stops redirecting | L | M | `TestCollidingNamespacesStillRedirect`; P5 | exclusion requires `/vN/` second segment and a module ID shape |
| S0 | `.prev` predates S0, compat guard blind | M | H | P10 | deploy S0 to production twice before S3 |
| S1 | Moving telemetry changes metric/label output | M | M | S1 done-check name diff | namespace/scope injected, golden of `/metrics` names |
| S2 | Listener move changes TLS/H3/peer-allowlist behaviour | M | H | smoke on `--http3-port`, peer test from a disallowed address | move code verbatim first, refactor later; `peer_allowlist` tests move with it |
| S2 | Boot order change (container vs migrations) delays readiness past systemd budget | M | M | `deploy-wait-ready.sh` timing | keep `extendStartTimeout` (`migrations.go:559`) in kernel |
| S3 | Rollback loses secrets if mirror skipped | L | H | R-test | mirror mandatory; plaintext kept through W (O1) |
| S3 | Encryption key file missing on restore to new host | M | H | restore matrix | open item O3 (05-backup) |
| S4 | Privilege change from atom expansion | M | H | `TestLegacyRoleReachUnchanged` | reverse map conservative; invariant test gates merge |
| S4 | Stale native identity after rollback/roll-forward | M | H | R-test "deactivate on prev" | reconcile every boot, fail-closed |
| S4 | `usermerge` undo or admin debug-edit restores legacy `u:` images at runtime, bypassing reconcile | M | H | test: undo a pre-S4 merge journal on S4 | journal undo for adopted prefixes routes through `Mirrored.Readopt(key)` |
| S4 | Session fallback lets a revoked legacy session live | L | H | test | fallback lookup applies `CredentialsCurrent` epoch check (`middleware/auth.go:229`) |
| S5 | Unknown kind on rollback leaves runs queued forever | L | M | timeline `waiting` counts | S0 teach-aliases; no new defs in S5 |
| S5 | Parallelism declarations change runtime behaviour | M | M | duration metrics per def | carry today's `Concurrency` values verbatim (`raw/06` §6) |
| S6 | Legacy SSE adapter drops events the UI relied on | M | L | UI e2e on legacy stream | adapter goldens per event type |
| S6 | Webhook receivers break on new headers | L | M | P9 | `format: legacy` per entry |
| S7 | Instance-scoped throttles double outbound rate (two instances) | M | M | provider 429 counts | one `providerhttp` instance per provider per App, asserted in kernel |
| S8 | Dual metric emission doubles cardinality | L | L | Prometheus TSDB head series | bounded W; legacy names dropped at C8 |
| S8 | Old build restores a v2 archive with stray manifest | L | L | restore matrix | manifest at archive root only, harmless |
| S9 | Bookmarks/old tabs hit removed SPA paths | H | L | `ui_route` counter | client redirects through W |
| S10 | Wire-shape drift between legacy alias and native handler | H | M | `TestAliasHeadersAndShape` goldens | `WithLegacyShape` per route, from `wirecontract` goldens |
| S10 | ABS re-host changes protocol behaviour | M | H | `internal/syncapi/conformance` fixtures; P7 | ABS group last, live-client smoke |
| C* | Contract merged before window | L | H | `TestNoContractBeforeSunset`; P8 | ledger dates + usage gate + compat guard |

## 9.12 Open items

| # | Item | Owner |
|---|---|---|
| O1 | Plaintext secrets in `config_blob` during W (rollback safety) vs scrub at expand (security). Recommendation: keep through W; owner decides. | owner |
| O2 | Final `core:` atom names (`core:tasks.view`, `core:events.subscribe`, …) | 05-rbac, 05-tasks |
| O3 | `.encryption_key` lives in the data dir outside the archived DB dir (`settings.go:44-52`, `backup.go:283-428`): does core backup include, wrap or exclude the key? Restore on a new host cannot decrypt otherwise. | 05-backup |
| O4 | Env grammar and prefix (`ABO_`) | 05-config |
| O5 | Exact list of the ~240 books claims (generated from `keyfamilies.go`) | 04, 05-storage |
| O6 | Whether `act:` (activity) becomes core audit history or stays books; this sequence keeps it books | 05-rbac |
| O7 | CLI flag window "until 1.0" deviates from 02 Q5's "two minors" for surfaces baked into users' compose files | owner |

## Objections to 03

1. **Module-ID vs reserved API namespace collision (§3.6).** `/api/<moduleID>/v1` shares the
   `/api/` space with ABO's ABS surface (`/api/me`, `/api/items`, `/api/libraries`, `/api/authors`,
   … `wire_abs_routes.go:308-345`). A future module ID `authors` or `series` would collide. Add:
   the kernel rejects a module ID equal to any namespace in `app.Options.ReservedAPINamespaces`
   (ABO passes its ABS lists) — registration-time, fail-closed.
2. **R8 needs two claim shapes 03 does not name**: exact-key claims (2 bare keys, `keyfamilies.go:84,316`)
   and prefix claims with excepted sub-prefixes (`idx:` is shared by auth and books). Not a
   contradiction; a requirement on 05-storage.
3. **No home for the compat primitives** in the §3.3 tree. This file assumes each SDK package
   (`storage`, `httpapi`, `config`, `rbac`, `metrics`) carries its own alias primitive and they share
   one counter (`mediacore_compat_legacy_use_total`) registered on the App's registry. If 06 prefers
   a tiny public `compat` package, that is compatible with this design.

## Out-of-scope defects found

1. `deploy/audiobook-organizer.service:97` sets `AUDIOBOOK_ROOT_DIR`, but no code reads that name:
   `AutomaticEnv` has no prefix or replacer (`cmd/root.go:625`), so `root_dir` maps to `ROOT_DIR`;
   the name occurs only in a comment (`internal/config/config.go:2571`). The shipped unit's variable
   is dead and the comment describes it as if it were read.
2. `--db-type` help still says "pebble (default) or sqlite" (`cmd/root.go:489`) although SQLite is
   rejected (`internal/database/store.go:1840-1843`; already noted in `raw/05` §1).
