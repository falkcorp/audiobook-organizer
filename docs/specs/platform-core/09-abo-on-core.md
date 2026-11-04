<!-- file: docs/specs/platform-core/09-abo-on-core.md -->
<!-- version: 1.2.1 -->
<!-- guid: 3c82c917-2fc4-4385-b085-73d143a2374f -->
<!-- last-edited: 2026-11-04 -->

# 09 — ABO on core: the strangler-fig sequence

Evidence commit: `748fd787a`. Raw data: [`raw/`](../../plans/platform-core/raw/00-index.md).
Names, paths and rules come from [03-architecture](03-architecture.md) (normative, v1.5.0). Package
moves are **not restated here**: [04-package-mapping](04-package-mapping.md) §8 keys every move by
this file's stages and is authoritative for a single package (MOD-6); each stage below names its 04
rows. This file is the single owner of **sequencing and compatibility windows** for ABO: where a
05-* "Migration from ABO" section and this file disagree on *when* something happens or *how long*
a legacy surface lives, this file wins; on *how* a mechanism works, the owning 05-* file wins. It
also names, per adopt/expand sequence, the **Teach build** and the level it declares (05-storage
§5S.3.7 leaves that policy here).

## 9.0 Purpose & scope

How audiobook-organizer (ABO) becomes **app = core + module `books`** without a flag day. Every
stage ends with ABO shippable, deployable, and reversible with `make rollback` (`Makefile:892-917`;
D3, gate G5, [02](02-decision-record.md) §2.4) to the previous build, **bounded below by the stage's
Teach build** once its adopt step has run on the store (02 N13, R2-1, §9.2.1). Deploys use the
operator's recipe (`Makefile.local.example:88-110`, the `deploy` target, which is not committed); the
CI-checkable proof of G5 is the rollback rehearsal `make compat-rehearsal` (§9.10), not a deploy
target. This file answers old Q5 (§9.6) and defines the one compatibility window (§9.2.1). It does not
design the core contracts (05-*, 06, 07); it says in what order ABO adopts them and what stays
compatible.

**Stage numbering (changed in v1.1.0).** Gin is never retired (cross-cutting resolution 2), so old S10
("native books API; gin retired") is removed and old S11 (final cleanup) is renumbered **S10**. Two
stages are inserted without renumbering the rest: **S2b** (core opens the store; books on claims,
OPS-12) and **UI-0** (UI groundwork before S4, FE-2/FE-12). S0–S9 keep their v1.0.0 numbers, so
"09 S4" etc. in other files still point at the same stage. The dispositions' "S0–S11" means this
whole sequence (S0–S10). A stage may ship as several **releases** (r1, r2, …; §9.4.1); 12's split
readiness criteria are named **SC1–SC10** so that "S5" always means this file's stage (MOD2-21).

Out of scope: TV/Movies (10), multi-app (11), the repo split (12). The deferred-build packages
(`acquire`, `arrfacade`, `integrations/indexer`, the download grab/monitor pipeline, non-Deluge
adapters; 03 §3.4) are built in **no** stage; neither is the facade-only key machinery
(`PurposeFacade`, query-key extraction, `FacadeGroup`; SIMP2-6). The storage-efficiency program that
owns `storage_format` 2 (`internal/database/storage_format.go:44-50`) is a separate track; this
sequence never raises `SupportedStorageFormat` (§9.2.2). **Ordering rule (OPS2-10):** that program's
release B ships only as a books `DataMigration` on core (S2b or later); if it ships earlier, 05-storage's
legacy projection constant (`LegacyMaxFormat + 1`) moves to cover it in the same release, so no
pre-guard build can accept a raised store's projection.

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
| 13 permission atoms; 4 seed roles **rewritten every boot**; admin checked by role **name**; an API key with **empty** scopes holds all of its owner's role permissions | `internal/auth/permissions.go:24-85`; `seed.go:80-129` (called at `server_lifecycle.go:1102`); `middleware/auth.go:405-408,522-537` |
| Legacy access-widening paths a rollback target must not keep (R2-1): `PATCH /api/v1/auth/api-keys/:id {"status":"active"}` re-activates any key, revoked included, and the auth path checks only `Status`; legacy `users.manage` mints temp-logins and sets any user's password with no dominance bound; `settings.manage` changes sign-in keys with only an interactive session | `handlers/apikeys.go:336-372` → `pebble_store_auth.go:1222-1247`; `middleware/auth.go:335-344`; `auth_temp_login.go:225-227`; `handlers/auth.go:581-616`; `internal/config/update_service.go:779-783` |
| Config: ~251 viper defaults, `AutomaticEnv` with no prefix, the `BindEnv` aliases (count generated from 05-config §1.12.4's alias table); whole config persisted as one flat `config_blob` + 4 encrypted secret rows | `internal/config/config.go:1048-1912,2621`; `cmd/root.go:625`; `persistence.go:884,1996-2008` |
| Shipped deploy files: unit `Type=notify`, `TimeoutStartSec=300`, **no `TimeoutStopSec`, no `KillMode`**, `Restart=on-failure`, `StartLimitBurst=5`/60 s, sets `HOME`, `GOMEMLIMIT`, `GOGC`, DB path only from `Environment=DATABASE_PATH`; compose sets `BASIC_AUTH_*`, no `stop_grace_period`; plist sets `DATABASE_PATH`, no `ExitTimeOut`; Docker `CMD … --db /data/audiobooks.pebble`, HEALTHCHECK `/health` | `deploy/audiobook-organizer.service:81-112`; `docker-compose.yml:18-22`; `deploy/com.audiobook-organizer.plist:36-43`; `Dockerfile:100-104` |
| Env names in shipped files that nothing reads: `AUDIOBOOK_ROOT_DIR` (unit, plist; deliberately unread), `AO_DIR`, `AO_DB` (compose), `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`) | `internal/config/config.go:2569-2575`; no Go reader at the evidence commit |
| Warmup is 130–200 s against a hard-coded 300 s in `TimeoutStartSec`, `deploy-wait-ready.sh` default and `make rollback`; HEAD extends the start timeout per startup step (store open, each migration, index open) | unit `:28,87`; `scripts/deploy-wait-ready.sh:13`; `Makefile:916`; `internal/database/startup_notify.go:15-27`, `internal/search/register.go:55` |
| Both Dockerfiles copy only `go.mod go.sum` before `go mod download && go mod verify`; `.dockerignore` excludes only top-level and `web/` `node_modules`/`dist` | `Dockerfile:54-55`; `Dockerfile.build-cgo:52-53`; `.dockerignore:18-20` |
| Prereleases skip Docker; no PR job builds the image; the first image of a version is built by the production release | `.github/workflows/prerelease.yml:48`; `release-prod.yml:53` |
| `npm ci` runs in 12 places, none of which builds anything first | `Dockerfile:18`, `Dockerfile.build-cgo:17`, `prerelease.yml:51`, `release-prod.yml:58`, `ci.yml:379`, `e2e.yml:167`, `binary-smoke.yml:55`, `security.yml:82`, `vulnerability-scan.yml:62`, `.woodpecker/checks-build.yaml:85`, `scripts/ci_remote.py:516`, `Makefile:194` (`web-install`) |
| Updater: downloads `releases/latest`, looks for a raw asset `audiobook-organizer-<os>-<arch>`, deletes `.old` after the swap, restarts with `os.Exit(0)` — under `Restart=on-failure` systemd does not restart it; the committed unit installs the binary into a root-owned system directory with `sudo cp` and runs as `User=audiobook` with `NoNewPrivileges=yes`, so the updater cannot write next to the executable | `internal/updater/updater.go:254-258,278,295,318-326,331-334`; `deploy/audiobook-organizer.service:41-43,88,122` |
| Live restore extracts over the running store | `internal/backup/backup.go:777-870` |
| Web client: `API_BASE = '/api/v1'`, no build/version check; 5 hard-coded permission constants and a legacy-atom scope picker; 52 source files import `react-router-dom` | `web/src/services/api.ts:24`; `hooks/useLibraryPermission.ts:9,11`, `hooks/useCanOrganizeLibrary.ts:9`, `hooks/useCollections.ts:24`, `components/system/ThrottlePanel.tsx:24`, `components/settings/APIKeysTab.tsx:49-61` |
| **Releases and merges are not CI-gated (OPS2-1).** Main merges without a pre-merge gate (CI runs afterwards and auto-reverts); an RC is cut on every push to main with no dependency on CI; `check-rc-ordinal` **auto-dispatches the production release** when a base version reaches 10 RCs, which builds the image and moves `releases/latest` (followed by the updater and `:latest` users); installs jump straight to `releases/latest` | `.github/workflows/auto-revert.yml:8-11`; `prerelease.yml:7-13,59-131`; `release-prod.yml:43-65`; `updater.go:295` |

## 9.2 Compatibility model (used by every stage)

### 9.2.1 The one compatibility window (SIMP-7)

This section is the **only** definition of the window. `compat` implements it once (§9.2.9); httpapi
aliases (05-http-api §4.6), config and env aliases (05-config §1.12.4), permission atoms (05-rbac
§6.3), task kinds (05-tasks), SSE projections (05-realtime) and metric aliases (05-metrics §13) all
register against it and define no window of their own.

Every renamed or moved surface goes through three steps, **never two in the same deployed build**:

1. **Teach** — the **first build of every adopt/expand sequence is a Teach build** (R2-1, 02 N13). It
   (a) learns to *read* (and preserve on rewrite) what the expand build will leave in shared state,
   (b) ships the **legacy-side guards** a rollback target needs — legacy handlers refuse status changes
   on revoked or mirror-disabled API keys, legacy `users.manage`/`settings.manage` handlers apply the
   dominance bound (including the `settings.manage` bound on integration URL and secret writes, which carry
   `core:integrations.manage` on rollback — 05-authn §11.1 item 3), and every revocation and restore it performs is journaled outside the database
   (05-backup §5B.3.6) — and (c) is the **minimum rollback target**: the sequence's adopt step stamps the
   Teach build's level as its `Floor` (05-storage §5S.3.7), so from then on the guard refuses every older
   build. §9.2.2 names each sequence's Teach build. The live-defect hotfixes filed in
   `todo.d/2026-11-04_platform_core_followups.md` are the same legacy-side changes and may ship
   earlier, as early as S0 (§9.4 S0 "Hotfixes").
2. **Expand** — the new form becomes truth; the old form is still written (data) or still served
   (API, config, metrics). Rollback to any build from the Teach build on is always safe.
3. **Contract** — after **W**, and only through a contract PR, the old form stops being written or
   served (410 or refusal for one more production minor), then is deleted. Nothing is removed or
   turned into 410 by a clock (OPS-10): `Sunset` headers are advisory.

**W** requires (a) **two production minor releases and 30 days** after the expand shipped — minors are
auto-promoted every 10 RCs (§9.1), so (a) alone can be as short as about 20 merges and the 30-day floor
always applies — and (c) **no recorded use for 14 consecutive days**, read from the persisted usage
record (OPS-16): each surface's handle writes `{count, last_used_at}` to the store in batched flushes,
exposed by `GET /api/core/v1/compat/usage` and the gauge
`mediacore_compat_legacy_last_use_timestamp_seconds`. The process-local counter
`mediacore_compat_legacy_use_total` stays for dashboards only; a restart cannot reset the gate. The
usage gate is sound only if it can see every use (OPS2-7):

- **Boot-read surfaces** (`config` and `env` aliases, resolved in 06 P0 before storage exists) record
  `Handle.Present(bootID)`: P0 buffers the uses and the kernel flushes them after the store opens; the
  record keeps `last_seen_set_boot` and `boots_seen`. Their gate is "not set on any boot within the last
  14 days, **and** at least one recording boot in that period" — a name set in a unit that is never
  restarted is not "unused". Env and file aliases record surface `env`, DB/blob aliases `config`
  (05-config).
- For **every** surface, the 14 days also require that the served ledger shows a recording
  (guard-aware, usage-recording) build serving the store continuously; days run by a non-recording
  build (a rollback to a pre-S2b build) do not count.
- (c) is evaluated on the **reference production store** (P8). On every other store, the contract
  build's first boot reads that store's own usage record; a removed or `Gone` surface used there within
  14 days reports Degraded `legacy_surface_removed_in_use` (surface and name) with a Settings banner, and
  the release notes list every removed surface (OPS2-19).
- After W, every window alias of a **secret-class** key is treated like a `ProtectAdmin` key: boot
  refuses it and names the canonical variable, instead of ignoring it (§9.2.5, §9.8).

Exemptions from (c): metric aliases, because a scrape reads every family and the server cannot see
which name a dashboard uses (05-metrics §13; their gate is P6), and surfaces marked **permanent**
(§9.6).

**Unit of compatibility = the store, not the release (OPS-2).** Installs skip releases (the updater
fetches `releases/latest`, Docker users pull `:latest`, a backup from any build can be restored), and
`make rollback` returns to `.prev`, which may be any main build. Therefore every contract step checks
**on the store** that its expand ran and has served the store long enough (§9.2.2); a store that never
adopted runs the adoption first and never a contract delete (cross-cutting resolution 6). Skipping is
bounded by the **minimum-upgrade-from release** (§9.2.2, 02 N14).

**One flip PR per stage (OPS-18).** A stage's native code lands dark across many PRs; exactly one
**flip PR** makes it truth, bumps the build's reader level, and is recorded in the ledger. An AST
ratchet, `TestAdoptedPrefixWritesGoThroughMirror` (precedent `TestOpsV2RowWritesGoThroughStageOpRow`),
forbids direct `Set`/`Delete` on an adopted legacy prefix outside the mirror writer from the flip PR
on, so an RC or production build cut from main between PRs never splits truth.

**Release gating during the extraction (R2-6, OPS2-1, 14 Q-I).** Because merges are not CI-gated and
production minors are auto-promoted (§9.1), the flip-PR, contract and ratchet checks above would run
only after the code shipped. S0 makes them real, in two layers (owner choice recorded in 14 Q-I;
recommended and specified here):

1. **Required check on gated paths.** One always-reporting required status, `extraction-gates`, runs the
   gate set — `make ci`'s subset for the touched modules, `TestAdoptedPrefixWritesGoThroughMirror`,
   `TestCompatLedgerMatchesCode`, `TestNoContractBeforeSunset`, the ratchets, the per-stage MOD-2 gate
   (04 §2.1 (c)–(f), generated from `go list -deps -test -json`, R2-5), the Docker image job and
   `make compat-rehearsal` — whenever a PR touches `docs/compat-surfaces.md`, a `RaisesLevel` or adopt
   step, a `Mirror` declaration, `core/`, `go.mod`/`go.sum` or a Dockerfile (such PRs carry the label
   `compat-gate`), and passes trivially otherwise, so the rest of the repo keeps ungated merges.
2. **RC promotion gate.** `check-rc-ordinal` refuses to auto-promote when the RC commit's CI is not
   green, when `compat-rehearsal` is red, or while the ledger has a stage in state "dark PRs landed,
   flip not landed". The operator, not the RC counter, chooses the production release that carries a
   flip.

### 9.2.2 The store guard: reader levels per owner (SIMP-18, OPS-3, OPS-26)

