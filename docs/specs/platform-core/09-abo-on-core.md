<!-- file: docs/specs/platform-core/09-abo-on-core.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3c82c917-2fc4-4385-b085-73d143a2374f -->
<!-- last-edited: 2026-11-04 -->

# 09 — ABO on core: the strangler-fig sequence

Evidence commit: `748fd787a`. Raw data: [`raw/`](../../plans/platform-core/raw/00-index.md).
Names, paths and rules come from [03-architecture](03-architecture.md) (normative, v1.3.0). Package
moves are **not restated here**: [04-package-mapping](04-package-mapping.md) §8 keys every move by
this file's stages and is authoritative for a single package (MOD-6); each stage below names its 04
rows. This file is the single owner of **sequencing and compatibility windows** for ABO: where a
05-* "Migration from ABO" section and this file disagree on *when* something happens or *how long*
a legacy surface lives, this file wins; on *how* a mechanism works, the owning 05-* file wins.

## 9.0 Purpose & scope

How audiobook-organizer (ABO) becomes **app = core + module `books`** without a flag day. Every
stage ends with ABO shippable, deployable, and reversible with `make rollback` (`Makefile:892-917`;
D3, gate G5, [02](02-decision-record.md) §2.4). Deploys use the operator's recipe
(`Makefile.local.example:88-110`, the `deploy` target, which is not committed); the CI-checkable proof
of G5 is the rollback rehearsal `make compat-rehearsal` (§9.10), not a deploy target. This file
answers old Q5 (§9.6) and defines the one compatibility window (§9.2.1). It does not design the core
contracts (05-*, 06, 07); it says in what order ABO adopts them and what stays compatible.

**Stage numbering (changed in v1.1.0).** Gin is never retired (cross-cutting resolution 2), so old S10
("native books API; gin retired") is removed and old S11 (final cleanup) is renumbered **S10**. Two
stages are inserted without renumbering the rest: **S2b** (core opens the store; books on claims,
OPS-12) and **UI-0** (UI groundwork before S4, FE-2/FE-12). S0–S9 keep their v1.0.0 numbers, so
"09 S4" etc. in other files still point at the same stage. The dispositions' "S0–S11" means this
whole sequence (S0–S10).

Out of scope: TV/Movies (10), multi-app (11), the repo split (12). The deferred-build packages
(`acquire`, `arrfacade`, `integrations/indexer`, the download grab/monitor pipeline, non-Deluge
adapters; 03 §3.4) are built in **no** stage. The storage-efficiency program that owns
`storage_format` 2 (`internal/database/storage_format.go:44-50`) is a separate track; this sequence
never raises `SupportedStorageFormat` (§9.2.2).

## 9.1 Current state at HEAD (compatibility-relevant facts)

| Fact | Evidence |
|---|---|
| One Pebble store, one WAL, one backup unit; 258 registered key families (243 namespaced, 2 bare, 10 legacy, 3 v1) | `internal/database/keyfamilies.go:37-345`; `raw/05-storage.md` §2–§3 |
| No family starts with `c/` or `m/`; a key outside every family is counted as `(unregistered)`, never deleted | `internal/database/census_families.go:128`, `keyfamilies.go:19-26` |
| `idx:` is ONE registered family shared by auth (`idx:user:`, `idx:role:name:`, `idx:sess:user:`, `idx:apikey:`, `idx:invite:username:`, `idx:oauth:`) and books (`idx:bv:`, `idx:col:name:`, `idx:ubs:status:`, `idx:upl:`, `idx:sidx:dirty:`, `idx:abs_sess:`) | `keyfamilies.go:64`; `pebble_store_auth.go:67,142,966,1126,1131,1335,1670`; `oauth_identity.go:32,78` |
| `setting:` is books' runtime-marker store (56 non-test `SetSetting` call sites), not only config | `internal/database/settings.go:316`; review DATA-1 |
| Storage-format guard refuses a store **newer or older** than the build; `make rollback` asks `.prev --print-storage-format`, reads `<db>.storage-format` with `sudo -n cat` | `pebble_store.go:670-680`; `Makefile:892-917`; `scripts/storage_format_guard.py`; sudoers lines `docs/system/deploy-and-gpu-ops.md:208-218` |
| Precedent: opv2→opv3 dual-write in ONE batch so the previous binary can be redeployed; reconcile every boot | `internal/database/pebble_store_ops_v3.go:29-42` (D25); `store.go:1861-1870` |
| Precedent: renamed op IDs via `OperationDef.FormerIDs`, counted by `audiobook_organizer_operation_deprecated_def_id_total` — a process-local counter whose rationale is "survives in Prometheus history" | `internal/operations/registry/aliases.go:16-52`; `internal/metrics/metrics.go:98-108` |
| Nothing scrapes `/metrics` in this repo; `/metrics` needs an API key | `deploy/prometheus/README.md:9-10` |
| Retired routes: `gone(replacement)` → 410 behind the original auth, one release, then deleted; 45 stubs | `internal/server/gone.go:14-36` |
| Unversioned `/api/*` is 301-redirected to `/api/v1/*` unless excluded (v1, v3, health, events, metrics, ABS-reserved) | `internal/server/server_lifecycle.go:1790-1822`; `wire_abs_routes.go:308-345` |
| 13 permission atoms; 4 seed roles upserted every boot; admin checked by role **name**; an API key with **empty** scopes holds all of its owner's role permissions | `internal/auth/permissions.go:24-85`; `seed.go:86-129`; `middleware/auth.go:405-408,522-537` |
| Config: ~251 viper defaults, `AutomaticEnv` with no prefix, 141 `BindEnv` aliases; whole config persisted as one flat `config_blob` + 4 encrypted secret rows | `internal/config/config.go:1048-1912,2621`; `cmd/root.go:625`; `persistence.go:884,1996-2008` |
| Shipped deploy files: unit `Type=notify`, `TimeoutStartSec=300`, **no `TimeoutStopSec`**, `Restart=on-failure`, `StartLimitBurst=5`/60 s, DB path only from `Environment=DATABASE_PATH`; compose sets `BASIC_AUTH_*`, no `stop_grace_period`; plist sets `DATABASE_PATH`, no `ExitTimeOut`; Docker `CMD … --db /data/audiobooks.pebble`, HEALTHCHECK `/health` | `deploy/audiobook-organizer.service:81-112`; `docker-compose.yml:18-22`; `deploy/com.audiobook-organizer.plist:36-43`; `Dockerfile:100-104` |
| Env names in shipped files that nothing reads: `AUDIOBOOK_ROOT_DIR` (unit, plist; deliberately unread), `AO_DIR`, `AO_DB` (compose), `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`) | `internal/config/config.go:2569-2575`; no Go reader at the evidence commit |
| Warmup is 130–200 s against a hard-coded 300 s in `TimeoutStartSec`, `deploy-wait-ready.sh` default and `make rollback` | unit `:28,87`; `scripts/deploy-wait-ready.sh:13`; `Makefile:916` |
| Both Dockerfiles copy only `go.mod go.sum` before `go mod download && go mod verify`; `.dockerignore` excludes only top-level and `web/` `node_modules`/`dist` | `Dockerfile:54-55`; `Dockerfile.build-cgo:52-53`; `.dockerignore:18-20` |
| Prereleases skip Docker; only the manual production release builds the image; no PR job builds it | `.github/workflows/prerelease.yml:48`; `release-prod.yml:53` |
| `npm ci` runs in 12 places, none of which builds anything first | `Dockerfile:18`, `Dockerfile.build-cgo:17`, `prerelease.yml:51`, `release-prod.yml:58`, `ci.yml:379`, `e2e.yml:167`, `binary-smoke.yml:55`, `security.yml:82`, `vulnerability-scan.yml:62`, `.woodpecker/checks-build.yaml:85`, `scripts/ci_remote.py:516`, `Makefile:194` (`web-install`) |
| Updater: downloads `releases/latest`, deletes `.old` after the swap, restarts with `os.Exit(0)` — under `Restart=on-failure` systemd does not restart it | `internal/updater/updater.go:278,295,331-334` |
| Live restore extracts over the running store | `internal/backup/backup.go:777-870` |
| Web client: `API_BASE = '/api/v1'`, no build/version check; 5 hard-coded permission constants and a legacy-atom scope picker; 52 source files import `react-router-dom` | `web/src/services/api.ts:24`; `hooks/useLibraryPermission.ts:9,11`, `hooks/useCanOrganizeLibrary.ts:9`, `hooks/useCollections.ts:24`, `components/system/ThrottlePanel.tsx:24`, `components/settings/APIKeysTab.tsx:49-61` |
| Releases: RC on every push to main, production minors by manual dispatch, irregular cadence; installs jump straight to `releases/latest` | `prerelease.yml:32-58`, `release-prod.yml:43-65`; `updater.go:295` |

## 9.2 Compatibility model (used by every stage)

### 9.2.1 The one compatibility window (SIMP-7)

This section is the **only** definition of the window. `compat` implements it once (§9.2.9); httpapi
aliases (05-http-api §4.6), config and env aliases (05-config §1.12.4), permission atoms (05-rbac
§6.3), task kinds (05-tasks), SSE projections (05-realtime) and metric aliases (05-metrics §13) all
register against it and define no window of their own.

Every renamed or moved surface goes through three steps, **never two in the same deployed build**:

1. **Teach** — an earlier deployed build learns to *read* what the new build will leave in shared
   state. Needed only where a newer build writes a form an older build must read (S0 carries the one
   teach this sequence needs: 05-config §1.12.2 phase T).
2. **Expand** — the new form becomes truth; the old form is still written (data) or still served
   (API, config, metrics). Rollback to the previous build is always safe.
3. **Contract** — after **W**, and only through a contract PR, the old form stops being written or
   served (410 or refusal for one more production minor), then is deleted. Nothing is removed or
   turned into 410 by a clock (OPS-10): `Sunset` headers are advisory.

**W** is the later of (a) two production minor releases after the expand shipped and (b) 30 days,
and removal additionally requires (c) **no recorded use for 14 consecutive days**, read from the
persisted usage record (OPS-16): each surface's handle writes `{count, last_used_at}` to the store in
batched flushes, exposed by `GET /api/core/v1/compat/usage` and the gauge
`mediacore_compat_legacy_last_use_timestamp_seconds`. The process-local counter
`mediacore_compat_legacy_use_total` stays for dashboards only; a restart cannot reset the gate.
Exemptions from (c): metric aliases, because a scrape reads every family and the server cannot see
which name a dashboard uses (05-metrics §13; their gate is P6), and surfaces marked **permanent**
(§9.6).

**Unit of compatibility = the store, not the release (OPS-2).** Installs skip releases (the updater
fetches `releases/latest`, Docker users pull `:latest`, a backup from any build can be restored), and
`make rollback` returns to `.prev`, which may be any main build. Therefore every contract step checks
**on the store** that its expand ran and has served the store long enough (§9.2.2); a store
that never adopted runs the adoption first and never a contract delete (cross-cutting resolution 6).

**One flip PR per stage (OPS-18).** A stage's native code lands dark across many PRs; exactly one
**flip PR** makes it truth, bumps the build's reader level, and is recorded in the ledger. An AST
ratchet, `TestAdoptedPrefixWritesGoThroughMirror` (precedent `TestOpsV2RowWritesGoThroughStageOpRow`),
forbids direct `Set`/`Delete` on an adopted legacy prefix outside the mirror writer from the flip PR
on, so an RC or production build cut from main between PRs never splits truth.

### 9.2.2 The store guard: reader levels per owner (SIMP-18, OPS-3, OPS-26)

There is **one** guard mechanism — per-owner reader levels, which replace both the storage-format
guard and v1.0.0's separate compat-level guard — owned by [05-storage](05-storage.md) §5S.3.7: level
stamp `c/storage/level/<owner>`, sidecar `<db>.storage-levels`, served ledger
`c/storage/served/<owner>/<level>`, fast exit `--print-storage-levels`, `CheckReader` (one decision
for open, `make rollback` and the updater), and books' **legacy projection** onto
`preference:storage_format` / `<db>.storage-format`, which makes every pre-guard build refuse a store
once any level was raised. This file owns the **policy**: which level each stage's build serves, and
which step raises a store's level. Only a `Breaking` step with `RaisesLevel` raises a level, and in this
sequence only contracts do; the storage-efficiency release B (books owner) is a separate track.

**Levels are reader capabilities** (OPS-3): a build declares a level as soon as it can read the shape
that level guards, long before any store is raised to it. All levels below are owner **`core`** (core
subsystems share one level); owner `books` stays at 0 throughout this sequence.

| `core` level | First build | What every build at this level can read |
|---|---|---|
| 0 | before S0 | — (pre-guard: reads only the legacy projection, and refuses projection value 2) |
| 1 | S0 | knows the guard; tolerates `c/` and `m/` keys; writes the served ledger; 05-config phase **T** (overlays every legacy-format secret row, not only the 4) |
| 2 | S3 (05-config phase X) | `c/config/` and `c/secrets/` as truth, with or without the legacy blob and rows |
| 3 | S4 | identity from `c/auth/`, `c/rbac/` with or without legacy rows |
| 4 | S5 | tasks from `c/tasks/` with or without the legacy reconcile and mirrors (05-tasks §5T.6) |
| 5 | S8 | core `backup`: restores `mediacore-backup/1` archives including encrypted ones (05-backup §5B.3.3) |

**Legacy restore bound** (05-backup B3): books declares `backup.LegacyRestoreBound{Owner: core,
Level: 4}`. While the store's `core` level is ≤ 4, some build that can still serve the store (S7 or
older) has only ABO's plaintext restore code, so backup encryption stays inactive; contract C8 raises
the store to 5 (§9.5), after which every build that can open the store restores encrypted archives.

**What the store records** (written by its own boots, never by a person; mechanism 05-storage):

| Record | Written by | Read by |
|---|---|---|
| level stamp + sidecar | only a contract (`Breaking` data migration with `RaisesLevel`), sidecar fsynced before the stamp's batch commits; on a commit **error** the sidecars are rewritten from the stamps (OPS-26b) | open (refuses a store above the build's level), `make rollback`, the updater's `ReaderCheck` |
| adopt-step applied record `c/storage/ks/core/applied/<v>` | the first boot that ran the stage's expand/adopt step (S3, S4, S5) | the stage's contract (`Adopts`, cross-cutting 6) |
| served ledger | every guard-aware boot (S0 on) | contracts (`RequiresServed`); 05-config's blanking rule (the build that booted before this one is level ≥ 1) |

**Contract rule (OPS-2, cross-cutting 6).** Every contract declares `Adopts: [<the stage's adopt
step>]` and `RequiresServed{Owner: core, Level: <expand level>, For: 30 days}` (the W floor; the
minor-release count and the usage gate are enforced on the PR, §9.2.1). On a store where the adopt
record is missing, the build runs the adopt step first (it stays registered in every later build); on a
store where the expand level has not served long enough, the contract is **deferred**: the build boots
in mirror phase and retries the contract on later boots — it never deletes, and it never fails boot for
this reason. Only a build that no longer registers the adopt step refuses to start on an unadopted
store (`ErrAdoptMissing`, naming the release to install). No stepping-stone release is required, so
release manifests need no `min_upgrade_from`.

**Rollback tooling.** `--print-storage-levels` prints the build's supported vector; `--print-storage-format`
stays for pre-guard scripts and prints the projection. `scripts/storage_format_guard.py` (name kept)
implements `CheckReader`'s table for `make rollback`, refuses while a staged restore is pending, and
prints both values when a stamp and its sidecar disagree (OPS-26c). **Owner action in S0:** add the
literal sudoers line for `<db>.storage-levels` next to the existing ones
(`docs/system/deploy-and-gpu-ops.md:208-218`); without it every rollback refuses (OPS-26a).

**Disk headroom (OPS-25).** Every stage whose first boot takes a pre-migration checkpoint (S2b, S3,
S4, S5, each contract) checks free space ≥ live store size first and fails closed with a message;
checkpoints are pruned by age as well as boot count; `mediacore_storage_checkpoint_bytes` and an
alert-rule snippet ship with S2b (mechanism: 05-storage).

### 9.2.3 Data: one adoption model per family group (cross-cutting 5)

| Family group | Model during W | Mechanism owner |
|---|---|---|
| Identity: `u:`, `role:`, `apikey:`, `invite:`, `inviteallow:`, `oauthid:` and the six auth `idx:` sub-prefixes | **Legacy rows stay authoritative.** Every native write stages the legacy primary row **and every legacy index row** the previous build resolves through (DATA-5) plus the core row in one batch; every boot **rebuilds the core copy from legacy** (no field merge, DATA-4 superseded); native-only data lives in core side records keyed by the legacy ID | [05-storage](05-storage.md) §5S.3.11 (mirror + rebuild), [05-authn](05-authn.md) §11, [05-rbac](05-rbac.md) §6.3 |
| Sessions `sess:`, `idx:sess:user:` | **No raw mirror** (SEC-19): new sessions are hashed only; a presented legacy cookie is looked up in `sess:` with epoch, status, revoked and expiry checks and upgraded on use; the fallback matches nothing after 7 days. Cost: one re-login for sessions created after S4 if the install rolls back | 05-authn §11 |
| Native invites | hash-only, not mirrored; a rollback inside an invite's TTL voids invites issued by the new build (resend) | 05-authn §11 |
| Config and secrets: `setting:config_blob` + the legacy secret rows | Teach (S0) → Expand (S3): import, and re-import keyed by value hash on every boot; mirror back to blob and legacy-format rows in the same batch; plaintext blanked only when the previous build reads the legacy-format rows → Contract (C3) | [05-config](05-config.md) §1.12.2 |
| Tasks: ops families | copy-and-mark of non-terminal runs into `c/tasks/` (one batch per run) + boot reconcile of runs an older build created; clock and pause mirrors; history read in place | [05-tasks](05-tasks.md) §5T.6 |
| Integration instances (Deluge, AI endpoints, metadata sources, SMTP, webhooks) | first-boot import into the one instance store; operator-pinned env values re-applied every boot as read-only `Origin=pinned` rows | [05-metadata](05-metadata.md) M1.4, M1.10 |
| Books families (the rest of the 258, incl. `setting:`, `preference:`, `pref:`, `act:`) | claimed in place, permanent | 05-storage §5S.9.1 (one ownership table, DATA-2) |
| `c/outbox/`, `c/audit/`, `c/log/journal/` | new core rows only, no legacy twin; the previous build ignores them (S0 tolerance) | 05-events, 05-rbac, 05-logging |

Rules that apply to every row:

1. **Never widen on rollback (SEC-1).** A native API key is mirrored with legacy atoms only when they
   re-expand exactly to its canonical scopes; otherwise its legacy row is written **disabled** with
   reason "no exact legacy scope set" and the scopes live in the side record (05-rbac §6.3). A role
   may be mirrored narrower (an empty legacy role grants nothing). A legacy empty-scope key is adopted
   as `LegacyFullScope` and mirrored back unchanged.
2. **A mirror-disabled key must survive the rebuild.** The rebuild drops a side record whose legacy
   row is gone or was revoked **by an older build**, but keeps one whose legacy row the mirror itself
   wrote disabled and the previous build has not rewritten since (05-storage §5S.3.11 `MirrorDisabled`
   + `Fresh`); otherwise every narrow native key would be revoked on the next boot. (05-rbac §6.3 and
   05-authn §11 still state the unconditional drop; cross-cluster request to align them.)
3. **Fail closed per principal, not per boot (OPS-19).** An undecodable legacy row during the rebuild
   gets no native copy (that principal cannot authenticate), health reports Degraded naming the key,
   `mediacore_storage_mirror_rows_total{outcome="undecodable"}` counts it (05-storage), and boot
   continues; one bad row never crash-loops the unit into `StartLimitBurst`.
4. A failed legacy (mirror) write fails the whole batch and the request.
5. Legacy codecs (mirror projections and read adapters) are `encoding/json` v1 permanently; v2 only
   for native rows (DATA-5).
6. A family over the atomic cap uses 05-storage's resumable path; legacy stays authoritative until it
   completes. Cross-owner atomic writes (admin user merge) follow 05-storage (DATA-18).

### 9.2.4 HTTP surfaces

The mechanism is 05-http-api §4.6 (aliases, `Gone`, headers) and §8 (`MountLegacy`). Policy:

- An **alias** serves the canonical handler under the canonical permission with the legacy wire
  shape (`WithLegacyShape`, pinned by goldens from `internal/wirecontract`), adds
  `Deprecation`/`Sunset` (advisory)/`Link: …; rel="successor-version"`, and records use on surface
  `http`. It becomes `Gone` only through a contract PR.