There is **one** guard mechanism — per-owner reader levels, which replace both the storage-format
guard and v1.0.0's separate compat-level guard — owned by [05-storage](05-storage.md) §5S.3.7: level
stamp `c/storage/level/<owner>`, sidecar `<db>.storage-levels`, served ledger
`c/storage/served/<owner>/<level>`, fast exit `--print-storage-levels`, `CheckReader` (one decision
for open, `make rollback` and the updater), and books' **legacy projection** onto
`preference:storage_format` / `<db>.storage-format`, which makes every pre-guard build refuse a store
once any level was raised. This file owns the **policy**: which level each stage's build serves, which
build is each sequence's Teach build, and which step raises a store's level. A store's level rises in
exactly two ways (SIMP2-14, R2-1, R2-3): an **adopt step** (`Additive`) stamps its sequence's **Teach
floor** (`Floor` = the Teach build's level), and a **contract** (`Breaking` with `RaisesLevel`) raises it
to the expand build's level. Every claim-mode change is one of these two stamped level steps
(05-storage §5S.3.4). The storage-efficiency release B (books owner) is a separate track.

**Levels are reader capabilities** (OPS-3): a build declares a level as soon as it can read the shape
that level guards, long before any store is raised to it. All levels below are owner **`core`** (core
subsystems share one level); owner `books` stays at 0 throughout this sequence. The single metric is
`mediacore_storage_level{owner,kind}` (05-storage §5S.8; OPS2-24).

| `core` level | First build | What every build at this level can read |
|---|---|---|
| 0 | before S0 | — (pre-guard: reads only the legacy projection, and refuses a raised projection) |
| 1 | S0 — **Teach build of the config/secrets sequence** | knows the guard; tolerates `c/` and `m/` keys; writes `c/storage/install` first, then the served ledger; 05-config phase **T** (overlays and writes every legacy-format secret row, not only the 4) |
| 2 | S3 r2 (05-config phase X) | `c/config/` and `c/secrets/` as truth, with or without the legacy blob and rows |
| 3 | S4 r1 — **identity Teach build** (05-authn §11.1) | legacy identity rows as the mirror writes them (mirror-disabled sentinel scope, `mirror_reason`, decoy hash), preserved on rewrite; the legacy-side guards; the revocation journal |
| 4 | S4 r3 (identity flip) | identity from `c/auth/`, `c/rbac/` with or without legacy rows |
| 5 | S5 | tasks from `c/tasks/` with or without the legacy reconcile and mirrors (05-tasks §5T.6) |
| 6 | S8 | core `backup`: restores `mediacore-backup/1` archives including encrypted ones (05-backup §5B.3.3) |

**Teach builds and floors (R2-1).** Each adopt/expand sequence, its Teach build (the rollback floor once
the adopt step has run on a store), and the floor its adopt steps stamp:

| Sequence | Teach build | `Floor` | Adopt step(s) | Expand / flip | Contract → level |
|---|---|---|---|---|---|
| Config/secrets | S0 (phase T, 05-config §1.12.2) | 1 | `core/config#1` (X's import) | S3 r2 | C3 → 2 |
| Identity | S4 r1 (05-authn §11.1; carries the S3 keyring, so it is a post-S3 build) | 3 | `core/auth#1`, `core/rbac#1` (S4 r2, dark) | S4 r3 | C4 → 4 |
| Tasks | the S4 r3 build: S5 writes only legacy shapes older builds read (copy-and-mark statuses, mirrored clocks, pause and `Requires` stamps, OPS2-9), and every build from S4 r1 on has the legacy-side guards | 4 | `core/tasks#1` (T0+T1) | S5 r1 | C5 → 5 |
| Integrations | the S6 build (any level-5 build, S5 or S6, reads the blob that 05-metadata M1.4 step 2a keeps current) | 5 | `core/integrations#1` (S7 import) | S7 | C7 (aliases; no level) |

**Fresh installs (DATA2-28).** On the boot that creates `c/storage/install` on an empty store, every
owner is stamped at the build's supported level and every adopt and contract step is recorded as
applied (as HEAD stamps a fresh store at the build's format, `internal/database/storage_format.go:338-352`);
mirrors are never active on a fresh store, and backup encryption is not held back by the legacy restore
bound.

**Legacy restore bound** (05-backup B3): books declares `backup.LegacyRestoreBound{Owner: core,
Level: 5}`. While the store's `core` level is ≤ 5, some build that can still serve the store (S7 or
older) has only ABO's plaintext restore code, so backup encryption stays inactive; contract C8 raises
the store to 6 (§9.5), after which every build that can open the store restores encrypted archives.

**What the store records** (written by its own boots, never by a person; mechanism 05-storage):

| Record | Written by | Read by |
|---|---|---|
| install marker `c/storage/install` | the first guard-aware boot (S0 on), **before any other `c/` key** (DATA2-6; S0 writes it and the served ledger with `storage/keys`, the core tuple encoding, 04 §8.1) | 05-storage §5S.3.4 rule 4 (a later core-storage boot finds `c/` non-empty only with the marker); `TestS2bOpensStoreWrittenByS0` |
| level stamp + sidecar | an adopt step (`Floor`) or a contract (`RaisesLevel`), sidecar fsynced before the stamp's batch commits; on a commit **error** the sidecars are rewritten from the stamps (OPS-26b) | open (refuses a store above the build's level), `make rollback`, the updater's `ReaderCheck` |
| adopt-step applied record `c/storage/ks/<owner>/applied/<step>` (owners `core/config`, `core/auth`, `core/rbac`, `core/tasks`, `core/integrations`; step names `core/auth#1` etc., DATA2-29) | the first boot that ran the stage's adopt step (S3, S4, S5, S7) | the stage's contract (`Adopts`, cross-cutting 6) |
| served ledger | every guard-aware boot (S0 on) | contracts (`RequiresServed`, `RequiresSelfServed`); 05-config's blanking rule |

**Contract rule (OPS-2, cross-cutting 6).** Every contract declares `Adopts: [<the stage's adopt
step>]` and `RequiresServed{Owner: core, Level: <expand level>, For: 30 days}` (the W floor; the
minor-release count and the usage gate are enforced on the PR, §9.2.1). `RequiresServed` is met only by
the latest **uninterrupted run** of boots by builds whose supported level is ≥ the expand level; a boot
by a lower build, or a detected foreign open, ends the run and restarts the clock (DATA2-9, 05-storage;
`TestContractClockResetsAfterRollback`). A contract also never runs on a boot where an update is pending
confirmation, nor on a build whose own served record is younger than `max(core.updater.confirm_after,
1h)` of continuous readiness (`RequiresSelfServed`, implicit on every `RaisesLevel` step, OPS2-2), so the
updater's `Boot` and `make rollback` can still undo a broken contract release; health reports
`storage_contract_pending` with the earliest run time from the first boot on. On a store where the adopt
record is missing, the build runs the adopt step first (it stays registered in every later build); on a
store where the expand level has not served long enough, the contract is **deferred**: the build boots
in mirror phase and retries the contract on later boots — it never deletes, and it never fails boot for
this reason.

**Minimum-upgrade-from release (R2-4, 02 N14; replaces v1.1.x's "no stepping-stone release").** Skips
are bounded, and the bound is explicit:

- Books' legacy migrations (`db_version` 1..66) run in a migration phase **before** core claims are
  enforced, under an explicit, logged legacy-migration grant (05-storage §5S.3.5); core adopt steps that
  take families a legacy step writes declare `After: {books, 66}` (DATA2-2).
- Their code, and every store method they call (steps 62/65/66 call `MigrateOpsV2ToV3` and the opv2
  sweeps), is **kept until the minimum-upgrade-from release**; 05-tasks T8 may delete it only from that
  release on.
- Every release manifest carries `min_upgrade_from`. Two releases set it in this sequence: the first
  release containing 05-tasks T8 names **the last production release before it** (it still carries
  legacy steps 59–66 and runs them before claims, so a store passing through it reaches books v66), and
  S10 names **the newest release that still carries the mirror code of every contract S10 deletes**. A
  store older than the bound (books legacy track < 66; or a contract S10 no longer carries mirror code
  for has not run on it) is refused at boot with `ErrUpgradeFloor{InstallFirst}` naming the release to
  install; the updater refuses a release whose `min_upgrade_from` is newer than the running build,
  and the Docker entrypoint prints the same message (05-updater). Releases newer than the minimum may
  be skipped.
- Only a build that no longer registers an adopt step would refuse an unadopted store
  (`ErrAdoptMissing`); adopt steps stay registered in every build (05-storage §5S.3.11), so in this
  sequence that error is reached only past a `min_upgrade_from` bound.

**Rollback tooling.** `--print-storage-levels` prints the build's supported vector; `--print-storage-format`
stays for pre-guard scripts and prints the projection. `scripts/storage_format_guard.py` (name kept)
implements `CheckReader`'s table for `make rollback`, refuses while a staged restore is pending, and
prints both values when a stamp and its sidecar disagree (OPS-26c). **Owner action in S0:** add the
literal sudoers line for `<db>.storage-levels` next to the existing ones
(`docs/system/deploy-and-gpu-ops.md:208-218`); without it every rollback refuses (OPS-26a).

**Disk headroom (OPS-25, DATA2-18).** Every stage whose first boot takes a pre-migration checkpoint
(S2b, S3, S4, S5, S7, each contract) checks free space ≥ live store size first and fails closed with a
message. A checkpoint is consumed by any later boot once its own boot completed every step, it has aged
`checkpoint_min_age`, and the served ledger shows a boot after completion; unconsumed checkpoints that
are ancestors of a consumed one are pruned (mechanism, metric `mediacore_storage_checkpoint_bytes` and
the alert snippet that ships with S2b: 05-storage).

### 9.2.3 Data: one adoption model per family group (cross-cutting 5)

| Family group | Model during W | Mechanism owner |
|---|---|---|
| Identity: `u:`, `role:`, `apikey:`, `inviteallow:`, `oauthid:` and the auth `idx:` sub-prefixes except `idx:sess:user:` and `idx:invite:username:` | **Legacy rows stay authoritative.** Every native write stages the legacy primary row **and every legacy index row** the previous build resolves through (DATA-5) plus the core row in one batch; every boot **rebuilds the core copy from legacy**, with key status = the **most restrictive** of legacy and side record (revocation is monotone, DATA2-3); native-only data lives in core side records keyed by the legacy ID. **Every revocation reaches the legacy row in the same batch as the native one, including post-restore re-application** (R2-2), so the next boot's rebuild keeps it | [05-storage](05-storage.md) §5S.3.11 (mirror + rebuild), [05-authn](05-authn.md) §11, [05-rbac](05-rbac.md) §6.3 |
| Sessions `sess:`, `idx:sess:user:` | **No raw mirror** (SEC-19): new sessions are hashed only; a presented legacy cookie is looked up in `sess:` with epoch, status, revoked and expiry checks and upgraded on use; the fallback matches nothing after 7 days. Cost: one re-login for sessions created after S4 if the install rolls back | 05-authn §11 |
| Invites `invite:`, `idx:invite:username:` | **not mirrored** (DATA2-8): legacy invites are read in place by presented token and deleted on accept or expiry (`AgingOut`; they hold raw tokens); native invites are hash-only and outside every mirror's native range; a rollback inside an invite's TTL voids invites issued by the new build (resend) | 05-storage §5S.9.1 row 4a, 05-authn §11 |
| Config and secrets: `setting:config_blob` + the legacy secret rows | Teach (S0) → Expand (S3 r2): import, and re-import keyed by value hash on every boot; mirror back to blob and legacy-format rows in the same batch; plaintext blanked only when the rollback slot reads the legacy-format rows (S3 "Plaintext") → Contract (C3) | [05-config](05-config.md) §1.12.2 |
| Tasks: ops families | copy-and-mark of non-terminal runs into `c/tasks/` (one batch per run) + boot reconcile of runs an older build created; clock, pause and `Requires`-stamp mirrors (stamps copied, never deleted, during W, OPS2-9); history read in place | [05-tasks](05-tasks.md) §5T.6 |
| Integration instances (Deluge, AI endpoints, metadata sources, SMTP, webhooks) | S7 import into the one instance store, then **hash-keyed re-import every boot and a mirror back to the blob keys** until C7 (URL and secret together); operator-layer values are an **in-memory overlay**, never persisted (SIMP2-5) | [05-metadata](05-metadata.md) M1.4 steps 2 and 2a, M1.10 |
| Books families (the rest of the 258, incl. `setting:`, `preference:`, `pref:`, `act:`, the live v1 ops families) | claimed in place, permanent | 05-storage §5S.9.1 |
| `c/outbox/`, `c/audit/`, `c/log/journal/` | new core rows only, no legacy twin; the previous build ignores them (S0 tolerance) | 05-events, 05-rbac, 05-logging |

**Ownership is per stage (R2-3, DATA2-1).** 05-storage §5S.9.1 is the one ownership table; it states for
every row the owner and mode *Before* its hand-over (books `ReadWrite`), the *Hand-over* adopt step, the
mode *After*, and the contract. Read by stage, the claim-mode changes this sequence makes are:

| Build contains | Rows of 05-storage §5S.9.1 whose claim changes on a store when the step runs | Step (stamped level step) |
|---|---|---|
| S2b | none: books' generated claims equal *Before* for every row (books `ReadWrite` everywhere except row 10) | — |
| S3 r2 | row 7 (`setting:config_blob`, legacy secret rows) → `core/config` Migrating | `core/config#1`, `Floor` 1 |
| first journal-only build ≥ S2b | row 14 (`syslog:`) → `core/log` AgingOut | `core/log#1` (05-logging) |
| S4 r2 | rows 1–6 and 8 (identity, sessions, invites, bootstrap) → `core/auth` / `core/rbac` | `core/auth#1`, `core/rbac#1`, `Floor` 3 |
| S5 r1 | rows 9, 11, 13a, 13 (clocks and pause, ops run families, `Requires` stamps, ops history) → `core/tasks` | `core/tasks#1`, `Floor` 4 |
| C3 / C4 / C5 | the same rows → `Retired` (C5: rows 11, 13a → `AgingOut`) | contract, `RaisesLevel` 2 / 4 / 5 |

A build resolves each claim from its own steps **and this store's bookkeeping**, so one build serves a
store before and after adoption; books' claim list for a build is 05-storage's *Before*/*After* for that
build's stage (S2b `TestBooksClaimsGenerated`).

Rules that apply to every row:

1. **Never widen on rollback (SEC-1).** A native API key is mirrored with legacy atoms only when they
   re-expand exactly to its canonical scopes; otherwise its legacy row is written **disabled** —
   `status:"revoked"`, a sentinel scope that maps to no atom, a decoy token hash and `mirror_reason` — and
   the real scopes live in the side record (05-rbac §6.3, 05-storage §5S.3.11). The Teach build refuses
   any status change on such a row (§9.2.1). A role may be mirrored narrower (an empty legacy role grants
   nothing). A legacy empty-scope key is adopted as `LegacyFullScope` only when it has no side record and
   no `mirror_reason`, and is mirrored back unchanged.
2. **A mirror-disabled key must survive the rebuild.** The rebuild drops a side record whose legacy
   row is gone or was revoked **by an older build**, but keeps one whose legacy row the mirror itself
   wrote disabled and the previous build has not rewritten since (05-storage §5S.3.11 `MirrorDisabled`
   + `Fresh`); a non-`Fresh` mirror-disabled row rebuilds as **revoked**, never by following legacy.
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
  `http`. A legacy path that multiplexes on a body or query field names its targets and selector, and
  every legacy request field decodes strictly (an unmapped field is 400 `legacy_field_unsupported`, never
  dropped; SEC2-5); a path that cannot be mapped without losing meaning stays a gin route. It becomes
  `Gone` only through a contract PR.
- ABO's gin engine is mounted **permanently** (cross-cutting 2, SIMP-2). `/api/v1/...` books routes
  are a permanent legacy prefix, not a surface with a sunset; a books route group moves to
  `/api/books/v1/...` only when it is rewritten for its own reasons, and then its old paths become
  aliases. The ABS-compatible unversioned paths and the unversioned `/api/*` → `/api/v1/*` redirect
  (`server_lifecycle.go:1790-1822`, carried by the legacy mount's `Redirects`) are permanent.
- Only **core** surfaces (auth, users, roles, keys, config, tasks, events, system, backup, update,
  health aliases) go through alias → `Gone` → delete.

### 9.2.5 Config keys and environment names

The mechanism and the full alias inventory are 05-config §1.12.4, which is also the **single list** of
permanent and runtime names (OPS2-21). Policy:

- **Window aliases**: the `BindEnv` names, the bare upper-case form of every flat key that
  `AutomaticEnv` made settable, the `ABK_*` names, and every flat YAML/blob key (counts generated from
  05-config's alias table).
- **Permanent aliases (OPS-5)**: ecosystem names (`OTEL_EXPORTER_OTLP_*`, `OPENAI_API_KEY`,
  `OPENAI_BASE_URL`) and **every name ABO reads today that a shipped deploy file sets**:
  `DATABASE_PATH` (unit `:98`, plist `:40` — the only DB-path source in both), `FP_PARALLEL_WORKERS`
  (unit `:108`), `BASIC_AUTH_ENABLED`/`USERNAME`/`PASSWORD` (compose `:20-22`), and the names in
  `deploy/local.conf.example` that ABO reads. Runtime and ecosystem names that config does not read
  (`HOME`, `GOMEMLIMIT`, `GOGC`, `TZ`, `NOTIFY_SOCKET`) are 05-config's third class.
  `TestShippedDeployEnvNamesArePermanent` scans `deploy/*.service`, `deploy/*.plist`,
  `deploy/local.conf.example`, `docker-compose.yml` and `Dockerfile*`, commented `Environment=` lines
  included.
- **Not compat surfaces**: names a shipped file sets that nothing reads (`AUDIOBOOK_ROOT_DIR`,
  deliberately unread at `config.go:2569-2575`; `AO_DIR`, `AO_DB`; `OLLAMA_BASE_URL`). Honouring them
  would change behaviour; they are deploy-file defects that S3 fixes (§9.4 S3 "Deploy files").
- After W, a window alias of a key at `ProtectAdmin` or higher (database path, sign-in, server
  paths) or of a **secret-class** key is **retired**: boot refuses to start if it is set and names the
  canonical variable; storage refuses to *create* a store at the resolved path when none exists there
  and a retired database-path name is set (05-storage). Other legacy env names after W are ignored with
  one boot WARN. An alias **conflict** (legacy and canonical both set, different values) on a
  `ProtectAdmin`+ key is fail-closed and names both variables (05-config §1.3.5, OPS2-12).
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

**Auth-off exceptions (SEC2-21; 05-rbac §6.2).** One deliberate tightening is recorded instead of an
identical reach: with auth off, HEAD's `s.perm` passes every `settings.manage` system route
(`wire_admin_role.go:18-22`); their successors under `core:system.manage` (`AuthRequired`) answer **401**
with auth off — status detail, storage and system info (`/api/core/v1/health`, `/system/info`) and the
log journal query and live tail (05-logging) — while `GET /api/core/v1/system/status` (readiness tokens
only) stays open, as today, and the read-only admin introspection routes keep their auth-off reach.
The auth-off half of `TestLegacyRoleReachUnchanged` excludes exactly the aliases of the routes 05-rbac
§6.2 marks tightened, and asserts 401 for them.

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
key: `core.tasks.kinds.<kind>.parallelism_cap` (≤ the declared value; 05-tasks owns the key, SIMP2-15),
`core.outbox.relay_enabled`, per-provider throttle overrides (05-metadata M2),
`core.storage.claims.mode` (`shadow`/`strict`, 05-storage), `core.updater.mode` and
`core.updater.allow_unsigned_legacy` (05-updater). Existing switches are untouched: `abs_api_enabled`,
`enable_auth`. Per-entry data such as a webhook instance's `legacy_headers` is data, not a flag.

### 9.2.9 The ledger and `compat.Ledger`

`docs/compat-surfaces.md` (the existing shim ledger, v1.1.0) gains a section per stage; each row:
surface, legacy name, canonical name, flip PR, shipped release and date, earliest removal date, gate
(`usage` | `p6` | `permanent`), state (`aliased` | `gone` | `retired` | `removed`), removal PR. The ledger
row is the **only** place that holds a surface's class, gate and dates (SIMP2-12): subsystem alias types
(05-config `Alias`, 05-http-api `LegacyPath`, 05-metrics aliases, 05-tasks `FormerKinds`) carry only
`Ledger string`, and the `Sunset` header is rendered from the ledger embedded in the binary (one
`//go:embed` of `docs/compat-surfaces.md`, or a generated table). The code side is one value per App:

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
	Ledger    string // row ID in docs/compat-surfaces.md; gate, state and dates come from that row
}

// Ledger is built by the kernel before listeners open; registration after that fails.
type Ledger struct{ /* unexported */ }

func (l *Ledger) Register(e Entry) (*Handle, error) // ErrDuplicate, ErrUnknownSurface, ErrNoLedgerRow
func (l *Ledger) Usage(ctx context.Context) iter.Seq2[Usage, error] // what GET /api/core/v1/compat/usage serves

// Use records one legacy use on a request path: increments mediacore_compat_legacy_use_total and the
// persisted {count, last_used_at}, flushed in batches (≤ 1 write per surface/name per minute). It never
// fails a request (§9.8).
func (h *Handle) Use(entry string)

// Present records that a boot-read surface (config/env alias) was set on this boot; uses made in P0
// are buffered and flushed once the store is open (OPS2-7).
func (h *Handle) Present(bootID string)

type Usage struct {
	Surface         Surface
	Legacy          string
	Count           uint64
	LastUsedAt      time.Time
	LastSeenSetBoot string // boot-read surfaces only
	BootsSeen       uint64
}
```

`TestCompatLedgerMatchesCode` (root module) compares the ledger file with every `Entry` registered at
boot; `TestNoContractBeforeSunset` fails a build whose code removes an entry before its earliest
removal date; its runtime twin (P8) reads `LastUsedAt`/`LastSeenSetBoot`.

## 9.3 Stage overview

| Stage | Name | Build `core` level | Native truth introduced | Contract (≥ W, §9.5) |
|---|---|---|---|---|
| S0 | Groundwork, guards, gates, build paths; config Teach build | 1 | — (install marker, served ledger) | — |
| S1 | Core module skeleton, leaf packages; staged restore | 1 | — | — |
| S2 | Composition root; perimeter; `books` wraps the server (r1–r3) | 1 | — | — |
| S2b | Core opens the store; books on claims (shadow → strict) | 1 | — | — |
| UI-0 | UI groundwork: react-router 8, generated-client runtime | 1 | — | — |
| S3 | Config and secrets (r1 façade; r2 05-config phase X) | 2 | `c/config/`, `c/secrets/` | C3 → 2 |
| S4 | Identity (r1 Teach build; r2 dark adoption; r3 flip): auth, RBAC, audit, first native core routes | 3 → 4 | `c/auth/`, `c/rbac/`, `c/audit/` | C4 → 4 |
| S5 | Tasks | 5 | `c/tasks/`; task kinds `books.*` | C5 → 5 |
| S6 | Events, outbox, realtime | 5 | `c/outbox/`; native SSE | C6 (aliases only) |
| S7 | Integrations, library, search index, notify (mail + webhooks) | 5 | instance store `c/integrations/` | C7 (aliases only) |
| S8 | Backup, updater, health, metrics registry | 6 | backup format (05-backup); core metric names | C8 → 6 |
| S9 | UI shell | 6 | `/books/...` SPA routes; shell build | C9 (UI redirects) |
| S10 | Final cleanup (was S11) | 6 | — | — |

Order: S0 → S1 → S2 → S2b → S3 → S4 → S5 are strictly ordered (R4 layers and data dependencies);
UI-0 runs **after S2** (`internal/openapi` imports `httpapi`, and the client is generated from the
`core.json` that the S2 hello skeleton emits) and must ship before S4; it may overlap S2b and S3. S6, S7,
S8 may overlap only if they touch disjoint package groups (CLAUDE.md "never launch overlapping waves");
S9 needs UI-0 and S3–S8 done. A stage ships as one or more production releases; its flip PR is in
exactly one (§9.2.1).

**Every stage's "Done when" also includes this section's boot budget and R-test** (OPS2-16); the stage
tables repeat only stage-specific checks.

**Boot-time budget (OPS-22).** `deploy-wait-ready` reports ready within the **S1 baseline + 10 %** on a
production-size restored copy (S1 records the baseline), and `mediacore_kernel_boot_phase_seconds{phase,
step}` attributes the time per boot phase of 06 §6.5.1 (`P−1`, `P0a`, `P0`–`P3`, `P4a`–`P4d`,
`P5`–`P8`) and per bounded migration/mirror/adoption step name (emitted from S2; OPS2-24). S0 turns the
hard-coded 300 s into a `DEPLOY_READY_TIMEOUT` variable in `Makefile` (`rollback`) and
`Makefile.local.example` (`deploy`), default 300, which bounds only the time after the unit leaves
`activating` (OPS2-17).

**Rollback rehearsal (OPS-20, OPS-21).** The R-test (§9.10) with `prev` = `PREV_REF` (§9.10). Cells a/b
(previous-release UI against cur, cur UI against prev) are mandatory in **every release that moves a UI
call** (any diff under `web/src/services` or the generated client); cell c (ABS conformance, plus a live
client when P7 is branch B) is mandatory in the **ABS-path stages S2, S4, S6 and S9** — the one list
used here, in §9.10 and in P7.

## 9.4 Stages

### S0 — Groundwork, guards, gates and build paths

| Aspect | Specification |
|---|---|
| Goal | Make every later stage buildable, reversible, gated and measurable, with no user-visible behaviour change; ship the config sequence's Teach build. |
| Toolchain | The **first PR of S0** is 13's toolchain bump to `go1.27.3`, moving every pinned copy together (13 §13.2.2); later commands use `make -C core <target>` or `$(make -s -C core print-toolchain)`, never a literal (MOD2-13). |
| Moves | 04 §8.1 row S0: `core/` with `go.mod` (03 §3.2: `exclude` of the monolithic genproto **only in `core/go.mod`**, `ignore ./web`, `ignore ./examples/hello/web`) and an **empty committed `core/go.sum`** (tidy keeps it; `check_module_skew.py` asserts it exists, MOD2-15), the `archtest` engine and `storage/keys`; root `go.mod` `require` + `replace`; `GO_MODULE_DIRS := . core`; `internal/arch/layering_test.go` redesigned per 03 §3.5.1 (ratio floor, `. → cmd` positive control, classifier control, core layers mapped onto ABO's, `loadGraph` keeping mediacore imports). |
| Gates (R2-6) | `extraction-gates` required check on the gated paths and the `check-rc-ordinal` promotion gate (§9.2.1 "Release gating"); `prerelease.yml` gains a Docker build without push (OPS2-14). |
| Docker (MOD-1, OPS-1, OPS2-14, MOD2-16) | Both Dockerfiles: `COPY go.mod go.sum ./` **and** `COPY core/go.mod core/go.sum ./core/` before `go mod download && go mod verify`, then `COPY . .` as today. The frontend stage copies the manifests and lockfiles of `core/web` and its members, runs `npm ci`, copies their sources and builds them in an explicit order, then does the same for `web/` (`file:` deps resolve to `dist/`). `.dockerignore` adds `**/node_modules`, `core/web/packages/*/dist`, `core/examples/*/web/node_modules`. A **PR CI job** runs on every PR that touches a non-doc file, with layer caching: it builds the **full** image (and `Dockerfile.build-cgo` with `--output type=local`), runs it with a seeded volume, and waits for `"ready":true` on `/health` and for `--print-storage-levels` inside the container. The ledger records which Dockerfile `reusable-release.yml` builds and whether the release publishes the raw updater asset (OPS2-4). |
| Web build entry points (MOD-9, FE-13, MOD2-16, MOD2-17) | One POSIX `sh` script, `scripts/ci/build_web.sh` (the frontend stages run on `node:26-alpine`, which has no `bash`): build `core/web` when present with an explicit workspace order (`npm run build -w <pkg> …`, never `--workspaces`, whose order is not topological; 07 §7.3.2), then `npm ci` + build in `web/`; behind `make web-deps`; every `npm ci` site of §9.1 calls it; `frontend-ci.yml` and `e2e.yml` path filters gain `core/web/**`, `core/go.mod`, `core/go.sum`; `ci.yml`'s `changes` classifier sets `web=true` for `core/web/*` and `scripts/ci/build_web.sh`; Dependabot npm `directories` gain `/core/web`, Dependabot gomod `directories: ["/", "/core"]` in one group producing one PR (or a workflow step runs the root tidy on Dependabot `/core` PRs, MOD2-6); every `cache-dependency-path` lists `core/web/package-lock.json` once it exists. CLAUDE.md's worktree recipe becomes `make web-deps`. |
| Module coupling (MOD2-6) | A change to `core/go.mod` carries the root `go mod tidy` result in the same PR; per-module `tidy-check` (`go mod tidy -diff` in `.` and `core`) in root `make ci`; `core-ci.yml` also runs the root `go build ./...` when `core/go.mod` or `core/go.sum` changes. |
| CI classification (MOD-8) | The table below; per-module baseline files; a per-stage checklist (shard weight files `short_test_weights.json`, `fixture_weights.json`, `go_test_timings.json`; `.mockery.yaml`; `tygo.yaml`; `ci/gates.txt` rows) run in every PR that moves a package. |
| Data/keys | Register reserved families `c/` (core) and `m/` (modules) in `keyfamilies.go`. Ship the per-owner guard (§9.2.2) in `internal/database` (S2b moves it into core `storage`); the build serves `core` level 1. The first guard-aware boot writes **`c/storage/install` before any other `c/` key** (DATA2-6), then each boot writes the served-ledger entry, both encoded with `storage/keys` (05-storage §5S.3.3), so the first core-storage boot (S2b) reads S0–S2 history and does not halt with `ErrForeignKeys`; HEAD-era builds ignore both (`TestLegacyToleratesNamespacedKeys`). **Teach** (config sequence): 05-config §1.12.2 phase T — ABO's legacy loader overlays and writes `IsSecret` rows `setting:<legacy-name>` for every secret field (inert until S3 writes them). No op-ID teach: under 05-tasks' copy-and-mark model the previous build never sees a `books.*` kind (OPS-7). |
| Hotfixes (R2-1) | The live defects filed in `todo.d/2026-11-04_platform_core_followups.md` may ship here, ahead of the identity Teach build that makes them a guarantee (05-authn §11.1): the legacy API-key status guard (no transition out of `revoked`; no `active` for a row with `revoked_at` or `mirror_reason`), the `users.manage`/`settings.manage` dominance bound (incl. integration URL/secret writes via `settings.manage`, 05-authn §11.1 item 3) in the legacy handlers, and the revocation journal. |
| Endpoints | `/api/*` redirect excludes `^/api/(core\|[a-z][a-z0-9]{1,15})/v[0-9]+(/\|$)` (`server_lifecycle.go:1798`); `TestNativePrefixesNotRedirected`; `TestCollidingNamespacesStillRedirect` unchanged. Every response carries `X-App-Build: <build id>` (OPS-20 teach); new fast exit `--version-json` (version and commit; §9.6 row 20). |
| Build ID and reload check (OPS2-13, FE2-5) | **One mechanism.** The build ID is generated once per `make build` (git commit plus build timestamp) and passed to both `go build -ldflags` and Vite (`define` and a `<meta name="mediacore-build">` tag); `dev`/empty IDs never trigger the check. The SPA compares `X-App-Build` of API responses with its own ID and offers a reload on mismatch; a **missing** header on an API response also counts as a mismatch (a pre-S0 `prev`). From S2 the core perimeter writes `X-App-Build` on every response, legacy mount, 404 and 410 included (05-http-api §8.1); 07 adopts the same ID as `ui_build_id`, and the S9 PR deletes this check in favour of the shell banner (07 §7.6.3). |
| Updater hotfix (OPS-6, OPS2-4, OPS2-5) | The HEAD updater's restart exits **75**, not 0 (`updater.go:331-334`), so `Restart=on-failure` restarts it; it keeps the previous binary as `<exe>.prev` instead of deleting `.old` (`:278`), so the updater and `make rollback` share one slot (05-updater). Because this updater has no guard, backup or boot-counter rollback, **scheduled** apply becomes check-only until S8: it notifies, health is Degraded `auto_update_paused_until_core_updater`, and the Settings UI shows a banner; a manual apply stays available behind the credential guard (owner may instead choose a minimal boot-counter rollback in the HEAD updater; §9.12 O9). |
| Deploy (OPS2-17) | `DEPLOY_READY_TIMEOUT` (§9.3); `scripts/deploy-wait-ready.sh` keeps polling while `systemctl show -p ActiveState` is `activating` (a long, extending start is not a failure) and applies the timeout only after that; owner action: the sudoers line for `<db>.storage-levels` (§9.2.2). |
| Rollback | Binary swap; the only keys written are `c/storage/install` and the served ledger, ignored by older builds. |
| Done when | `audiobook-organizer --print-storage-levels` prints `core 1`; the S0 image job green (full image, run, ready, levels inside the container); `make ci` exits 0 with the classification below; the genproto exclude check (as redefined, MOD2-5) and the throwaway `go work init . ./core && go build ./... ./core/...` step pass; `TestLegacyToleratesNamespacedKeys` (seeds `c/x`, `m/y` into a HEAD store and runs census, retention sweeps, backup, restore, boot) and `TestS2bOpensStoreWrittenByS0` pass; `scripts/test_storage_format_guard.py` covers the compat cases; `curl -s -o /dev/null -w '%{http_code}' <abo-url>/api/core/v1/ping` returns 404, not 301; an updater test (fake release server serving the HEAD asset name to a binary in a temp directory) asserts exit 75 and a kept `.prev`, and that scheduled apply is check-only (replaces the staging check, OPS2-4); `make rollback` on staging S0 → HEAD and HEAD → S0 passes the new guard and `deploy-match-unit-type.sh` runs (OPS2-16); a 12-minute fake migration under the unit is not killed (OPS2-17); `extraction-gates` is a required check and `check-rc-ordinal` refuses a red RC (scripted). |

`make ci` prerequisites (`Makefile:785`) and CI jobs after S0 (MOD-8, MOD2-18):

| Target / job | Runs in | Notes |
|---|---|---|
| `staticcheck`, `fmt-check`, `test-short`, `go vet`/`go build`, `fix-check` (`go fix -diff`), `tidy-check` | per Go module (`GO_MODULE_DIRS`) | `fmt-check` excludes `web/`, `core/web/` and `core/examples/*/web/`; root gains `fix-check` (03 §3.2 item 4) |
| `web-test` (was half of `test-all-short`) | per npm workspace: `web`, `core/web` | `test-all-short` = `test-short` + `web-test`, so neither runs twice |
| `lint-errcheck-ratchet`, interface-width job | per module, **separate baselines** (`core/.errcheck-baseline`, `core/.interface-width-baseline`) | one integer over root `./...` today; a move must not silently re-baseline |
| `lint-deadcode-ratchet` | root, with ABO's and hello's mains and `-filter 'github.com/falkcorp/mediacore/(internal\|kernel)'` (MOD2-19) | exported SDK packages are out of scope (as `pkg/` is today, `scripts/deadcode-ratchet.py:64`); no separate core baseline |
| `coverage-check-short` | per module: root floor `.ci/coverage-floor.txt`; core: a **ratchet floor** in `make -C core ci` from S2b; 12's SC5 thresholds run report-only in `make -C core readiness` (MOD2-8) | |
| `mocks-check` | per module: root `.mockery.yaml`; `core/.mockery.yaml` from the first core interface with a mock | |
| `pprof-check` | per module from S1 (`internal/diag/flight` tests move to `mediacore/health`) | |
| `oplint`, `sdkguard`, `ops-check`, `types-check`, `bench-check` | root only (`tools/cmd/oplint`, `tygo.yaml:28`) until their subject moves (oplint → `archtest/tasklint` at S5; sdkguard deleted with `pkg/plugin/sdk`) | `bench-check` is unaffected: no file of `cmd` moves (04 `cmd` row, MOD2-4) |
| `go-test-short-shards` (`scripts/ci/short_test_shards.py`) and Woodpecker `test-rest`/`test-native-taglib` | root module, plus one core shard | the weight files are updated in the PR that moves a package (stale entries fail, `short_test_shards.py:257-258`) |
| `nightly.yml` (`test-nightly`) | per module | core's non-short and property tests run nightly |
| `ratchet-lower.yml` | per module | learns core's baselines |
| `codeql.yml`, `memory-leak-scan.yml`, `ci.yml` `fixture-tests`, Woodpecker `test-database`, `test-fixtures-{1,2,3}`, `test-server-scanner`, `checks-build`, `checks-lint` | root (fixtures, scanner, database) / per module (CodeQL Go, leak scan, build, lint) | each row added to `ci/gates.txt` with its classification |
| `ci/gates.txt` `store-width`, `store-references` | root, until the S2b+ split deletes `database.Store`; retired in that PR with the reason recorded | `scripts/tests/test_run_gates.py` validates every go-suite gate |
| core layering gate, `TestCoreImportsOnlyCore`, genproto check | core only, registered in `ci/gates.txt` with `core-job`; core-owned check scripts live under `core/scripts/ci/` (MOD2-9) | |
| `extraction-gates` (required on gated paths), `compat-rehearsal` | root; `compat-rehearsal` runs on `compat-gate` PRs and nightly, is registered in `ci/gates.txt`, and blocks auto-promotion while red (OPS2-25) | §9.2.1 |
| `core-checkpoint` (`release-prod.yml`, `permissions: contents: write`) | prod release | when `core/` changed since the newest checkpoint, requires `version.Version` above it and records `refs/core-checkpoints/v0.Y.Z` (12 §12.4.1, MOD2-14) |

### S1 — Core module skeleton, leaf packages, staged restore

| Aspect | Specification |
|---|---|
| Goal | Prove the nested module with real code; move dependency leaves that have no data or API surface; fix the one defect every later rollback argument relies on. |
| Moves | **04 §8.1 row S1 and §8.2 S1 rows** (authoritative): `internal/logging` → `mediacore/log`; `internal/logger`'s `sanitize.go`/`attrs.go`/`retention.go` → `log` (the façade stays, SIMP-3); `internal/telemetry` → `mediacore/metrics` with namespace and scope as constructor options (its `ratchet_test.go` and `internal/telemetry/contract` stay in ABO as `internal/telemetrypolicy`, MOD-6); `internal/lifecycle` → public `mediacore/lifecycle`; `internal/security/{safepath,pathvalidation}`, `internal/libroot`, `internal/boundedcall`, `internal/diskstats`, `internal/util` `path.go`/`perms.go` → `mediacore/library`; `internal/diag/flight` → `mediacore/health`, **rewritten to take a `*slog.Logger` through its constructor** (it imports ABO's `internal/logger` today, `flight.go:108`; MOD2-11); `internal/logger/logtest` → `coretest`. **Stay in ABO** (04 §2.3): `internal/keyedmutex`, `internal/errhandling`, `internal/flight`. `internal/sdnotify` and `internal/startup` do **not** move here (their ABO importers are rewired at S2). |
| ABO-local fixes | 05-config stage 0: the 8 bypassing `config.AppConfig` writes go through `Mutate`, `TestNoDirectAppConfigWrites`. **Staged restore** (OPS-15): restore stops extracting over the live store (`backup.go:777-870`) and stages the archive for the next boot, per 05-backup §5B.3.4, implemented in `internal/backup` before it moves at S8; `make rollback` and the updater refuse while a staged restore is pending. |
| Deploy (OPS2-26) | The `deploy` recipe in `Makefile.local.example` refuses while `restore/pending.json` exists (override `DEPLOY_DISCARD_RESTORE=1`), like `make rollback`, so a deploy never silently discards a staged restore. |
| Data/keys, config, endpoints, permissions, UI | None changed. Metric and scope names byte-identical (namespace injected). |
| Rollback | Binary swap; no state. Older builds ignore a pending staged restore, so `make rollback` refuses while one is pending instead of leaving it to apply by surprise later. |
| Done when | `archtest.TestCoreImportsOnlyCore` (`go list -deps -test`) green; the sorted series-name set of `curl -s <abo-url>/metrics` equals the S0 build's (diff empty); `go build ./... && (cd core && go build ./...)` exit 0 with **the pinned toolchain** (OPS-24); restore rehearsal on a copy: stage, boot, store equals the archive; the **S1 boot baseline** (p50 of 3 boots on the restored copy) is written into the ledger (OPS2-16). |

### S2 — Composition root; core perimeter; `books` wraps the server

| Aspect | Specification |
|---|---|
| Goal | ABO's `main` becomes `app.New(ctx, opts, books.Module())`. Core owns process lifecycle, listeners, the typed services struct and the **perimeter**; ABO's whole gin engine is the books module's legacy mount, permanently. **Authentication and authorization do not change** (OPS-11, SEC-13). |
| Releases (OPS2-15) | **r1** composition root, lifecycle, listeners and supervisor timeouts (perimeter still in gin); **r2** perimeter move (05-http-api H0); **r3** logging stages 1–2. Each release carries the done-checks of its own rows below. |
| Moves | 04 §8.1 row S2: `internal/serviceregistry` registrations → `module.Registrar` calls inside `books.Module()` (N4; the package itself is deleted at S10); listener code (`server_lifecycle.go:903-1078`: TLS, HTTP/2, HTTP/3, port-80 redirect, peer allow-list) → `core/httpapi`; `internal/startup` → `kernel` + `health` and `internal/sdnotify` → `mediacore/internal/sdnotify`, **with every ABO importer rewired in the same stage** (04 §8.3, MOD2-1): `.` (the `--print-capabilities` fast exit) → `app`; `internal/server` (READY/STOPPING) → kernel lifecycle; `internal/database` and `internal/search` → the kernel's `ExtendStart` hook handed to books (05-health §5H.3.4); `internal/operations/registry` → `lifecycle.NotifySocketEnv`; `cmd`, `internal/server/handlers/system` tests → `health` constants / `coretest`. `internal/config` loader files → `mediacore/config`. **No file of `cmd` moves** (MOD2-4): `serve`'s store/OTel/slog bootstrap in `cmd/root.go` is replaced by `app.New`/`app.InstallProcessDefaults` calls and its flag wiring moves onto `app.Options`; the cobra tree stays in ABO. `main.go` stops calling the process-global setters `server.SetVersion`/`SetEmbeddedFS` (`main.go:32-34`). `examples/hello` skeleton lands (module, app, one route, `openapi` subcommand emitting `core/web/packages/api-client/openapi/core.json` with a core-CI freshness gate; 04 §8.1, MOD2-10, FE2-9). Until S2b the kernel runs **without storage** (no P2/P4; books opens its store in a start hook; `Deps.Storage` absent; 06 §6.5.1, COR2-9). |
| HTTP | `books` calls `httpapi.MountLegacy` with `Auth: LegacyAuthGin` (allowed only through `app.Options.LegacyAuthGinUntil`, which ABO sets here and removes in the S4 PR; SEC2-17), `ServesSPA: true` (FE-1: `ui.Handler` is **not** mounted; gin's `NoRoute` keeps serving the SPA at `/`; `app.Options.UI` is the zero bundle) and the route manifest (every gin route its own mux pattern; the 36 permission-less authenticated routes annotated `AnyAuthenticated`, R6; the legacy handler wrapper passes `r.Pattern` to gin and returns 404 when gin's matched route is not that manifest entry, SEC2-22). The perimeter moves to core and is removed from gin **in the same PR**: logger, recovery, security headers, CORS, Basic auth with today's per-route exemption, gzip, tracing, the `/api` rate limiter with per-route `RateClass` (05-http-api §8.1). **CSP (FE2-1):** the books mount supplies its CSP per request (`DefaultCSP func(*http.Request) string` = today's `contentSecurityPolicy()`, so the inline-script hash and the Access team host stay), and stage 5 writes `Reporting-Endpoints: csp="/api/v1/csp-report"`. **New**: cross-origin protection for cookie-authenticated unsafe requests (SEC-10); non-browser clients (no `Sec-Fetch-Site`/`Origin`) are unaffected and **no credential type is exempt** (FE2-23); refusals before S4 go to the access log with a `security_event` attribute, the audit write starts at S4 (SEC2-20). `client_trust.go` stays in ABO until S4 (gin's bootstrap, invite and ABS limiters keep calling `trustedClientIP`; SEC2-19). Gin's `RequireAuth`, permission checks and credential guards stay unchanged inside gin. |
| Lifecycle (OPS-13, OPS2-23) | Stop order of 06 §6.5.2 with one budget `core.shutdown.budget` = 25 s (05-health §5H.4; restart-bound, not hot-reloadable). Behaviour change listed: HTTP drain 30 s → `core.shutdown.http_drain` 10 s (`server_lifecycle.go:582`). Supervisor settings ship in this stage: `TimeoutStopSec=30` and **`KillMode=mixed`** in the committed unit (so operation-runner children are not signalled at t = 0), `stop_grace_period: 30s` plus `ABO_CORE_SHUTDOWN_BUDGET=25s` in `docker-compose.yml`, `ExitTimeOut` 30 in the plist, documented `docker run --stop-timeout 30`; in a container with the key unset the default budget is 8 s (under plain `docker run`'s 10 s). |
| Logging | 05-logging stage 1 (`app.InstallProcessDefaults`) and stage 2 (books shim over core handles). |
| Data/keys | None. Books migrations 1..66 keep their bookkeeping (`db_version`, `migration_<n>`) as the books legacy track; the store is still opened by books. |
| Config | Core config loader reads the **same** sources (YAML, env, `config_blob`) through a books-provided legacy source; canonical `core.*`/`modules.books.*` keys are accepted but undocumented. Flags `--port --host --tls-cert --tls-key --http3-port --external-url --read-timeout --write-timeout --idle-timeout --workers` (`cmd/root.go:526-535`) feed `core.http.*`/`core.tasks.workers`. |
| Deploy | Owner action: install the updated unit/plist on each host and `systemctl daemon-reload` (a unit change reaches a host only when copied, `deploy/audiobook-organizer.service:8-10`; OPS2-23). |
| Rollback | Binary swap; no state written. The previous build serves the same paths; the previous UI bundle is served by whichever build runs. |
| Done when | `main.go` contains no `server.NewServer`; `grep -rn 'serviceregistry.Register' --include=*.go internal \| grep -v _test` lists none of the services 04 moves; `coretest` boots core with zero modules (G7); the S2 rows of 04 §8.3 have zero remaining ABO users (generated); **perimeter tests** (05-http-api §8.1): `TestBasicGate_ExemptSetEqualsHEAD`, CSP header goldens per owner (`TestCSP_DefaultPerOwner` asserts the legacy SPA response carries the inline-script hash and the configured team host), `X-App-Build` on every response, the rate-limit class golden, the cross-origin test, `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash`, and the invite and bootstrap limiter tests unchanged; **wire goldens**: the 20 sampled routes (one per top-level group, `raw/09` §1.1) match `internal/wirecontract` goldens with volatile fields (timestamps, uptime, versions, request IDs) normalised — not byte equality across builds (OPS-24); **ABS** cell c; `systemctl stop` during a running op, in-process and subprocess mode, stamps it interrupted within the budget with no SIGKILL in the journal. |

### S2b — Core opens the store; books on claims

| Aspect | Specification |
|---|---|
| Goal | Core `storage` opens the one Pebble store; books reaches its families only through declared claims (R8); the guard, census, checkpoint and compat bookkeeping are core's (OPS-12 item 1). |
| Moves | 04 §8.1 row S2b: `internal/database` storage files → `mediacore/storage` + `internal/pebble` (`keyfamilies.go` stays ABO as the claim generator's input); `internal/compactprogress` → `storage`; Pebble collector → `internal/pebble` (gate S2b: ABO's Open Library store opens through `storage.OpenAuxiliary`); `internal/database/dbtest` `newstore.go` → `coretest`; `compat` lands (usage persistence, served ledger). The residue becomes `internal/books/store` on `storage.LegacyRange`. Storage opens at `core.storage.path` resolved per 05-storage §5S.4 step 1 from flags/env/file (Docker's `--db`, the unit's `DATABASE_PATH`), not under a state-directory root (COR2-8, DATA2-19). `examples/hello` gains its keyspace and one migration. |
| Releases | **Release 1 — shadow** (05-storage §5S.9.4): claims enforced in log-only mode, `core.storage.claims.mode=shadow`; every violation is fixed by registering or correcting the writer. **Release 2 — strict**, after release 1 served production with zero violations for 14 days. The flip from shadow to strict is the stage's flip PR. |
| Data/keys | No family moves, nothing is copied; every family keeps its *Before* claim (§9.2.3 ownership view). New core keys only under `c/storage/` (level stamps, served ledger, core migration bookkeeping `c/storage/ks/<owner>/`) and `c/compat/` (usage records). Books migrations 1..66 keep `db_version`/`migration_<n>` through 05-storage's version-source adapter and run before claims are enforced (§9.2.2). |
| Rollback | Binary swap (no data shape change; `c/` keys ignored). The dial `core.storage.claims.mode=shadow` undoes strict mode without a rollback. |
| Done when | The persisted claim-violation record of 05-storage lists no writer for ≥ 14 days of production on release 1 (not a process-local counter, OPS-16); `TestBooksClaimsGenerated` (claim list = 05-storage §5S.9.1 *Before*/*After* for this build's stage; fixture with books writing every family, DATA2-1); `TestS2bOpensStoreWrittenByS0`; free-space check before checkpoints and the checkpoint metric present (OPS-25). |

### UI-0 — UI groundwork (after S2, before S4; FE-2, FE-12)

| Aspect | Specification |
|---|---|
| Goal | Remove the two UI ordering hazards before any UI call moves: the shell needs react-router 8, and S4–S6 must call native routes through the generated client, not hand-written code rewritten twice. |
| Moves | 07 §7.19 steps 1–3: react-router-dom 7 → react-router 8 in ABO with the declarative `<BrowserRouter>` unchanged — an **import swap only** (52 source + 70 test files, `vite.config.ts:81-82` chunk regex); the data router and the test harness rewrite follow at S9 (FE2-19). `core/web` workspace with `@mediacore/api-client` (runtime and generator) only — `ui-kit` and `shell` are created at S9 (04 §8.1, 07 §7.19 step 2; FE2-4) — `make generate-api-client`, consumed by ABO via `file:` and generated from the `core.json` the S2 hello skeleton emits; `internal/openapi` (04 §8.1 row UI-0). The JSON field naming of native routes is the one rule of 05-http-api §4.3 (FE-12a). |
| Data, config, endpoints, permissions | None. |
| Rollback | Binary swap (the UI is embedded per build). |
| Done when | `! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts` (FE2-19); vitest and Playwright e2e green; the generator produces a typed client from `core.json` in `core/web` CI; every build entry point of S0 builds `core/web` first (the S0 image job green with `core/web` present); R-test cells a/b. |

### S3 — Config and secrets

| Aspect | Specification |
|---|---|
| Goal | `core/config` and `core/secrets` become truth; every secret is encrypted at rest without making a rollback lose credentials (N10, G12, OPS-9). |
| Releases (OPS2-15, R2-1) | **r1 — façade, no data change**: ABO's `internal/config` becomes the `legacyconfig.Facade` over core handles; viper leaves the boot path, replaced by core `Load` + aliases (every implicit `AutomaticEnv` name registered as an alias, so nothing is dropped silently); like every build since S0 it carries phase T. Done when the effective-config golden for the production env, YAML and blob is identical to S2b's. **r2 — Expand** (05-config phase X): the flip. The sequence's **Teach build is S0** (phase T, level 1; 05-config §1.12.2): X's adopt step `core/config#1` stamps `Floor` 1, so every build from S0 on — and no older one — can still open the store. |
| Mechanism | **05-config §1.12.2 phase X**, the single config/secrets migration (SIMP-15, SEC-20): one keyring (legacy key imported as `k0-legacy`), import and hash-keyed re-import on every boot, mirror back to `config_blob` and to legacy-format secret rows in the same batch. 09 schedules it; it does not restate it. |
| Plaintext (14 Q-E, OPS-9, OPS2-8, DATA2-9) | The rendered blob carries plaintext secret fields **empty** only while **both** hold: the served ledger shows the previous boot at level ≥ 1, **and** the actual rollback slot `<exe>.prev --print-storage-levels` reports `core ≥ 1` (the probe `ReaderCheck` uses); a failed probe, no `.prev` (containers) or a detected foreign open keeps plaintext until C3 with health `secrets_plaintext_mirror` (documented). Rotation is refused while the mirror is active. |
| Moves | 04 §8.1 row S3: `settings.go` encryption → `mediacore/secrets` (the `setting:` row methods stay books, cross-cutting 7); config persistence (`c/config/`); `core/internal/keyedmutex` (04 §8.1, COR2-16). |
| Config compatibility | All flat keys remain valid in YAML, env and blob (§9.2.5). Canonical keys documented. Env canonical form per 05-config §1.3.6. |
| Deploy files (OPS2-12) | The shipped unit, plist, compose file and `local.conf.example` keep **only the permanent legacy names** for keys that have one (`DATABASE_PATH`, `BASIC_AUTH_*`, `FP_PARALLEL_WORKERS`, …) and use canonical names only for keys without a permanent alias, so a drop-in that overrides a legacy name can never be shadowed by a canonical twin in the base file; the four unread names (`AUDIOBOOK_ROOT_DIR`, `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL`) are deleted from them or, for `OLLAMA_BASE_URL`, replaced by the name ABO reads (owner decides); P4 scans the plist and compose files too. |
| Endpoints | `GET/PUT /api/v1/config` unchanged (masks per 05-config §2.10). |
| Rollback | 05-config §1.12.2 rollback matrix: to S3 r1 or S2b the blob and the legacy rows are current (mirror); secrets entered on S3 are readable through the T overlay; a build older than S0 can no longer open the store once X ran (floor). Roll-forward re-imports what the previous build changed, clears included (DATA-6, DATA2-11). |
| Done when | P1 census: `c/secrets/` count = legacy rows + configured plaintext secrets; `TestEveryLegacyConfigKeyHasCanonical`; `TestShippedDeployEnvNamesArePermanent`; `TestAliasConflictOnProtectedKeyRefusesBoot`; R-test cells X→T, T→X, and "pre-guard boot between two guard-aware boots" (OPS2-8); on production, health shows no `secrets_plaintext_mirror` after the second S3 r2 boot. |

### S4 — Identity: auth engine, RBAC, audit

| Aspect | Specification |
|---|---|
| Goal | One core identity resolver and authorizer for every surface (web, API keys, CF Access, OAuth, ABS); permissions namespaced; admin is `core:admin`; audit trail (G13). **The one stage in which the auth engine changes** (cross-cutting 2). |
| Releases (OPS2-15, R2-1) | **r1 — identity Teach build** (05-authn §11.1; level 3): the legacy-side guards — API-key status guard (no transition out of `revoked`, no `active` for a row with `revoked_at` or `mirror_reason`), `users.manage`/`settings.manage` dominance bound in the legacy handlers, revocation and restore journal — plus reading and preserving every field the mirror writes; no adoption. Production-deployed before r2, so `make rollback` lands on it. **r2 — dark adoption**: `core/auth#1`/`core/rbac#1` run (stamping `Floor` 3), mirror and rebuild active, no reader of the native copy; P1 census equality. **r3 — flip** (level 4): `LegacyAuthCore`, native routes, UI. |
| Moves | 04 §8.1 row S4: `internal/auth` → `rbac` + `auth`; `internal/oauth` → `mediacore/auth/oidc`; the gin auth middleware is replaced by the read-only `legacyauth` bridge (`LegacyAuthCore`: core stage 12 binds the principal, stage 13 enforces each manifest route's permission; bridge adapters still call `rbac.Require`); `internal/database` auth files → `auth` (types, capability interfaces, errors) and `internal/authstore` (gate S4); auth/user/API-key/invite handlers → `internal/authhttp` (gate S4; `user_merge.go`'s methods move onto an ABO-owned merge handler type first, 04 §8.6, MOD2-3); `events` (bus) lands. ABO removes `app.Options.LegacyAuthGinUntil` in the r3 PR. **Stay in ABO** (G11, G15): owner proof and CF Access glue (`internal/cfaccess`), `internal/cfaccesspolicy`, `internal/cfservicetoken`, guest tokens, ABS JWT/sessions (compat surface with per-route `OwnAuth`, which still refuses a confined worker principal, SEC2-4), `internal/usermerge`. The facade-only key machinery is not built (SIMP2-6). |
| Data/keys | §9.2.3 identity model: adoption into `c/auth/`, `c/rbac/` as migrations `core/auth#1`/`core/rbac#1` after books track ≥ 66, in a pre-migration checkpoint; legacy authoritative, rebuild every boot, side records, never-widen keys, monotone revocation, no raw session mirror, invites read in place. New `c/audit/` (append-only, no legacy twin). User IDs unchanged, so books rows keyed by user are untouched. |
| Config | `oauth_*`, `cf_access_*`, `basic_auth_*`, `enable_auth`, `owner_email` aliases → `core.auth.*` (owner/CF keys → `modules.books.identity.*`). |
| Endpoints | Native `/api/core/v1/auth/*`, `/me` (05-authn §4.13), `/users/*`, `/roles/*`, `/api-keys/*`, `/audit`, `/permissions` (05-rbac §4.7), `/config/*`, `/modules` and `internal/uihttp`'s `/ui/diagnostics` and `/ui/csp-report` (07 §7.5.1; FE2-25), served by `internal/<capability>http` packages imported only by `kernel` (03 §3.3). Aliases (r3): `/api/v1/auth/*` (22), `/api/v1/users/*` (15), `/api/v1/me`, `/api/v1/setup`, `/api/v1/config` (2), `/api/v1/preferences` (3); the gin routes they replace are deleted in the same PR, except the unmapped ones below. Every `core.json` regeneration lands in the PR that adds the route. |
| Alias targets (SEC2-5) | Legacy paths that select their target by a field (05-http-api §4.6; strict decoding, `legacy_field_unsupported`): `POST /api/v1/auth/api-keys` → own `POST /api/core/v1/me/api-keys`, or with body `user_id` → `POST /api/core/v1/users/{id}/api-keys` (selector `user_id`); `GET /api/v1/auth/api-keys` → own `GET /api/core/v1/me/api-keys`, or with query `all=true` → the admin key list of 05-authn §4.13 (selector `all`); `PUT /api/v1/auth/me/password` → `PUT /api/core/v1/me/password` without `user_id`; **with** `user_id` it is not aliased and stays a gin route calling the dominance-checked `auth.Service` (05-authn §4.13). Every other alias of this stage has exactly one target. Tests `TestAlias_UnknownLegacyFieldRefused`, `TestAlias_CreateKeyForOtherUserKeepsTarget`. |
| Invite and temp-login links (SEC2-6, FE2-8) | Core issues fragment links (`/invite#token=…`, `/login/temp#token=…`); the legacy `/invite/:token` and `/auth/temp-login` links are aliased for W ≥ max invite TTL (P5) and answered by a server-side 302 to the fragment form with `Referrer-Policy: no-referrer`; ABO's pre-shell `AcceptInvite` reads `#token=` as well as the path parameter, in the same build that switches invite creation to core. |
| Permissions | 05-rbac §6.1 mapping live; legacy atoms accepted on input for W (surface `rbac_atom`); output canonical, plus `legacy_permissions` on the `/api/v1/auth/me` alias during W. `RequireAdmin` role-name checks (`middleware/auth.go:522-537`, `handlers/user.go:285,441,453`, `wire_master_list_routes.go:41`) → permission checks per 05-rbac §6.2. |
| UI (FE-11, FE-12, FE2-17) | Auth, users and config calls switch to generated client operations in the **same** build. The 5 hard-coded permission constants and the `APIKeysTab` scope list are replaced by canonical permissions read from `GET /api/core/v1/permissions` (titles from 05-rbac); an ESLint `no-restricted-syntax` rule fails on any string literal passed to `usePermission`/`useAction`/`hasPermission`, assigned to a `permission`/`*_PERMISSION` property or constant, or used in the API-key scope list, unless it comes from `manifest.permissions` or the generated client. The Users page gains role management. |
| Rollback | To r1 (the floor once r2 ran): `make rollback` passes. r1 reads `u:`, `role:`, `apikey:` — current through the mirror, including keys created on r3 (exact legacy atoms, or the disabled projection when none exist: never wider, and r1 refuses to re-activate them). Sessions created on r3 do not exist for r1: one re-login. Invites issued on r3 are void on r1 (resend). Custom roles created on r3 appear with reverse-mapped (possibly narrower) atoms; a custom role holding `users.manage`/`settings.manage` stays dominance-bounded on r1 (SEC2-2). A seed role edited on r3 is represented faithfully on r1 per 05-rbac §4.8 (the edit becomes a custom role holding the edited set with the members reassigned, or seed edits are refused until C4), so HEAD's per-boot seed overwrite cannot widen it (DATA2-20). Revocations and restores r1 performs are journaled. Roll-forward: the rebuild honours every change r1 made (deactivation, revocation, password reset), with revocation monotone. |
| Done when | `TestLegacyRoleReachUnchanged` green over 572 routes × 4 seed roles + scoped keys + auth-off (with §9.2.6's exceptions), wired as a permanent CI gate; `TestMirror_NarrowKeyNeverBecomesEmptyScope` (property test, 05-rbac), `TestRebuildKeepsMirrorDisabledKey` (§9.2.3 rule 2), `TestMirrorDisabled_PrevBuildReactivateStillRefused` and `TestRollbackReactivationNeverWidens` (PATCH `{"status":"active"}` on the Teach build for a mirror-disabled and a natively revoked key: the real token stays refused; roll-forward keeps the native key revoked); `TestTeachBuild_UsersManageDominance`, `TestTeachBuild_JournalsRevocations`, `TestConfining_WorkerKeyRefusedOnABS`; `TestNoRoleNameAuthz` (AST, OPS-24); P1 census: `c/auth/user/` count = `u:` count; `GET /api/core/v1/audit?kind=auth.login` shows the rehearsal login; `core.json` contains the stage's native routes; R-test incl. "deactivate on prev, roll forward, login refused", "create a scrape-only key on cur, roll back, key refused on prev", "narrow a seed role on cur, roll back, still narrow on prev", and "invite created on cur opens in cur UI and in prev UI"; cells a, b, c. |

### S5 — Tasks

| Aspect | Specification |
|---|---|
| Goal | One task system (N9): v3 authoring on the v2 engine becomes `core/tasks`; required parallelism (N8, R7); `/api/core/v1/tasks` is the one API. |
| Mechanism | **05-tasks §5T.6 and §5T.8** (OPS-7: 05-tasks' model): copy-and-mark of non-terminal runs into `c/tasks/` (one batch per run), boot reconcile of runs an older build created, clock, pause and `Requires`-stamp mirrors to the legacy keys (`core.tasks.req-migrate` **copies** stamps and never deletes during W, OPS2-9), terminal history read in place by `legacyhist`; the D25 opv2↔opv3 mirror ends at the adoption. The sequence's Teach build is the S4 r3 build (§9.2.2); `core/tasks#1` stamps `Floor` 4. |
| Moves and batches | 04 §8.1 row S5 and §8.3 gates: engine → `internal/executor` (T0), the `internal/operations/registry` path stays as the `opsbridge` v2 adapter until T8 (§8.3 counts its remaining importers; T8 ships at zero); `internal/scheduler` copied at T0 and its path deleted at T7; `opsv3` and the scheduler-admin handler → `internal/taskhttp`; `pkg/ops` → `tasks`; run fencing (`opswriter/fence.go`) → `tasks`. 05-tasks batches, as releases: **T0+T1 in one release (the flip)**; T2–T7 in later releases of the stage, mechanically similar, suited to `/parallel-sweep` partitioned by package; **T8 ships only with or after C5 and from the minimum-upgrade-from release on** (§9.2.2, §9.4.1). `examples/hello` gains `hello.count`; G6/G7 are proven from here (`TestSecondModuleNoAudiobookEdit`). |
| Data/keys | §9.2.3 tasks row. The first adopting boot records the S5 adopt step's applied record. ABO's opv2/opv3 store files stay in the books residue as legacy writers until T8 deletes them (04 §4.5); books legacy migrations 59–66 run before tasks claims are enforced (§9.2.2). |
| Config (R2-7) | `scheduled.*` (~40), `maintenance.*` → schedule keys `modules.books.<subsystem>.schedules.<name>` declared under books' namespace and registered through `tasks.ScheduleFromConfig` (05-tasks §5T.3.8, the one schedule mechanism) with window aliases; worker count → `core.tasks.workers`. Dial: `core.tasks.kinds.<kind>.parallelism_cap`. |
| Endpoints (FE2-18) | Native `/api/core/v1/tasks/` routes of 05-tasks §5T.3.10: `definitions`, `definitions/{kind}`, `definitions/{kind}/runs`, `runs`, `runs/{id}` (+ `rows`, `items`, `log`), `runs/{id}/cancel`, `runs/{id}/approve`, `runs/{id}/decide`, `counts`, `timeline`, `schedules`, `schedules/{kind}`, `pause`, `unpause`; there is no tasks events route (run changes are bus topics streamed by `/api/core/v1/events`). Aliases (W): `/api/v3/ops/*` (12), `/api/v1/operations/*` (38), `/api/v1/op-defs` (2), `/api/v1/tasks` (3); legacy status vocabulary kept on aliased responses via `WithLegacyShape` (`web/src/generated/ops.ts`). |
| Legacy streams until S6 (FE2-3) | Until the S6 projections exist, a books-side bridge keeps streams A/B working: `core/tasks` publishes every run change to ABO's legacy `registry.EventHub` (`internal/operations/registry/bus.go`), which still feeds `/api/v1/operations/events` and `/api/v3/ops/events` (05-tasks §5T.6). |
| Permissions | Per-definition `Permissions.Start` (05-tasks §5T.3.7); the implicit `settings.manage` default (`operations_v2.go:781-800`) becomes explicit on every definition that had none. |
| UI (FE2-3) | **No change to the Activity page and `useOperationsStore`**: from S5 they call the task **aliases** in the legacy shape, and move to native REST and stream together at S9 (07 §7.19 step 4). Other UI calls move per 07 §7.19 step 6 as their native routes ship. |
| Rollback | To S4 r3: runs adopted by S5 are marked `interrupted_dropped` + `migrated_to` in legacy (no double execution); runs S4 creates are adopted on the next roll-forward; clocks and `Requires` stamps are mirrored (no stampede, no double fire, no library-wide prerequisite re-run, OPS2-9); runs created by S5 are invisible to S4 and resume under their `ResumePolicy` after roll-forward (05-tasks §5T.6 "four rules"). |
| Done when | `opscatalog`'s 233 defs run through `core/tasks` with declared `Parallelism`; the G5 rollback test of 05-tasks §5T.9 and the resume rehearsal (kill -9 mid `books.acoustid.backfill`, restart, checkpoint resumes) green; P11 run before the flip and its over-cap list empty or handled; `TestOpStateWriters` ratchet at its target for each finished batch; persisted usage shows no UI-originated enqueue on `task_kind` beyond the Activity page's aliases; the Activity page shows live progress on S5; `core.json` contains the stage's native routes; boot phase `P4d` step `core/tasks#1` timed by `mediacore_kernel_boot_phase_seconds`; R-test incl. "a `Requires` stamp written on cur is honoured on prev" and "store at books v64 → cur" (DATA2-2). |

### S6 — Events, outbox, realtime

| Aspect | Specification |
|---|---|
| Goal | One notify-only bus with one topic catalog (SIMP-5), one durable outbox, one resumable permission-filtered SSE hub (G4). Webhooks move at S7 with the instance store and `providerhttp` they need (SIMP2-4). |
| Moves | 04 §8.1 row S6: `internal/realtime` → `realtime` (+`internal/sse`); `internal/operations/registry/bus.go` and `internal/plugin/events.go` → `events` with topics such as `books.book.imported.v1` and `books.read-status.changed.v1` (12 legacy event types mapped one-to-one in 05-notify §10). The tag write-back outbox `pref:_system:outbox:writeback:` stays a books claim. |
| Data/keys | New `c/outbox/` (core-owned, no legacy twin). |
| Endpoints (COR2-1) | Native `/api/core/v1/events` (SSE with `id:`, `Last-Event-ID`, caps). Legacy streams `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events` are **core projections** of the same hub with their legacy framing: books obtains each handler from `Registrar.Realtime().Projection(p)` during P6 and mounts it in its gin manifest at the legacy path (`AnyAuthenticated`, per-event topic filtering inside the hub; 05-realtime), counted on surface `sse`; the S5 bridge is removed. |
| UI (FE-10) | **No change.** The SPA keeps using the legacy streams through the projections; the shell's realtime client replaces them at S9. |
| Switches | Dial `core.outbox.relay_enabled`. |
| Rollback | To S5: outbox rows left unprocessed; nothing correctness-critical is carried only by the outbox before C6 (the stage ships no such path; webhooks are not on it until S7). |
| Done when | `grep -rln 'text/event-stream' --include=*.go internal core \| grep -v _test` lists only the core hub and its projections; legacy-stream goldens per event type; cells a, b (previous-release UI against cur and cur UI against prev stream events) and c (ABS). |

### S7 — Integrations, library, search index, notify

| Aspect | Specification |
|---|---|
| Goal | Capability contracts in core with ABO's adapters behind them; one integration-instance store (SIMP-4); per-instance HTTP resilience (R5); the search index behind `storage/index`; notify v1 (mail and webhooks) built in one stage. |
| Moves | 04 §8.1 row S7: `internal/metadata/{providerhttp,dailyquota}`, `internal/security/safehttp` → `integrations/providerhttp` (instance-scoped); `internal/ai/{aijobs,resultjournal}`, generic `internal/aidispatch` → `integrations/ai`; Deluge client → `integrations/download/deluge` + books glue; `internal/mail` → `integrations/notify/mail`; `internal/plugins/webhook` → `integrations/notify/webhook`; generic `internal/fileops`, `internal/watcher`, `internal/pathutil` parts → `library`; search engine lifecycle → `internal/bleveindex`, dirty set and reconciler → `storage/index`. Metadata sources stay books, implementing `metadata.Provider[Q,R]`. **No books row rewrite**: books keeps `BookVersion.TorrentHash`/`BookFile.DelugeHash` and builds `download.ItemRef{Instance: <the migrated Deluge ref>, ID: …}` at the call site; the `ItemRef` migration is phase 2 with its own stage entry (SIMP2-7). |
| Data/keys | Import of Deluge (`download/deluge`), AI endpoints (`ai/<id>`), metadata sources, SMTP (`notify/email`) and each configured webhook URL (`notify/webhook-<n>`, `legacy_headers` on, keeping `X-Audiobook-Event`/`X-Audiobook-Signature-256`, plus a global `Rule` for the configured event list mapped to `books.*` topics; 05-notify §10) into the instance store, `Local=true` for loopback/private hosts (05-metadata M1.10), as adopt step `core/integrations#1` (`Floor` 5); secrets under `integrations.<kind>.<instance>.<field>` (05-config §2.2). Until C7, **05-metadata M1.4 step 2a** re-imports every boot (hash-keyed, URL and secret together) and mirrors every `Put` back to the blob keys and import hashes in the same batch (DATA2-10). Env-bound values ABO treats as authoritative today (Deluge, OpenAI) are an **in-memory overlay** over the stored row, never persisted: removing the env var reverts to the stored row (HEAD semantics; 05-metadata M1.4 step 2, SIMP2-5). `provider_throttle:`, `provider_daily_budget:`, `aijob*`, `aijournal:` stay books claims read through adapters until their owning spec adopts them. The search index format is unchanged (or rebuilt; it is derived data). |
| Notify (COR2-3) | Books emits its 12 legacy event types as notifications through `module.Deps.Notify` (`Publish(ctx, b, n)`, outbox-backed, refused for topics books does not own); undelivered entries older than `core.notify.max_delivery_age` (default 24 h) are dead-lettered and counted (OPS2-20). |
| Config | `deluge_*`, `download_client`, `openai_*`, `ai_endpoints*`, `smtp_*`, `metadata_sources`, `plugins.webhook.*` → instance records (window aliases for the flat keys; M1.4 step 2a keeps the blob current for rollback); `deluge_move_enabled` etc. → `modules.books.download.*`. `OPENAI_API_KEY`/`OPENAI_BASE_URL` stay permanent env aliases (§9.2.5). |
| Endpoints (SIMP2-17) | Native `/api/core/v1/integrations/{kind}/...` and `POST /api/core/v1/integrations/{kind}/test`; aliases (W): generic parts of `/api/v1/ai` (14), `/api/v1/deluge` (6), `/api/v1/metadata-sources`. Books Deluge behaviour routes stay on the legacy mount (cross-cutting 2, §9.2.4); only generic client routes become core instance routes with aliases. |
| Rollback | To S6: S6 reads Deluge/AI/SMTP/webhook settings from the blob, which M1.4 step 2a keeps current, and settings S6 edits are re-imported on roll-forward with URL and secret together; instance rows are ignored by S6. Webhook deliveries pending in `c/outbox/` at rollback are not sent by S6: on roll-forward they are delivered once, or dead-lettered past `core.notify.max_delivery_age` (OPS2-20). |
| Done when | `plugin.DownloadClient` (0 implementors, `raw/11` §4) deleted; `download.Client` has 1 production implementor (Deluge) passing `coretest` conformance; no package-level throttle/quota map (`archtest.TestNoGlobals` on moved code); **deferred-build packages do not exist** (SIMP-1): `test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e core/acquire`, `integrations/download` contains no grab, monitor or pending-recovery code, and `grep -rn 'FacadeWriter' --include=*.go core internal` finds nothing (the facade writer is deferred with `arrfacade`); webhook golden (legacy headers and body) byte-equal to S6 for a `legacy_headers` instance; search results for the fixture query set equal S6's; R-test incl. "an S6 edit of a Deluge URL and password is re-imported together on roll-forward" and "a webhook pending at rollback is delivered once on roll-forward or dead-lettered after the max age". |

### S8 — Backup, updater, health, metrics registry

| Aspect | Specification |
|---|---|
| Goal | Platform operations owned by core; per-App metrics registry. |
| Preconditions (OPS-23) | A signed release (`release-manifest.json.sig`) is already published and its public key is compiled into the S8 build; installs migrated from `auto_update.enabled=true` get `core.updater.allow_unsigned_legacy=true` until their first signed apply (05-updater); `no_trusted_keys` and `unsigned_updates` are Degraded with a UI banner. **Owner action (OPS2-4):** install the binary into a service-owned directory referenced by `ExecStart` (or a user-writable location on macOS, 05-updater §5U.5); otherwise the updater disables itself with `binary_not_writable` (component Degraded), never failing half-way. |
| Moves | 04 §8.1 row S8: `internal/backup` → `backup`; `internal/updater` → `updater` (release source from `app.Options`, not the slug `updater.go:72`); host parts of `internal/sysinfo`, flight-recorder wiring, trace download → `health`; `internal/metrics` registrations onto the App registry. |
| Backup (cross-cutting 8, SIMP2-2) | Archive format, encryption policy, retention floor, staged restore, provenance check and catalogue location are **05-backup's** (one format, DATA-21, OPS-14). The backup age identity is derived from the one keyring (`DeriveKey`), so the **05-config keyring export is the only export** and covers both secrets and archives. Policy for the window: until the last window a pre-core build can roll back into has closed, the default archive is restorable by the previous build; encryption is opt-in on upgrade; retention never deletes the newest verified archive the previous build can restore until an encrypted one is verified and the keyring export is confirmed (DATA-22). Encryption cannot activate before C8 raises the store's `core` level above the legacy restore bound (5, §9.2.2). A staged restore records the staging build and an expiry; a stale or foreign intent is rejected (moved aside, Degraded); `make rollback`, the updater and the `deploy` recipe refuse while one is pending; applying a restore rewrites both sidecars from the restored stamps; post-restore revocations reach the legacy rows in the same batch (R2-2). |
| Updater (OPS-6, OPS2-3, OPS2-6) | `updater.Boot` runs in boot phase **P−1**, before the restore swap and config (06 §6.5.1), so a refusal in P0 (retired alias, C3's YAML refusal, invalid config) counts as a failed boot; automatic rollback runs `ReaderCheck(<exe>.prev)` (the same guard decision as `make rollback`); if refused, no rollback: `rollback_blocked`, Degraded, boot continues. One slot, `.prev`. A rolled-back version is recorded and skipped by scheduled apply until a manual apply or a newer release (05-updater). Scheduled apply (paused since S0) resumes with this build. |
| Health | `/health` with its `ready` field is **permanent** (`deploy-wait-ready.sh`, Docker HEALTHCHECK; 05-health OPS-27b); core adds `/livez`, `/readyz`; `health`, `metrics`, `livez` and `readyz` are non-SPA segments, so `/health/` and `/metrics/` never answer HTML (FE2-6). |
| Metrics | Core-subsystem renames with gather-time aliases (§9.2.7); books names unchanged; `mediacore_build_info{version}` with the supported level vector; `deploy/prometheus` gains alert snippets for store level > supported (`mediacore_storage_level`), undecodable mirror rows > 0, mirror-write failures > 0 (OPS-27d), and `rollbacks_total{outcome="done"}` (OPS2-6). |
| Config | `backup_*` → `core.backup.*`; `auto_update*` → `core.updater.*`. |
| Endpoints | Native `/api/core/v1/{backup,update,system,health}`; aliases (W): `/api/v1/backup` (4), `/api/v1/update` (3), `/api/v1/system` (7), `/api/health`, `/api/v1/health`. Permanent: `/health`, `/metrics`. |
| Rollback | Binary swap; archives written during the window are restorable by the previous build (05-backup); a restore of a post-contract archive onto an older build is refused by the guard at next boot. |
| Done when | Every books series name in `curl -s <abo-url>/metrics` equals S7's and every aliased core family answers under both names; `promtool test rules deploy/prometheus/alert-rules.test.yml` green; restore matrix (archive from S0 × S8 build, archive from S8 × S7 build, staged intent across a rollback) green; R-test cells d (failed boot → `Boot` rolls back, or `rollback_blocked` past a contract), d' (a contract release that fails readiness on its first boot can still be rolled back by `Boot`, because the contract waits for `RequiresSelfServed`; OPS2-2) and e; a staging install with `auto_update.enabled=true` and a service-owned binary directory receives the next signed release; cells a/b for the moved system/backup/update UI calls. |

### S9 — UI shell

| Aspect | Specification |
|---|---|
| Goal | `@mediacore/shell` + books UI contribution (D6, N6). |
| Preconditions (FE2-25) | UI-0 shipped (react-router 8, generated client); **S3–S8 done** — the shell's System, Settings and Integrations pages call S3, S7 and S8 native routes, and `/api/core/v1/modules`, `/ui/diagnostics`, `/ui/csp-report` exist since S4. |
| Moves | 07 §7.19 step 4 (one PR series): create `@mediacore/ui-kit` and `@mediacore/shell` (with `/vite` and `/testing`) and add hello's web package to the workspace; shell-classified files move into `core/web` with audiobook imports cut into books slots; `main.tsx` → `createShell` with the data router; **then** `renderWithProviders` onto `renderInShell` (it needs the providers to have moved, FE2-4); `useUnsavedChangesBlocker` deleted; the shell's realtime client replaces `eventSourceManager` and the operations stream, and the Activity page moves to native REST and stream together (FE-10, FE2-3); the preset writes the build-ID meta tag and the S0 `X-App-Build` check is deleted in favour of the shell banner; ABO adopts the `UIBundle()` pattern (an API-only build gets a placeholder, never a panic, FE2-10) and `main.go` passes it to `app.Options.UI`; books drops `ServesSPA` and gin's `NoRoute`/`setupStaticFiles` SPA serving is deleted **in the same PR** (FE-1); `ui.Handler` mounts from this build. Then step 5 (server manifest and intersection) and, per native route, step 6. `examples/hello` gains `ui_embed.go` (UI half of G6). |
| UI routes | Module pages under `/books/...`; shell pages per `ui.ReservedSegments` (`/settings`, `/system/...`, `/users`, `/activity`, `/invite` and `/login/temp` with the token in the URL fragment, SEC2-6). Legacy SPA paths (`/library`, `/authors`, `/series`, `/works`, `/playlists`, `/collections`, `/review`, `/versions`, `/fingerprints`, `/dedup*`) → client redirects to `/books/...`, plus the pages the shell absorbs: `/dashboard → /`, `/files → /books/files`, `/diagnostics → /system/diagnostics`, `/operations → /activity`, `/maintenance → /system` (07 §7.11, FE2-16; pattern of `web/src/utils/absRedirects.ts:17-25`); a legacy `/invite/:token` link redirects to `/invite#token=…`. ABO's `PageMiddleware` records full-page loads of the paths in books' `redirects` list on surface `ui_route`. `localStorage` keys migrated on first load. |
| Rollback | Binary swap; the UI is embedded per build. Bookmarks to `/books/...` 404 on the older build's SPA (accepted, cosmetic); the S0 build check offers a reload to tabs left open. |
| Done when | `App.tsx` has no hard-coded module routes; a dummy second contribution renders with no edit to books UI (G6, UI half); G3: the shell rejects a module descriptor with unknown version; the legacy streams show no use from first-party tabs (persisted usage on `sse` limited to non-UI clients); `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder (FE2-10); cells a, b, c. |

### S10 — Final cleanup (was S11)

Delete after their windows: removed flags `--db-type` and the SQLite no-op flag (`cmd/root.go:489,508`);
`internal/serviceregistry`, `internal/plugin`, `pkg/plugin/sdk`; the **mirror, rebuild, reconcile and
alias code** of every contract that has run. Adopt steps are **not** deleted: they stay registered in
every build (05-storage §5S.3.11). Because a store on which a removed contract has not run would still
need that mirror code, S10's release manifest sets `min_upgrade_from` to the newest release that carries
it, and S10 refuses such a store with `ErrUpgradeFloor{InstallFirst}` (§9.2.2; OPS2-11). Compat-ledger
rows closed. Not deleted: the legacy mount, the `/api/*` → `/api/v1/*` redirect, ABS paths, `/health`,
`/metrics`, permanent env names, the `config.AppConfig` and logger façades (optional ratchets,
SIMP-3; 11 §11.5 checklist). Done when §9.6 has no row in state "aliased" except those marked
permanent and `TestCompatLedgerMatchesCode` shows only permanent entries.

### 9.4.1 Sub-spec batch → stage map (OPS-12)

| Batch (owner) | Stage | Runtime behaviour change? | Rollback mechanism | Done-check |
|---|---|---|---|---|
| Toolchain bump to `go1.27.3` (13) | S0, first PR | no | revert PR | 13's pin check |
| Build paths, CI classification, Docker PR job, release gates (03 §3.2, 07 §7.3.2, 14 Q-I) | S0 | no | revert PR | S0 done-checks |
| 05-config phase T (legacy loader overlays and writes every secret row) — config Teach build | S0 | no (inert until S3) | binary swap | unit test: overlay reads a legacy-format row for every §2.2 field |
| Live hotfixes of `todo.d/2026-11-04_platform_core_followups.md` (optional here) | S0 or later | yes (refusals on legacy key/role paths) | binary swap | 05-authn §11.1 tests |
| Updater hotfix (exit 75, `.prev`, scheduled apply check-only) | S0 | yes (no unattended apply) | binary swap | updater test |
| 05-config stage 0 (8 bypassing writes) | S1 | no | binary swap | `TestNoDirectAppConfigWrites` |
| Staged restore, ABO-local (05-backup §5B.3.4) | S1 | yes (restore applies at next boot) | binary swap; pending intents refused by rollback and deploy | S1 restore rehearsal |
| Composition root, lifecycle, listeners; 05-health budget; supervisor stop settings | S2 r1 | yes (stop order, budget, drain 10 s) | binary swap | `systemctl stop` check |
| 05-http-api H0: `MountLegacy`, perimeter to core, cross-origin check | S2 r2 | yes (perimeter owner, new cross-origin check) | binary swap | S2 perimeter tests, ABS, wire goldens |
| 05-logging stages 1–2 | S2 r3 | yes (log file path, levels now honoured) | binary swap | journal shows ABO records; level change applies |
| 05-storage rollout 1–2: core opens the store; books on `LegacyRange`, shadow claims | S2b release 1 | no (log-only) | binary swap | zero persisted violations ≥ 14 days |
| 05-storage rollout 3: strict claims (flip) | S2b release 2 | yes (refused writes) | dial `core.storage.claims.mode`, binary swap | strict on production 14 days, zero refusals |
| `database.Store` split: storage files (04 §4.5) | S2b | no (pure moves and 04 §8.6 rewrites) | binary swap | `make ci` both modules |
| 07 §7.19 steps 1–3 | UI-0 | no | binary swap | UI-0 done-checks |
| 05-config façade stage 1 (viper out of boot path) | S3 r1 | no (effective config identical) | binary swap | effective-config golden |
| 05-config phase X | S3 r2 | yes (config truth moves; blanking) | 05-config rollback matrix (floor: S0) | S3 done-checks |
| 05-config optional ratchet (accessor) | none (optional) | no | — | `TestAppConfigRatchet` ceilings only go down |
| Identity Teach build (05-authn §11.1) | S4 r1 | yes (legacy guards refuse) | binary swap | 05-authn §11.1 tests |
| 05-storage adoption for identity (dark); `database.Store` split: auth files | S4 r2 | no (native copy unread) | mirror + rebuild (§9.2.3); floor S4 r1 | P1 census equality |
| 05-http-api S4: `LegacyAuthCore`, native core auth routes; UI auth/users/config calls and permission constants | S4 r3 | yes (auth engine, UI) | binary swap under the mirror | `TestLegacyRoleReachUnchanged`, permission-literal rule |
| 05-tasks T0+T1 (flip: executor, adoption, reconcile, mirrors, legacy-stream bridge) | S5 release 1 | yes (executor, run store) | 05-tasks §5T.6 G5 rules (floor: S4 r3) | 05-tasks §5T.9 G5 test |
| 05-tasks T2–T7 (native definitions; T7 moves schedule keys) | S5 later releases | T2–T6 no; T7 yes (schedule keys, aliases) | binary swap | per-batch gate in 05-tasks §5T.8.2 |
| 05-tasks T8 (delete `OperationDef`, `opsbridge`, ABO's opv2 writers) | with or after C5, from the minimum-upgrade-from release | yes (rollback boundary) | none past C5 (guard refuses) | 04 §8.3 registry count 0 |
| 05-events/05-realtime hub, projections, outbox | S6 | yes (stream server) | binary swap | S6 goldens |
| 05-metadata M1 instance store + imports and M1.4 step 2a mirror; 05-download/05-ai adapters; 05-notify mail + webhook instances | S7 | yes (settings home, webhook delivery path) | blob mirror (M1.4 step 2a) | S7 done-checks |
| 05-backup format and catalogue; 05-updater signed apply; 05-metrics renames | S8 | yes | binary swap; restorable archives | S8 done-checks |
| 07 §7.19 steps 4–5 | S9 | yes (UI routes, stream client) | binary swap | S9 done-checks |
| 07 §7.19 step 6 (remaining `api.ts` sections) | S4–S9, per native route | no for users | binary swap | generated client covers the route |
| 05-logging optional ratchet (injection); logger façade removal | none (optional) | no | — | slog ratchet ceilings only go down |
| Mock conversion (`internal/database/mocks`, 04 §4.6) | S2b–S4 (owner decides: in-stage or deferred behind `storetest`) | no | — | 04 open item 1 |

## 9.5 Contract track

Every contract is a PR gated by `TestNoContractBeforeSunset`, P8 and the `extraction-gates` check
(§9.2.1), and runs on each store only when the store-side preconditions of §9.2.2 hold (`Adopts`
applied; `RequiresServed` met by an uninterrupted run; `RequiresSelfServed`); otherwise the build adopts
if needed and stays in mirror phase. A contract migration runs in a pre-migration checkpoint (disk
headroom checked) as **three persisted phases** (05-storage §5S.3.11, DATA2-14): `folded` (the last
rebuild/re-import/reconcile and the fold of side records, committed in one batch, after which the
mirror is out of phase *mirror* and the boot rebuild skips it), `raised` (the level raise, before the
first legacy delete), and `deleting` (≤ 10 000-key batches with a cursor); a resumed contract skips
completed phases, so it never re-runs a rebuild over half-deleted legacy rows.

| Contract | Earliest | Store-side and usage preconditions | Action | Required level after |
|---|---|---|---|---|
| C3 config/secrets | R(S3 r2) + W | `core/config#1` applied; `core` level 2 served ≥ 30 days; P8 shows no use on the `config`/`env` window aliases (boot-read rule, §9.2.1); S1 staged restore in production (OPS-15) | 05-config phase C: last re-import, stop the mirror, delete `setting:config_blob`, the legacy secret rows and `c/config/import/`; YAML legacy keys refused at boot for one minor, then alias rows deleted | 2 |
| C4 identity | R(S4 r3) + W | `core/auth#1`, `core/rbac#1` applied; `core` level 4 served ≥ 30 days; P8 zero for `http` auth/users aliases and `rbac_atom`; staged restore in production | stop mirror; delete adopted legacy rows and the identity `idx:` sub-prefixes; fold side records; legacy atoms → 400 naming the canonical permission; HTTP aliases → `Gone` one minor → delete | 4 |
| C5 tasks | R(S5) + W | `core/tasks#1` applied; `core` level 5 served ≥ 30 days; P8 zero for `task_kind` and task `http` aliases | in the same `BootBlocking` step and **before** the level raise, run the per-run copy-and-mark one last time for every legacy non-terminal run lacking `migrated_to` (over-cap runs dropped with the existing notice; DATA2-13), then remove reconcile and the clock, pause and `Requires`-stamp mirrors and delete the legacy stamps (05-tasks §5T.6); aliases → `Gone`; T8 may ship with or after it; stored-row kind resolution through `FormerKinds` is **kept**. P11 stays an operator preview | 5 |
| C6 events | R(S6) + W and after S9 | P8 zero on `sse` | legacy streams → 410 (EventSource stops on non-200) → delete projections | — |
| C7 integrations | R(S7) + W | `core/integrations#1` applied; P8 zero on the generic `/api/v1/{ai,deluge,metadata-sources}` aliases | last M1.4 step 2a re-import, stop the blob mirror; aliases → `Gone` → delete; webhook instances keep `legacy_headers` as instance data; the operator overlay and its permanent env names are unaffected (05-config C11) | — |
| C8 backup/update/system/metrics | R(S8) + W | `core` level 6 served ≥ 30 days; P8 zero on `/api/v1/{backup,update,system}` and health aliases; P6 shows no external dashboard on aliased core metric names | aliases → `Gone` → delete; metric aliases removed by PR (gate P6); raise `core` to 6 (no data deleted), which passes the legacy restore bound so backup encryption can activate (05-backup B3) | 6 |
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
| 15 | Env vars, window | `BindEnv` names + implicit `AutomaticEnv` names + `ABK_*` (counts: 05-config §1.12.4) | 05-config §1.3.6 | W after S3 | not read; `ProtectAdmin`+ and secret-class keys refuse to start (§9.2.5) | `cmd/root.go:625` |
| 16 | Env vars, permanent and runtime | the single list of 05-config §1.12.4 (permanent aliases, including every read name a shipped deploy file sets; runtime/ecosystem names config does not read) | unchanged | **permanent** (OPS-5) | none | 05-config §1.12.4; §9.2.5 |
| 17 | CLI commands | `serve scan playlist tag organize inspect-metadata seed diagnostics{cleanup-invalid,reserved-prefs,query} fp-worker dedup-bench` (bench tag) | unchanged | **permanent** | none | `cmd/*.go` `Use:` |
| 18 | CLI flags in shipped artifacts | `--host --port --db --config --dir --log-level --playlists --tls-cert --tls-key --http3-port --external-url --workers --read-timeout --write-timeout --idle-timeout` | same flags feed canonical keys | until next **major** (1.0) | release note at the major; not usage-gated | `cmd/root.go:486-535`; `Dockerfile:104` |
| 19 | Removed/no-op flags | `--db-type`, SQLite flag | — | W after S2 | deleted S10 | `cmd/root.go:489,508` |
| 20 | Fast-exit protocol | `--print-storage-format`, `--print-capabilities`, new `--print-storage-levels` and `--version-json` (S0) | unchanged | **permanent** | none (rollback protocol) | `main.go:40,52`; `Makefile:892` |
| 21 | Child-mode sentinel/env | `--operation-runner`, `registry.EnvChild*` | unchanged | none (same binary) | n/a | `cmd/child_mode.go:53-96` |
| 22 | Other binaries in `cmd/` | `itl-*`, `mtls-bridge`, `pebble-inject-skip`, `pid-census`, `verify-suite` | stay ABO | permanent (books tools) | n/a | `cmd/` |
| 23 | Metric names, books | 73 `audiobook_organizer_*` families | unchanged | **permanent** (SIMP-13) | none | `internal/telemetry/contract/testdata/series.golden` |
| 24 | Metric names, core subsystems | ops, Pebble, AI, two process gauges | `mediacore_*` (05-metrics §13) | W after S8, gate P6 | gather-time alias → removed by PR | `metrics.go:15-300`; `opsmetrics.go:12-16` |
| 25 | Pebble identity families | `u: role: apikey: inviteallow: oauthid:` + their `idx:` sub-prefixes (mirrored); `invite:`, `sess:` (read in place) | `c/auth/`, `c/rbac/` | mirror (or read in place) until C4 | contract migration + required level | `keyfamilies.go:63-71`; 05-storage §5S.9.1 rows 1–6 |
| 26 | Pebble books families | the rest, incl. `setting:`, `preference:`, `pref:`, `act:` | books claims | **permanent** (02 #13) | none | 05-storage §5S.9.1 |
| 27 | Pebble ops families and clocks | `opv2:`, `opv3:`, `op:`, settings clock keys, `Requires` stamps | `c/tasks/` | mirror/reconcile until C5; history ages out | 05-tasks retention | 05-tasks §5T.6 |
| 28 | Task kinds (op def IDs) | 233 | `books.<id>`, `core.<id>` | input W after S5; stored-row resolution **permanent** | `FormerKinds` + persisted usage on `task_kind` | `aliases.go:16-52` |
| 29 | Webhook headers and event names | 2 headers, 12 event names | `books.*.v1` topics, core notify headers | per instance: `legacy_headers` is instance data, kept while set | operator clears it per instance | 05-notify §10; `webhook/plugin.go:101-126` |
| 30 | Backup archives | tar(+codec) of DB dir | 05-backup format | read: **permanent**; previous-build-restorable default: through the window | 05-backup | `backup.go:283-860` |
| 31 | SPA routes | 14 module paths + 5 absorbed pages (`/dashboard`, `/files`, `/diagnostics`, `/operations`, `/maintenance`) + `/invite/:token` | `/books/...`, shell pages, `/invite#token=` | W after S9 | client redirect → removed (C9) | `App.tsx:204,253-448` |
| 32 | Wire shapes of aliased routes | hand-written TS interfaces | generated | = route window | `WithLegacyShape` goldens | `raw/13` §8 |
| 33 | Release `min_upgrade_from` | the first T8 release; S10 | — | per release | install the named release first (`ErrUpgradeFloor`) | §9.2.2; 02 N14 |

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
| P7 | ABS surface in use | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.abs_api_enabled'`; `... --prefix abs_sess: ... \| grep -c '^Key:'` | off: conformance fixtures suffice | on with sessions: fixtures **plus** live login and progress sync with a real ABS client, before and after rollback, gate S2, S4, S6 and S9 (§9.3) |
| P8 | Legacy use (removal gate, reference store) | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/core/v1/compat/usage \| jq '.[] \| select(.last_used_at > (now - 14*86400 \| todate)) \| {surface,legacy,count,last_seen_set_boot}'` | empty for the contract's surfaces, and the ledger shows a recording build for the whole 14 days: contract may run | entries: window extends; `legacy` names the caller to fix |
| P9 | Webhooks configured | `curl … /api/v1/config \| jq '.plugins.webhook // empty \| keys'` | none: S7 creates no webhook instance | some: one instance per URL with `legacy_headers` |
| P10 | Rollback inventory | `ssh <host-a> '<bin>.prev --print-storage-levels \|\| <bin>.prev --print-storage-format'`; `ssh <host-a> '<bin>.prev --version-json'` (S0 on); `curl -s <abo-url>/api/v1/system/version` | `.prev` ≥ S0: guard effective, T in place; its commit is the R-test's `PREV_REF` for the next flip | `.prev` older than S0: deploy S0 twice before S3 so `.prev` knows the guard and the T overlay |
| P11 | Active-run set (S5, C5 preview) | `audiobook-organizer --print-upgrade-blockers --db <scratch>/audiobooks.pebble` (05-tasks §5T.6) and, on a copy, `diagnostics query --raw --prefix opv2:act: \| grep -c '^Key:'` | no over-cap run: flip | over-cap runs listed: finish or cancel them on the previous release before S5 |
| P12 | Disk headroom | `df -B1 --output=avail <db-dir-parent>` vs `du -sb <db-dir>` | free ≥ store size: checkpoints fit | less: free space before any stage with a checkpoint (S2b, S3, S4, S5, S7, C*) |

## 9.8 Error semantics (compatibility machinery)

| Error path | Behaviour | Why |
|---|---|---|
| Store required level > build level | **fail-closed**: refuse to open, name both levels and the release to install | an old build on a contracted store would read stale or missing rows |
| Build older than the store's Teach floor (an adopt step ran) | **fail-closed** (`LevelTooNewError`; pre-guard builds through the projection) | the rollback floor is the Teach build: older builds lack the legacy-side guards (R2-1) |
| Contract preconditions not met on this store | **open**: adopt if needed, stay in mirror phase, retry next boot | never delete legacy data on an unadopted or briefly served store (cross-cutting 6) |
| Contract due while an update awaits confirmation, or the build has not served long enough itself | **open**: deferred, `storage_contract_pending` | keeps `Boot` and `make rollback` able to undo a broken contract release (OPS2-2) |
| Store below the release's `min_upgrade_from` | **fail-closed** (`ErrUpgradeFloor{InstallFirst}`): refuse to start, name the release to install | the legacy migrations or mirror code it needs are gone (R2-4) |
| Adoption or import error | **fail-closed**: boot halts, checkpoint kept unconsumed for the offline restore, legacy untouched | G2 |
| Undecodable legacy row during rebuild | **closed per principal**: no native copy, Degraded naming the key, counted; boot continues | one bad row must not crash-loop the unit (OPS-19) |
| Mirror write fails | **fail-closed**: whole batch fails, request errors; `mediacore_compat_mirror_write_failures_total` | a native write without its mirror breaks rollback |
| Native API key with no exact legacy scope set | mirror **disabled** (closed: revoked status, sentinel scope, decoy hash) | an empty legacy scope list is the widest value (SEC-1) |
| Status change on a revoked or mirror-disabled key on the Teach build | **refused** (409) | re-activation would widen the key (R2-1) |
| Stored role holds an unknown atom (adoption) | **fail-closed** boot, names role id | P2 branch B |
| Legacy alias request | served (**open**), recorded, headers | compatibility is the point |
| Legacy request field an alias cannot map | **refused** 400 `legacy_field_unsupported` | a dropped selector retargets the action to the caller (SEC2-5) |
| Legacy key in YAML after W | **fail-closed** refuse to start, names canonical | silently ignoring `root_dir` would point the library at nothing |
| Retired env name of a `ProtectAdmin`+ or secret-class key after W | **fail-closed** refuse to start, names canonical | ignoring `DATABASE_PATH` would open or create the wrong store (`config.go:2519-2544`); an ignored secret alias would disable a feature (`ABS_JWT_SECRET`, OPS2-7) |
| Legacy and canonical name both set with different values, `ProtectAdmin`+ key | **fail-closed**, names both | a base-unit canonical twin would shadow an operator's drop-in (OPS2-12) |
| Other legacy env name after W | ignored + one WARN (**open**) | env carries unrelated names (`HOST`, `PORT`) |
| Same key in legacy and canonical form, other keys | canonical wins + WARN (**open**) | deterministic, visible |
| Usage cannot be recorded | ignored (**open**); the flush retries | telemetry must not fail requests |
| Checkpoint without disk headroom | **fail-closed** before the migration | a full pool is a known outage (`config.go:2521-2528`) |
| Staged restore intent stale or from another build | rejected, moved aside, Degraded (**closed**) | a late restore would discard writes made in between (OPS-14) |
| Backup without a manifest (old archive) | restore proceeds as legacy (**open**) | read support is permanent |

## 9.9 Concurrency contract

Adoption, rebuild, import and contract migrations run in the kernel's migration phase (06 §6.5.1
`P4a`–`P4d`), before listeners and the executor start (03 §3.7), and nothing writes the store before the
pre-migration checkpoint (`P4a`). The rebuild and import steps are bounded by small families (P1 branch
A) and are **sequential by declared necessity** (correctness needs one atomic batch, and the bottleneck
is one Pebble commit — R7 `tasks.Sequential(why)` wording reused); a family over the cap uses 05-storage's
resumable path with its own `tasks.Parallelism`. 05-tasks' copy-and-mark is one batch per run and may
run runs in parallel (05-tasks). Mirror writes are staged in the caller's batch; their correctness comes
from 05-storage's `Expect` preconditions under its commit stripes, not from a store mutex (DATA2-29).
Alias handlers inherit the canonical handler's concurrency. `compat.Handle.Use` is lock-free on the
request path (atomic counters); the flusher is one goroutine per App, owned by the kernel lifecycle. All
migration steps take `ctx` and honour cancellation between batches; a cancelled adoption leaves legacy
as truth.

## 9.10 Config keys, metrics, tests

Config keys this file adds: none beyond the operational dials of §9.2.8, which their owning specs
define; the reader level is compiled into the build.

| Metric | Type | Labels |
|---|---|---|
| `mediacore_compat_legacy_use_total` | counter | `surface`, `name` (bounded: ledger entry), `entry` — dashboards only |
| `mediacore_compat_legacy_last_use_timestamp_seconds` | gauge | `surface`, `name` — from the persisted record |
| `mediacore_compat_mirror_write_failures_total` | counter | `family` |
| `mediacore_storage_level` (05-storage §5S.8; the only level metric) | gauge | `owner`, `kind` (supported, store) |
| `mediacore_build_info` | gauge | `version` |
| `mediacore_compat_adoption_seconds` | histogram | `family` |
| `mediacore_kernel_boot_phase_seconds` (06 §6.9) | gauge, per boot | `phase` (`P−1`, `P0a`, `P0`–`P3`, `P4a`–`P4d`, `P5`–`P8`), `step` (bounded step names) |

| Test | Proves |
|---|---|
| `TestLegacyToleratesNamespacedKeys` (S0) | HEAD-era code paths ignore `c/`, `m/` keys |
| `TestS2bOpensStoreWrittenByS0` (S0, S2b) | the install marker precedes every `c/` key; S2b reads S0–S2 ledger history |
| `TestNativePrefixesNotRedirected` (S0) | `/api/core/vN/`, `/api/<module>/vN/` bypass the 301 |
| `TestCompatGuard*` + `scripts/test_storage_format_guard.py` | open refusal, rollback refusal, Teach floor, sidecar repair after a commit error, both values printed on disagreement |
| `TestContractWaitsForServedLedger`, `TestContractClockResetsAfterRollback` | a contract build on a store whose expand served < W, or whose run was interrupted, adopts and mirrors, never deletes |
| `TestAdoptedPrefixWritesGoThroughMirror` | flip-PR ratchet (OPS-18) |
| `TestMirrorLegacyIndexesResolve`, `TestRebuildHonoursRollbackWrites`, `TestRebuildKeepsMirrorDisabledKey`, `TestRebuildSkipsUndecodableRow`, `TestRollbackReactivationNeverWidens` (with 05-storage's mirror tests) | §9.2.3 under `testing/synctest` with a fake clock |
| `TestLegacyRoleReachUnchanged` (permanent CI gate) | §9.2.6 invariant and its recorded auth-off exceptions |
| `TestEveryLegacyConfigKeyHasCanonical`, `TestShippedDeployEnvNamesArePermanent`, `TestAliasConflictOnProtectedKeyRefusesBoot` | config/env completeness and conflicts |
| `TestCompatLedgerMatchesCode`, `TestNoContractBeforeSunset` | the ledger is the code |
| `TestAliasHeadersAndShape`, `TestAlias_UnknownLegacyFieldRefused` | headers, legacy-shape goldens, strict legacy fields |
| **R-test (rollback rehearsal), per stage**, `make compat-rehearsal` | in CI: build `prev` from `PREV_REF` — by default the latest production release tag; before each flip deploy the operator overrides it with the commit P10 prints (`<bin>.prev --version-json`), because deploys build main and `.prev` is usually not a tag (OPS2-25) — and `cur`; boot `cur` on a seeded store, write through every adopted surface, stop; boot `prev` on the same store, assert reads, write (password reset, deactivate, key revoke, config edit, secret change and clear, enqueue, PATCH a disabled key active), stop; boot `cur`, assert the rebuild/re-import/reconcile honoured `prev`'s writes. The **DATA-6 cases** are explicit rows: a run enqueued, re-queued or interrupted on `prev` is adopted on roll-forward and runs once (05-tasks reconcile); a schedule clock advanced on `prev` is adopted and the kind neither double-fires nor stampedes (clock mirror); a `Requires` stamp written on `cur` is honoured on `prev`; a config value or secret changed or cleared on `prev` is re-imported, including a secret re-entered as blob plaintext, which is re-imported and re-blanked (05-config re-import); an integration URL and password changed on `prev` are re-imported together. Matrix cells (OPS-20): **(a)** the previous release's UI bundle against `cur` (old tabs during W); **(b)** `cur`'s bundle against `prev` after rollback (new tabs, S0 reload prompt); **(c)** ABS conformance against `prev` and `cur`; **(d)** the updater's `Boot` rollback (and `rollback_blocked` past a contract); **(d')** a contract release that fails readiness on first boot is rolled back by `Boot` (OPS2-2); **(e)** restore across levels (archive from `prev` onto `cur`, from `cur` onto `prev`, a staged intent across a rollback, a revocation made after a restore surviving a restart and a rollback, R2-2). Cells a/b are mandatory in every release that moves a UI call, cell c in S2, S4, S6 and S9 (§9.3). The job runs on `compat-gate` PRs and nightly and blocks RC promotion while red (§9.2.1). |

## 9.11 Risk table

| Stage | Risk | L | I | Detection | Mitigation |
|---|---|---|---|---|---|
| S0 | Redirect exclusion too broad: an unversioned client path `/api/x/v2/...` stops redirecting | L | M | `TestCollidingNamespacesStillRedirect`; P5 | exclusion requires `/vN/` and a module-ID shape |
| S0 | `.prev` predates S0, guard and T overlay blind | M | H | P10; the S3 `.prev` probe | deploy S0 to production twice before S3; 05-config keeps plaintext unless `.prev` reports level ≥ 1 |
| S0 | A build path missed by the entry-point sweep breaks once `core/web` exists | M | M | the S0 image job on every non-doc PR; UI-0 done-check | one script, every site listed in §9.1 |
| S0 | A gate runs only after the code shipped (ungated merges, auto-promoted minors) | H | H | `extraction-gates` required; promotion gate | §9.2.1 release gating (14 Q-I, K20) |
| S0–S7 | The HEAD updater applies an adoption release unattended with no guard or rollback | M | H | Degraded `auto_update_paused_until_core_updater` | scheduled apply check-only until S8 (OPS2-5) |
| S1 | Moving telemetry changes metric/label output | M | M | S1 name diff | namespace/scope injected; golden of names |
| S2 | Perimeter move changes Basic-auth exemptions, CSP or rate classes | M | H | S2 perimeter tests | manifest-driven per-route data copied from HEAD; per-request CSP from the mount; goldens |
| S2 | Listener move changes TLS/H3/peer-allowlist behaviour | M | H | smoke on `--http3-port`, peer test from a disallowed address | move code verbatim first; `peer_allowlist` tests move with it |
| S2 | Supervisor kills the process or its runner children mid-shutdown | M | M | journal SIGKILL check, subprocess-mode stop check | stop timeouts and `KillMode=mixed` shipped in S2; budget below them |
| S2b | Strict claims refuse a live books writer | M | H | shadow release with persisted violations | shadow ≥ 14 days; dial back to shadow; per-stage claims (no core owner before its hand-over) |
| S3 | Rollback loses secrets | L | H | R-test | 05-config mirror + T overlay; plaintext kept unless the rollback slot reads legacy-format rows |
| S3 | Keyring missing on restore to a new host | M | H | restore matrix | the 05-config keyring export (one export covers secrets and the derived backup identity, SIMP2-2) |
| S4 | Privilege change from atom expansion | M | H | `TestLegacyRoleReachUnchanged` | permanent gate |
| S4 | Rollback widens a key or a role | L | H | property tests; Teach-build tests | exact-or-disabled mirror (SEC-1); Teach build refuses re-activation and bounds `users.manage`/`settings.manage` (R2-1) |
| S4 | Rebuild drops mirror-disabled keys | M | H | `TestRebuildKeepsMirrorDisabledKey` | `MirrorDisabled` + `Fresh` (§9.2.3 rule 2) |
| S4 | `usermerge` undo or admin debug-edit writes legacy `u:` images at runtime | M | H | test: undo a pre-S4 merge journal on S4 | journal undo for adopted prefixes goes through the mirror writer (ratchet) |
| S4 | Session fallback lets a revoked legacy session live | L | H | test | fallback applies epoch, status, revoked and expiry checks (05-authn) |
| S4 | Rollback floor costs reach: no rollback past S4 r1 once adoption ran | M | M | `make rollback` refusal | r1 production-deployed before r2 (14 K19) |
| S5 | Copy-and-mark over cap drops a run | L | M | P11; Degraded `tasks_adopt_dropped` | finish/cancel listed runs before S5 |
| S5 | Parallelism declarations change runtime behaviour | M | M | duration metrics per kind | carry today's `Concurrency` values; dial `core.tasks.kinds.<kind>.parallelism_cap` |
| S5 | A skipped store below books v66 hits T8 | L | H | `ErrUpgradeFloor` | `min_upgrade_from` (§9.2.2) |
| S6 | Legacy stream projection drops events the UI relied on | M | L | per-type goldens | projections built from HEAD frames |
| S7 | Webhook receivers break | L | M | P9 | `legacy_headers` per instance |
| S7 | Instance-scoped throttles double outbound rate | M | M | provider 429 counts | one `providerhttp` instance per provider per App, asserted in kernel |
| S7 | Operator edits a field an env value overlays | L | L | 403 on an overlay-locked field | lock reason names the env variable |
| S8 | Auto-update users stuck on S8 | M | H | staging install with `auto_update.enabled=true` | signed-release precondition; `allow_unsigned_legacy`; service-owned binary directory |
| S8 | Updater rolls back onto a store it cannot read | L | H | R-test cell d | `ReaderCheck`; `rollback_blocked` |
| S9 | Bookmarks/old tabs hit removed SPA paths | H | L | `ui_route` usage | client redirects through W; reload prompt |
| C* | Contract runs on a store that skipped the expand | M | H | `TestContractWaitsForServedLedger` | store-side preconditions (§9.2.2) |
| C* | Contract merged before window | L | H | `TestNoContractBeforeSunset`; P8; `extraction-gates` | ledger dates + persisted usage + guard |
| C* | Contract crash mid-delete loses identities | L | H | `TestContractCrashMidDeleteKeepsIdentities` | three persisted phases (§9.5) |

## 9.12 Open items

| # | Item | Owner |
|---|---|---|
| O1 | (closed by 05-config §1.12.2) Plaintext secrets in `config_blob` during W: blanked when the rollback slot reads the legacy-format rows, kept otherwise. | — |
| O2 | (closed by 05-rbac §6.1) Final `core:` permission names. | — |
| O3 | `.encryption_key` lives outside the archived DB dir (`settings.go:44-52`, `backup.go:283-428`): restore on a new host needs the 05-config keyring export (SIMP2-2). | 05-config, 05-backup |
| O4 | Env grammar and prefix (`ABO_`) | 05-config §1.3.6 |
| O5 | (closed by 05-storage §5S.9.1) Exact books claim list, generated per stage. | — |
| O6 | (closed by 03 §3.3 `log` row, 05-logging §5) `act:` stays books; audit gets new rows only. | — |
| O7 | CLI flag window "until 1.0" deviates from the two-minor floor for surfaces baked into users' compose files | owner |
| O8 | Pilot cost before any books route group is rewritten to native `httpapi` (SIMP-2 item 4): measure one group (e.g. playlists, 10 routes) | owner, when a group is first rewritten |
| O9 | S0–S8 auto-update: scheduled apply check-only (specified) or a minimal boot-counter rollback in the HEAD updater instead (OPS2-5) | owner |
| O10 | Release gating: required check on gated paths plus promotion gating (specified, §9.2.1) or promotion gating only (14 Q-I) | owner (14) |

## Objections to 03

None open. 03 v1.5.0 carries everything this file relies on: reserved compat namespaces
(`app.Options.ReservedAPINamespaces`, §3.6), R8's three claim kinds, the `compat` package (§3.3), the
merged store guard owned by 05-storage, and the core-to-ABO layer mapping of §3.5.1.

## Out-of-scope defects found

1. Shipped deploy files set env names nothing reads: `AUDIOBOOK_ROOT_DIR` (`deploy/audiobook-organizer.service:97`,
   `deploy/com.audiobook-organizer.plist:38`; deliberately unread, `internal/config/config.go:2569-2575`),
   `AO_DIR` and `AO_DB` (`docker-compose.yml:18-19`; the Docker `CMD` supplies `--db`, `Dockerfile:104`),
   `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`). They mislead operators; S3 removes them.
2. `--db-type` help still says "pebble (default) or sqlite" (`cmd/root.go:489`) although SQLite is
   rejected (`internal/database/store.go:1840-1843`).
3. The HEAD updater exits 0 to restart (`internal/updater/updater.go:331-334`), so under the committed
   unit's `Restart=on-failure` every auto-update leaves the service stopped; S0 fixes it. It also cannot
   write next to the executable in the committed install layout and looks for a raw asset name the
   release may not publish (`updater.go:254-258,318-326`; `deploy/audiobook-organizer.service:41-43,88,122`).
4. A revoked API key can be re-activated: `PATCH /api/v1/auth/api-keys/:id {"status":"active"}`
   (`internal/server/handlers/apikeys.go:336-372`) reaches `SetAPIKeyStatus`, which sets `Status` without
   checking `RevokedAt` (`internal/database/pebble_store_auth.go:1226-1247`); filed as a live hotfix in
   `todo.d/2026-11-04_platform_core_followups.md` and made a guarantee by the identity Teach build.

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
- integration pass (lead): secret names use the dotted `integrations.<kind>.<instance>.<field>` form of 05-config §2.2.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1 / SEC2-1 / SEC2-2 / DATA2-3 / DATA2-5 — Teach build per adopt/expand sequence (§9.2.1, §9.2.2 table): legacy-side guards, journal, rollback floor via adopt-step `Floor`; identity Teach build = S4 r1 (05-authn §11.1); levels renumbered (S4 r1 3, S4 r3 4, S5 5, S8 6); live hotfixes may ship from S0.
- R2-2 / SEC2-3 / DATA2-4 — revocations, including post-restore re-application, reach the legacy row in the same batch (§9.2.3, S8, R-test cell e).
- R2-3 / DATA2-1 — per-stage ownership view referencing 05-storage §5S.9.1; claim-mode changes are stamped level steps; S2b claim test per stage.
- R2-4 / DATA2-2 / OPS2-11 — minimum-upgrade-from release (02 N14) replaces "no stepping stone"; legacy migrations before claims; T8 and S10 bounded by `min_upgrade_from`; adopt steps stay registered.
- R2-5 / MOD2-1 / MOD2-2 / MOD2-3 / MOD2-4 / MOD2-11 — S2 rewires all five `sdnotify` importers; no file of `cmd` moves; `flight` rewritten onto slog; references to 04's per-stage gate.
- R2-6 / OPS2-1 — §9.1 releases row corrected (ungated merges, auto-promotion every 10 RCs); release gating in §9.2.1 (required check + promotion gate, 14 Q-I); W counts minors and 30 days.
- R2-7 / SIMP2-1 / COR2-6 — S5 schedule keys `modules.books.<subsystem>.schedules.<name>` via `ScheduleFromConfig`.
- R2-8 / COR2-7 — boot phases per 06 §6.5.1 (P−1 … P8) in the boot metric and §9.9.
- DATA2-6 — S0 writes `c/storage/install` before any `c/` key, with `storage/keys`; `TestS2bOpensStoreWrittenByS0`.
- DATA2-8 — invites read in place, not mirrored (§9.2.3, §9.6 row 25).
- DATA2-9 — `RequiresServed` uninterrupted-run rule; blanking uses the `.prev` probe.
- DATA2-10 — S7 import with M1.4 step 2a re-import and blob mirror; S7 rollback row corrected.
- DATA2-13 / DATA2-14 — C5 final copy-and-mark before the raise; contracts in three persisted phases.
- DATA2-18 / DATA2-20 / DATA2-28 / DATA2-29 — checkpoint consumption wording; seed-role edits on rollback (S4); fresh-install level rule; stale cross-references (rule 2 note, §9.9 mutex, bookkeeping keys and step names).
- SEC2-4 / SEC2-5 / SEC2-6 / SEC2-17 / SEC2-19 / SEC2-20 / SEC2-21 / SEC2-22 / SEC2-25 — ABS confinement test; S4 alias target/selector table; fragment invite links; `LegacyAuthGinUntil`; `client_trust.go` stays until S4; access-log refusals before S4; auth-off tightening recorded (§9.2.6); mux/gin agreement test; stale note deleted.
- OPS2-2 … OPS2-26 — `RequiresSelfServed` and cell d'; updater `Boot` in P−1; updater layout and test-based done-check; scheduled apply check-only until S8; rolled-back version skipped; boot-read usage gate, recording-build rule, reference-store rule; blanking probe; `Requires` stamps copied not deleted; storage-efficiency ordering rule; canonical names only where no permanent alias; one build-ID mechanism; Docker job on every non-doc PR with a full image run; S2/S3/S4 releases; per-stage done-check rule and one ABS stage list; `deploy-wait-ready.sh` polls while activating; reference-store gate; webhook dead-letter on rollback; 05-config single env list; `KillMode=mixed`, container budget, unit install owner action; one level metric and phase labels; `PREV_REF` and the `compat-rehearsal` job; deploy refuses a staged restore.
- MOD2-5 / MOD2-6 / MOD2-8 / MOD2-9 / MOD2-10 / MOD2-13 / MOD2-15 / MOD2-16 / MOD2-17 / MOD2-18 / MOD2-19 / MOD2-21 — genproto check as build pair; tidy coupling; coverage ratchet (SC5 report-only); core scripts under `core/scripts/ci/`; hello from S2; go1.27.3 bump as S0's first PR; empty `core/go.sum`; POSIX build script, Docker frontend stage, triggers, classifier; explicit workspace build order; CI table completed; deadcode from the root; SC numbering.
- COR2-1 / COR2-3 / COR2-8 / COR2-9 — projections mounted from `Registrar.Realtime().Projection`; books notifies through `Deps.Notify`; storage opens at `core.storage.path`; kernel without storage until S2b, hello and G6 from S5.
- FE2-1 / FE2-3 / FE2-5 / FE2-6 / FE2-8 / FE2-9 / FE2-10 / FE2-16 / FE2-17 / FE2-18 / FE2-19 / FE2-23 / FE2-25 — per-request CSP and `Reporting-Endpoints`; Activity on task aliases until S9 with the S5 legacy-stream bridge; build ID; non-SPA infra segments; invite fragment and R-test cell; `core.json` per stage; API-only placeholder; five extra redirects; ESLint permission rule; owner route names; UI-0 import swap and grep; no credential exempt from cross-origin; S9 after S3–S8, `/modules` at S4.
- SIMP2-2 / SIMP2-4 / SIMP2-5 / SIMP2-6 / SIMP2-7 / SIMP2-12 / SIMP2-14 / SIMP2-15 / SIMP2-16 / SIMP2-17 — one keyring export; webhooks at S7; operator overlay, never persisted; facade key machinery deferred; no books row rewrite at S7 and a `FacadeWriter` check; ledger row holds class and dates; level steps (floors and contracts); `parallelism_cap`; stale note deleted; Deluge behaviour routes stay on the legacy mount.
- integration pass (round 2, lead): legacy-side guard list includes the integration URL/secret bound.