- ABO's gin engine is mounted **permanently** (cross-cutting 2, SIMP-2). `/api/v1/...` books routes
  are a permanent legacy prefix, not a surface with a sunset; a books route group moves to
  `/api/books/v1/...` only when it is rewritten for its own reasons, and then its old paths become
  aliases. The ABS-compatible unversioned paths and the unversioned `/api/*` → `/api/v1/*` redirect
  (`server_lifecycle.go:1790-1822`, carried by the legacy mount's `Redirects`) are permanent.
- Only **core** surfaces (auth, users, roles, keys, config, tasks, events, system, backup, update,
  health aliases) go through alias → `Gone` → delete.

### 9.2.5 Config keys and environment names

The mechanism and the full alias inventory are 05-config §1.12.4. Policy:

- **Window aliases**: the 141 `BindEnv` names, the bare upper-case form of every flat key that
  `AutomaticEnv` made settable, the `ABK_*` names, and every flat YAML/blob key.
- **Permanent aliases (OPS-5)**: ecosystem names (`OTEL_EXPORTER_OTLP_*`, `OPENAI_API_KEY`,
  `OPENAI_BASE_URL`) and **every name ABO reads today that a shipped deploy file sets**:
  `DATABASE_PATH` (unit `:98`, plist `:40` — the only DB-path source in both), `FP_PARALLEL_WORKERS`
  (unit `:108`), `BASIC_AUTH_ENABLED`/`USERNAME`/`PASSWORD` (compose `:20-22`), and the names in
  `deploy/local.conf.example` that ABO reads. `TestShippedDeployEnvNamesArePermanent` scans
  `deploy/*.service`, `deploy/*.plist`, `deploy/local.conf.example`, `docker-compose.yml` and
  `Dockerfile*`.
- **Not compat surfaces**: names a shipped file sets that nothing reads (`AUDIOBOOK_ROOT_DIR`,
  deliberately unread at `config.go:2569-2575`; `AO_DIR`, `AO_DB`; `OLLAMA_BASE_URL`). Honouring them
  would change behaviour; they are deploy-file defects that S3 fixes (§9.4 S3 "Deploy files").
- After W, a window alias of a key at `ProtectAdmin` or higher (database path, sign-in, server
  paths) is **retired**: boot refuses to start if it is set and names the canonical variable; storage
  refuses to *create* a store at the resolved path when none exists there and a retired
  database-path name is set (05-storage). Other legacy env names after W are ignored with one boot WARN.
- During W, legacy YAML keys report health Degraded `legacy_config_keys`, the Settings page shows a
  banner, and `audiobook-organizer config check` names the offending keys (OPS-27e).

### 9.2.6 Permission atoms

The normative atom → permission mapping is 05-rbac §6.1 (it drops v1.0.0's `core:events.subscribe`
and `core:tasks.run` expansions and adds `core:roles.view`/`core:system.view`). The **invariant** is
fixed here: for every route of the legacy mount (497 + 75 ABS) and every seed role, and for scoped API
keys and auth-off mode, allow/deny is identical before and after S4
(`TestLegacyRoleReachUnchanged`, built on the real-router dump technique of `raw/09` §0). Because the
mount is permanent, the test is a **permanent CI test**, not a migration test (SIMP-2 trade). Role IDs
(`admin`, `editor`, `viewer`, `fp-worker`) do not change, so `User.Roles` and `Invite.RoleID`
(`internal/database/store.go:1004-1012`) need no rewrite.

### 9.2.7 Metrics (SIMP-13)

ABO sets `app.Options.MetricPrefix = "audiobook_organizer"` (03 §3.6), so every books series keeps its
exact name, is **emitted once**, and needs no alias, recording rule or alert duplication. Only the
series whose emitter moves into a core subsystem (ops → `mediacore_tasks_*`, Pebble →
`mediacore_storage_pebble_*`, AI → `mediacore_ai_*`, two process gauges → the Go collector) are
renamed; their old names keep working through **gather-time aliases** (05-metrics §13, no double
instrumentation), each a ledger row with gate P6 instead of (c). `deploy/` dashboards and rules move to
the new names in the PR that adds the aliases. New core-only series use `mediacore_*`. OTel scope
names follow 05-metrics.

### 9.2.8 Switches: none on state shape, a few operational dials (OPS-17)

**State-shape switches: none.** Whether a store is in mirror or native phase is decided by the build
and the store's stamps, never by a runtime flag, because a flag would double the states rollback must
handle and could put a store into a shape the previous build cannot read.

**Operational dials are allowed** when they never change persisted shape; each is a declared config
key: `core.tasks.kinds.<kind>.parallelism` (cap, ≤ the declared value), `core.outbox.relay_enabled`,
per-provider throttle overrides (05-metadata M2), `core.storage.claims.mode` (`shadow`/`strict`,
05-storage), `core.updater.mode` and `core.updater.allow_unsigned_legacy` (05-updater). Existing
switches are untouched: `abs_api_enabled`, `enable_auth`. Per-entry data such as a webhook instance's
`legacy_headers` is data, not a flag.

### 9.2.9 The ledger and `compat.Ledger`

`docs/compat-surfaces.md` (the existing shim ledger, v1.1.0) gains a section per stage; each row:
surface, legacy name, canonical name, flip PR, shipped release and date, earliest removal date, gate
(`usage` | `p6` | `permanent`), removal PR. The code side is one value per App:

```go
// core/compat (03 §3.3); registered by httpapi, config, rbac, tasks, realtime, metrics and books.
package compat

type Surface string // "http", "config", "env", "rbac_atom", "task_kind", "sse", "webhook", "ui_route", "metric"

type Gate uint8
const (
	GateUsage     Gate = iota // §9.2.1 (c): 14 days without recorded use
	GateP6                    // metric aliases: operator dashboards checked (P6) instead of (c)
	GatePermanent             // never removed (§9.6)
)

type Entry struct {
	Surface   Surface
	Legacy    string // bounded alias-table key, e.g. "GET /api/v1/users/:id", "root_dir", "library.view"
	Canonical string
	Ledger    string // row ID in docs/compat-surfaces.md
	Gate      Gate
}

// Ledger is built by the kernel before listeners open; registration after that fails.
type Ledger struct{ /* unexported */ }

func (l *Ledger) Register(e Entry) (*Handle, error) // ErrDuplicate, ErrUnknownSurface
func (l *Ledger) Usage(ctx context.Context) iter.Seq2[Usage, error] // what GET /api/core/v1/compat/usage serves

// Use records one legacy use: increments mediacore_compat_legacy_use_total{surface,name,entry} and the
// persisted {count, last_used_at}, flushed in batches (≤ 1 write per surface/name per minute). It never
// fails a request (§9.8).
func (h *Handle) Use(entry string)

type Usage struct {
	Surface    Surface
	Legacy     string
	Count      uint64
	LastUsedAt time.Time
}
```

`TestCompatLedgerMatchesCode` (root module) compares the ledger file with every `Entry` registered at
boot; `TestNoContractBeforeSunset` fails a build whose code removes an entry before its earliest
removal date; its runtime twin (P8) reads `LastUsedAt`.

## 9.3 Stage overview

| Stage | Name | Build `core` level | Native truth introduced | Contract (≥ W, §9.5) |
|---|---|---|---|---|
| S0 | Groundwork, guards, build paths | 1 | — (served ledger only) | — |
| S1 | Core module skeleton, leaf packages; staged restore | 1 | — | — |
| S2 | Composition root; perimeter; `books` wraps the server | 1 | — | — |
| S2b | Core opens the store; books on claims (shadow → strict) | 1 | — | — |
| UI-0 | UI groundwork: react-router 8, generated-client runtime | 1 | — | — |
| S3 | Config and secrets (05-config phase X) | 2 | `c/config/`, `c/secrets/` | C3 → required 2 |
| S4 | Identity: auth engine, RBAC, audit, first native core routes | 3 | `c/auth/`, `c/rbac/`, `c/audit/` | C4 → required 3 |
| S5 | Tasks | 4 | `c/tasks/`; task kinds `books.*` | C5 → required 4 |
| S6 | Events, outbox, realtime, webhooks | 4 | `c/outbox/`; native SSE | C6 (aliases only) |
| S7 | Integrations, library, search index | 4 | instance store `c/integrations/` | C7 (aliases only) |
| S8 | Backup, updater, health, metrics registry | 5 | backup format (05-backup); core metric names | C8 → store `core` 5 |
| S9 | UI shell | 5 | `/books/...` SPA routes; shell build | C9 (UI redirects) |
| S10 | Final cleanup (was S11) | 5 | — | — |

Order: S0 → S1 → S2 → S2b → S3 → S4 → S5 are strictly ordered (R4 layers and data dependencies);
UI-0 runs in parallel with S1–S3 but must ship before S4; S6, S7, S8 may overlap only if they touch
disjoint package groups (CLAUDE.md "never launch overlapping waves"); S9 needs UI-0 and S4–S6 routes.
A stage ships as one or more production releases; its flip PR is in exactly one (§9.2.1).

**Boot-time budget at every stage (OPS-22).** Each stage's "Done when" includes: `deploy-wait-ready`
reports ready within the **S1 baseline + 10 %** on a production-size restored copy, and
`mediacore_kernel_boot_phase_seconds{phase}` attributes the time per phase (emitted from S2). S0 turns
the hard-coded 300 s into a `DEPLOY_READY_TIMEOUT` variable in `Makefile` (`rollback`) and
`Makefile.local.example` (`deploy`), default 300.

**Rollback rehearsal at every stage (OPS-20, OPS-21).** Each stage's "Done when" includes the R-test
(§9.10) with `prev` = the **last production-deployed tag**, including the previous release's UI
bundle and an external ABS client wherever the stage changes their request path (S2, S4, S6, S9).

## 9.4 Stages

### S0 — Groundwork, guards and build paths

| Aspect | Specification |
|---|---|
| Goal | Make every later stage buildable, reversible and measurable, with no user-visible behaviour change. |
| Moves | 04 §8.1 row S0: `core/` with `go.mod` (03 §3.2: `exclude` of the monolithic genproto **only in `core/go.mod`**, `ignore ./web`, `ignore ./examples/hello/web`), `archtest` engine; root `go.mod` `require` + `replace`; `GO_MODULE_DIRS := . core`; `internal/arch/layering_test.go` redesigned per 03 §3.5.1 (ratio floor, `. → cmd` positive control, classifier control, layer −1 for mediacore). |
| Docker (MOD-1, OPS-1, cross-cutting 9) | Both Dockerfiles: `COPY go.mod go.sum ./` **and** `COPY core/go.mod core/go.sum ./core/` before `go mod download && go mod verify`, then `COPY . .` as today. `.dockerignore` adds `**/node_modules`, `core/web/packages/*/dist`, `core/examples/*/web/node_modules`. A **PR CI job** builds the image up to the `go-builder` stage (and `Dockerfile.build-cgo` with `--output type=local`) whenever `go.mod`, `core/go.mod`, `Dockerfile*`, `.dockerignore`, `web/package*.json` or `core/web/**` change. |
| Web build entry points (MOD-9, FE-13) | One script, `scripts/ci/build_web.sh` (build `core/web` when present, then `npm ci` + build in `web/`), behind `make web-deps`; every `npm ci` site of §9.1 calls it (both Dockerfiles' frontend stage copies every workspace member's `package.json` first); `frontend-ci.yml` and `e2e.yml` path filters gain `core/web/**`, `core/go.mod`, `core/go.sum`; Dependabot npm `directories` gain `/core/web`, Dependabot gomod `directories: ["/", "/core"]` in one group (MOD-11); every `cache-dependency-path` lists `core/web/package-lock.json` once it exists. CLAUDE.md's worktree recipe becomes `make web-deps`. |
| CI classification (MOD-8) | The table below; per-module baseline files; a per-stage checklist (shard weight files `short_test_weights.json`, `fixture_weights.json`, `go_test_timings.json`; `.mockery.yaml`; `tygo.yaml`; `ci/gates.txt` rows) run in every PR that moves a package. |
| Data/keys | Register reserved families `c/` (core) and `m/` (modules) in `keyfamilies.go`. Ship the per-owner guard (§9.2.2) in `internal/database` (S2b moves it into core `storage`); the build serves `core` level 1. Each boot writes the served-ledger entry (one key under `c/`, which HEAD-era builds ignore, proven by `TestLegacyToleratesNamespacedKeys`). **Teach**: 05-config §1.12.2 phase T — ABO's legacy loader overlays `IsSecret` rows `setting:<legacy-name>` for every secret field (inert until S3 writes them). No op-ID teach: under 05-tasks' copy-and-mark model the previous build never sees a `books.*` kind (OPS-7). |
| Endpoints | `/api/*` redirect excludes `^/api/(core\|[a-z][a-z0-9]{1,15})/v[0-9]+(/\|$)` (`server_lifecycle.go:1798`); `TestNativePrefixesNotRedirected`; `TestCollidingNamespacesStillRedirect` unchanged. Every response carries `X-App-Build: <build id>` (OPS-20 teach). |
| UI | A small SPA check compares `X-App-Build` of API responses with the bundle's own build id and offers a reload on mismatch, so tabs left open across S4–S9 deploys and rollbacks recover (OPS-20). |
| Updater hotfix (OPS-6) | The HEAD updater's restart exits **75**, not 0 (`updater.go:331-334`), so `Restart=on-failure` restarts it; it keeps the previous binary as `<exe>.prev` instead of deleting `.old` (`:278`), so the updater and `make rollback` share one slot (05-updater). |
| Deploy | `DEPLOY_READY_TIMEOUT` (§9.3); owner action: the sudoers line for `<db>.storage-levels` (§9.2.2). |
| Rollback | Binary swap; the only key written is the served ledger under `c/`, ignored by older builds. |
| Done when | `audiobook-organizer --print-storage-levels` prints `core 1`; `docker build .` and `docker buildx build -f Dockerfile.build-cgo --output type=local,dest=<tmp> .` exit 0 in the new PR job; `make ci` exits 0 with the classification below; `scripts/ci/check_genproto_exclude.py` and the throwaway `go work init . ./core && go build ./... ./core/...` step pass; `TestLegacyToleratesNamespacedKeys` (seeds `c/x`, `m/y` into a HEAD store and runs census, retention sweeps, backup, restore, boot) passes; `scripts/test_storage_format_guard.py` covers the compat cases; `curl -s -o /dev/null -w '%{http_code}' <abo-url>/api/core/v1/ping` returns 404, not 301; a scripted auto-update on a staging unit ends `active (running)` (exit 75 restarted). |

`make ci` prerequisites (`Makefile:785`) and CI jobs after S0 (MOD-8):

| Target / job | Runs in | Notes |
|---|---|---|
| `staticcheck`, `fmt-check`, `test-all-short`, `go vet`/`go build` | per module (`GO_MODULE_DIRS`) | `fmt-check` excludes `web/`, `core/web/` and `core/examples/*/web/` |
| `lint-errcheck-ratchet`, `lint-deadcode-ratchet`, interface-width job | per module, **separate baselines** (`core/.errcheck-baseline`, `core/scripts/deadcode-baseline.txt`, `core/.interface-width-baseline`) | one integer over root `./...` today; a move must not silently re-baseline |
| `coverage-check-short` | per module: root floor `.ci/coverage-floor.txt`; core gate added at S2b (12 S5 thresholds) | |
| `mocks-check` | per module: root `.mockery.yaml`; `core/.mockery.yaml` from the first core interface with a mock | |
| `pprof-check` | per module from S1 (`internal/diag/flight` tests move to `mediacore/health`) | |
| `oplint`, `sdkguard`, `ops-check`, `types-check`, `bench-check` | root only (`tools/cmd/oplint`, `tygo.yaml:28`) until their subject moves (oplint → `archtest/tasklint` at S5; sdkguard deleted with `pkg/plugin/sdk`) | |
| `go-test-short-shards` (`scripts/ci/short_test_shards.py`) and Woodpecker `test-rest`/`test-native-taglib` | root module, plus one core shard | the weight files are updated in the PR that moves a package (stale entries fail, `short_test_shards.py:257-258`) |
| `ci/gates.txt` `store-width`, `store-references` | root, until the S2b+ split deletes `database.Store`; retired in that PR with the reason recorded | `scripts/tests/test_run_gates.py` validates every go-suite gate |
| core layering gate, `TestCoreImportsOnlyCore`, genproto check | core only, registered in `ci/gates.txt` with `core-job` | |

### S1 — Core module skeleton, leaf packages, staged restore

| Aspect | Specification |
|---|---|
| Goal | Prove the nested module with real code; move dependency leaves that have no data or API surface; fix the one defect every later rollback argument relies on. |
| Moves | **04 §8.1 row S1 and §8.2 S1 rows** (authoritative): `internal/logging` → `mediacore/log`; `internal/logger`'s `sanitize.go`/`attrs.go`/`retention.go` → `log` (the façade stays, SIMP-3); `internal/telemetry` → `mediacore/metrics` with namespace and scope as constructor options (its `ratchet_test.go` and `internal/telemetry/contract` stay in ABO as `internal/telemetrypolicy`, MOD-6); `internal/lifecycle` → public `mediacore/lifecycle`; `internal/security/{safepath,pathvalidation}`, `internal/libroot`, `internal/boundedcall`, `internal/diskstats`, `internal/util` `path.go`/`perms.go` → `mediacore/library`; `internal/diag/flight` → `mediacore/health`; `internal/logger/logtest` → `coretest`. **Stay in ABO** (04 §2.3): `internal/keyedmutex`, `internal/errhandling`, `internal/flight`. `internal/sdnotify` and `internal/startup` do **not** move here (MOD-2: their ABO importers are rewired at S2). |
| ABO-local fixes | 05-config stage 0: the 8 bypassing `config.AppConfig` writes go through `Mutate`, `TestNoDirectAppConfigWrites`. **Staged restore** (OPS-15): restore stops extracting over the live store (`backup.go:777-870`) and stages the archive for the next boot, per 05-backup §5B.3.4, implemented in `internal/backup` before it moves at S8; `make rollback` and the updater refuse while a staged restore is pending. |
| Data/keys, config, endpoints, permissions, UI | None changed. Metric and scope names byte-identical (namespace injected). |
| Rollback | Binary swap; no state. Older builds ignore a pending staged restore, so `make rollback` refuses while one is pending instead of leaving it to apply by surprise later. |
| Done when | `archtest.TestCoreImportsOnlyCore` (`go list -deps -test`) green; the sorted series-name set of `curl -s <abo-url>/metrics` equals the S0 build's (diff empty); `go build ./... && (cd core && go build ./...)` exit 0 with **the pinned toolchain** (13 owns bumps; OPS-24); restore rehearsal on a copy: stage, boot, store equals the archive. |

### S2 — Composition root; core perimeter; `books` wraps the server

| Aspect | Specification |
|---|---|
| Goal | ABO's `main` becomes `app.New(ctx, opts, books.Module())`. Core owns process lifecycle, listeners, the typed services struct and the **perimeter**; ABO's whole gin engine is the books module's legacy mount, permanently. **Authentication and authorization do not change** (OPS-11, SEC-13). |
| Moves | 04 §8.1 row S2: `internal/serviceregistry` registrations → `module.Registrar` calls inside `books.Module()` (N4; the package itself is deleted at S10); listener code (`server_lifecycle.go:903-1078`: TLS, HTTP/2, HTTP/3, port-80 redirect, peer allow-list) → `core/httpapi`; `internal/startup` → `kernel` + `health` (MOD-2 gate S2); `internal/sdnotify` → `mediacore/internal/sdnotify` behind `lifecycle` readiness and start-timeout hooks (gate S2); `internal/config` loader files → `mediacore/config`; `cmd/root.go` bootstrap → `mediacore/app`. `main.go` stops calling the process-global setters `server.SetVersion`/`SetEmbeddedFS` (`main.go:32-34`). |
| HTTP | `books` calls `httpapi.MountLegacy` with `Auth: LegacyAuthGin`, `ServesSPA: true` (FE-1: `ui.Handler` is **not** mounted; gin's `NoRoute` keeps serving the SPA at `/`; `app.Options.UI` is the zero bundle) and the route manifest (every gin route its own mux pattern; the 36 permission-less authenticated routes annotated `AnyAuthenticated`, R6). The perimeter moves to core and is removed from gin **in the same PR**: logger, recovery, security headers with the mount's `DefaultCSP`, CORS, Basic auth with today's per-route exemption, gzip, tracing, the `/api` rate limiter with per-route `RateClass` (05-http-api §8.1). **New**: cross-origin protection for cookie-authenticated unsafe requests (SEC-10); bearer, API-key and Basic requests are unaffected. Gin's `RequireAuth`, permission checks and credential guards stay unchanged inside gin. |
| Lifecycle (OPS-13) | Stop order of 06 §6.5.2 with one budget `core.shutdown.budget` = 25 s (05-health §5H.4). Behaviour change listed: HTTP drain 30 s → `core.shutdown.http_drain` 10 s (`server_lifecycle.go:582`). Supervisor stop timeouts ship in this stage: `TimeoutStopSec=30` in the committed unit, `stop_grace_period: 30s` in `docker-compose.yml`, `ExitTimeOut` 30 in the plist, documented `docker run --stop-timeout 30`. |
| Logging | 05-logging stage 1 (`app.InstallProcessDefaults`) and stage 2 (books shim over core handles). |
| Data/keys | None. Books migrations 1..66 keep their bookkeeping (`db_version`, `migration_<n>`) as the books legacy track; the store is still opened by books. |
| Config | Core config loader reads the **same** sources (YAML, env, `config_blob`) through a books-provided legacy source; canonical `core.*`/`modules.books.*` keys are accepted but undocumented. Flags `--port --host --tls-cert --tls-key --http3-port --external-url --read-timeout --write-timeout --idle-timeout --workers` (`cmd/root.go:526-535`) feed `core.http.*`/`core.tasks.workers`. |
| Rollback | Binary swap; no state written. The previous build serves the same paths; the previous UI bundle is served by whichever build runs. |
| Done when | `main.go` contains no `server.NewServer`; `grep -rn 'serviceregistry.Register' --include=*.go internal \| grep -v _test` lists none of the services 04 moves; `coretest` boots core with zero modules (G7); **perimeter tests** (05-http-api §8.1): `TestBasicGate_ExemptSetEqualsHEAD`, CSP header goldens per owner, the rate-limit class golden, the cross-origin test; **wire goldens**: the 20 sampled routes (one per top-level group, `raw/09` §1.1) match `internal/wirecontract` goldens with volatile fields (timestamps, uptime, versions, request IDs) normalised — not byte equality across builds (OPS-24); **ABS** (OPS-21): `internal/syncapi/conformance` fixtures plus, when P7 is branch B, a live login and progress sync with a real ABS client on cur, after `make rollback` to prev, and after roll-forward; `systemctl stop` during a running op stamps it interrupted within the budget with no SIGKILL in the journal; boot within S1 + 10 %; R-test green. |

### S2b — Core opens the store; books on claims

| Aspect | Specification |
|---|---|
| Goal | Core `storage` opens the one Pebble store; books reaches its families only through declared claims (R8); the guard, census, checkpoint and compat bookkeeping are core's (OPS-12 item 1). |
| Moves | 04 §8.1 row S2b: `internal/database` storage files → `mediacore/storage` + `internal/pebble` (`keyfamilies.go` stays ABO as the claim generator's input); `internal/compactprogress` → `storage`; Pebble collector → `internal/pebble` (gate S2b: ABO's Open Library store opens through `storage.OpenAuxiliary`); `internal/database/dbtest` `newstore.go` → `coretest`; `compat` lands (usage persistence, served ledger). The residue becomes `internal/books/store` on `storage.LegacyRange`. |
| Releases | **Release 1 — shadow** (05-storage §5S.9.4): claims enforced in log-only mode, `core.storage.claims.mode=shadow`; every violation is fixed by registering or correcting the writer. **Release 2 — strict**, after release 1 served production with zero violations for 14 days. The flip from shadow to strict is the stage's flip PR. |
| Data/keys | No family moves, nothing is copied. New core keys only under `c/storage/` (level stamps, served ledger, core migration bookkeeping `c/storage/ks/<owner>/`) and `c/compat/` (usage records). Books migrations 1..66 keep `db_version`/`migration_<n>` through 05-storage's version-source adapter. |
| Rollback | Binary swap (no data shape change; `c/` keys ignored). The dial `core.storage.claims.mode=shadow` undoes strict mode without a rollback. |
| Done when | The persisted claim-violation record of 05-storage lists no writer for ≥ 14 days of production on release 1 (not a process-local counter, OPS-16); `TestBooksClaimsGenerated` (claim list = `keyfamilies.go`); free-space check before checkpoints and the checkpoint metric present (OPS-25); boot within S1 + 10 %; R-test green. |

### UI-0 — UI groundwork (before S4; FE-2, FE-12)

| Aspect | Specification |
|---|---|
| Goal | Remove the two UI ordering hazards before any UI call moves: the shell needs react-router 8, and S4–S6 must call native routes through the generated client, not hand-written code rewritten twice. |
| Moves | 07 §7.19 steps 1–3: react-router-dom 7 → react-router 8 in ABO with the declarative `<BrowserRouter>` unchanged (import swap in 52 source + 70 test files, `vite.config.ts:81-82` chunk regex); `core/web` workspace with `@mediacore/ui-kit`, `@mediacore/api-client` runtime and generator, `make generate-api-client`, consumed by ABO via `file:`; `internal/openapi` (04 §8.1 row UI-0). The JSON field naming of native routes is the one rule of 05-http-api §4.3 (FE-12a). |
| Data, config, endpoints, permissions | None. |
| Rollback | Binary swap (the UI is embedded per build). |
| Done when | `grep -rl "react-router-dom" web/src` is empty; vitest and Playwright e2e green; the generator produces a typed client for a fixture route set in `core/web` CI; every build entry point of S0 builds `core/web` first (the S0 PR job green with `core/web` present). |

### S3 — Config and secrets

| Aspect | Specification |
|---|---|
| Goal | `core/config` and `core/secrets` become truth; every secret is encrypted at rest without making a rollback lose credentials (N10, G12, OPS-9). |
| Mechanism | **05-config §1.12.2 phase X**, the single config/secrets migration (SIMP-15, SEC-20): one keyring (legacy key imported as `k0-legacy`), import and hash-keyed re-import on every boot, mirror back to `config_blob` and to legacy-format secret rows in the same batch. 09 schedules it; it does not restate it. The Teach phase T shipped in S0, so it has served production before X under normal deploys. |
| Plaintext (14 Q-E, OPS-9, rollback-safe) | The rendered blob carries plaintext secret fields **empty** only while the build that booted the store before the current one is level ≥ 1 (served ledger), i.e. the build `make rollback` returns to reads the legacy-format rows; otherwise (an install that skipped S0) plaintext is kept and health raises `secrets_plaintext_mirror`. Rotation is refused while the mirror is active. |
| Moves | 04 §8.1 row S3: `settings.go` encryption → `mediacore/secrets` (the `setting:` row methods stay books, cross-cutting 7); config persistence (`c/config/`). 05-config stage 1: ABO's `internal/config` becomes the `legacyconfig.Facade` over core handles; viper leaves the boot path, replaced by core `Load` + aliases, with every implicit `AutomaticEnv` name registered as an alias, so nothing is dropped silently. |
| Config compatibility | All flat keys remain valid in YAML, env and blob (§9.2.5). Canonical keys documented. Env canonical form per 05-config §1.3.6. |
| Deploy files | The shipped unit, plist, compose file and `local.conf.example` switch to canonical env names **in addition to** the permanent legacy names they set (the legacy names stay valid forever); the four unread names (`AUDIOBOOK_ROOT_DIR`, `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL`) are deleted from them or, for `OLLAMA_BASE_URL`, replaced by the name ABO reads (owner decides); P4 scans the plist and compose files too. |
| Endpoints | `GET/PUT /api/v1/config` unchanged (masks per 05-config §2.10). |
| Rollback | 05-config §1.12.2 rollback matrix: to S2b the blob and the 4 legacy rows are current (mirror); secrets entered on S3 are readable by S2b through the T overlay; to a pre-S0 `.prev` plaintext was never blanked. Roll-forward re-imports what the previous build changed (DATA-6). |
| Done when | P1 census: `c/secrets/` count = 4 legacy rows + configured plaintext secrets; `TestEveryLegacyConfigKeyHasCanonical`; `TestShippedDeployEnvNamesArePermanent`; R-test cells X→T, X→pre-T, T→X (05-config matrix) green; on production, health shows no `secrets_plaintext_mirror` after the second S3 boot; boot within S1 + 10 %. |

### S4 — Identity: auth engine, RBAC, audit

| Aspect | Specification |
|---|---|
| Goal | One core identity resolver and authorizer for every surface (web, API keys, CF Access, OAuth, ABS); permissions namespaced; admin is `core:admin`; audit trail (G13). **The one stage in which the auth engine changes** (cross-cutting 2). |
| Moves | 04 §8.1 row S4: `internal/auth` → `rbac` + `auth`; `internal/oauth` → `mediacore/auth/oidc`; the gin auth middleware is replaced by the read-only `legacyauth` bridge (`LegacyAuthCore`: core stage 12 binds the principal, stage 13 enforces each manifest route's permission); `internal/database` auth files → `auth` (types, capability interfaces, errors) and `internal/authstore` (gate S4); auth/user/API-key/invite handlers → `internal/authhttp` (gate S4). **Stay in ABO** (G11, G15): owner proof and CF Access glue (`internal/cfaccess`), `internal/cfaccesspolicy`, `internal/cfservicetoken`, guest tokens, ABS JWT/sessions (compat surface with per-route `OwnAuth`), `internal/usermerge`. |
| Data/keys | §9.2.3 identity model: adoption into `c/auth/`, `c/rbac/` as migration `core.auth#1` after books track ≥ 66, in a pre-migration checkpoint; legacy authoritative, rebuild every boot, side records, never-widen keys, no raw session mirror. New `c/audit/` (append-only, no legacy twin). User IDs unchanged, so books rows keyed by user are untouched. |
| Config | `oauth_*`, `cf_access_*`, `basic_auth_*`, `enable_auth`, `owner_email` aliases → `core.auth.*` (owner/CF keys → `modules.books.identity.*`). |
| Endpoints | Native `/api/core/v1/auth/*`, `/users/*`, `/roles/*`, `/api-keys/*`, `/audit`, `/rbac/permissions`, `/config/*`, served by `internal/<capability>http` packages imported only by `kernel` (03 §3.3). Aliases: `/api/v1/auth/*` (22), `/api/v1/users/*` (15), `/api/v1/me`, `/api/v1/setup`, `/api/v1/config` (2), `/api/v1/preferences` (3); the gin routes they replace are deleted in the same PR. `/auth/temp-login` and `/invite/:token` links: aliases with W ≥ max invite TTL (P5). |
| Permissions | 05-rbac §6.1 mapping live; legacy atoms accepted on input for W (surface `rbac_atom`); output canonical, plus `legacy_permissions` on the `/api/v1/auth/me` alias during W. `RequireAdmin` role-name checks (`middleware/auth.go:522-537`, `handlers/user.go:285,441,453`, `wire_master_list_routes.go:41`) → permission checks per 05-rbac §6.2. |
| UI (FE-11, FE-12) | Auth, users and config calls switch to generated client operations in the **same** build. The 5 hard-coded permission constants and the `APIKeysTab` scope list are replaced by canonical permissions read from `GET /api/core/v1/rbac/permissions` (titles from 05-rbac); a vitest fails on any `'<word>.<word>'` permission literal outside generated files. The Users page gains role management. |
| Rollback | To S3: `make rollback` passes (required level unchanged). S3 reads `u:`, `role:`, `apikey:` — current through the mirror, including keys created on S4 (exact legacy atoms, or disabled when none exist: never wider). Sessions created on S4 do not exist for S3: one re-login. Invites issued on S4 are void on S3 (resend). Custom roles created on S4 appear with reverse-mapped (possibly narrower) atoms. Roll-forward: the rebuild honours every change S3 made (deactivation, revocation, password reset). |
| Done when | `TestLegacyRoleReachUnchanged` green over 572 routes × 4 seed roles + scoped keys + auth-off, wired as a permanent CI gate; `TestMirror_NarrowKeyNeverBecomesEmptyScope` (property test, 05-rbac) and `TestRebuildKeepsMirrorDisabledKey` (§9.2.3 rule 2); `TestNoRoleNameAuthz` (AST: no comparison of a role ID or name inside authz paths; replaces a grep, OPS-24); P1 census: `c/auth/user/` count = `u:` count; `GET /api/core/v1/audit?kind=auth.login` shows the rehearsal login; R-test incl. "deactivate on prev, roll forward, login refused" and "create a scrape-only key on cur, roll back, key refused on prev"; previous-release UI against cur and cur UI against prev (R-test cells a, b); ABS conformance + live client (P7 branch B) before and after rollback; boot within S1 + 10 %. |

### S5 — Tasks

| Aspect | Specification |
|---|---|
| Goal | One task system (N9): v3 authoring on the v2 engine becomes `core/tasks`; required parallelism (N8, R7); `/api/core/v1/tasks` is the one API. |
| Mechanism | **05-tasks §5T.6 and §5T.8** (OPS-7: 05-tasks' model): copy-and-mark of non-terminal runs into `c/tasks/` (one batch per run), boot reconcile of runs an older build created, clock and pause mirrors to the legacy keys, terminal history read in place by `legacyhist`; the D25 opv2↔opv3 mirror ends at the adoption. |
| Moves and batches | 04 §8.1 row S5 and §8.3 gates: engine → `internal/executor` (T0), the `internal/operations/registry` path stays as the `opsbridge` v2 adapter until T8; `internal/scheduler` moves at T7; `opsv3` and the scheduler-admin handler → `internal/taskhttp`; `pkg/ops` → `tasks`. 05-tasks batches, as releases: **T0+T1 in one release (the flip)**; T2–T7 in later releases of the stage, mechanically similar, suited to `/parallel-sweep` partitioned by package; **T8 ships only with or after C5** (§9.4.1). |
| Data/keys | §9.2.3 tasks row. The first adopting boot records the S5 adopt step's applied record. ABO's opv2/opv3 store files stay in the books residue as legacy writers until T8 deletes them (04 §4.5). |
| Config | `scheduled.*` (~40), `maintenance.*` → `ScheduleFromConfig` keys (05-tasks §5T.3.8, the one schedule mechanism, SIMP-6) with window aliases; worker count → `core.tasks.workers`. Dial: `core.tasks.kinds.<kind>.parallelism`. |
| Endpoints | Native `/api/core/v1/tasks/{defs,runs,runs/{id},runs/{id}/cancel,approve,timeline,counts,events}`. Aliases (W): `/api/v3/ops/*` (12), `/api/v1/operations/*` (38), `/api/v1/op-defs` (2), `/api/v1/tasks` (3); legacy status vocabulary kept on aliased responses via `WithLegacyShape` (`web/src/generated/ops.ts`). |
| Permissions | Per-definition `Permissions.Start` (05-tasks §5T.3.7); the implicit `settings.manage` default (`operations_v2.go:781-800`) becomes explicit on every definition that had none. |
| UI | Activity/operations pages and `useOperationsStore` switch to generated task-client operations in the same build. |
| Rollback | To S4: runs adopted by S5 are marked `interrupted_dropped` + `migrated_to` in legacy (no double execution); runs S4 creates are adopted on the next roll-forward; clocks are mirrored (no stampede, no double fire); runs created by S5 are invisible to S4 and resume under their `ResumePolicy` after roll-forward (05-tasks §5T.6 "four rules"). |
| Done when | `opscatalog`'s 233 defs run through `core/tasks` with declared `Parallelism`; the G5 rollback test of 05-tasks §5T.9 and the resume rehearsal (kill -9 mid `books.acoustid.backfill`, restart, checkpoint resumes) green; P11 run before the flip and its over-cap list empty or handled; `TestOpStateWriters` ratchet at its target for each finished batch; persisted usage shows no UI-originated enqueue on `task_kind`; boot within S1 + 10 % with `core.tasks.adopt-active` timed by `mediacore_kernel_boot_phase_seconds`; R-test green. |

### S6 — Events, outbox, realtime, webhooks

| Aspect | Specification |
|---|---|
| Goal | One notify-only bus with one topic catalog (SIMP-5), one durable outbox, one resumable permission-filtered SSE hub (G4); webhooks through notify. |
| Moves | 04 §8.1 row S6: `internal/realtime` → `realtime` (+`internal/sse`); `internal/operations/registry/bus.go` and `internal/plugin/events.go` → `events` with topics such as `books.book.imported.v1` and `books.read-status.changed.v1` (12 legacy event types mapped one-to-one in 05-notify §10); `internal/plugins/webhook` → `integrations/notify` webhook adapter. The tag write-back outbox `pref:_system:outbox:writeback:` stays a books claim. |
| Data/keys | New `c/outbox/` (core-owned, no legacy twin). Each configured webhook URL becomes one `notify/webhook-<n>` instance in the instance store with `legacy_headers` on (keeps `X-Audiobook-Event`/`X-Audiobook-Signature-256`), plus a global `Rule` for the configured event list mapped to `books.*` topics; its secret moves into `secrets` (05-notify §10). |
| Endpoints | Native `/api/core/v1/events` (SSE with `id:`, `Last-Event-ID`, caps). Legacy streams `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events` are served by **core projections** of the same hub with their legacy framing (05-realtime), counted on surface `sse`. |
| UI (FE-10) | **No change.** The SPA keeps using the legacy streams through the projections; the shell's realtime client replaces them at S9. |
| Switches | Dial `core.outbox.relay_enabled`. |
| Rollback | To S5: outbox rows left unprocessed; nothing correctness-critical is carried only by the outbox before C6 (the stage ships no such path); webhook instances are ignored by S5, which still reads `plugins.webhook.*` from the blob (kept by the 05-config mirror). |
| Done when | `grep -rln 'text/event-stream' --include=*.go internal core \| grep -v _test` lists only the core hub and its projections; webhook golden (legacy headers and body) byte-equal to S5 for a `legacy_headers` instance; legacy-stream goldens per event type; previous-release UI against cur and cur UI against prev stream events (R-test cells a, b); boot within S1 + 10 %. |

### S7 — Integrations, library, search index

| Aspect | Specification |
|---|---|
| Goal | Capability contracts in core with ABO's adapters behind them; one integration-instance store (SIMP-4); per-instance HTTP resilience (R5); the search index behind `storage/index`. |
| Moves | 04 §8.1 row S7: `internal/metadata/{providerhttp,dailyquota}`, `internal/security/safehttp` → `integrations/providerhttp` (instance-scoped); `internal/ai/{aijobs,resultjournal}`, generic `internal/aidispatch` → `integrations/ai`; Deluge client → `integrations/download/deluge` + books glue; `internal/mail` → `integrations/notify/mail`; generic `internal/fileops`, `internal/watcher`, `internal/pathutil` parts → `library`; search engine lifecycle → `internal/bleveindex`, dirty set and reconciler → `storage/index`. Metadata sources stay books, implementing `metadata.Provider[Q,R]`. |
| Data/keys | First-boot import of Deluge (`download/deluge`), AI endpoints (`ai/<id>`), metadata sources and SMTP (`notify/email`) into the instance store, `Local=true` for loopback/private hosts (05-metadata M1.10); secrets under `integrations/<kind>/<id>/<field>`. Env-bound values ABO treats as authoritative today (Deluge, OpenAI) are re-applied every boot as read-only `Origin=pinned` rows (05-metadata M1.4). `provider_throttle:`, `provider_daily_budget:`, `aijob*`, `aijournal:` stay books claims read through adapters until their owning spec adopts them. The search index format is unchanged (or rebuilt; it is derived data). |
| Config | `deluge_*`, `download_client`, `openai_*`, `ai_endpoints*`, `smtp_*`, `metadata_sources` → instance records (window aliases for the flat keys; the 05-config mirror keeps the blob current for rollback); `deluge_move_enabled` etc. → `modules.books.download.*`. `OPENAI_API_KEY`/`OPENAI_BASE_URL` stay permanent env aliases (§9.2.5). |
| Endpoints | Native `/api/core/v1/integrations/{kind}/...` and `POST /api/core/v1/integrations/{kind}/test`; aliases (W): generic parts of `/api/v1/ai` (14), `/api/v1/deluge` (6), `/api/v1/metadata-sources`. Books Deluge behaviour routes stay on the legacy mount unless 05-download rewrites them (§9.2.4). |
| Rollback | To S6: S6 reads Deluge/AI/SMTP settings from the blob, which the mirror keeps current; instance rows are ignored. Binary swap otherwise. |
| Done when | `plugin.DownloadClient` (0 implementors, `raw/11` §4) deleted; `download.Client` has 1 production implementor (Deluge) passing `coretest` conformance; no package-level throttle/quota map (`archtest.TestNoGlobals` on moved code); **deferred-build packages do not exist** (SIMP-1): `test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e core/acquire`, and `integrations/download` contains no grab, monitor or pending-recovery code; search results for the fixture query set equal S6's; boot within S1 + 10 %; R-test green. |

### S8 — Backup, updater, health, metrics registry

| Aspect | Specification |
|---|---|
| Goal | Platform operations owned by core; per-App metrics registry. |
| Preconditions (OPS-23) | A signed release (`release-manifest.json.sig`) is already published and its public key is compiled into the S8 build; installs migrated from `auto_update.enabled=true` get `core.updater.allow_unsigned_legacy=true` until their first signed apply (05-updater); `no_trusted_keys` and `unsigned_updates` are Degraded with a UI banner. |
| Moves | 04 §8.1 row S8: `internal/backup` → `backup`; `internal/updater` → `updater` (release source from `app.Options`, not the slug `updater.go:72`); host parts of `internal/sysinfo`, flight-recorder wiring, trace download → `health`; `internal/metrics` registrations onto the App registry. |
| Backup (cross-cutting 8) | Archive format, encryption policy, retention floor, staged restore, provenance check and catalogue location are **05-backup's** (one format, DATA-21, OPS-14). Policy for the window: until the last window a pre-core build can roll back into has closed, the default archive is restorable by the previous build; encryption is opt-in on upgrade; retention never deletes the newest verified archive the previous build can restore until an encrypted one is verified and the identity export is confirmed (DATA-22). Encryption cannot activate before C8 raises the store's `core` level above the legacy restore bound (4, §9.2.2). A staged restore records the staging build and an expiry; a stale or foreign intent is rejected (moved aside, Degraded); `make rollback` and the updater refuse while one is pending; applying a restore rewrites both sidecars from the restored stamps. |
| Updater (OPS-6) | Automatic rollback in `Boot` runs `ReaderCheck(<exe>.prev)` (the same guard decision as `make rollback`); if refused, no rollback: `rollback_blocked`, Degraded, boot continues. One slot, `.prev`. |
| Health | `/health` with its `ready` field is **permanent** (`deploy-wait-ready.sh`, Docker HEALTHCHECK; 05-health OPS-27b); core adds `/livez`, `/readyz`. |
| Metrics | Core-subsystem renames with gather-time aliases (§9.2.7); books names unchanged; `mediacore_build_info{version}` with the supported level vector; `deploy/prometheus` gains alert snippets for store level > supported, undecodable mirror rows > 0, mirror-write failures > 0 (OPS-27d). |
| Config | `backup_*` → `core.backup.*`; `auto_update*` → `core.updater.*`. |
| Endpoints | Native `/api/core/v1/{backup,update,system,health}`; aliases (W): `/api/v1/backup` (4), `/api/v1/update` (3), `/api/v1/system` (7), `/api/health`, `/api/v1/health`. Permanent: `/health`, `/metrics`. |
| Rollback | Binary swap; archives written during the window are restorable by the previous build (05-backup); a restore of a post-contract archive onto an older build is refused by the guard at next boot. |
| Done when | Every books series name in `curl -s <abo-url>/metrics` equals S7's and every aliased core family answers under both names; `promtool test rules deploy/prometheus/alert-rules.test.yml` green; restore matrix (archive from S0 × S8 build, archive from S8 × S7 build, staged intent across a rollback) green; updater R-test cell d (failed boot → `Boot` rolls back, or `rollback_blocked` past a contract) green; a staging install with `auto_update.enabled=true` receives the next signed release; boot within S1 + 10 %. |

### S9 — UI shell

| Aspect | Specification |
|---|---|
| Goal | `@mediacore/shell` + books UI contribution (D6, N6). |
| Preconditions | UI-0 shipped (react-router 8, generated client); S4–S6 native routes. |
| Moves | 07 §7.19 steps 4–6: `renderWithProviders` onto `renderInShell`; shell-classified files move into `core/web` with audiobook imports cut into books slots; `main.tsx` → `createShell` with the data router; `useUnsavedChangesBlocker` deleted; the shell's realtime client replaces `eventSourceManager` and the operations stream (FE-10); the build gains the preset meta tag; `main.go` passes the bundle to `app.Options.UI`; books drops `ServesSPA` and gin's `NoRoute`/`setupStaticFiles` SPA serving is deleted **in the same PR** (FE-1); `ui.Handler` mounts from this build. |
| UI routes | Module pages under `/books/...`; shell pages per `ui.ReservedSegments` (`/settings`, `/system/...`, `/users`, `/activity`, `/invite/{token}`). Legacy SPA paths (`/library`, `/authors`, `/series`, `/works`, `/playlists`, `/collections`, `/review`, `/versions`, `/fingerprints`, `/dedup*`) → client redirects (pattern of `web/src/utils/absRedirects.ts:17-25`); server records full-page loads of legacy SPA paths on surface `ui_route`. `localStorage` keys migrated on first load. |
| Rollback | Binary swap; the UI is embedded per build. Bookmarks to `/books/...` 404 on the older build's SPA (accepted, cosmetic); the S0 build check offers a reload to tabs left open. |
| Done when | `App.tsx` has no hard-coded module routes; a dummy second contribution renders with no edit to books UI (G6, UI half); G3: the shell rejects a module descriptor with unknown version; the legacy streams show no use from first-party tabs (persisted usage on `sse` limited to non-UI clients); previous-release UI against cur and cur UI against prev (R-test cells a, b); boot within S1 + 10 %. |

### S10 — Final cleanup (was S11)

Delete after their windows: removed flags `--db-type` and the SQLite no-op flag (`cmd/root.go:489,508`);
`internal/serviceregistry`, `internal/plugin`, `pkg/plugin/sdk`; the mirror code of every contract
that has run (a build without a stage's adopt step refuses an unadopted store, §9.2.2); compat-ledger
rows closed. Not deleted: the legacy mount, the `/api/*` → `/api/v1/*` redirect, ABS paths, `/health`,
`/metrics`, permanent env names, the `config.AppConfig` and logger façades (optional ratchets,
SIMP-3; 11 §11.5 checklist). Done when §9.6 has no row in state "aliased" except those marked
permanent and `TestCompatLedgerMatchesCode` shows only permanent entries.

### 9.4.1 Sub-spec batch → stage map (OPS-12)

| Batch (owner) | Stage | Runtime behaviour change? | Rollback mechanism | Done-check |
|---|---|---|---|---|
| Build paths, CI classification, Docker PR job (03 §3.2, 07 §7.3.2) | S0 | no | revert PR | S0 done-checks |
| 05-config phase T (legacy loader overlays every secret row) | S0 | no (inert until S3) | binary swap | unit test: overlay reads a legacy-format row for every §2.2 field |
| 05-config stage 0 (8 bypassing writes) | S1 | no | binary swap | `TestNoDirectAppConfigWrites` |
| Staged restore, ABO-local (05-backup §5B.3.4) | S1 | yes (restore applies at next boot) | binary swap; pending intents refused by rollback | S1 restore rehearsal |
| 05-http-api H0: `MountLegacy`, perimeter to core, cross-origin check | S2 | yes (perimeter owner, new cross-origin check, HTTP drain 10 s) | binary swap | S2 perimeter tests, ABS, wire goldens |
| 05-logging stages 1–2 | S2 | yes (log file path, levels now honoured) | binary swap | journal shows ABO records; level change applies |
| 05-health lifecycle and budget; supervisor stop timeouts | S2 | yes (stop order, budget) | binary swap | `systemctl stop` check |
| 05-storage rollout 1–2: core opens the store; books on `LegacyRange`, shadow claims | S2b release 1 | no (log-only) | binary swap | zero persisted violations ≥ 14 days |
| 05-storage rollout 3: strict claims (flip) | S2b release 2 | yes (refused writes) | dial `core.storage.claims.mode`, binary swap | strict on production 14 days, zero refusals |
| `database.Store` split: storage files (04 §4.5) | S2b | no (pure moves) | binary swap | `make ci` both modules |
| 07 §7.19 steps 1–3 | UI-0 | no | binary swap | UI-0 done-checks |
| 05-config phase X + façade stage 1 (viper out of boot path) | S3 | yes (config truth moves; blanking) | 05-config rollback matrix | S3 done-checks |
| 05-config optional ratchet (accessor) | none (optional) | no | — | `TestAppConfigRatchet` ceilings only go down |
| 05-storage adoption for identity; `database.Store` split: auth files | S4 | yes (identity truth moves) | mirror + rebuild (§9.2.3) | S4 done-checks |
| 05-http-api S4: `LegacyAuthCore`, native core auth routes | S4 | yes (auth engine) | binary swap under the mirror | `TestLegacyRoleReachUnchanged` |
| UI auth/users/config calls and permission constants (07 §7.19 step 3 consumers) | S4 | yes (UI) | binary swap | vitest permission-literal check |
| 05-tasks T0+T1 (flip: executor, adoption, reconcile, mirrors) | S5 release 1 | yes (executor, run store) | 05-tasks §5T.6 G5 rules | 05-tasks §5T.9 G5 test |
| 05-tasks T2–T7 (native definitions; T7 moves schedule keys) | S5 later releases | T2–T6 no; T7 yes (schedule keys, aliases) | binary swap | per-batch gate in 05-tasks §5T.8.2 |
| 05-tasks T8 (delete `OperationDef`, `opsbridge`, ABO's opv2 writers) | with or after C5 | yes (rollback boundary) | none past C5 (guard refuses) | `grep` no `registry.` import in ABO |
| 05-events/05-realtime hub, projections, outbox; 05-notify webhook instances | S6 | yes (stream server, webhook delivery path) | binary swap | S6 goldens |
| 05-metadata M1 instance store + imports; 05-download/05-ai adapters | S7 | yes (settings home) | blob mirror (05-config) | S7 done-checks |
| 05-backup format and catalogue; 05-updater signed apply; 05-metrics renames | S8 | yes | binary swap; restorable archives | S8 done-checks |
| 07 §7.19 steps 4–6 | S9 | yes (UI routes, stream client) | binary swap | S9 done-checks |
| 07 §7.19 step 7 (remaining `api.ts` sections) | S5–S9, per native route | no for users | binary swap | generated client covers the route |
| 05-logging optional ratchet (injection); logger façade removal | none (optional) | no | — | slog ratchet ceilings only go down |
| Mock conversion (`internal/database/mocks`, 04 §4.6) | S2b–S4 (owner decides: in-stage or deferred behind `storetest`) | no | — | 04 open item 1 |

## 9.5 Contract track

Every contract is a PR gated by `TestNoContractBeforeSunset` and P8, and runs on each store only when
the store-side preconditions of §9.2.2 hold (`Adopts` applied; `RequiresServed` met);
otherwise the build adopts if needed and stays in mirror phase. A contract migration runs in a
pre-migration checkpoint (disk headroom checked), deletes in ≤ 10 000-key batches, raises the required
level before its first legacy delete (05-storage raise procedure), and is idempotent on resume.

| Contract | Earliest | Store-side and usage preconditions | Action | Required level after |
|---|---|---|---|---|
| C3 config/secrets | R(S3) + W | S3 adopt record; `core` level 2 served ≥ 30 days; P8 shows no use on the `config`/`env` window aliases; S1 staged restore in production (OPS-15) | 05-config phase C: last re-import, stop the mirror, delete `setting:config_blob`, the legacy secret rows and `c/config/import/`; YAML legacy keys refused at boot for one minor, then alias rows deleted | 2 |
| C4 identity | R(S4) + W | S4 adopt record; `core` level 3 served ≥ 30 days; P8 zero for `http` auth/users aliases and `rbac_atom`; staged restore in production | stop mirror; delete adopted legacy rows and the six `idx:` sub-prefixes; fold side records; legacy atoms → 400 naming the canonical permission; HTTP aliases → `Gone` one minor → delete | 3 |
| C5 tasks | R(S5) + W | S5 adopt record; `core` level 4 served ≥ 30 days; P8 zero for `task_kind` and task `http` aliases; P11 shows no legacy non-terminal run lacking `migrated_to` | remove reconcile and clock/pause mirrors (05-tasks §5T.6); aliases → `Gone`; T8 may ship with or after it; stored-row kind resolution through `FormerKinds` is **kept** | 4 |
| C6 events | R(S6) + W and after S9 | P8 zero on `sse` | legacy streams → 410 (EventSource stops on non-200) → delete projections; webhook instances keep `legacy_headers` as instance data | — |
| C7 integrations | R(S7) + W | P8 zero on the generic `/api/v1/{ai,deluge,metadata-sources}` aliases | aliases → `Gone` → delete; `Origin=pinned` import ends per 05-metadata M1.4 | — |
| C8 backup/update/system/metrics | R(S8) + W | `core` level 5 served ≥ 30 days; P8 zero on `/api/v1/{backup,update,system}` and health aliases; P6 shows no external dashboard on aliased core metric names | aliases → `Gone` → delete; metric aliases removed by PR (gate P6); raise `core` to 5 (no data deleted), which passes the legacy restore bound so backup encryption can activate (05-backup B3) | 5 |
| C9 UI routes | R(S9) + W | P8 zero on `ui_route` | client redirects removed | — |

## 9.6 Q5 — every compatibility surface, window, removal mechanism

| # | Surface | Count / examples | Canonical | Window | Removal mechanism | Evidence |
|---|---|---|---|---|---|---|
| 1 | App API `/api/v1/*` (core concerns) | auth 22, users 15, operations 38, system 7, backup 4, update 3, config 2, preferences 3, tasks 3, op-defs 2, ai/deluge (generic parts) | `/api/core/v1/...` | W after the native route ships (S4–S8) | `Alias` + headers → `Gone` 410 one minor → delete (contract PR) | `raw/09` §1.1 |
| 2 | App API `/api/v1/*` (books) | ~280 routes on the legacy mount | unchanged | **permanent** (SIMP-2); per group only if rewritten | — | `raw/09` §1.1 |
| 3 | `/api/v3/ops/*` | 12 | `/api/core/v1/tasks/*` | W after S5 | as row 1 | `wire_ops_v3_routes.go` |
| 4 | SSE streams | `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events` | `/api/core/v1/events` | W after S6, and not before S9 | projection → 410 → delete | `raw/09` §6 |
| 5 | Unversioned `/api/*` → `/api/v1/*` 301 | all non-reserved | — | **permanent** (legacy mount `Redirects`) | — | `server_lifecycle.go:1790-1822` |
| 6 | ABS-compatible surface | 75 routes, unversioned | unchanged, books-owned | **permanent** | none | `wire_abs_routes.go:628-653` |
| 7 | Infra paths | `/health` (incl. `ready`), `/metrics` | unchanged | **permanent** | none | `Dockerfile:100-101`; `scripts/deploy-wait-ready.sh` |
| 8 | Infra aliases | `/api/health`, `/api/v1/health` | `/api/core/v1/health` | W after S8 | as row 1 | `server_lifecycle.go:1746-1782` |
| 9 | Retired 410 stubs | 45 | — | their own rule (one release) | deleted from gin | `gone.go:14-36` |
| 10 | Permission atoms in API input/output | 13 | 05-rbac §6.1 | W after S4 | input counted → 400 naming canonical | `permissions.go:24-85` |
| 11 | Permission atoms in stored roles/API keys | `role:`, `apikey:` | `c/rbac/`, `c/auth/` | mirror until C4 | contract migration | `store.go:968-994` |
| 12 | Role-name admin check | `"admin"` | 05-rbac §6.2 | none (internal) | replaced in S4 | `middleware/auth.go:522-537` |
| 13 | Config keys in YAML | ~251 flat keys | `core.*`, `modules.books.*`, instance records | W after S3 (S7 for instance keys) | refuse-to-start naming canonical (one minor) → alias deleted | `config.go:2621` |
| 14 | Config in DB (`config_blob`) and legacy secret rows | 1 blob + 4 rows | `c/config/`, `c/secrets/` | mirror until C3 | 05-config phase C | `persistence.go:359-884,1996-2008` |
| 15 | Env vars, window | 141 `BindEnv` + implicit `AutomaticEnv` names + `ABK_*` | 05-config §1.3.6 | W after S3 | not read; `ProtectAdmin`+ keys refuse to start (§9.2.5) | `cmd/root.go:625` |
| 16 | Env vars, permanent | `OTEL_EXPORTER_OTLP_*`, `OTEL_METRIC_EXPORT_INTERVAL`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `NOTIFY_SOCKET`, `HOME`, and every read name a shipped deploy file sets (`DATABASE_PATH`, `FP_PARALLEL_WORKERS`, `BASIC_AUTH_*`, …) | unchanged | **permanent** (OPS-5) | none | §9.2.5; 05-config §1.12.4 |
| 17 | CLI commands | `serve scan playlist tag organize inspect-metadata seed diagnostics{cleanup-invalid,reserved-prefs,query} fp-worker dedup-bench` (bench tag) | unchanged | **permanent** | none | `cmd/*.go` `Use:` |
| 18 | CLI flags in shipped artifacts | `--host --port --db --config --dir --log-level --playlists --tls-cert --tls-key --http3-port --external-url --workers --read-timeout --write-timeout --idle-timeout` | same flags feed canonical keys | until next **major** (1.0) | release note at the major; not usage-gated | `cmd/root.go:486-535`; `Dockerfile:104` |
| 19 | Removed/no-op flags | `--db-type`, SQLite flag | — | W after S2 | deleted S10 | `cmd/root.go:489,508` |
| 20 | Fast-exit protocol | `--print-storage-format`, `--print-capabilities`, new `--print-storage-levels` | unchanged | **permanent** | none (rollback protocol) | `main.go`; `Makefile:892` |
| 21 | Child-mode sentinel/env | `--operation-runner`, `registry.EnvChild*` | unchanged | none (same binary) | n/a | `cmd/child_mode.go:53-96` |
| 22 | Other binaries in `cmd/` | `itl-*`, `mtls-bridge`, `pebble-inject-skip`, `pid-census`, `verify-suite` | stay ABO | permanent (books tools) | n/a | `cmd/` |
| 23 | Metric names, books | 73 `audiobook_organizer_*` families | unchanged | **permanent** (SIMP-13) | none | `internal/telemetry/contract/testdata/series.golden` |
| 24 | Metric names, core subsystems | ops, Pebble, AI, two process gauges | `mediacore_*` (05-metrics §13) | W after S8, gate P6 | gather-time alias → removed by PR | `metrics.go:15-300`; `opsmetrics.go:12-16` |
| 25 | Pebble identity families | `u: role: apikey: invite: inviteallow: oauthid:` + 6 `idx:` sub-prefixes | `c/auth/`, `c/rbac/` | mirror until C4 | contract migration + required level | `keyfamilies.go:63-71` |
| 26 | Pebble books families | the rest, incl. `setting:`, `preference:`, `pref:`, `act:` | books claims | **permanent** (02 #13) | none | 05-storage §5S.9.1 |
| 27 | Pebble ops families and clocks | `opv2:`, `opv3:`, `op:`, settings clock keys | `c/tasks/` | mirror/reconcile until C5; history ages out | 05-tasks retention | 05-tasks §5T.6 |
| 28 | Task kinds (op def IDs) | 233 | `books.<id>`, `core.<id>` | input W after S5; stored-row resolution **permanent** | `FormerKinds` + persisted usage on `task_kind` | `aliases.go:16-52` |
| 29 | Webhook headers and event names | 2 headers, 12 event names | `books.*.v1` topics, core notify headers | per instance: `legacy_headers` is instance data, kept while set | operator clears it per instance | 05-notify §10; `webhook/plugin.go:101-126` |
| 30 | Backup archives | tar(+codec) of DB dir | 05-backup format | read: **permanent**; previous-build-restorable default: through the window | 05-backup | `backup.go:283-860` |
| 31 | SPA routes | 14 module paths | `/books/...` | W after S9 | client redirect → removed (C9) | `App.tsx:253-448` |
| 32 | Wire shapes of aliased routes | hand-written TS interfaces | generated | = route window | `WithLegacyShape` goldens | `raw/13` §8 |

## 9.7 Production-data unknowns (read-only commands, both branches designed)

Run these against production before the stage that needs them; paste counts only, never values.
`<abo-url>`, `<redacted-token>` (an admin API key from `server-bootstrap`), `<unit>`, `<scratch>`,
`<host-a>` are placeholders. Offline commands run on a **restored backup copy** so production is never
stopped. Never print `sess:` or secret values (they hold credentials); count keys only.

| ID | Question | Command | Branch A | Branch B |
|---|---|---|---|---|
| P1 | Key counts/bytes per family (S2b, S3, S4, C*) | `curl -sS -H "Authorization: Bearer <redacted-token>" "<abo-url>/api/v1/diagnostics/db-census?fresh=true" \| jq '.data.families[] \| select(.prefix \| test("^(u\|role\|apikey\|invite\|inviteallow\|oauthid\|sess\|idx\|setting\|opv2\|opv3\|act):")) \| {prefix,keys,raw_key_bytes,raw_value_bytes}'` (estimated, read-only, `handlers/diagnostics.go:1195-1220`) | under 05-storage's atomic cap: one-batch adoption | over: 05-storage's resumable path; legacy stays authoritative until done |
| P2 | Custom (non-seed) roles exist? | `audiobook-organizer diagnostics query --raw --db <scratch>/audiobooks.pebble --prefix role: --limit 1000 \| grep -c '"is_seed":true'` vs total `^Key:` | none: adoption translates seeds only | some: each translated; any atom outside 05-rbac §6.1 → boot refuses naming the role (fail-closed), owner maps it |
| P3 | API keys with empty scopes | same with `--prefix apikey:` and `grep -c '"scopes":\[\]'` | none: no `LegacyFullScope` rows | some: adopted as `LegacyFullScope`; the native API forbids new empty-scope keys |
| P4 | Env names actually set | `systemctl show <unit> -p Environment \| tr ' ' '\n' \| cut -d= -f1`; `systemctl cat <unit> \| grep -o 'Environment="[A-Z_0-9]*'`; Docker: `docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' <container> \| cut -d= -f1`; launchd: `plutil -extract EnvironmentVariables xml1 -o - <plist> \| grep -o '<key>[A-Z_0-9]*'`; compose: `docker compose config \| grep -oE '^\s+[A-Z_0-9]+:'` | only bound or permanent names: alias table suffices | an implicit name in use: add an explicit alias row (and a permanent one if a deploy file sets it) before S3 |
| P5 | Invite TTLs outstanding; external API callers | `... --prefix invite: ... \| grep -o '"expires_at":"[^"]*"' \| sort \| tail -1`; callers: `journalctl -u <unit> --since -30d -o cat \| grep -oE '(GET\|POST\|PUT\|PATCH\|DELETE) /api/[a-z0-9/_-]*' \| sed -E 's#/[0-9A-Za-z]{20,}#/:id#g' \| sort \| uniq -c \| sort -rn \| head -50` | only the SPA calls `/api/v1` core routes: W floor applies | scripts/clients found: list them in the ledger; the window stays open until P8 shows no use |
| P6 | Metric consumers | operator's Grafana/Prometheus: `grep -rhoE '(audiobook_organizer\|ai_dispatch\|ai)_[a-z_]+' <dashboards-and-rules-dir> \| sort -u` | only repo templates: updated in S8 | private dashboards on aliased core names: C8 waits until they move |
| P7 | ABS surface in use | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.abs_api_enabled'`; `... --prefix abs_sess: ... \| grep -c '^Key:'` | off: conformance fixtures suffice | on with sessions: fixtures **plus** live login and progress sync with a real ABS client, before and after rollback, gate S2, S4 and S9 |
| P8 | Legacy use (removal gate) | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/core/v1/compat/usage \| jq '.[] \| select(.last_used_at > (now - 14*86400 \| todate)) \| {surface,legacy,count}'` | empty for the contract's surfaces: contract may run | entries: window extends; `legacy` names the caller to fix |
| P9 | Webhooks configured | `curl … /api/v1/config \| jq '.plugins.webhook // empty \| keys'` | none: S6 creates no webhook instance | some: one instance per URL with `legacy_headers` |
| P10 | Rollback inventory | `ssh <host-a> '<bin>.prev --print-storage-levels \|\| <bin>.prev --print-storage-format'`; `curl -s <abo-url>/api/v1/system/version` | `.prev` ≥ S0: guard effective, T in place | `.prev` older than S0: deploy S0 twice before S3 so `.prev` knows the guard and the T overlay |
| P11 | Active-run set (S5, C5) | `audiobook-organizer --print-upgrade-blockers --db <scratch>/audiobooks.pebble` (05-tasks §5T.6) and, on a copy, `diagnostics query --raw --prefix opv2:act: \| grep -c '^Key:'` | no over-cap run: flip | over-cap runs listed: finish or cancel them on the previous release before S5 |
| P12 | Disk headroom | `df -B1 --output=avail <db-dir-parent>` vs `du -sb <db-dir>` | free ≥ store size: checkpoints fit | less: free space before any stage with a checkpoint (S2b, S3, S4, S5, C*) |

## 9.8 Error semantics (compatibility machinery)

| Error path | Behaviour | Why |
|---|---|---|
| Store required level > build level | **fail-closed**: refuse to open, name both levels and the release to install | an old build on a contracted store would read stale or missing rows |
| Contract preconditions not met on this store | **open**: adopt if needed, stay in mirror phase, retry next boot | never delete legacy data on an unadopted or briefly served store (cross-cutting 6) |
| Unadopted store and the build no longer registers the adopt step | **fail-closed** (`ErrAdoptMissing`): refuse to start, name the release to install | the build can no longer adopt |
| Adoption or import error | **fail-closed**: boot halts, checkpoint kept, legacy untouched | G2 |
| Undecodable legacy row during rebuild | **closed per principal**: no native copy, Degraded naming the key, counted; boot continues | one bad row must not crash-loop the unit (OPS-19) |
| Mirror write fails | **fail-closed**: whole batch fails, request errors; `mediacore_compat_mirror_write_failures_total` | a native write without its mirror breaks rollback |
| Native API key with no exact legacy scope set | mirror **disabled** (closed) | an empty legacy scope list is the widest value (SEC-1) |
| Stored role holds an unknown atom (adoption) | **fail-closed** boot, names role id | P2 branch B |
| Legacy alias request | served (**open**), recorded, headers | compatibility is the point |
| Legacy key in YAML after W | **fail-closed** refuse to start, names canonical | silently ignoring `root_dir` would point the library at nothing |
| Retired env name of a `ProtectAdmin`+ key after W | **fail-closed** refuse to start, names canonical | ignoring `DATABASE_PATH` would open or create the wrong store (`config.go:2519-2544`) |
| Other legacy env name after W | ignored + one WARN (**open**) | env carries unrelated names (`HOST`, `PORT`) |
| Same key in legacy and canonical form | canonical wins + WARN (**open**) | deterministic, visible |
| Usage cannot be recorded | ignored (**open**); the flush retries | telemetry must not fail requests |
| Checkpoint without disk headroom | **fail-closed** before the migration | a full pool is a known outage (`config.go:2521-2528`) |
| Staged restore intent stale or from another build | rejected, moved aside, Degraded (**closed**) | a late restore would discard writes made in between (OPS-14) |
| Backup without a manifest (old archive) | restore proceeds as legacy (**open**) | read support is permanent |

## 9.9 Concurrency contract

Adoption, rebuild, import and contract migrations run in the kernel's migration phase, before
listeners and the executor start (03 §3.7), under the storage migration lock. The rebuild and import
steps are bounded by small families (P1 branch A) and are **sequential by declared necessity**
(correctness needs one atomic batch, and the bottleneck is one Pebble commit — R7
`tasks.Sequential(why)` wording reused); a family over the cap uses 05-storage's resumable path with
its own `tasks.Parallelism`. 05-tasks' copy-and-mark is one batch per run and may run runs in
parallel (05-tasks). Mirror writes are staged in the caller's batch under the family's existing store
mutex (the `opsMu` pattern, `pebble_store_ops_v3.go:33-35`). Alias handlers inherit the canonical
handler's concurrency. `compat.Handle.Use` is lock-free on the request path (atomic counters); the
flusher is one goroutine per App, owned by the kernel lifecycle. All migration steps take `ctx` and
honour cancellation between batches; a cancelled adoption leaves legacy as truth.

## 9.10 Config keys, metrics, tests

Config keys this file adds: none beyond the operational dials of §9.2.8, which their owning specs
define; the reader level is compiled into the build.

| Metric | Type | Labels |
|---|---|---|
| `mediacore_compat_legacy_use_total` | counter | `surface`, `name` (bounded: ledger entry), `entry` — dashboards only |
| `mediacore_compat_legacy_last_use_timestamp_seconds` | gauge | `surface`, `name` — from the persisted record |
| `mediacore_compat_mirror_write_failures_total` | counter | `family` |
| `mediacore_compat_level` | gauge | `owner`, `kind` (supported, store) |
| `mediacore_build_info` | gauge | `version` |
| `mediacore_compat_adoption_seconds` | histogram | `family` |
| `mediacore_kernel_boot_phase_seconds` | gauge | `phase` |

| Test | Proves |
|---|---|
| `TestLegacyToleratesNamespacedKeys` (S0) | HEAD-era code paths ignore `c/`, `m/` keys |
| `TestNativePrefixesNotRedirected` (S0) | `/api/core/vN/`, `/api/<module>/vN/` bypass the 301 |
| `TestCompatGuard*` + `scripts/test_storage_format_guard.py` | open refusal, rollback refusal, sidecar repair after a commit error, both values printed on disagreement |
| `TestContractWaitsForServedLedger` | a contract build on a store whose expand served < W adopts and mirrors, never deletes |
| `TestAdoptedPrefixWritesGoThroughMirror` | flip-PR ratchet (OPS-18) |
| `TestMirrorLegacyIndexesResolve`, `TestRebuildHonoursRollbackWrites`, `TestRebuildKeepsMirrorDisabledKey`, `TestRebuildSkipsUndecodableRow` (with 05-storage's mirror tests) | §9.2.3 under `testing/synctest` with a fake clock |
| `TestLegacyRoleReachUnchanged` (permanent CI gate) | §9.2.6 invariant |
| `TestEveryLegacyConfigKeyHasCanonical`, `TestShippedDeployEnvNamesArePermanent` | config/env completeness |
| `TestCompatLedgerMatchesCode`, `TestNoContractBeforeSunset` | the ledger is the code |
| `TestAliasHeadersAndShape` | headers + legacy-shape goldens |
| **R-test (rollback rehearsal), per stage**, `make compat-rehearsal` | in CI: build `prev` from the **last production-deployed tag** and `cur`; boot `cur` on a seeded store, write through every adopted surface, stop; boot `prev` on the same store, assert reads, write (password reset, deactivate, key revoke, config edit, secret change, enqueue), stop; boot `cur`, assert the rebuild/re-import/reconcile honoured `prev`'s writes. The **DATA-6 cases** are explicit rows: a run enqueued, re-queued or interrupted on `prev` is adopted on roll-forward and runs once (05-tasks reconcile); a schedule clock advanced on `prev` is adopted and the kind neither double-fires nor stampedes (clock mirror); a config value or secret changed on `prev` is re-imported, including a secret re-entered as blob plaintext, which is re-imported and re-blanked (05-config re-import). Matrix cells (OPS-20): **(a)** the previous release's UI bundle against `cur` (old tabs during W); **(b)** `cur`'s bundle against `prev` after rollback (new tabs, S0 reload prompt); **(c)** ABS conformance against `prev` and `cur`; **(d)** the updater's `Boot` rollback (and `rollback_blocked` past a contract); **(e)** restore across levels (archive from `prev` onto `cur`, from `cur` onto `prev`, a staged intent across a rollback). Cells a–c are mandatory in every stage that changes the UI's or ABS's request path (S2, S4, S6, S9). |

## 9.11 Risk table

| Stage | Risk | L | I | Detection | Mitigation |
|---|---|---|---|---|---|
| S0 | Redirect exclusion too broad: an unversioned client path `/api/x/v2/...` stops redirecting | L | M | `TestCollidingNamespacesStillRedirect`; P5 | exclusion requires `/vN/` and a module-ID shape |
| S0 | `.prev` predates S0, guard and T overlay blind | M | H | P10 | deploy S0 to production twice before S3; 05-config keeps plaintext for such stores |
| S0 | A build path missed by the entry-point sweep breaks once `core/web` exists | M | M | the S0 PR image job; UI-0 done-check | one script, every site listed in §9.1 |
| S1 | Moving telemetry changes metric/label output | M | M | S1 name diff | namespace/scope injected; golden of names |
| S2 | Perimeter move changes Basic-auth exemptions, CSP or rate classes | M | H | S2 perimeter tests | manifest-driven per-route data copied from HEAD; goldens |
| S2 | Listener move changes TLS/H3/peer-allowlist behaviour | M | H | smoke on `--http3-port`, peer test from a disallowed address | move code verbatim first; `peer_allowlist` tests move with it |
| S2 | Supervisor kills the process mid-shutdown | M | M | journal SIGKILL check | stop timeouts shipped in S2; budget below them |
| S2b | Strict claims refuse a live books writer | M | H | shadow release with persisted violations | shadow ≥ 14 days; dial back to shadow |
| S3 | Rollback loses secrets | L | H | R-test | 05-config mirror + T overlay; plaintext kept when `.prev` < level 1 |
| S3 | Encryption key file missing on restore to a new host | M | H | restore matrix | 05-backup identity export |
| S4 | Privilege change from atom expansion | M | H | `TestLegacyRoleReachUnchanged` | permanent gate |
| S4 | Rollback widens a key | L | H | property test | exact-or-disabled mirror (SEC-1) |
| S4 | Rebuild drops mirror-disabled keys | M | H | `TestRebuildKeepsMirrorDisabledKey` | `MirrorDisabled` + `Fresh` (§9.2.3 rule 2) |
| S4 | `usermerge` undo or admin debug-edit writes legacy `u:` images at runtime | M | H | test: undo a pre-S4 merge journal on S4 | journal undo for adopted prefixes goes through the mirror writer (ratchet) |
| S4 | Session fallback lets a revoked legacy session live | L | H | test | fallback applies epoch, status, revoked and expiry checks (05-authn) |
| S5 | Copy-and-mark over cap drops a run | L | M | P11; Degraded `tasks_adopt_dropped` | finish/cancel listed runs before S5 |
| S5 | Parallelism declarations change runtime behaviour | M | M | duration metrics per kind | carry today's `Concurrency` values; dial `core.tasks.kinds.<kind>.parallelism` |
| S6 | Legacy stream projection drops events the UI relied on | M | L | per-type goldens | projections built from HEAD frames |
| S6 | Webhook receivers break | L | M | P9 | `legacy_headers` per instance |
| S7 | Instance-scoped throttles double outbound rate | M | M | provider 429 counts | one `providerhttp` instance per provider per App, asserted in kernel |
| S7 | Operator edits a pinned value in the UI | L | L | 403 on `Origin=pinned` | lock reason names the env variable |
| S8 | Auto-update users stuck on S8 | M | H | staging install with `auto_update.enabled=true` | signed-release precondition; `allow_unsigned_legacy` |
| S8 | Updater rolls back onto a store it cannot read | L | H | R-test cell d | `ReaderCheck`; `rollback_blocked` |
| S9 | Bookmarks/old tabs hit removed SPA paths | H | L | `ui_route` usage | client redirects through W; reload prompt |
| C* | Contract runs on a store that skipped the expand | M | H | `TestContractWaitsForServedLedger` | store-side preconditions (§9.2.2) |
| C* | Contract merged before window | L | H | `TestNoContractBeforeSunset`; P8 | ledger dates + persisted usage + guard |

## 9.12 Open items

| # | Item | Owner |
|---|---|---|
| O1 | (closed by 05-config §1.12.2) Plaintext secrets in `config_blob` during W: blanked when `.prev` reads the legacy-format rows, kept otherwise. | — |
| O2 | (closed by 05-rbac §6.1) Final `core:` permission names. | — |
| O3 | `.encryption_key` lives outside the archived DB dir (`settings.go:44-52`, `backup.go:283-428`): restore on a new host needs the identity export. | 05-backup |
| O4 | Env grammar and prefix (`ABO_`) | 05-config §1.3.6 |
| O5 | (closed by 05-storage §5S.9.1) Exact books claim list, generated from `keyfamilies.go`. | — |
| O6 | (closed by 03 §3.3 `log` row, 05-logging §5) `act:` stays books; audit gets new rows only. | — |
| O7 | CLI flag window "until 1.0" deviates from the two-minor floor for surfaces baked into users' compose files | owner |
| O8 | Pilot cost before any books route group is rewritten to native `httpapi` (SIMP-2 item 4): measure one group (e.g. playlists, 10 routes) | owner, when a group is first rewritten |

## Objections to 03

None open. v1.0.0's three objections are resolved by 03 v1.3.0: module IDs that collide with
compatibility namespaces are rejected via `app.Options.ReservedAPINamespaces` (03 §3.6); R8 names the
three claim kinds (prefix, exact key, prefix-with-exceptions); the `compat` package exists (03 §3.3) and
the store guard is the merged mechanism owned by 05-storage.

## Out-of-scope defects found

1. Shipped deploy files set env names nothing reads: `AUDIOBOOK_ROOT_DIR` (`deploy/audiobook-organizer.service:97`,
   `deploy/com.audiobook-organizer.plist:38`; deliberately unread, `internal/config/config.go:2569-2575`),
   `AO_DIR` and `AO_DB` (`docker-compose.yml:18-19`; the Docker `CMD` supplies `--db`, `Dockerfile:104`),
   `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`). They mislead operators; S3 removes them.
2. `--db-type` help still says "pebble (default) or sqlite" (`cmd/root.go:489`) although SQLite is
   rejected (`internal/database/store.go:1840-1843`).
3. The HEAD updater exits 0 to restart (`internal/updater/updater.go:331-334`), so under the committed
   unit's `Restart=on-failure` every auto-update leaves the service stopped; S0 fixes it.

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- Cross-cutting 2 / SIMP-2 / OPS-11 / SEC-13 / FE-1 — no gin retirement (old S10 removed, old S11 → S10); `MountLegacy` permanent; S2 moves only the perimeter, S4 is the one auth-engine stage; SPA stays on gin until S9.
- Cross-cutting 5 / DATA-3 / DATA-4 / OPS-19 — identity: legacy authoritative, rebuild every boot, side records (05-storage mechanism); lossy "legacy wins" reconcile dropped; per-principal quarantine.
- Cross-cutting 6 / OPS-2 — store-side records (adopt-step applied record, served ledger); contracts declare `Adopts` and `RequiresServed`, adopt first or defer; only a build without the adopt step refuses.
- Cross-cutting 7 / DATA-1 — `setting:`/`preference:`/`pref:` stay books.
- Cross-cutting 8 / DATA-21 / OPS-14 / OPS-15 — backup format owned by 05-backup; window policy, staged-restore intent rules; staged restore pulled to S1; C3/C4 precondition.
- Cross-cutting 9 / MOD-1 / OPS-1 / MOD-8 / MOD-9 / FE-13 — S0 Docker layers, `.dockerignore`, PR image job, one web build script for every `npm ci` site, Dependabot, CI classification table.
- Cross-cutting 10 / MOD-2 / MOD-6 — move lists referenced to 04 §8 (stage-keyed); S1 list reconciled; `sdnotify`/`startup` move at S2.
- Cross-cutting 11 / MOD-3 — genproto exclude only in `core/go.mod`.
- SIMP-3 — config and logger façades kept; removal optional ratchets, not stages.
- SIMP-4 — integration settings to the one instance store (S7), webhooks as notify instances (S6).
- SIMP-6 — schedule keys via `ScheduleFromConfig` (S5).
- SIMP-7 / OPS-10 / OPS-16 — one window in §9.2.1; no clock-based removal; persisted usage record; `compat.Ledger` defined (§9.2.9).
- SIMP-13 — books metric names unchanged, emitted once; core renames via gather-time aliases, gate P6.
- SIMP-15 / SEC-20 / OPS-9 / OPS-3 — S3 references 05-config §1.12.2 (T in S0, X in S3, C3); reader-capability levels.
- SIMP-18 / OPS-8 / DATA-7 — one per-owner level guard (05-storage §5S.3.7), policy here; contracts are `Breaking` steps with `RaisesLevel`; legacy restore bound `core` 4, lifted by C8.
- SEC-1 / OPS-4 — exact-or-disabled key mirror; mirror-disabled keys survive the rebuild (`MirrorDisabled` + `Fresh`).
- SEC-19 — no raw session mirror; one re-login on rollback.
- OPS-5 — permanent env aliases for read names in shipped deploy files; retired DB-path name refuses to start; unread names are deploy-file defects.
- OPS-6 — S0 updater exit-code hotfix and `.prev` slot; S8 `ReaderCheck`.
- OPS-7 — 05-tasks' model referenced; S5 level 4 and C5; S0 op-ID teach dropped.
- OPS-12 — S2b storage stage; §9.4.1 batch → stage map incl. 05-tasks T0–T8.
- OPS-13 — S2 stop sequence, one budget, supervisor stop timeouts.
- OPS-17 — operational dials listed (§9.2.8).
- OPS-18 — one flip PR per stage + ratchet.
- OPS-20 / OPS-21 — R-test with last production tag, UI and ABS cells at S2/S4/S6/S9; `X-App-Build` teach in S0.
- OPS-22 — boot budget per stage, `DEPLOY_READY_TIMEOUT`, P11, boot-phase metric.
- OPS-23 — S8 signed-release precondition and legacy unsigned path.
- OPS-24 — measurable done-checks (wire goldens with normalisation, `TestNoRoleNameAuthz`, pinned toolchain).
- OPS-25 / OPS-26 / OPS-27 — disk headroom (P12), sudoers and sidecar repair, `/health` permanent, build-info gauge and alert snippets, legacy-key health and `config check`.
- FE-2 / FE-12 / SIMP-25 — UI-0 stage: react-router 8 and the generated client before S4.
- FE-10 — S6 has no UI change; the shell moves to native streams at S9.
- FE-11 — S4 replaces the UI's permission constants and scope list.
- COR-17 / COR-26 — no `EntityRef` or `Resource{…}` spellings; task kinds follow 03 §3.6 (`books.<id>`, segments `[a-z][a-z0-9-]*`).
- SIMP-1 — deferred-build packages built in no stage; S7 done-check asserts they do not exist.
