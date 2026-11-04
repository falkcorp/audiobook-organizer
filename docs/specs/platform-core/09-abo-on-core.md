<!-- file: docs/specs/platform-core/09-abo-on-core.md -->
<!-- version: 1.8.1 -->
<!-- guid: 3c82c917-2fc4-4385-b085-73d143a2374f -->
<!-- last-edited: 2026-11-04 -->

# 09 — ABO on core: the strangler-fig sequence

Evidence commit: `748fd787a`. Raw data: [`raw/`](../../plans/platform-core/raw/00-index.md).
Names, paths and rules come from [03-architecture](03-architecture.md) (normative, v1.10). Package
moves are **not restated here**: [04-package-mapping](04-package-mapping.md) §8 keys every move by
this file's stages and is authoritative for a single package (MOD-6); each stage below names its 04
rows. This file is the single owner of **sequencing and compatibility windows** for ABO: where a
05-* "Migration from ABO" section and this file disagree on *when* something happens or *how long*
a legacy surface lives, this file wins; on *how* a mechanism works, the owning 05-* file wins.

## 9.0 Purpose & scope

How audiobook-organizer (ABO) becomes **app = core + module `books`** without a flag day. Every
stage ends with ABO shippable, deployable, and reversible with `make rollback` (`Makefile:892-917`;
D3, gate G5, [02](02-decision-record.md) §2.4) to the previous release **by a binary swap**: during
S0–S10 every stored family keeps exactly one format (§9.2.3; round-3 resolution R3-1, 02 decision 13),
so the previous release reads every row the new one wrote. The rollback costs and consequences this
leaves are stated once, in the table of §9.2.1 "Rollback". Deploys use the operator's recipe
(`Makefile.local.example:88-110`, the `deploy` target, which is not committed); the CI-checkable proof
of G5 is the prev-build one-format tests that every PR runs in `extraction-gates` plus the nightly
rollback rehearsal `make compat-rehearsal` (§9.2.1, §9.10), not a deploy target. This file answers old Q5 (§9.6) and defines the
one compatibility window (§9.2.1). It does not design the core contracts (05-*, 06, 07); it says in
what order ABO adopts them and what stays compatible.

**Stage numbering (changed in v1.1.0).** Gin is never retired (cross-cutting resolution 2), so old S10
("native books API; gin retired") is removed and old S11 (final cleanup) is renumbered **S10**. Two
stages are inserted without renumbering the rest: **S2b** (core opens the store; books on claims,
OPS-12) and **UI-0** (UI groundwork before S4, FE-2/FE-12). S0–S9 keep their v1.0.0 numbers, so
"09 S4" etc. in other files still point at the same stage. The dispositions' "S0–S11" means this
whole sequence (S0–S10). A stage may ship as several **releases** (r1, r2, …; §9.4.1); 12's split
readiness criteria are named **SC1–SC10** so that "S5" always means this file's stage (MOD2-21).

Out of scope: TV/Movies (10), multi-app (11), the repo split (12), and moving any stored family to a
native core keyspace (**cutover**, §9.13, 02 N15). The deferred-build packages (`acquire`, `arrfacade`,
`integrations/indexer`, the download grab/monitor pipeline, non-Deluge adapters; 03 §3.4) are built
in **no** stage; neither is the facade-only key machinery (`PurposeFacade`, query-key extraction,
`FacadeGroup`; SIMP2-6), which `archtest.DeferredMembersAbsent` keeps out from S2 (SIMP3-2). The
storage-efficiency program that owns `storage_format` 2 (`internal/database/storage_format.go:44-50`)
is a separate track; this sequence raises no reader level (§9.2.2). **Ordering rule (OPS2-10):** that
program's release B ships only as a books `DataMigration` on core (S2b or later); if it ships earlier,
05-storage's legacy projection constant (`LegacyMaxFormat + 1`) moves to cover it in the same release,
so no pre-guard build can accept a raised store's projection.

## 9.1 Current state at HEAD (compatibility-relevant facts)

| Fact | Evidence |
|---|---|
| One Pebble store, one WAL, one backup unit; 258 registered key families (243 namespaced, 2 bare, 10 legacy, 3 v1) | `internal/database/keyfamilies.go:37-345`; `raw/05-storage.md` §2–§3 |
| No family starts with `c/` or `m/`; a key outside every family is counted as `(unregistered)`, never deleted | `internal/database/census_families.go:128`, `keyfamilies.go:19-26` |
| `idx:` is ONE registered family shared by auth (`idx:user:`, `idx:role:name:`, `idx:sess:user:`, `idx:apikey:`, `idx:invite:username:`, `idx:oauth:`) and books (`idx:bv:`, `idx:col:name:`, `idx:ubs:status:`, `idx:upl:`, `idx:sidx:dirty:`, `idx:abs_sess:`) | `keyfamilies.go:64`; `pebble_store_auth.go:67,142,966,1126,1131,1335,1670`; `oauth_identity.go:32,78` |
| `setting:` is books' runtime-marker store (56 non-test `SetSetting` call sites), not only config | `internal/database/settings.go:316`; review DATA-1 |
| Storage-format guard refuses a store **newer or older** than the build; `make rollback` asks `.prev --print-storage-format`, reads `<db>.storage-format` with `sudo -n cat` | `pebble_store.go:670-680`; `Makefile:892-917`; `scripts/storage_format_guard.py`; sudoers lines `docs/system/deploy-and-gpu-ops.md:208-218` |
| Precedent: opv2→opv3 dual-write in ONE batch so the previous binary can be redeployed; reconcile every boot. This is HEAD's own stored format of the ops families, which core keeps (§9.2.3) | `internal/database/pebble_store_ops_v3.go:29-42` (D25); `store.go:1861-1870` |
| Precedent: renamed op IDs via `OperationDef.FormerIDs`, counted by `audiobook_organizer_operation_deprecated_def_id_total` — a process-local counter whose rationale is "survives in Prometheus history" | `internal/operations/registry/aliases.go:16-52`; `internal/metrics/metrics.go:98-108` |
| The run dispatcher skips, and the resume sweep drops, a run whose definition ID the build does not register | `internal/operations/registry/dispatcher.go:113-118`; `resume.go:29` |
| Nothing scrapes `/metrics` in this repo; `/metrics` accepts any authenticated credential (an API key in practice) | `deploy/prometheus/README.md:9-10`; `internal/server/server_lifecycle.go:1735-1747` |
| Retired routes: `gone(replacement)` → 410 behind the original auth, one release, then deleted; 45 stubs | `internal/server/gone.go:14-36` |
| Unversioned `/api/*` is 301-redirected to `/api/v1/*` unless excluded (v1, v3, health, events, metrics, ABS-reserved) | `internal/server/server_lifecycle.go:1790-1822`; `wire_abs_routes.go:308-345` |
| 13 permission atoms; 4 seed roles **rewritten every boot**; admin checked by role **name**; permissions resolve by **exact string match** over the role's stored list, so an unknown (namespaced) string grants nothing; an API key with **empty** scopes holds all of its owner's role permissions; key auth accepts only the `abk_` prefix | `internal/auth/permissions.go:24-85`; `seed.go:80-129` (called at `server_lifecycle.go:1102`); `middleware/auth.go:76-99,405-408,430-455,522-537` |
| Live access-widening defects, fixed by **the six early fixes of 05-authn §11.1** (the single list, SEC5-11; among them, early fix 6 (R6-5, SEC6-3): `SetAPIKeyStatus` and `SetAPIKeyExpiry` rewrite the key row without `apiKeyMu` while `TouchAPIKeyLastUsed` holds it for a read-modify-write on every key-authenticated request, so a touch can write back the pre-revoke row and silently undo a revocation; `PATCH /api/v1/auth/api-keys/:id {"status":"active"}` re-activates any key, revoked included, and the auth path checks only `Status`; `GET /api/v1/auth/sessions` returns the bearer session token as `id`; a scoped key can mint an empty-scope key; `RequireAdmin` ignores API-key scopes; the Basic gate admits `<username>:` when the stored password is empty) | `handlers/apikeys.go:336-372` → `pebble_store_auth.go:1222-1247`; `pebble_store_auth.go:1226-1248,1254-1268,1284-1302`, `middleware/auth.go:382`; `middleware/auth.go:335-344`; `handlers/auth.go:525-536`; `handlers/apikeys.go:199-210`; `middleware/auth.go:522-537`; `middleware/basicauth.go:81-87` |
| Config: ~251 viper defaults, `AutomaticEnv` with no prefix, the `BindEnv` aliases (count generated from 05-config §1.12.4's alias table); whole config persisted as one flat `config_blob` + 4 encrypted secret rows (OpenAI key, Google Books key, Hardcover token, Basic password); every other secret sits in the blob in plaintext | `internal/config/config.go:1048-1912,2621`; `cmd/root.go:625`; `persistence.go:884,1955-1958,1996-2008` |
| Precedence: an env name that HEAD reads through `AutomaticEnv`/`BindEnv` is only a **default**: the blob is laid over it and the encrypted secret rows over that, except the names in `envLockedSettings`, `applyEnvAuthoritativeConfig` and `SyncConfigFromEnv`; an env value set to `""` counts as unset (viper's default). Compose always sets `BASIC_AUTH_ENABLED=${BASIC_AUTH_ENABLED:-false}`, `BASIC_AUTH_USERNAME` and `BASIC_AUTH_PASSWORD`, so a UI-enabled Basic gate works on compose only because of this order (OPS5-1) | `cmd/root.go:625`; `persistence.go:1131-1170,1186-1199,2023-2057`; `config.go:2241-2253,2435`; `docker-compose.yml:20-22` |
| State directory: with no explicit state dir set, `EnsureSecureStateDir` falls back to the database's directory when the compiled default cannot be created, so on Docker (unprivileged user, one data volume) and launchd installs `.encryption_key` lives beside the database | `internal/config/state_dir.go:118-155`; `Dockerfile:93-96,104`; `docker-compose.yml:17-22`; `deploy/com.audiobook-organizer.plist:36-43` |
| Shipped deploy files: unit `Type=notify`, `TimeoutStartSec=300`, **no `TimeoutStopSec`, no `KillMode`**, `Restart=on-failure`, `StartLimitBurst=5`/60 s, sets `HOME`, `GOMEMLIMIT`, `GOGC`, DB path only from `Environment=DATABASE_PATH`; compose sets `BASIC_AUTH_*`, no `stop_grace_period`; plist sets `DATABASE_PATH`, no `ExitTimeOut`; Docker `ENTRYPOINT ["/app/audiobook-organizer"]` (no entrypoint script), `CMD … --db /data/audiobooks.pebble`, HEALTHCHECK `/health` | `deploy/audiobook-organizer.service:81-112`; `docker-compose.yml:18-22`; `deploy/com.audiobook-organizer.plist:36-43`; `Dockerfile:100-104` |
| Env names in shipped files that nothing reads: `AUDIOBOOK_ROOT_DIR` (unit, plist; deliberately unread), `AO_DIR`, `AO_DB` (compose), `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`) | `internal/config/config.go:2569-2575`; no Go reader at the evidence commit |
| Warmup is 130–200 s against a hard-coded 300 s in `TimeoutStartSec`, `deploy-wait-ready.sh` default and `make rollback`; HEAD extends the start timeout per startup step (store open, each migration, index open); `deploy-wait-ready.sh` takes only `BASE_URL [TIMEOUT]` and runs on the operator's machine | unit `:28,87`; `scripts/deploy-wait-ready.sh:8-13`; `Makefile:916`; `internal/database/startup_notify.go:15-27`, `internal/search/register.go:55` |
| Both Dockerfiles copy only `go.mod go.sum` before `go mod download && go mod verify`; `.dockerignore` excludes `.git`, **`docs`**, root-level `*.md` (no `**/`, so Markdown below the root stays in the context), `.github`, `testdata`, `dist` and the top-level and `web/` `node_modules`/`dist`, among others — so a `//go:embed` of anything under `docs/` fails inside the image build (MOD4-1) | `Dockerfile:54-55`; `Dockerfile.build-cgo:52-53`; `.dockerignore:5-32` (`docs` `:11`, `*.md` `:30`) |
| Prereleases skip Docker; no PR job builds the image; the first image of a version is built by the production release | `.github/workflows/prerelease.yml:48`; `release-prod.yml:53` |
| `npm ci` runs in 12 places, none of which builds anything first (this is the one list of build sites; 07 §7.3.2 cites it, MOD5-13) | `Dockerfile:18`, `Dockerfile.build-cgo:17`, `prerelease.yml:51`, `release-prod.yml:58`, `ci.yml:379`, `e2e.yml:167`, `binary-smoke.yml:55`, `security.yml:82`, `vulnerability-scan.yml:62`, `.woodpecker/checks-build.yaml:85`, `scripts/ci_remote.py:516`, `Makefile:194` (`web-install`) |
| Updater: downloads `releases/latest`, looks for a raw asset `audiobook-organizer-<os>-<arch>`, treats any `latestVersion != currentVersion` as an update, deletes `.old` after the swap, restarts with `os.Exit(0)` — under `Restart=on-failure` systemd does not restart it; the maintenance-window op's step 1 calls `DownloadAndReplace` too; the committed unit installs the binary into a root-owned system directory with `sudo cp` and runs as `User=audiobook` with `NoNewPrivileges=yes`, so the updater cannot write next to the executable | `internal/updater/updater.go:153,254-258,278,295,318-326,331-334`; `internal/server/scheduler_maintenance_window_op.go:72-91`; `deploy/audiobook-organizer.service:41-43,88,122` |
| Live restore extracts over the running store | `internal/backup/backup.go:777-870` |
| Web client: `API_BASE = '/api/v1'`, no build/version check; 5 hard-coded permission constants and a legacy-atom scope picker; 52 source files import `react-router-dom` | `web/src/services/api.ts:24`; `hooks/useLibraryPermission.ts:9,11`, `hooks/useCanOrganizeLibrary.ts:9`, `hooks/useCollections.ts:24`, `components/system/ThrottlePanel.tsx:24`, `components/settings/APIKeysTab.tsx:49-61` |
| **Releases and merges are not CI-gated (OPS2-1).** Main merges without a pre-merge gate (CI runs afterwards and auto-reverts); an RC is cut on every push to main with no dependency on CI; `check-rc-ordinal` **auto-dispatches the production release** when a base version reaches 10 RCs, which builds the image and moves `releases/latest` (followed by the updater and `:latest` users); installs jump straight to `releases/latest` | `.github/workflows/auto-revert.yml:8-11`; `prerelease.yml:7-13,59-131`; `release-prod.yml:43-65`; `updater.go:295` |

## 9.2 Compatibility model (used by every stage)

### 9.2.1 The one compatibility window (SIMP-7)

This section is the **only** definition of the window. `compat` implements it once (§9.2.9); httpapi
aliases (05-http-api §4.6), config and env aliases (05-config §1.12.4), permission atoms on the API
(05-rbac §6.3), task kinds (05-tasks), SSE projections (05-realtime) and metric aliases (05-metrics §13)
all register against it and define no window of their own.

**Stored data has no window (R3-1).** During S0–S10 no step changes the stored format of an existing
family (§9.2.3): nothing is copied into a core keyspace, mirrored, rebuilt at boot or handed over
between claim owners, and no step deletes legacy data. The window applies to **surfaces** only.

Every renamed or moved surface goes through two steps, **never both in the same deployed build**:

1. **Expand** — the new form is served; the old form is still served, through an alias that records
   its use. Where a client must understand something before a server starts sending it, the
   client-side reader ships in an earlier release than the server change (S0's `X-App-Build` reader;
   the legacy SPA's `/login/temp` fragment reader, which ships with S4's temp-login change; the legacy
   SPA's `/invite#token=` reader, which ships with S4 while core keeps issuing the `/invite/<token>` path
   form until S9, so an invite issued on S9 still opens after a rollback to S4 r2–S8 — S4 r1 is dark and its SPA has no fragment reader; FE4-2, FE5-4, FE6-5).
2. **Contract** — after **W**, and only through a contract PR (§9.5), the old form stops being served
   (410 or refusal for one more production minor), then is deleted. Nothing is removed or turned into
   410 by a clock (OPS-10): `Sunset` headers are advisory.

**W** requires (a) **two production minor releases and 30 days** after the expand shipped — minors are
auto-promoted every 10 RCs (§9.1), so (a) alone can be as short as about 20 merges and the 30-day floor
always applies — and (c) **no recorded use for 14 consecutive days**, read from the persisted usage
record (OPS-16): each surface's handle writes `{count, last_used_at}` to the store in batched flushes,
exposed by `GET /api/core/v1/compat/usage` together with the gate as the server computes it
(`gate_met`, `gate_reason`, `last_recording_boot_at`, `recording_continuous_since`; OPS3-18) and by the
gauge `mediacore_compat_legacy_last_use_timestamp_seconds`. The process-local counter
`mediacore_compat_legacy_use_total` stays for dashboards only; a restart cannot reset the gate. The
usage gate is sound only if it can see every use (OPS2-7):

- **Boot-read surfaces** (`config` and `env` aliases, resolved in 06 P0 before storage exists) record
  `Handle.Present(bootID)`: P0 buffers the uses and the kernel flushes them after the store opens; the
  record keeps `last_seen_set_boot` and `boots_seen`. Their gate is "not set on any boot within the last
  14 days, **and** at least one recording boot in that period" — a name set in a unit that is never
  restarted is not "unused". Which surface a name records (`env` or `config`) is 05-config §1.12.4's.
- For **every** surface, the 14 days also require that every boot in them listed the `usage` duty in
  the served ledger (`c/storage/served/<owner>/last`, 05-storage §5S.3.7; SIMP4-4) and that no foreign
  open was detected: days run by a non-recording build (a rollback to a pre-S2b build, detected through
  the foreign-open record, §9.2.2) do not count, and an owed `usage` duty restarts the clock at its
  **`detected`** time, the end of the unrecorded interval (DATA6-7): the usage consumer (06 P4d step 5) reads
  the duty through storage's duty handle and, in the batch that clears it (05-storage §5S.3.7 `Duties`;
  COR6-2), writes `c/compat/recording_since/<owner>` = `detected`, which `RecordingContinuousSince` reads.
  On a boot with no previous `last` (the first S0 boot on a HEAD store) `since` is zero, i.e. unbounded. The ledger's `last` is replaced only in the served-ledger commit at
  06 Run step (3b), after every boot step that reads it has committed, so a boot that fails in between
  leaves the previous record and the owed duties in place (R5-5; COR5-3, DATA5-6).
- (c) is evaluated on the **reference production store** (P8). On every other store, the contract
  build's first boot reads that store's own usage record; a removed or `Gone` surface used there within
  14 days reports Degraded `legacy_surface_removed_in_use` (surface and name) with a Settings banner, and
  the release notes list every removed surface (OPS2-19).
- After W, every window alias of a **secret-class** key is treated like a `ProtectAdmin` key: boot
  refuses it and names the canonical variable, instead of ignoring it (§9.2.5, §9.8).

Exemptions from (c): metric aliases, because a scrape reads every family and the server cannot see
which name a dashboard uses (05-metrics §13; their gate is P6), and surfaces marked **permanent**
(§9.6).

**Rollback = binary swap (R3-1).** At every stage `make rollback` (and, from S8, the updater's `Boot`)
returns to the previous release by swapping binaries, and that release reads every row this one wrote.
This table is the **one complete list** of what a rollback and the roll-forward after it cost or change
(OPS4-11); 01 goal 3 and 14 K13 refer to it, and the R-test asserts exactly these rows. Each row's text
goes into the release note of the release that introduces it, and every row is listed in the §9.3 overview and the Rollback row of
the stage its "From" names: `scripts/ci/check_rollback_rows.py` in `extraction-gates` fails when a row's "From" stage does not list
it, or a stage lists a row whose "From" is later (OPS8-6).

| # | Cost or consequence | From | Release-note text |
|---|---|---|---|
| 1 | **Moved plaintext secrets must be re-entered on the older build, which fails closed until then** (R4-2, R5-1(b)). The four secrets HEAD already stores encrypted (OpenAI key, Google Books key, Hardcover token, Basic password; `internal/config/persistence.go:1955-1958`) are **not moved**: books' legacy config store reads and writes HEAD's encrypted rows in place, so they work on every older build, and the Basic gate never locks an older build out (OPS5-2). Every other secret sits in the blob in plaintext; its first core write stores it encrypted in `c/secrets/` and replaces **every** plaintext copy — the blob field and the gap-fill copy in `<db parent>/config.yaml` (HEAD's `filepath.Dir(DatabasePath)/config.yaml`, beside the DB directory; DATA8-13) — with an unguessable, never-matching sentinel, never an empty value. After a rollback the older build therefore refuses those credentials: webhook receivers reject its signatures, Deluge and SMTP refuse its passwords, providers reject its keys (an empty value could have matched an empty secret: DATA4-1, SEC4-1, OPS4-1). On roll-forward the encrypted copy wins unless the legacy value differs from the sentinel, which means an operator re-entered it on the older build: that value is re-imported and re-sentinelled (05-config §1.12.2). An older build's unrelated settings save re-persists the sentinel, never a stale value (DATA4-3) | S3 | "After a rollback, re-enter every secret saved on this release except the Basic password and the OpenAI, Google Books and Hardcover keys (webhook secrets, Deluge and SMTP passwords, other provider keys): until you do, the older release refuses them." |
| 2 | **One re-login, both ways (S4 on).** Sessions and one-time tokens issued by the new build are stored hashed in books' extension family, which no older build reads, so a rollback signs every browser out once. On the first S4+ boot after a boot by a build without the `auth` duty (the served ledger's owed `auth` duty, 05-storage §5S.3.7), every native session and native one-time token created before that interval is revoked in one audited batch (`core.session.revoke_after_rollback`), so a session the user ended on the older build does not come back (05-authn §4.3, §11; DATA4-14, SEC4-9, SEC5-3) | S4 | "A rollback, and the upgrade after it, each sign every browser out once." |
| 3 | **Temp-login links, and invites that exist only in the new build, are void on a build older than S4** (resend). An invite issued from S9 on (fragment form `/invite#token=`) stays valid after a rollback to S4 r2–S8, because those builds run core auth and their legacy SPA reads the fragment form from S4 r2 (FE5-4, FE6-5) | S4 | "Resend invites and temp-login links issued on this release after a rollback to a release older than S4." |
| 4 | **API keys created by the new build do not authenticate on an older build.** They carry the `mck_` prefix; each is a legacy `apikey:` row holding only the legacy atoms its canonical scopes admit — or, when none is admitted, a scope that grants nothing on an older build, never an empty list (R5-6) — with the canonical scopes in books' extension family (05-authn §4.4; DATA5-5, SEC5-3). An older build lists and can revoke them but accepts only `abk_` keys for authentication (`middleware/auth.go:76-99`); legacy keys keep working | S4 | "Keys created on this release do not work after a rollback; legacy keys do." |
| 5 | A role the new build wrote grants an older build only the legacy atoms whose legacy reach its native permissions dominate on the frozen snapshot release (05-rbac §6.3 row rule; narrower, never wider); its namespaced scopes are in books' extension family, and if the older build edits the role row the extension is suspended until an admin re-confirms it (R4-1) | S4 | "Custom roles may grant less after a rollback; re-confirm roles edited on the older release." |
| 6 | A run of a task kind the older build does not register is dropped by its resume sweep, as on any restart, and runs again on schedule after roll-forward; an `opstate:` → checkpoint move resumes from zero once (05-tasks T2) | S5 | "Runs of new task kinds restart after a rollback; one checkpointed job may start over." |
| 7 | **System-log entries written on S5+ are not shown by an older build.** From S5 `logjournal` writes the journal, and the same release removes the activity `Writer`'s line parsing that fed HEAD's Activity system view, so an older build's system view shows none of the entries made on S5+ (OPS5-12) | S5 | "System-log entries written on this release are not shown after a rollback; they reappear after the upgrade." |
| 8 | Webhook deliveries pending in `c/outbox/` at rollback are not sent by the older build; on roll-forward they are delivered once, or dead-lettered past `core.notify.max_age` (OPS2-20) | S7 | "Webhooks queued at rollback are delivered after the upgrade, or dropped after 24 h." |
| 9 | **Integration instances created natively on S7+** (a second webhook, mail target or download client, in `c/integrations/instances/`) do not exist on the older build: events that happen while rolled back are never queued for them (row 8 covers only deliveries already pending; OPS5-12) | S7 | "Integrations created on this release do nothing after a rollback; events during the rollback are not delivered to them." |
| 10 | An integration URL changed on the older build suspends that instance's `Local` grant on roll-forward until an admin re-confirms it (extension record hash, R4-1, SEC3-5) | S7 | "Re-confirm Local for integrations whose URL you changed on the older release." |
| 11 | Bookmarks to `/books/...` answer 404 on the older build's SPA | S9 | "`/books/...` bookmarks need the new release." |
| 12 | An older build's **settings reset** does not touch values that live only in core records — books' extension records and `c/secrets/` records of names with no legacy copy — so they are effective again after roll-forward; a secret that had a legacy copy follows the reset, because a legacy copy that differs from its sentinel (re-entered, cleared or removed) is re-imported at P4d (05-config §1.12.2 consequence (b)). An older build's **factory reset** (`PebbleStore.Reset`, `internal/database/pebble_store.go:6310-6331`) deletes the whole key range, `c/` and `m/` included, so core records go too; a sentinel left behind in `<db parent>/config.yaml` is recognisable without its record and reads as unset (05-config; DATA5-10, DATA8-13). **A core build's reset only stages** (S2b on; R7-2, R8-1; 05-storage §5S.3.15): both of HEAD's whole-store wipes are `KindReset` of the one restore swap (one intent slot `restore/pending.json`, one journal, one token pair) — `ResetFactory` for `POST /system/factory-reset` and `ResetStore` for `POST /system/reset` (store and settings only, no file effects; COR8-1). Books' gin handlers stage both through `legacy.Maintenance` behind HEAD's chain (S2b–S4 r1), and from S4 r2 both legacy paths are `Alias`es of the `internal/storagehttp` route; nothing destructive runs before the restart. Every target is pre-validated at staging, and a dangerous `RootDir` answers 400 with nothing staged. The next boot sets each target aside beside itself, on its own filesystem (a `Recreatable` auxiliary DB on another filesystem is handed to the hook for deletion), and is a fresh install with a new install ID whose fresh-install batch stages HEAD's factory defaults (`factoryDefaultConfig`, `root_dir` empty, `setup_complete` false), so a stale `ENABLE_AUTH=false` loses as it does after HEAD's reset (SEC8-5); HEAD's whole-range `PebbleStore.Reset` cannot run under claims (DATA6-6). A set-aside that fails moves back what it moved, journals `rejected` and boots the untouched store `degraded` (COR8-3, DATA8-1). For `ResetFactory`, HEAD's side effects (the gap-fill file reset, the Open Library data and, unless 14 Q-K drops it, the library `RootDir` wipe) run in books' post-reset hook after the swap journal reads `applied`; the hook **never fails boot**: an entry it cannot delete is logged, audited and reported as `factory_reset_cleanup_incomplete`, the hook always records `hook_done`, only a crash reruns it, and an app with no hook gets `hook_done` from the kernel (COR8-4, OPS8-5). Moving `previous/` back restores the store, its secrets and its settings but not the files that hook deleted (05-storage §5S.3.15). While a reset is staged or in flight, `make rollback`, `deploy` and the updater refuse with `restore_pending` or `restore_inflight`, whose message names the kind (§9.2.2) | S2b (factory and store reset), S3 (settings reset) | "After a settings reset on an older release, reset again on the upgraded one; after a factory reset there, re-enter every secret." (S3) — "A factory reset or a reset of the database on this release takes effect at the next restart; undoing a factory reset restores the database but not the library files or Open Library data it deleted, and files it could not delete are listed in the log and in health." (S2b; OPS8-6) |
| 13 | Any widening extension value whose legacy basis an older build changed (a role's scopes, an instance's `Local`) stays suspended after roll-forward until an admin re-confirms it (05-rbac §6.3, 05-metadata M1.10; no config key declares a basis, SIMP6-1) | S4 | "Re-confirm suspended roles and integrations after an upgrade that follows a rollback." |
| 14 | **Time spent on a build older than S2b does not count toward removing legacy names**: such a build records no usage, so every surface contract's W clock restarts after it (§9.2.1 (c); OPS5-12) | S2b | "Time spent on a rollback to a release older than S2b does not count toward removing legacy names." |
| 15 | **Restoring an archive written on S3+ with an older build has rows 1–5's costs** (its moved plaintext secrets are sentinels, its `mck_` keys and native sessions are void, its role scopes absent); restore such an archive with a build at or above the archive's stage (DATA5-19) | S3 | "Restore a backup made on this release with this release or a newer one." |
| 16 | **Renamed series answer only under their old names after a rollback** below the stage that renamed them (Pebble S2b, ops S5, AI S7, process gauges S8; §9.2.7): the `deploy/` dashboards and rules switched in that PR, and any operator dashboard moved to the new names, show no data until the upgrade (OPS6-9) | S2b | "After a rollback, renamed series answer only under their old names; dashboards switched to the new names show no data until the upgrade." |
| 17 | **Settings that exist only on S3+** (keys with no legacy name, e.g. `core.auth.disabled_allow_remote`, `core.integrations.egress.lan_allow`, `core.updater.*`, `core.tasks.kinds`) are ignored by an older build, which runs its own defaults (OPS6-9) | S3 | "Settings that exist only on this release are not applied after a rollback; they return after the upgrade." |
| 18 | **A replacement scrape key does not work below S4**: from S4 r2 a scoped legacy scrape key must be replaced by a key carrying `core:metrics.read` (§9.2.6), which is an `mck_` key that every pre-S4 build refuses (row 4), so Prometheus gets 401 for the whole rollback (OPS6-9) | S4 | "Keep the old scrape key configured as a second scrape job until you no longer need to roll back below this release; the new key does not work there." |
| 19 | **Auth or the Basic gate enabled through an operator name stays enabled after a rollback** (R7-1; SEC7-1, SEC7-2). No older build has R6-6's exemption, so an operator value that enables auth (`ABO_CORE_AUTH_MODE=required`, `ENABLE_AUTH=true`, the operator file) over a stored `enable_auth: false` would turn auth back off on the build rolled back to. So when the effective `core.auth.mode` is `required` and the blob stores `enable_auth: false`, books' legacy config store writes `enable_auth: true` at P4d step 1 — from **S4 r1**, so a rollback S4 r1 → S3 and S4 r2 → S4 r1 or S3 keeps auth required — as an audited core write (`core.config.legacy_narrowed`); `core.http.basic_gate.enabled` and `core.http.rate_limit.enabled` follow the same rule from **S3**, written `true` whenever the effective value is `true` (SEC8-1, SEC8-6): without a Basic password the older build can read (its encrypted row or its legacy `BASIC_AUTH_PASSWORD`), a hotfixed older build refuses every request (early fix 5), which is fail-closed, never open. It is the one exception to "stored values of locked keys are kept" and only ever narrows an older build; afterwards, removing the operator value does not turn auth off (05-config §1.3.5, 05-authn §4.11) | S3 (Basic gate, rate limit), S4 (auth) | "Turning authentication, the Basic gate or the rate limit on through the environment or the operator file now also saves it as on, so it stays on after a rollback; removing `ENABLE_AUTH=true` afterwards does not turn authentication off: use `ABO_CORE_AUTH_MODE=disabled` or the owner UI. If the Basic password is set only through `ABO_CORE_HTTP_BASIC_GATE_PASSWORD`, also set `BASIC_AUTH_PASSWORD` before rolling back below S3, or the older release refuses every request." (SEC8-1) |
| 20 | **Restoring an archive made by ABO's backup (any release before S8) on S4 r2–S7 needs every API key re-confirmed** (DATA7-16, OPS8-14). Until S8, archives are made by ABO's `internal/backup` in HEAD's format (`legacy-abo/0`, 05-backup B7), although from S4 r2 the revocation journal and the content marker exist, so restoring such an archive is a foreign restore: it needs `core:admin` and owner proof and has nil coverage, so every API key is `needs_confirmation` after it; archives taken before S2b, HEAD's included, are equally foreign | S4 | "Until the release that brings the new backup format, re-confirm every API key after restoring a backup." |
| 21 | **A setting given only under a canonical `ABO_CORE_*` name is not read by an older release** (R8-4, SEC8-6): a canonical name exists only from the stage that registers its key, and every older build reads only the legacy name, so after a rollback a canonical-only value is gone — `allowed_peer_cidrs` (env-only at HEAD, `config.go:1709-1719`; empty admits every peer), a rate-limit switch, a Basic password. Every boot from S3 writes `<state_dir>/operator-names.json`, which lists names only, never values: each set canonical name whose key has a legacy name that is unset; `CheckReader` reports it as the advisory token `canonical_only_operator_values`, which `make rollback`, the `deploy` recipe and the updater print before swapping (05-config §1.3.5). The two narrowed switches (row 19) are covered by their stored legacy value | S3 | "A setting given only under an `ABO_CORE_*` name is not read by a release older than the one that introduced that name. Keep the legacy name set to the same value while you may roll back below it." |

ABO's backup archives stay unencrypted for the whole extraction (S8), so every build can restore every
archive it can open; that is not a cost.

**Unit of compatibility = the store, not the release (OPS-2).** Installs skip releases (the updater
fetches `releases/latest`, Docker users pull `:latest`, a backup from any build can be restored), and
`make rollback` returns to `.prev`, which may be any main build. Because no stage changes a stored
format, a skipped release costs nothing, except where a release deletes code an older store still
needs: books' legacy migrations, deleted by the **floor release** and bounded by the **stepping-stone
release** it names (§9.2.2, 02 N14), which is checked on the store.

**One flip PR per stage release (OPS-18, OPS6-8).** A stage's native code lands dark across many PRs;
exactly one **flip PR** per stage release makes it truth and is recorded in the ledger — mechanically, the
PR whose diff changes the stage-visibility gate's `shipped_stage:` marker (OPS5-8) to the value its stage table
names (04 §2.1 owns the ordered values: `pre-S0`, committed by S0, then `S2r1` … `S10`; UI-0, T8 and S4 r1 are
never marker values; OPS7-8). **S3–S5 land dark behind their flip**, so an RC or production build cut from
main between their PRs never runs half a stage; **S2b and S6–S8 land live PR by PR** (OPS7-6): their flip is
the first state-writing PR (S2b) or the deletion and mount PR (S6–S8), and the PRs that write each stage's other
new state (the compat ledger and the Pebble rename at S2b; `c/outbox/` rows at S6; the `Local` migration,
`c/integrations/instances/` and the AI rename at S7; the backup catalogue and `updater.Boot` at S8) merge
separately and can reach production through the 10-RC auto-promotion. So `extraction-gates`' red-rehearsal
rule applies to **every PR whose diff** changes `shipped_stage:`, changes the generated claim list
(`TestBooksClaimsGenerated`'s input), registers a `Migration` or `DataMigration`, adds a `c/` or `m/` key family
to 05-storage's table, changes `docs/compat-surfaces.md`, or changes a file listed in `ci/state_writers.txt` — all five
mechanical from the diff and tested in `scripts/tests/test_run_gates.py`. `ci/state_writers.txt` is committed at S0 and lists the
code that writes state-directory files rather than key families (OPS8-9): ABO's `internal/backup` staging and the updater hotfix's
slot code from S0, then `core/internal/restoreswap`, `storage/restorejournal` and books' gin reset handlers (S2b),
`internal/storagehttp` (S4 r2) and the updater's pending-marker code (S8); the PR that creates each package extends it. **Enforcing
releases (OPS8-13).** A `CheckedWrites` flag that lands count-only is enforced by a later, named release of its stage: **S3 r2** (blob
and secret rows), **S4 r3** (`apikey:`), **S5 rE** (the run guard rows) and **S7 r2** (`import_path:`, `aijob*`). None is a marker
value; each is a state-writing PR under the red-rehearsal rule, ships only after ≥ 14 days of production with zero persisted
blind-write violations, has a §9.4.1 row (rollback: binary swap or the claims dial), and its stage is **done** when it has served
14 days with zero refusals; it never lands inside the next stage's dark window. Every stage table names its flip in a **"Flip PR"** row with the marker value
it writes (S2 has one per release r1–r3; S0, S1 and UI-0 have none and leave the marker unchanged), and
`check_stage_visibility.py` fails a PR that changes `shipped_stage:` to an unknown value or to a stage whose
table names no flip. From the PR that first makes a core service call books' legacy
implementation of its store interface (§9.2.3), an ABO AST ratchet,
`TestLegacyFamiliesWrittenOnlyByLegacyStores` (precedent `TestOpsV2RowWritesGoThroughStageOpRow`;
05-tasks' `TestOpsFamiliesWrittenOnlyByTaskstore` is its ops instance), keeps the books legacy store
packages — with an explicit allow-list per family: `internal/books/store`, `internal/books/taskstore`,
`internal/books/configstore` and `internal/metadata/dailyquota`, built by `internal/books` (04 §2.4;
COR5-13) — the only code that writes those families, and ABO callers reach writes only through the core
service (ABO's `internal/usermerge`, the admin debug edit and usermerge's undo journal are rewired onto
the core service in that stage), so every write has one code path. Write exclusivity is a package
structure rule checked in ABO CI, not a storage grant (R4-1; SIMP4-2 cost (b)).

**Release gating is a list of named required checks (R3-2; OPS4-7, MOD4-8, OPS4-6).** Every PR to
`main` passes, on an up-to-date branch or through a merge queue (rebase merges otherwise put an untested
tree on `main`), these required checks:

| Check | What it runs | Trigger |
|---|---|---|
| `ci.yml` jobs mirroring root `make ci` | the per-module targets of the S0 table, including the always-run embed-context check `scripts/ci/check_embed_context.py` (MOD5-7), which accepts an embed input excluded from the Docker context only when it is listed in `scripts/ci/embed_build_outputs.txt` as produced in-image by an earlier stage (MOD6-3, S0 "Gates"); root `make ci` stays the local mirror (`/parallel-sweep` and developers run it) and needs no Docker | every PR; a path-skipped job skips through a job-level `if:` on the `changes` classifier, so a skip reports success instead of leaving the check "expected"; the classifier sets `go=true` **before** its docs glob for every Markdown file a Go test or `//go:embed` reads (OPS5-4, S0 "Gates") |
| `extraction-gates` | the ratchets (`TestLegacyFamiliesWrittenOnlyByLegacyStores` and its instances, `TestLegacyRoleReachUnchanged`), the compat ledger checks (`TestCompatLedgerMatchesCode`, `TestNoContractBeforeSunset`, the ledger-copy freshness check), the stage-visibility gate (04 §2.1, `scripts/ci/check_stage_visibility.py`, R3-3), `check_module_skew.py`, and the **prev-build one-format tests** (05-authn `TestOneFormat_LegacyRowsReadByPrevBuild`, 05-storage's, 05-config §1.12.2's and 05-tasks §5T.9's), with `prev` built from `PREV_REF` and cached by tag; it **fails a stage's flip PR, and every state-writing PR of the paragraph above** (OPS7-6), **unless both rows of the most recent `compat-rehearsal` run on `main` (`gh run list --workflow compat-rehearsal --branch main --limit 1`) — the latest production tag and the committed `ci/prev_ref.txt`, the reference host's `.prev` (OPS7-9, §9.10) — concluded `success` and are less than 36 h old** (OPS5-8; `scripts/tests/test_run_gates.py` tests the rule, a red second row included) | every PR that touches a non-doc file, where `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md`, `core/compat/*.md` and `docs/specs/platform-core/*.md` count as non-doc (MOD5-8, OPS5-4) |
| `docker-image` | the S0 PR image job (build, run with a seeded volume, probe), with layer caching | every PR that touches a non-doc file — the same `changes` classification as `extraction-gates` (OPS5-3): every root `.go` file is an input of the image's tagged, statically linked build, and the image-only assertions (auth-off boot, keyring location, build-ID header, levels inside the container) guard code under `cmd/` and `internal/` |
| `core-ci` | `make -C core ci` (`fix-check`, core-mode `exclude-check`, coverage ratchet, `replace-check`, `toolchain-check`, `check_core_version.py`, and core's own `errcheck-ratchet`, `interface-width`, `mocks-check`, `pprof-check` and the `core/web` `web-test`, with their scripts under `core/scripts/ci/`, so the gate an export of `core/` runs is whole; MOD5-15) | every PR that changes `core/**`, `go.mod`, `go.sum` or `.github/workflows/core-ci.yml` (one list, 12 SC4(b) cites it; MOD5-15) |

A release then contains only commits that passed. How the list is enforced — a ruleset with no bypass
actors and merge tooling that never merges with `--admin` past it, or an alternative — is owner question
14 Q-I (K20); under Q-I's option (b) releases **can** contain unchecked commits until the backstop
reverts them. The nightly rollback rehearsal (`make compat-rehearsal`) opens or updates one issue when
red; it gates through `extraction-gates`' flip-PR rule above, not through promotion.

### 9.2.2 The store guard, the floor release and the stepping-stone release (SIMP-18, OPS-3, OPS-26, R2-4, OPS4-5)

There is **one** guard mechanism — per-owner reader levels, which replace both the storage-format
guard and v1.0.0's separate compat-level guard — owned by [05-storage](05-storage.md) §5S.3.7: level
stamp `c/storage/level/<owner>`, sidecar `<db>.storage-levels`, served ledger
`c/storage/served/<owner>/last` with its `duties` (`usage`, `journal`, `auth`, derived-index generations;
the per-level run records are deferred-build members of the cutover, SIMP4-4), fast exit
`--print-storage-levels`, `CheckReader` (one decision
for open, `make rollback` and the updater), and books' **legacy projection** onto
`preference:storage_format` / `<db>.storage-format`, which makes every pre-guard build refuse a store
once any level was raised. S0 ships it in `internal/database`; S2b moves it into core `storage`.

**Policy for S0–S10: no extraction step raises a level (R3-1).** Every family keeps one stored format
(§9.2.3), so every build from S0 to S10 serves every owner at level 0 and the legacy projection stays at
HEAD's format 1: a pre-guard build still opens every store of the sequence, and `make rollback` passes
at every stage. There are no adopt-step floors, no contracts that raise a level and no level
renumbering. A level rises only for a real format change: books' own migrations (the
storage-efficiency program's release B, §9.0) and, after the extraction, a per-family cutover, whose
floor is the cutover release itself (§9.13, 02 N15). The single metric is
`mediacore_storage_level{owner,kind}` (05-storage §5S.8; OPS2-24).

**What the store records** (written by its own boots, never by a person; mechanism 05-storage):

| Record | Written by | Read by |
|---|---|---|
| install marker `c/storage/install` | the first guard-aware boot (S0 on), **before any other `c/` key** (DATA2-6; S0 writes it and the served ledger with `storage/keys`, the core tuple encoding, 04 §8.1) | 05-storage §5S.3.4 rule 4 (a later core-storage boot finds `c/` non-empty only with the marker); `TestS2bOpensStoreWrittenByS0` |
| served ledger: `last` (+ duties) and the owed-duty entries `c/storage/owed/<duty>` (store-wide, one per duty, `{since, detected}`; COR6-2) | every guard-aware boot (S0 on); `last` is replaced only in the served-ledger commit — 06 Run step (3b) from S2b; at S0–S2, after books' store has finished its startup steps — so a boot that fails earlier leaves the previous record, and each owed duty stays recorded until its consumer commits (R5-5; COR5-3, DATA5-6) | the W recording-build rule (§9.2.1), the journal gap line, the S4+ session and one-time-token revocation after a rollback (§9.2.1 row 2), the derived loader's `gen` check |
| foreign-open record `c/storage/sidecar_identity` (the full stat identity `{device, inode, size, mtime_ns, ctime_ns}` of the sidecar, plus the staged `pending` identity) | every guard-aware boot from **S0** on, and every sidecar rewrite in the same batch as its stamp (DATA3-20); the new identity is staged as `pending` and committed with the served-ledger commit (SEC5-5) | the served ledger's run continuity: a pre-guard open ends a recording run, even after two pre-guard opens that reuse the recorded inode |
| Pebble open stamp `c/storage/pebble_options` `{num, pending}` (the highest `OPTIONS-<n>` file of the DB directory) | every guard-aware boot from **S0** on lists the directory before `pebble.Open`; the new highest number is staged as `pending` and promoted in the served-ledger commit (SEC6-4) | foreign-open detection independent of the sidecar: every read-write open by any binary writes a higher `OPTIONS-<n>`, so a failed or skipped sidecar write by an older build still reads as a foreign open (05-storage §5S.3.7) |
| boot lineage `c/storage/boot` `{n, nonce, at}`, mirrored in `<state_dir>/storage.boot.json` | every core-storage boot (S2b on), in the served-ledger commit at 06 Run step (3b), and on a restore boot also in the swap's `marker_written` batch (DATA8-13) | detection of a store replaced in place by an older build's restore or a reset (05-storage §5S.3.7, 05-backup; SEC5-5, SEC5-6, DATA5-13) |
| level stamp + sidecar `<db>.storage-levels` | a level step only (none in S0–S10); every guard-aware boot rewrites the sidecar from the stamps (OPS-26b) | open (refuses a store above the build's level), `make rollback`, the updater's `ReaderCheck` |
| applied-steps sidecar `<db>.storage-applied` | every guard-aware boot from S0: applied versions per owner (books from its version-source adapter) and finished data migrations (05-storage §5S.3.7; OPS3-7) | the updater's stepping-stone check (05-updater §5U.4 step 3); P13 |

**Fresh installs (DATA2-28, DATA3-21).** On the boot that creates `c/storage/install` on an empty store,
the marker, every owner's level stamp at the build's supported level and the applied record of every registered
migration step below that owner's lowest `Seed` step (the `Seed` steps run in P4b; 05-storage §5S.3.5; COR5-8, COR6-4, DATA8-13) are written in **one** batch (as HEAD stamps a fresh store at the build's
format, `internal/database/storage_format.go:338-352`), so a crash leaves either an empty store or a
fully stamped one. A fresh ABO store keeps its families in the legacy format too (§9.2.3). **An empty store is
exempt from the upgrade-floor check** (R7-2, DATA7-7): it is a fresh install, and its fresh-install batch records,
through books' version-source adapter, every books legacy step 1..`LegacyCap` and every registered books step
below its lowest `Seed` step as applied, so a floor build boots a store it created, and boots it again
(`TestFreshInstallPostFloorBootsTwice`), and a factory reset or a staging rebuild is never refused with
`ErrUpgradeFloor`. The three marker one-shots of S2b are `Seed` steps, so a fresh install runs them and writes
HEAD's markers (DATA7-13).

**Floor release and stepping-stone release (R2-4, 02 N14, OPS3-7, OPS4-5; replaces v1.1.x's "no
stepping-stone release").** Skips are bounded, and the bound is a property of the **store**. Two terms,
always naming two different releases:

- the **floor release** is the first release that contains 05-tasks T8: it deletes the code only books'
  legacy steps call and declares books' `UpgradeFloor{Version: LegacyCap}`;
- the **stepping-stone release** (`InstallFirst`, and the `release` of the signed manifest's
  `min_upgrade_from`) is the newest production tag that exists when the T8 PR merges; it carries every
  books legacy step and the code they call and contains no T8 deletion. Root CI's
  `TestStepSteppingStoneValid` asserts that the tag exists, predates the T8 commit, registers books
  `LegacyCap` and prints an empty `--print-upgrade-floor` (05-tasks).

Rules:

- **`LegacyCap`** is the highest HEAD-style books migration step at the S2b flip (66 at the evidence
  commit; DATA4-19). HEAD-style steps (`db_version` 67, 68, … through `migrations.go`'s list, which HEAD
  tolerates, `internal/database/migrations.go:511-532`) are allowed until S2b and forbidden after; every
  later books step is a versioned `Migration` or `DataMigration`, numbered from `LegacyCap+1`, contiguous with the
legacy track (DATA7-7). The pin in `TestNoNewLegacyMigrations`
  and the floor use `LegacyCap`, never a literal.
- Books' legacy migrations (`db_version` 1..`LegacyCap`) run in a migration phase **before** any core
  service calls books' legacy stores, on books' own claims (no grant exists to need: R4-1): steps 62, 65
  and 66 write the ops families that books' `tasks.Stores` serves from S5
  (`internal/database/migrations.go:479-506,1238-1250,1340-1352,1412-1424`). From S5 their bodies live in
  `internal/books/taskstore` and the step list calls them through funcs the composition root wires, with
  direct calls instead of capability assertions, so no step can log "nothing to do" and be recorded as
  applied without running (R5-2, MOD5-1; 04 §2.4, §8.6 gate E; `TestLegacyStepPreconditionsResolve`).
- Their code, and every store method they call (`SweepHollowOperationsV2`, `MigrateOpsV2ToV3`,
  `StripLegacyPlanRows`), is kept in **every release before the floor release**; T8 deletes it only in
  the floor release. S10 sets no bound of its own: it deletes no code an older store needs.
- The check is on the store, never on the binary being replaced (OPS3-7). The **S0 updater hotfix** runs
  `<exe>.new --print-upgrade-floor` (shipped at S0, "Rollback tooling" below) before the swap and compares it
  with the store's `<db>.storage-applied`, refusing with `stepping_stone_required` and naming the release
  the new binary prints — an **empty** floor passes without reading the sidecar, and a non-empty floor with a
  missing sidecar refuses with "boot any release ≥ S0 once" (OPS7-10), so the HEAD → S0 deploy is never
  refused for a sidecar only S0 writes, while a host with **no DB directory** at the resolved path (or an empty one)
  and no sidecars passes, the boot's own empty-store exemption, so a new host or one prepared for a restore can
  install the floor binary (DATA8-11; `TestReaderCheckMatchesRollbackScript` and `scripts/test_storage_format_guard.py`
  have the case); it uses the floor the binary carries, so it needs no signed manifest, which arrives
  only before S8. The **S8 updater** also checks the signed manifest's `min_upgrade_from` (05-updater §5U.4
  step 3). The `deploy` recipe template and `scripts/deploy-preflight.sh` run the same pair of commands
  against the uploaded binary before `cp … .prev`, through one helper shared with
  `scripts/storage_format_guard.py`, so a manual deploy of the floor release onto a store below it is
  refused before anything is swapped (OPS6-1). Boot refuses
  a store that falls short with `ErrUpgradeFloor{InstallFirst}` before any write (an empty store is exempt, above). "Passing through" the
  stepping stone means the store **ran** it, not that its binary was installed once. A pre-S0 updater
  swaps any newer binary unchecked (`internal/updater/updater.go:214-280`), so a pre-S0 auto-update
  install meets the boot refusal: the floor release's release note and its last log line name the
  stepping stone (DATA4-18).
- The boot refusal exits with a distinct code (78; 75 instead while an S8+ updater's pending marker names
  the running binary, so `Boot` can roll back, 05-updater §5U.4; OPS6-5) and prints `install <release> first`
  as its last stderr line; under Docker (no entrypoint script, `Dockerfile:103`) that line in `docker logs`
  is the message (OPS3-21). systemd must not restart it (OPS5-15): the S2 unit change adds
  `RestartPreventExitStatus=78` to the committed unit. launchd has no per-exit-code setting, so it
  relaunches a refusal (throttled) whatever `KeepAlive` says; the floor release's note tells macOS users to
  `launchctl bootout` the job, and the binary sleeps 60 s before exiting 78 when it detects launchd
  (`XPC_SERVICE_NAME` set), to bound log spam. The same note tells Docker users (whose
  `restart: unless-stopped` restarts it forever) to read the line in `docker logs` and stop the
  container, and `deploy-wait-ready.sh` reads `ExecMainStatus` and reports exit status 78 as "install the
  stepping-stone release first", not as a crash loop (OPS6-5). For a store with no applied-steps sidecar the advice is "boot any release ≥ S0
  once", which writes it (DATA4-18).
- A restore stages an archive only if it meets the build's upgrade floors: staging refuses an older
  archive with `ErrUpgradeFloor{InstallFirst}` before it extracts anything (05-backup §5B.3.4;
  DATA3-9), so a fresh host never ends up with an empty fresh-install store. The host-loss procedure for
  such an archive is "install the stepping-stone release, restore, run it until the store has applied
  its steps, then upgrade".
- Releases newer than the stepping stone may be skipped.

**Rollback tooling.** `--print-storage-levels` prints the build's supported vector; `--print-storage-format`
stays for pre-guard scripts and prints the projection; `--print-upgrade-floor` ships at **S0**, printing
an empty requirement until the floor release (OPS4-16). `scripts/storage_format_guard.py` (name kept)
implements `CheckReader`'s table for `make rollback`, prints both values when a stamp and its sidecar
disagree (OPS-26c), treats "unknown flag" with empty stdout from `--print-upgrade-floor` as "none" only
when `--print-storage-levels` also fails (a pre-guard binary), and refuses while the one swap slot holds a staged
restore **or a staged reset** (R7-2, R8-1, SIMP8-1): a reset is `KindReset` of the restore swap, so there is **one token
pair**, `restore_pending` (`restore/pending.json`, whatever its `kind`) and `restore_inflight` (`restore/*/inflight.json`,
for a reset kept until its journal reads `hook_done`, or, for a checkpoint restore, `restore/*/inflight-checkpoint.json`,
under any ID), whose message names the kind; `CheckReader` reports them from S2b and the S0 tooling probes the same paths.
A rollback to a pre-S2b build with a swap in flight would open a half-moved store, and a deploy between staging and
restart would drop the admin's audited restore or reset. That check runs over ssh as the deploy user (OPS3-13): a `DEPLOY_STATE_DIR`
variable names the state directory (next to `DEPLOY_BIN` and `DEPLOY_DB`, `Makefile:893-895`), and the
probe is 05-storage §5S.3.7's **one** `find` under `sudo -n` over `restore/`, which exits 0 whenever the state directory
is readable, prints a path when the slot is taken or a swap is in flight, and is `cannot_determine` on a non-zero exit or
stderr starting with `sudo:` (OPS4-3) — so a store that never staged one (no `restore/` directory) passes, and a missing
sudoers line refuses, naming it. **One override**, `ROLLBACK_IGNORE_RESTORE=1`, read by `make rollback` and the
`deploy` recipe alike (`DEPLOY_DISCARD_RESTORE` and `ROLLBACK_IGNORE_RESET` are deleted, R8-1), and it overrides
`restore_pending` only, which is safe because the intent is rejected later when the open stamp moved; `restore_inflight`
has **no** override: "boot the current build once to finish the swap" (OPS8-7). The updater has no override. **The
S0–S7 updater runs the same decision in process** (OPS8-8): as the service user it stats `restore/` directly under the
state directory, never through `sudo`, which the unit's `NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:122`)
forbids; from S2b ABO's `internal/updater` calls core `storage`'s `CheckReader` through the kernel binding instead.
`CheckReader` also reports two **advisory** tokens that never refuse but that `make rollback`, the `deploy` recipe and the
updater print before swapping: `canonical_only_operator_values` from `<state_dir>/operator-names.json` (S3 on, §9.2.1 row 21;
R8-4) and `config_narrowing_owed` (05-config §1.5). `scripts/test_storage_format_guard.py` covers each case, including "no
`restore/` directory → pass", "no sudoers line → refuse naming it", "in flight under any ID → refuse", "reset pending →
refuse naming the kind", "reset journal in flight before `hook_done` → refuse", "override with a pending intent → pass",
"override with a swap in flight → still refuse" (OPS8-7), "no DB directory, no sidecars → pass" (DATA8-11), "empty floor,
no sidecar → pass", "non-empty floor, no sidecar → refuse" (OPS7-10) and the pre-guard `--print-upgrade-floor` case. **Owner actions, all in S0:** the literal sudoers lines for
`<db>.storage-levels`, `<db>.storage-applied` and the restore-probe `find` (one `find` over `restore/`, matching
`restore/pending.json`, `restore/*/inflight.json` and `restore/*/inflight-checkpoint.json`, delivered at S0 so it is in
place before S2b; OPS7-11, R8-1), next to the existing ones
(`docs/system/deploy-and-gpu-ops.md:208-218`) — without the first, every rollback refuses (OPS-26a) —
and the updated `deploy` recipe copied from `Makefile.local.example` into the uncommitted
`Makefile.local` (timeout variable, ssh arguments, restore probe; OPS4-10). Nothing is left to be
found at the moment a rollback is needed (OPS5-16): `scripts/deploy-preflight.sh` runs
`ssh $(DEPLOY_HOST) sudo -n -l` and refuses an S0+ deploy unless the `.storage-levels`,
`.storage-applied` and restore-probe lines are listed, and `scripts/storage_format_guard.py` treats an
unreadable `.storage-levels` together with `.storage-format` = 1 as "all owners at 0" (logged) while
still refusing on the restore probe; `scripts/test_storage_format_guard.py` covers both cases.

**Disk headroom and checkpoint reuse (OPS-25, DATA2-18, DATA3-1, DATA3-22, OPS3-14).** A boot takes the
pre-migration checkpoint only when a migration step will actually run on it (a books legacy or
versioned step, a core versioned step, a `BootBlocking` data migration, the install marker on a
non-empty store); a step whose preconditions defer it is not pending. Before taking it, the boot checks
free space ≥ live store size and fails closed with a message. A checkpoint is reused only by a crash
loop of the **same** upgrade attempt on the **same** store identity (install marker and store
generation): never a completed checkpoint, never one left beside the store by a restore or a swap-back;
`storage restore-checkpoint` refuses a checkpoint older than the current upgrade attempt; and the
checkpoint record is written after the checkpoint and before `.migration-checkpoint`, so a directory
without a record is garbage. Consumption, pruning, the metric `mediacore_storage_checkpoint_bytes` and
the alert snippet that ships with S2b are 05-storage §5S.3.12's.

### 9.2.3 Data: one stored format per family; books supplies the legacy stores (R3-1, R4-1)

Every core service that persists state ABO already keeps calls its SDK **store interface**; core ships a
native implementation over `c/<subsystem>/` (the one `examples/hello`, `coretest` and new apps use), and
the `books` module supplies a **legacy implementation** of the same interface — ABO code over ABO's
existing keys and row formats — through `module.Manifest.LegacyStores` (06 §6.11.6; 03 §3.3 "Store
interfaces"). So during S0–S10 **core holds no ABO key name or row format and no storage grant on any
books family** (grants are deferred-build members of the cutover, §9.13). Native-only ABO data (hashed
sessions, native one-time tokens, namespaced scopes, `mck_` keys, keys with no legacy name, native-only
clock, job and instance fields) is stored by the legacy implementation in a **books-owned extension
family that no older build reads** (`m/books/ext/<store>/`, 05-storage §5S.3.11), never additively in a legacy row:
an extra string in a role row would defeat HEAD's worker confinement, a "nothing else" test
(`internal/server/middleware/auth.go:271-297`; SEC4-3). Each extension record carries a hash of the
legacy row it extends; if an older build changed that row while rolled back, the extension is suspended
until an admin re-confirms it, so a roll-forward never widens anything silently. The 04 file layout:
04 §4.5 and its `internal/database` row (the "stays" parts); `internal/books/taskstore` for the ops
families, which imports the residue and is never imported by it; `internal/books/configstore` for the
config-blob-backed stores (config, integration instances, the books root), because they run
`internal/config`'s blob code and `internal/config` imports the residue (R5-2; 04 §2.4, §8.6 gate E).

The interface list is 03 §3.3 "Store interfaces" (the one list, SIMP5-8); this table adds only ABO's
families, extension contents, the stage at which core starts calling books' store, and rollback, keyed
by 03's row names.

| Store interface (owner) | Families of ABO's legacy implementation (books) | What books' extension family holds | Core starts calling it | Rollback to the previous release |
|---|---|---|---|---|
| `config.OverrideStore` with its `legacy.SecretsStore` (05-config §1.12.2) | `setting:config_blob`, legacy JSON shape, and the four secret rows HEAD already stores encrypted (OpenAI key, Google Books key, Hardcover token, Basic password), read and written in place in HEAD's encryption and **never moved** (R5-1(b)); ABO's settings load, settings-encryption init and key-regeneration guard run inside it, through the kernel's one DB handle at the boot phase 06 assigns — never a second Pebble open, never defaults written over the stored blob (R4-6) — and it keeps HEAD's precedence for legacy keys until the cutover: a value persisted through the UI wins over an environment default exactly as at HEAD (`Alias.BelowStored`, R5-1(a)); no step deletes the blob (COR3-1, DATA3-3). Claim: `Serialized` from S2b, `CheckedWrites` from S3, the stage whose façade replaces HEAD's blind `SetSetting` writers with `Store.Apply` (R5-3; COR5-1, DATA5-11) — count-only in that release, enforced from a later release after ≥ 14 days with zero persisted blind-write violations (DATA6-12) | keys with no legacy name; a **plaintext** secret's encrypted copy lives in core's `c/secrets/`, and its legacy copies (blob field and `config.yaml`) hold the sentinel (R4-2) | S2 read (`app.Options.LegacyConfigStore` over the store ABO's `serve` opened); S2b read over the kernel's handle (`LegacyStores.Config`); S3 written | binary swap; §9.2.1 rows 1 and 12 |
| `auth.Store` (05-authn §3.4, §11) | `u:`, `apikey:`, `inviteallow:`, `oauthid:` and the auth `idx:` sub-prefixes read and written in place, every write staging the primary row and every legacy index row the format keeps, as HEAD's store methods do (DATA-5); `sess:`, `idx:sess:user:` read and deleted, never written (SEC-19), a legacy cookie created before the store's cut-off upgraded on use; `invite:`, `idx:invite:username:` read in place by presented token, deleted on accept, expiry or restore hook, never written (DATA2-8), accepting a legacy invite re-checks `CanAssign` | hashed sessions, native one-time tokens and invites, `mck_` keys, restriction fields (`restriction`, `password_reset_required`) — every restriction is also in a field an older build reads (`status`, `revoked_at`, `credential_epoch`, a blanked hash); a key's last-use write stages `Expect` on the row it read and is dropped on a conflict, every status, expiry and scope write stages `Expect` too, and `apikey:` is `CheckedWrites` from S4 r2 (count-only first, as above), so a request can never undo a revocation (R6-5, SEC6-3) | S4 r1 read-only (decode census at P4d); S4 r2 read-write | binary swap; §9.2.1 rows 2–4 |
| `rbac.Store` (05-rbac §6.3) | `role:`, `idx:role:name:`; a role the new build writes holds only the legacy atoms the row rule admits (§9.2.1 row 5) | namespaced scopes per role | S4 (as `auth.Store`) | binary swap; §9.2.1 row 5 |
| `tasks.Stores` (05-tasks 5T.6.2) | `opv2:`, `opv3:`, `op:` run records and pages, the clock and pause keys in `setting:`, the `Requires` stamps (`op:completion:`, `op:deprev:`) — HEAD's code in `internal/books/taskstore`, including its opv2↔opv3 dual write, the clock-key codec of `interval_clock.go` (`ClockStore`), the pause marker of `pause_gate.go` (`PauseStore`), the scan stand-down marker `registry.scan_standdown` (`StandDownStore`; MOD5-4, MOD6-4) and HEAD's maintenance-window marker `setting:maintenance_window_last_run`, the `ClockStore` clock of the maintenance-window kind with HEAD's `YYYY-MM-DD` codec (R7-8, MOD7-3), over HEAD's run-state vocabulary `internal/operations/state`, which stays in ABO (R6-2); HEAD's every-boot `ReconcileOpsV3WithV2` as `RunStore.Recover`, no new reconcile (COR5-7); no copy-and-mark, no `migrated_to`, no mirror; the live v1 books writers (`opstate:`, `op_result:`, `opsummary:`, `operation:`) keep books' own claim | native-only clock fields, stamps of subjects with no legacy type; definition snapshots and HTTP idempotency records are core's, in `c/tasks/def/` and `c/tasks/idem/` (05-tasks 5T.6; COR5-11) | S5 (T0+T1) | binary swap; a run of a kind the older build does not register is dropped by its resume sweep (`resume.go:29`), §9.2.1 row 6 |
| `log.JournalStore` (05-logging §5) | system log rows (`syslog:`) read in place and retention-deleted | new journal rows | S5 (with `logjournal`) | binary swap; §9.2.1 row 7 |
| `legacy.LibraryRootSource` (05-library; legacy-only as a whole, so it lives in core's `legacy` package, R6-3) | `import_path:`, `counter:import_path`, and the books root from the config blob (that half in `internal/books/configstore`); books' memdb `import_paths` table is kept current by the same code (DATA4-4); `import_path:` is `CheckedWrites` from S7, in the PR that converts the scanner, autoscan and filesystem root writers to the field-merge method — S7's flip PR — count-only first (R5-3; COR5-1, DATA6-12) | native-only root fields | S7 | binary swap |
| `ai.JobStore` (05-ai) | `aijob:`, `aijob_batch:`, `aijob_payload:`. `aijournal:` is not served by any core store: it stays a books family written only by books' apply handlers (`internal/ai/resultjournal`, plain books code; COR5-10, DATA5-15, SIMP5-3) | native-only job fields | S7 | binary swap |
| `providerhttp.GateStore` (05-metadata M2) | `provider_throttle:`, `provider_daily_budget:` (`internal/metadata/dailyquota` stays ABO) | — | S7 | binary swap |
| `index.DirtySet` (05-storage §5S.3.8) | `idx:sidx:dirty:`, HEAD's format; books passes it per index in `index.Spec.Dirty` when it opens its search index, not through a manifest field (SIMP5-2) | — | S7 | binary swap; both builds drain one set |
| `integrations.InstanceStore` — the instances ABO has today (Deluge, AI endpoints, metadata sources, SMTP, webhooks; 05-metadata M1) | their blob fields and secret rows, read and written in place by books' legacy store in `internal/books/configstore` (no import, no mirror); `integrations` is the one **registry** of instances (SIMP4-12) | native-only fields (`Local`, limits); instances with no legacy form live in core's `c/integrations/instances/`; operator-layer values are an in-memory overlay, never persisted (SIMP2-5) | S7 | binary swap; secrets as config's; §9.2.1 rows 9 and 10 |
| `notify.RuleStore` (05-notify) | the one legacy-backed global rule over ABO's webhook settings in the blob (`plugins.webhook.*`, its configured event list mapped to `books.*` topics; 05-notify §10) | — | S7 | binary swap |
| books families (the rest of the 258, incl. the rest of `setting:`, `preference:`, `pref:`, `act:`) | claimed in place by books, permanent | — | — | — |
| `c/storage/`, `c/compat/`, `c/outbox/`, `c/audit/`, `c/secrets/` | new core rows only, no legacy twin | — | — | ignored by older builds (`TestLegacyToleratesNamespacedKeys`) |

**Ownership does not change (R3-1, DATA3-2).** Books holds every legacy family at every stage of S0–S10,
in the mode 05-storage §5S.9.1 states: the same families and modes at every stage, except that a
family's `CheckedWrites` turns on at the stage its row names — the stage whose PR converts its last blind
writer (R5-3; COR5-1), which must be the stage's flip PR — and a family whose store interface uses
commit-time checks (`Transition`, `Advance`, `Insert`, `Extension.Put`) is `Serialized` (R5-4). **Run-store
write checking is per guard row** (R7-3; DATA7-1, DATA7-2, DATA7-3, DATA7-8): `CheckedWrites` applies only to
the guard rows `opv3:run:`, `opv2:op:` and `opv3:meta:counts`; every other run family is `Serialized` only, and
its writes — progress ticks, chunk-ledger commits, checkpoints, row marks — are legal only in a batch that
carries the run guard's `Expect` (taskstore's archtest enforces it); `DeleteRange` is refused in a
`CheckedWrites` claim and allowed in a `Serialized`-only one. A ledger commit or row mark is accepted while
the run is non-terminal under the same `Attempt`/`FenceEpoch` (HEAD's closed-ledger rule, so a `stopping`
run still takes them) and dropped otherwise; only a bare progress tick is dropped on any state change, never
retried blindly, so a tick can never revert a cancel; `Transition` and `Insert` re-read the run on a conflict
and recommit under `storage.RetryOnConflict` while `want` (state, attempt, fence) still matches. There is no claim hand-over and no grant. Each `CheckedWrites` flag gets
**one count-only release** (DATA6-12): `ErrBlindWrite` is recorded in `c/storage/claimviol/` with
`op=blind` and allowed, and the flag is enforced from a later release once that record shows no blind
write for ≥ 14 days. In shadow mode an `ErrBlindWrite` is a recorded violation. A store interface lands with its subsystem; until then
books' own code serves those rows exactly as at HEAD. The claim ledger records which backend each
subsystem uses (`backends`); a build whose choice differs fails boot with `ErrLegacyStoreMissing`
(05-storage), and because books supplies its stores in every release of a stage, a rollback within a
stage (e.g. S4 r2 → r1) never meets a missing store (DATA4-2).

Rules that apply to every row:

1. **Never widen on rollback (SEC-1).** A key the new build creates has explicit, non-empty,
   namespaced scopes in books' extension family and a legacy `apikey:` row holding only the legacy atoms
   those scopes admit — or, when none is admitted, a scope that grants nothing on an older build, never an
   empty list (R5-6; 05-authn §4.4; DATA5-5, SEC5-1, SEC5-3); a role row it writes holds
   only the legacy atoms whose legacy reach its native permissions dominate on the **frozen reach
   snapshot** of the last pre-S4 release (05-rbac §6.3 row rule; SEC3-1, SEC4-4) — reach on releases
   older than the snapshot is K21's territory; a role whose native scopes are all `Confining` is written
   with its legacy atoms only, so HEAD's worker confinement still holds (SEC4-3); a key's scopes come from
   its extension record **iff the record holds canonical scopes** (`scopes_source: canonical`, written only
   at native creation); otherwise — no record, or a record holding only Independent restriction fields such
   as the `needs_confirmation` stamp a nil-coverage restore writes (`scopes_source: legacy`) — they come from
   the legacy row: `LegacyFullScope` ("all role permissions") for an empty list, and `rbac.LegacyAtom` role ∩
   key reach on legacy surfaces (the stored atoms, or any atom when the list is empty), the one predicate of
   05-authn §4.4, 05-rbac §4.5 and 05-storage row 3 (SEC6-2); an extension's fingerprint covers only
   security-relevant fields, never `last_used_*` or `use_count`, and a revocation made by an older build
   stays a revocation (R5-6);
   no value an older build reads may relax a check when blank, which is why moved secrets leave a
   sentinel, never an empty value (R4-2).
2. **Seed roles stay read-only (DATA3-16, SEC3-17).** Every build before S4 rewrites the four seed roles
   at boot (`seed.go:80-129`), so a native edit would be discarded by a rollback: seed-role edits are
   refused (`ErrSeedRoleLocked`, 05-rbac §4.8) until the role cutover; a customised role is a custom
   role.
3. **Fail closed per principal (OPS-19).** A legacy row a legacy store cannot decode, or a role row
   holding an atom outside 05-rbac §6.1, is treated as restricted (user inactive, key refused, the
   unknown atom granting nothing), health reports Degraded naming the key or role, and the count is
   recorded (05-authn, 05-rbac); boot continues, so one bad row never crash-loops the unit into
   `StartLimitBurst` (SEC4-13).
4. **One write path per family**, enforced by `TestLegacyFamiliesWrittenOnlyByLegacyStores` (§9.2.1);
   cross-owner atomic writes (admin user merge) bind books' rows and core rows in one batch by
   participant binding (05-storage D-S10, DATA-18).
5. Legacy-format codecs are `encoding/json` v1 permanently; v2 only for core-only rows (DATA-5).

### 9.2.4 HTTP surfaces

The mechanism is 05-http-api §4.6 (aliases, `Gone`, headers) and §8 (`MountLegacy`). Policy:

- An **alias** serves the canonical handler under the canonical permission with the legacy wire
  shape (`LegacyPath.Shape`, pinned by goldens from `internal/wirecontract`; FE3-6), adds
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
  `AutomaticEnv` made settable, the `ABK_*` names, and every flat YAML key (counts generated from
  05-config's alias table). The field names inside `setting:config_blob` are not a window surface: they
  are the blob's one stored format (§9.2.3) and stay until its cutover.
- **Permanent aliases (OPS-5)**: ecosystem names (`OTEL_EXPORTER_OTLP_*`, `OPENAI_API_KEY`,
  `OPENAI_BASE_URL`) and **every name ABO reads today that a shipped deploy file sets**:
  `DATABASE_PATH` (unit `:98`, plist `:40` — the only DB-path source in both), `FP_PARALLEL_WORKERS`
  (unit `:108`), `BASIC_AUTH_ENABLED`/`USERNAME`/`PASSWORD` (compose `:20-22`), and the names in
  `deploy/local.conf.example` that ABO reads. No shipped file sets the canonical state-directory
  variable: HEAD's fallback next to the database is the one source (S2 "Deploy", OPS4-2). Runtime and ecosystem names that config does not read
  (`HOME`, `GOMEMLIMIT`, `GOGC`, `TZ`, `NOTIFY_SOCKET`) are 05-config's third class.
  `TestShippedDeployEnvNamesArePermanent` scans `deploy/*.service`, `deploy/*.plist`,
  `deploy/local.conf.example`, `docker-compose.yml` and `Dockerfile*`, commented `Environment=` lines
  included.
- **HEAD's precedence is kept (R5-1(a), OPS5-1, OPS6-7).** Every legacy env name and legacy file key
  (not flags, which keep C3 and are recorded in §9.2.6) that HEAD lets the stored blob override (every
  `AutomaticEnv`/`BindEnv` name outside `envLockedSettings`, `applyEnvAuthoritativeConfig`,
  `absEnvOnlySettings` and `SyncConfigFromEnv`, §9.1) is an alias with `Alias.BelowStored` (05-config
  §1.3.5): wherever core config attaches books' legacy config store (S2 on), it feeds the default layer,
  below the stored value, and locks nothing, so compose's always-set `BASIC_AUTH_ENABLED=false` cannot switch
  off a Basic gate enabled in the UI, and an env value set to `""` is unset. The one exception is the
  auth-enable switch, whose operator value **enables** auth over a stored `false` (R6-6, §9.2.6; SEC7-13). Canonical `ABO_*` names keep the
  operator lock (C3). The flag is generated from HEAD's lists, so the table cannot drift.
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
(`TestLegacyRoleReachUnchanged`, built on the real-router dump technique of `raw/09` §0), except for the
recorded changes below. Because the mount is permanent, the test is a **permanent CI test**, not a
migration test (SIMP-2 trade). Role IDs (`admin`, `editor`, `viewer`, `fp-worker`) do not change, so
`User.Roles` and `Invite.RoleID` (`internal/database/store.go:1004-1012`) need no rewrite.

**Stored permissions (R3-1, R4-1).** Legacy atoms in `role:` and `apikey:` rows are the stored format;
a role row the new build writes holds only the legacy atoms the row rule admits (§9.2.3 rule 1), and its
namespaced scopes live in books' extension family, never as extra strings in the row (SEC4-3).

**Who declares the legacy aliases (COR5-5).** ABO's 13 legacy atom aliases, with their frozen `Reach`,
are app data: ABO's `main` passes them as `app.Options.LegacyPermissionAliases`, filled from books'
snapshot package, and the kernel declares them as app-level aliases, which may target `core:*` (the
app's `main` is the trust root) and are validated at `Freeze` (each target in the catalog, `Reach`
dominating `New`). Books' `Manifest.LegacyAliases` stays own-namespace only, and core holds no ABO atom
(06 §6.4.5, 05-rbac §4.9).

**Legacy routes require the full legacy reach (R4-3, SEC4-2).** From S4 the bridge enforces each legacy
gin route (and each in-handler atom check) against the native scope set equivalent to the **entire**
legacy reach of the route's atom (`AllOf`, 05-rbac §6.3 `Catalog.LegacyReach`), not against the atom's
expansion, so a custom role holding part of `settings.manage`'s reach cannot pass a `settings.manage`
route (restore, backup, logs, filesystem browse, import paths, tool installs, plugin settings); seed-role
reach is unchanged, because only `admin` holds `settings.manage` and it holds `core:admin`.
`TestLegacyRoleReachUnchanged` gains a **custom-role half**: for random native roles and every legacy
route, stage 13 admits only if the route's full legacy reach is held.

**Scoped legacy keys keep HEAD's reach (SEC5-7).** At HEAD a key's effective set is the intersection of
its owner's role atoms and its stored atoms (`middleware/auth.go:364-365,405-421`), so an admin's `abk_`
key scoped `["settings.manage"]` (backup or maintenance automation) passes every `settings.manage` route.
For a principal authenticated by a key whose scopes come from its legacy row (no extension record with
`scopes_source: canonical`, §9.2.3 rule 1; SEC6-2) — every legacy `abk_` key, also after a restore stamped
it or an admin re-enabled it — the bridge's `s.perm(atom)`, every in-handler atom check and an adapted
legacy definition's `Perms.Start` pass iff the atom is among the key's stored atoms (or the list is empty)
**and** the owner's role checker satisfies `Catalog.LegacyReach(atom)`
— HEAD's intersection, with the row rule's dominance on the role side (05-rbac §6.3, 05-http-api §8.3,
05-tasks). Native routes keep reading the expansion. The custom-role half gains scoped-key cells. Legacy atoms are accepted on API
**input** for W (surface `rbac_atom`) and refused with 400 after C4; their place in stored rows is not a
surface and does not end with W.

**Recorded changes instead of identical reach:**

- **Auth-off principal on legacy routes and adapted op starts (R4-3, COR5-4; 05-rbac §4.11, §6.3).** The
  auth-disabled principal is not a bounded role, so it is judged against the atom's **expansion**, as
  before R4-3, which matches HEAD's `s.perm` pass-through with auth off (`server_lifecycle.go:1693-1698`);
  otherwise the `{core:admin}` reach of `settings.manage` would close its 83 legacy routes with auth off.
  The mechanism is one requirement value carrying the atom, `rbac.LegacyAtom(atom)` — an S2 leaf value
  that the S2 legacy manifest already records for every `s.perm` route, resolved through the catalog from
  S4 (COR6-1): stage 13 resolves it through `Catalog.LegacyReach` for role and key principals
  and through the expansion for the auth-disabled principal, and the same value is an adapted legacy
  definition's `Perms.Start` from S5 (05-tasks 5T.3.6), so an op that kept the old `settings.manage`
  default still starts with auth off: **op starts get no auth-off tightening**. The expansion applies
  only to in-handler check sites that pass at HEAD with auth off: HEAD's `auth.Can` returns false when no
  permission set is bound (`internal/auth/context.go:171-178`), so the reviewed list of in-handler checks
  carries an `auth_off: pass|deny` column copied from each site at HEAD, and a `deny` site keeps denying
  the auth-disabled principal (e.g. other users' trash progress, `handlers/audiobooks/handler.go:986`;
  SEC5-9). This is not a change in reach; it is recorded because the rule differs from role principals.
  The auth-off half of `TestLegacyRoleReachUnchanged` asserts 200 on a `settings.manage` route and a
  started `settings.manage` op with auth off, and response-content cells for the `deny` sites.
- **Auth-off tightenings (SEC5-8; release notes).** HEAD's credential guard is a **pass-through** with auth
  off (`internal/server/credential_routes.go:80-82`, "exactly like s.perm"), so at HEAD `POST
  /backup/restore`, the key routes and temp-login are open with auth off. Two tightenings are deliberate
  and recorded: (a) **from S4** every `Guards.Interactive` route refuses the auth-disabled principal, which
  is non-interactive (credential routes, key routes, temp-login, restore); (b) **at S8** the backup routes —
  create, delete, verify and import (`core:system.manage`) and restore (`core:backup.restore`), both
  `AuthRequired` — answer 401 with auth off (05-rbac §4.10). Release-note text: "With authentication off,
  credential and backup routes now refuse requests; turn authentication on to use them." The auth-off half
  of `TestLegacyRoleReachUnchanged` excludes these and asserts 401/403 for them.
- **Auth off on a remote listener (SEC4-8; 05-authn §4.11).** Native `disabled` mode refuses to start
  on a non-loopback listener unless `core.auth.disabled_allow_remote=true`; **ABO's default is `true`**,
  because every Docker install with `enable_auth=false` listens on a non-loopback address and would
  crash-loop at S4 r2. While it runs remote-reachable, health reports Degraded `auth_disabled_remote`
  with a Settings banner (as HEAD's warning did, `server_lifecycle.go:1847`), carried by HEAD's degraded token
  list (`internal/server/handlers/system/handler.go:222-275`) until S8's health routes land (05-health; SEC7-12); release note;
  `TestS4_AuthOffDockerBoots` (image job).
- **Turning auth on with auth off (SEC4-8).** HEAD let protected fields change with auth off
  (`internal/config/update_service.go:781`); natively `ProtectAdmin` needs `core:admin` and an
  interactive method, which the auth-disabled principal never has, so from S4 auth is turned on through
  the operator layers: set `ABO_CORE_AUTH_MODE=required` (canonical, locks the key), `core.auth.mode:
  required` in the operator file, or the legacy `ENABLE_AUTH=true` / `enable_auth: true`, and restart.
  **An operator value that enables auth always takes effect, even over a stored `false`** (R6-6, SEC6-1):
  this is the one exception to `Alias.BelowStored` and a recorded change — at HEAD a blob that ever saved
  `enable_auth=false` kept auth off whatever the environment said (`internal/config/persistence.go:1932-1977`,
  `config.go:1558`). A value that **disables** auth keeps HEAD's precedence, so a stale `ENABLE_AUTH=false`
  cannot turn off auth that the UI turned on. **The tightening survives a rollback (R7-1):** books' legacy config
  store writes the narrowing legacy value (`enable_auth: true` from S4 r1; `basic_auth_enabled: true` and
  `enable_rate_limit: true` from S3, whenever the effective value is `true`; SEC8-1, SEC8-6) at P4d step 1 as an
  audited core write (`core.config.legacy_narrowed`), §9.2.1 row 19. S3 reads `enable_auth` with HEAD's precedence;
  S4 r1 follows the effective `core.auth.mode` (05-authn §4.11), so both r1 and the narrowing write keep auth on,
  and after a rollback to S3 only the write does (SEC8-8). The other `ProtectAdmin`/`ProtectOwner` keys an auth-off
  operator may need (the Basic gate's `core.http.basic_gate.{enabled,username,password}`, among them) are set
  through their canonical names (`ABO_CORE_HTTP_BASIC_GATE_PASSWORD` …), which lock; their legacy
  `BASIC_AUTH_*` names stay below a stored value. A canonical name is unread by every release older than the one that
  introduced it, so **keep the legacy name set to the same value** while a rollback below that release is possible
  (§9.2.1 row 21, which `operator-names.json` reports before a rollback; R8-4, SEC8-6); for the Basic gate that means
  setting `BASIC_AUTH_PASSWORD` too, or a hotfixed release below S3 refuses every request (row 19). When a `BelowStored` legacy name of a key at `ProtectAdmin`
  or higher is set and differs from the stored effective value, boot logs at warn and health raises
  `config_legacy_name_shadowed`, naming the key and its canonical variable (SEC6-1; carried by HEAD's degraded
  token list until S8, SEC7-12). Release note: "To turn
  authentication on, set `ABO_CORE_AUTH_MODE=required` or `ENABLE_AUTH=true` and restart; to change other
  protected settings with authentication off, use their `ABO_CORE_*` names, and keep the old name set to the same value
  while you may roll back below this release." The auth-off half of
  `TestLegacyRoleReachUnchanged` asserts 403 for protected keys through the `/api/v1/config` alias.
- **Both resets only stage; a factory reset's side effects run after the restart (R7-2, R8-1; 14 Q-K; SEC7-5,
  DATA7-6, COR8-1, SEC8-5).** At HEAD `FactoryReset` wipes the store, deletes the Open Library data, deletes every
  entry of the library `RootDir` and resets and saves every setting, all in the request
  (`internal/server/handlers/system/handler.go:507-589`), and `ResetSystem` wipes the store and resets the settings
  (`:463-491`). From S2b both requests only stage a reset, `ResetFactory` and `ResetStore`, kinds of the one restore
  swap (§9.2.1 row 12). A dangerous `RootDir` is refused at staging with 400 and nothing staged, where HEAD refused
  only after it had already wiped the store (OPS8-5). The reset boot is a fresh install that **stages HEAD's factory
  defaults** in its fresh-install batch — the `factoryDefaultConfig` blob with `root_dir` empty and `setup_complete`
  false, as HEAD's handler leaves them — so `enable_auth: true` and `enable_rate_limit: true` are stored and a stale
  `ENABLE_AUTH=false` or `ENABLE_RATE_LIMIT=false`, a `BelowStored` legacy value, loses to them as after HEAD's reset;
  this is a fresh-store write after the restart, so the live blob is still never written with defaults (SEC8-5;
  `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`). For `ResetFactory` the gap-fill config file reset, the Open
  Library data and the library `RootDir` wipe run in books' post-reset hook after the swap journal reads `applied`;
  they are **not undoable**, and an entry the hook cannot delete is logged and reported as
  `factory_reset_cleanup_incomplete` instead of failing boot, as HEAD warned and continued (COR8-4, OPS8-5). From S4 r2
  the legacy paths are `Alias`es of the core route: the `POST /system/reset` alias passes `reset_kind=store`, both
  require HEAD's `{"confirm":"RESET"}` and **answer 422 where HEAD answered 400** for a missing or wrong confirmation,
  and success keeps HEAD's 200 with `{reset_id, message}` in HEAD's `{data: {message}}` envelope (05-rbac, 05-http-api;
  COR8-2). Whether to keep the `RootDir` wipe is owner question **Q-K** in 14; the recommendation is HEAD parity with
  this release-note line: "A factory reset now takes effect at the next restart, and still deletes the contents of
  your library folder; undoing it does not bring those files back, and files it could not delete are listed in the log
  and in health. A missing or wrong confirmation now answers 422 instead of 400."
- **Operator secrets bind to origin plus provenance at S7 (R8-5; SEC8-2, SEC8-3; 05-metadata M1.4).** An operator-sourced
  secret (`OPENAI_API_KEY`, an env Deluge or SMTP password) is served when every destination field's effective origin
  equals the origin of that field's operator value (or the adapter default when there is none), or equals the instance's
  `OperatorSecretOrigin`, which the S7 migration sets from the stored destination (at HEAD only `settings.manage` holders
  could write it) and every `Put` that passes ProtectAdmin or the confirm route sets again; otherwise the secret is
  **withheld** and health reports `degraded`. So HEAD's stored copy of an operator's own `OPENAI_BASE_URL`, or an
  admin-set Deluge or SMTP host beside an env password, keeps working at S7, while a destination changed by a principal
  without `core:admin` withholds the secret. Until the integrations cutover, a `Put` that changes a destination origin of a
  **legacy-backed** instance while any of its secrets is operator-sourced requires ProtectAdmin and otherwise answers 403
  `ErrOperatorSecretBound` naming the canonical variable, because an older build would re-apply the env secret and send it
  to the stored URL after a rollback (SEC8-3). Release note: "From this release a key or password set in the environment is
  sent only to the address an administrator configured; a changed address needs an administrator."
- **Flags beat a stored value from S3 (OPS6-7).** `--log-level` and `--playlists` keep C3 (a typed flag
  wins) from the S3 façade; at HEAD the stored value won (`cmd/root.go:648-651`). Release note: "From this
  release `--log-level` and `--playlists` override the value saved in Settings."
- **Auth-off (SEC2-21; 05-rbac §6.2).** With auth off, HEAD's `s.perm` passes every `settings.manage`
  system route (`wire_admin_role.go:18-22`); their successors under `core:system.manage`
  (`AuthRequired`) answer **401** with auth off — status detail, storage and system info
  (`/api/core/v1/health`, `/system/info`) and the log journal query and live tail (05-logging) — while
  `GET /api/core/v1/system/status` (readiness tokens only) stays open, as today, and the read-only admin
  introspection routes keep their auth-off reach. The auth-off half of `TestLegacyRoleReachUnchanged`
  excludes exactly the aliases of the routes 05-rbac §6.2 marks tightened, and asserts 401 for them.
- **`/metrics` for scoped keys (SEC3-13; 05-metrics §6, §13 item 5; OPS5-5, SEC5-13).** HEAD lets any
  authenticated credential scrape (`server_lifecycle.go:1735-1747`). From **S4 r2** `/metrics` needs
  `core:metrics.read`: the S4 r2 PR changes the legacy manifest entry of gin's `/metrics` route from
  `AnyAuthenticated` to `core:metrics.read`, so stage 13 enforces it until S8, when the kernel's
  `Router.Infra(…, "core:metrics.read", …)` replaces the gin route (`TestLegacyMetricsRoute_RequiresScopeFromS4`).
  `admin` holds it through `core:admin`, and `editor` and `viewer` are seeded with it (in books'
  extension family, which older builds never read; R4-1); it is never seeded to `fp-worker` or a
  role holding a `Confining` scope, so a fingerprint worker has no scrape access. An API key with
  explicit scopes reads `/metrics` only if its scopes include `core:metrics.read`; existing keys are
  never widened to include it, so a scoped legacy scrape key must be replaced by a key carrying that
  scope (release note).

### 9.2.7 Metrics (SIMP-13)

ABO sets `app.Options.MetricPrefix = "audiobook_organizer"` (03 §3.6), so every books series keeps its
exact name, is **emitted once**, and needs no alias, recording rule or alert duplication. Only the
series whose emitter moves into a core subsystem (ops → `mediacore_tasks_*`, Pebble →
`mediacore_storage_pebble_*`, AI → `mediacore_ai_*`, two process gauges → the Go collector) are
renamed; their old names keep working through **gather-time aliases** (05-metrics §13, no double
instrumentation), each a ledger row with gate P6 instead of (c). The two process-gauge aliases target the
Go collector's `go_goroutines` and `go_memstats_alloc_bytes`, which `metrics.AddAliases` accepts as the
core-owned runtime families of `metrics.RuntimeFamilies()` (OPS6-2). Each rename happens in the stage that
moves its emitter (OPS5-6): **Pebble at S2b**, **ops at S5 (T0+T1)**, **AI at S7**, the **two process
gauges at S8**; the alias row, and the `deploy/` dashboards and rules switched to the new names, ship in
the same PR (05-metrics §13 item 4). **The normative series check is the seeded contract test of
05-metrics §6** (OPS6-3, OPS7-1), which owns its mechanism: from S2 r3 `internal/telemetrypolicy` builds the
`metrics.Provider` the kernel builds (through `coretest.MetricsProvider`, with the metric-alias rows ABO's `main`
passes) and scrapes the served `Provider.Handler()`, never `promhttp.Handler()` (`TestContractScrapesServedHandler`),
the S2 r3 PR adds `mediacore_` to its in-scope prefixes, core-owned emitters are seeded through public SDK
paths, and at S8 its registry becomes the private one in the same PR. Its golden's seeding table gains each
renamed family's alias rows in the renaming PR, so its diff is the done-check of every stage — empty at S1; at S2 r3 the newly in-scope `mediacore_*`
families (05-metrics §6); at S2b the kernel's new storage families plus the renamed families and their aliases; only the
renamed families and their aliases at S5 and S7; at S8 as 05-metrics §6 states. A live scrape lists only
families that recorded a sample since boot (integration-bound families never do on staging), so it is a
**smoke check** only: one credentialed scrape — `curl -sS -H "Authorization: Bearer <redacted-token>"
<abo-url>/metrics | grep -v '^#' | sed -E 's/[{ ].*//' | sort -u` — answers 200, is non-empty and contains
05-metrics §6's short always-on list (OPS5-9). W for each alias counts from its own release (§9.6 row 24).
New core-only series use `mediacore_*`. OTel scope
names follow 05-metrics.

### 9.2.8 Switches: none on state shape, a few operational dials (OPS-17)

**State-shape switches: none.** A family's stored format is fixed for the whole extraction (§9.2.3), and
no runtime flag selects a format: a flag would double the states rollback must handle and could put a
store into a shape the previous build cannot read.

**Operational dials are allowed** when they never change persisted shape; each is a declared config
key: `core.tasks.kinds` (one map-typed key keyed by task kind, entry `parallelism_cap` ≤ the declared
value; env as a JSON value; 05-tasks owns it, SIMP2-15, COR3-11 — a per-kind dotted path would not be a
valid config path for kinds such as `books.dedup.full-scan`), `core.outbox.relay_enabled`, per-provider
throttle overrides (05-metadata M2), `core.storage.claims.mode` (`shadow`/`strict`, 05-storage),
`core.updater.mode` and `core.updater.allow_unsigned_legacy` (05-updater). Existing switches are
untouched: `abs_api_enabled`, `enable_auth`. Per-entry data such as a webhook instance's
`legacy_headers` is data, not a flag.

### 9.2.9 The ledger and `compat.Ledger`

`docs/compat-surfaces.md` (the existing shim ledger, v1.1.0) gains a section per stage; each row:
surface, legacy name, canonical name, flip PR, shipped release and date, earliest removal date, gate
(`usage` | `p6` | `permanent`), state (`aliased` | `gone` | `retired` | `removed`), removal PR. The ledger
row is the **only** place that holds a surface's class, gate and dates (SIMP2-12): subsystem alias types
(05-config `Alias`, 05-http-api `LegacyPath`, 05-metrics aliases, 05-tasks `FormerKinds`) carry only
`Ledger string`. **The app supplies the ledger (SIMP3-3, R4-4):** a core `//go:embed` cannot reach
outside `core/`, and the Docker build context excludes `docs` (`.dockerignore:11`, §9.1), so an embed of
`docs/compat-surfaces.md` would fail every image build (MOD4-1). `docs/compat-surfaces.md` stays the
source of truth; the books composition root `internal/books` embeds a **generated copy,
`internal/books/compat-surfaces.md`** (below the root, so `*.md` does not exclude it), which a CI
freshness check (`TestCompatLedgerCopyFresh`, in `extraction-gates`; `make compat-ledger` regenerates)
keeps byte-equal to the source, and `main` passes it as `app.Options.CompatLedger` (from S2b, 06
§6.4.10). `.dockerignore` keeps its exclusions. Core's own fragment `core/compat/core-ledger.md`,
embedded by `compat`, holds only names every app shares — the permanent `OTEL_*` env aliases
(05-config C7); every window alias, metric aliases included, is the app's (SIMP4-10), and a test asserts
that every core-fragment row has gate `permanent`. `Ledger` merges the two, and a row ID in both is a
boot error. An app with no legacy surfaces passes nil. The
code side is one value per App:

```go
// core/compat (03 §3.3); registered by httpapi, config, rbac, tasks, realtime, metrics, ui and books.
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
	Ledger    string // row ID in the merged ledger; gate, state and dates come from that row
}

// Ledger is built by the kernel from app.Options.CompatLedger and core's embedded fragment before
// listeners open; registration after that fails.
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

	// The §9.2.1 (c) gate as the server computes it from this record, the served ledger and
	// c/compat/recording_since/<owner> (OPS3-18, DATA6-7),
	// so P8 and the runtime twin of TestNoContractBeforeSunset read one answer.
	GateMet                  bool
	GateReason               string // "used_within_14d", "set_on_recent_boot", "no_recording_boot", "recording_gap"
	LastRecordingBootAt      time.Time
	RecordingContinuousSince time.Time
}
```

`TestCompatLedgerMatchesCode` (root module) compares the merged ledger with every `Entry` registered at
boot; `TestNoContractBeforeSunset` fails a build whose code removes an entry before its earliest
removal date; its runtime twin (P8) reads `GateMet`. The record has no client-origin dimension:
done-checks that must show "no first-party use" are end-to-end assertions over a request log, not
usage queries (OPS3-19). Surface `ui_route` is core-generic (FE4-5): the kernel registers one
`compat.Entry{Surface: "ui_route"}` per `UIDescriptor.LegacyRedirects` entry at P7, and `ui.Handler`
calls `Handle.Use` when a full page load matches one, so no app `PageMiddleware` needs a handle before
`app.New` has built the ledger.

## 9.3 Stage overview

| Stage | Name | Native truth introduced (new core-only data) | Rollback cost (§9.2.1) | Surface contract (≥ W, §9.5) |
|---|---|---|---|---|
| S0 | Groundwork, guards, gates, build paths | — (install marker, served ledger, foreign-open record, applied-steps sidecar) | — | — |
| S1 | Core module skeleton, leaf packages; staged restore | — | — | — |
| S2 | Composition root; perimeter (Basic gate stays in gin); `books` wraps the server (r1–r3) | — | — | — |
| S2b | Core opens the store; books on claims (shadow → strict); core config reads through books' legacy config store | `c/storage/`, `c/compat/` | W clock restarts after time on an older build; renamed Pebble series answer under old names only; a reset staged on S2b takes effect at the next restart and its file deletions are not undone (§9.2.1 rows 12, 14, 16) | — |
| UI-0 | UI groundwork: react-router 8, generated-client runtime | — | — | — |
| S3 | Config and secrets (façade; a plaintext secret's first write leaves sentinels; Basic gate to core) | `c/secrets/`; keys with no legacy name in books' extension family | re-enter plaintext secrets written on S3+; the older build refuses them until then; operator-enabled Basic gate and rate limit stay on; canonical-only settings are unread (§9.2.1 rows 1, 12, 15, 17, 19, 21) | C3 (config/env aliases) |
| S4 | Identity (r1 dark; r2 flip): auth, RBAC, audit, first native core routes | `c/audit/`; hashed sessions, one-time tokens, `mck_` keys and role scopes in books' extension family | §9.2.1 rows 2–5, 13, 18, 19, 20 | C4 (identity routes, atoms on input) |
| S5 | Tasks | native-only run data in books' extension family; task kinds `books.*` | §9.2.1 rows 6–7, 16 | C5 (task routes and kinds on input) |
| S6 | Events, outbox, realtime | `c/outbox/`; native SSE | — | C6 (legacy SSE streams) |
| S7 | Integrations, library, search index, notify (mail + webhooks) | `c/integrations/instances/` (instances with no legacy form); native-only instance fields in books' extension family | §9.2.1 rows 8–10, 16 | C7 (generic integration routes) |
| S8 | Backup, updater, health, metrics registry | backup catalogue and archive manifest (05-backup; archives stay plaintext, encryption is built later, SIMP4-5); core metric names | §9.2.1 row 16 | C8 (backup/update/system/health routes; metric aliases by P6) |
| S9 | UI shell | `/books/...` SPA routes; shell build; fragment invite links | §9.2.1 row 11 | C9 (UI redirects) |
| S10 | Final cleanup (was S11) | — | — | — |

Order: S0 → S1 → S2 → S2b → S3 → S4 → S5 → S6 → S7 → S8 are strictly ordered (R4 layers and data
dependencies; S7's notify needs S6's outbox and S8's updater needs S7's notify, 04 §8.1; SIMP3-6). UI-0
runs **after S2** (the client is generated from the `core/openapi/core.json` that the S2 hello skeleton
emits; MOD3-4) and must ship before S4; it may overlap S2b and S3. S9 needs UI-0 and S3–S8 done. A stage
ships as one or more production releases; each stage release's flip PR, named in the stage table's "Flip PR" row, is in exactly one (§9.2.1).

**Every stage's "Done when" also includes this section's boot budget and R-test, and every PR of it
passes `make ci` (R3-2)** (OPS2-16); the stage tables repeat only stage-specific checks.

**Boot-time budget (OPS-22, OPS3-10, OPS4-12).** Measured per stage as an A/B on **one**
production-size restored copy by `scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir> --runs 5`:
it interleaves the binaries (A/B/A/B), discards the first boot of each, records restart → `"ready":true`
on `/health` and HEAD's `warmup_ms` (`internal/server/handlers/system/handler.go:268-272`), and appends
the result to the stage's ledger section. **Steady state:** p50(cur) ≤ max(1.10 × p50(prev),
p50(prev) + 15 s) — warm-ups of 130–200 s (§9.1) make 10 % alone smaller than the noise. **First boot
that runs a migration step** (a stage's books or core versioned steps, a `BootBlocking` data migration):
report each `mediacore_kernel_boot_phase_seconds{phase,step}` (labels as 06 §6.9 defines them, ASCII;
emitted from S2; OPS3-16) against an absolute ceiling the owner records in the ledger per stage (e.g.
≤ 2 × steady state + the checkpoint). S1's measurement is the first data point, not a reference held for
a year of library growth. S0 turns the hard-coded 300 s into a `DEPLOY_READY_TIMEOUT` variable in
`Makefile` (`rollback`) and `Makefile.local.example` (`deploy`), default 300, which starts counting when
the unit leaves `activating` (OPS2-17, S0 "Deploy").

**Staging (OPS4-13; R7-6: this is the one staging procedure, and 05-backup cites it).** "Staging" in a
done-check means a disposable host built by the committed `scripts/staging/provision.sh`: a systemd-in-container
image (or a scratch VM) with the committed unit, the S0 sudoers lines, a deploy user distinct from the service
user and a store restored from a production archive. S0 "Deploy" delivers the script (OPS5-11). **A staging copy
is inert at every boot and every stage** (R6-7, OPS6-4, SEC7-6): key regeneration alone drops only the four
encrypted rows, and only those the gap-fill file cannot recover (`internal/config/persistence.go:1229-1262`); every
other secret sits in the blob in plaintext, `sess:` rows hold raw bearer tokens valid on production, and a booted
copy runs its schedulers against the production Deluge, SMTP, webhook and AI endpoints. The script, in order:

1. **Restores the archive offline, never by staging** (R8-3; OPS8-2, SIMP8-2): `provision.sh` runs the ABO subcommand
   `audiobook-organizer staging-restore --archive <file> --db <empty path>`, which ships **at S0** in `cmd` (it stays
   ABO). It reads and verifies the archive offline (HEAD's `extractArchive` logic, later also the `legacy-abo/0` and
   05-backup readers, with 05-backup's manifest and entry checks), refuses a non-empty target, never writes
   `config.yaml` and boots nothing; a staged restore would leave nothing at the DB path before the first boot, and from
   S2b `app.OpenOffline` would refuse the copy while its intent is pending. It never takes a copy of the database
   directory, and **refuses to continue while `<db parent>/config.yaml` exists** beside the copy: an archive never
   contains that gap-fill file (05-backup), while a directory copy brings it back, and HEAD's load refills empty
   OpenAI, Google Books and Hardcover keys from it and re-encrypts them into the store
   (`persistence.go:42-95,1229-1262`), undoing step 3 (OPS7-4).
2. **Installs the egress rules before the first boot** (OPS7-2): an `nftables` OUTPUT policy `drop` for the
   container or VM, except loopback and `established,related`, with **inbound ssh and the API port open**, so
   `make rollback`, the `deploy` recipe and `deploy-wait-ready.sh` reach the host over ssh as on production (a
   container network `none` would publish no port and block ssh); with Docker the equivalent is an
   `internal: true` network reached through a jump container that runs sshd. The rules stay for the host's life,
   so **every boot of a staging copy** — `cur`, `prev`, the boot-budget A/B, the R-test and the `make rollback`
   and updater done-checks — runs egress-limited, never only the first (SEC7-6).
3. **Scrubs the copy offline, before its first boot**, with the ABO subcommand
   `audiobook-organizer staging-scrub --db <copy> --fields scripts/staging/secret_fields.txt` (OPS7-4): a cobra
   subcommand in `cmd` that stays ABO, uses books' store code offline and writes only the listed keys. **`staging-restore`
   and `staging-scrub` run with the binary of the release that wrote the archive** (the production release on the
   reference host; every release from S0 carries both), which has no pending step on its own archive, so from S2b
   `app.OpenOffline` admits it (R8-3; SEC8-11); for a pre-S0 archive they run with `cur`, whose open is then a plain
   open. Before S2b the scrub's open is a foreign open to the guard, whose cost the boot budget absorbs by discarding
   the first boot. It:
   - (a) blanks every plaintext secret field of the blob named in `scripts/staging/secret_fields.txt`, committed
     **at S0** and written by hand from 05-config §2.2's table; from S3, `TestSecretClassListMatchesLegacyStore`
     also asserts that the file equals the generated secret-class list, so it cannot drift (OPS7-4, SEC7-10);
   - (b) deletes `sess:`, `idx:sess:user:`, `invite:`, every one-time token row and the encrypted secret rows;
     replaces every `u:` password hash with a never-matching value and sets every `apikey:` row `revoked`, so
     staging access goes through the bootstrap-token flow and no production credential verifier is left to guess
     against offline (OPS7-7). E-mail addresses (`u:`, `oauthid:`, `inviteallow:`) are kept: they are not
     credentials, and the identity census and the R-test's identity cells need real row shapes; the host holds
     production data, so it gets the production host's access controls (SEC7-10);
   - (c) **clears every legacy integration URL at every stage** (OPS7-7, SEC7-6; R7-6 as amended by R8-3): the
     legacy Deluge, SMTP, webhook (their URLs too, which are not secret-class and can carry a token in the path),
     AI-endpoint and metadata-source URLs and the metadata sources' `base_url` (`internal/config/config.go:155-157`).
     Native instances under `c/integrations/instances/` are not touched — core's keyspace is no ABO subcommand's to
     write (R8) — and need no clearing: only S7+ binaries read them, and every staging boot of such a binary carries
     `core.integrations.disabled=true` in the operator layer, which locks the key and makes `providerhttp` refuse every
     `Acquire` before dialling, while the firewall of step 2 drops egress for the host's life (SIMP8-2). The switch is
     keyed on the binaries that boot the copy, not on the copy's age, and an S6 `prev` during S7's rollout cannot read
     native rows at all;
   - (d) leaves **schedules running**, so S5's smoke check (S5 "Done when") sees the enabled definitions fire: a
     schedule that fires against a disabled or URL-less integration fails closed (05-ai, 05-download and 05-indexer rows
     for `core.integrations.disabled`), and its run is still enqueued and its clock advanced (OPS7-3, DATA5-8); it sets
     `auto_update.enabled=false`.
4. Boots the copy with `ABK_ALLOW_ENCRYPTION_KEY_REGEN=1` (`cmd/root.go:760-791`), because the production
   `.encryption_key` is **never** copied to staging (nor any keyring export), and then seeds test secrets through
   the API.
5. **`provision.sh --updater-check`** (S8; OPS8-3, SIMP8-10; lead decision) exercises the **shipped** updater between
   two real signed prereleases: staging runs the S8 release candidate *n*, installed with
   `core.updater.channel=prerelease`, and it applies RC *n+1*, which the real release pipeline signed (`prerelease.yml`
   publishes the signed `release-manifest.json` too, 05-updater). The release source is the `github.New` argument set in
   ABO's `main`, which no setting redirects, so the script opens egress **to the release host names only** (an `nftables`
   set resolved from them) for the length of the check; the scrubbed copy has nothing else to call. There is no fake
   release server and no staging build with a test key (05-updater's own unit tests keep their fake key). It sets
   `auto_update.enabled=true` after the scrub.

`scripts/tests/test_staging_scrub.py` plants a production-shaped archive, runs `staging-restore`, then `staging-scrub`
with the archive's binary, then boots `cur`, and asserts that **the first serve boot finds every secret, session and
legacy URL already scrubbed** (R8-3). Its cases are dated by the stage that adds them (OPS8-11), and each stage's
done-check names its own: **S0** — no secret-class blob field, session, one-time token, usable password hash, API key
or legacy integration URL survives; a `config.yaml` beside the copy holding an OpenAI key makes provisioning refuse,
and no key survives the first boot; `staging-restore` refuses a non-empty target; egress (`test_staging_egress.sh`);
**S2b** — the scrub opens through `app.OpenOffline` with the archive's binary on a fixture that `cur` still has to
migrate, and is refused with `cur`; **S7** — `core.integrations.disabled` is set and the scrubbed fixture booted with the
`prev` of S7's rollout (S6) attempts no outbound connection. `scripts/tests/test_staging_egress.sh` asserts that an outbound TCP connect from the service user fails
and that ssh to the deploy user succeeds (OPS7-2). The boot-budget copy is this scrubbed copy, and
`scripts/boot_budget.py` documents the same preparation. The `make rollback` and updater done-checks run against
it as the deploy user; the reference production host is never the staging host.

**Rollback rehearsal (OPS-20, OPS-21).** The R-test (§9.10) with `prev` = `PREV_REF` (§9.10). Cells a/b
(previous-release UI against cur, cur UI against prev) are mandatory in **every release that moves a UI
call** (any diff under `web/src/services` or the generated client); cell c (ABS conformance, plus a live
client when P7 is branch B) is mandatory in the **ABS-path stages S2, S4, S6 and S9** — the one list
used here, in §9.10 and in P7.

## 9.4 Stages

### S0 — Groundwork, guards, gates and build paths

| Aspect | Specification |
|---|---|
| Goal | Make every later stage buildable, reversible, gated and measurable, with no user-visible behaviour change. |
| Flip PR | None: S0 leaves `shipped_stage:` unchanged (OPS6-8). |
| Toolchain | The **first PR of S0** is 13's toolchain bump to `go1.27.3`, moving every pinned copy together (13 §13.2.2); later commands use `make -C core <target>` or `$(make -s -C core print-toolchain)`, never a literal (MOD2-13). |
| Moves | 04 §8.1 row S0: `core/` with `go.mod` (03 §3.2: `exclude` of the monolithic genproto **only in `core/go.mod`**, `ignore ./web`, `ignore ./examples/hello/web`) and an **empty committed `core/go.sum`** (tidy keeps it; `check_module_skew.py` asserts it exists, MOD2-15), the `archtest` engine, `storage/keys` and `buildid` (the L0 leaf: `buildid.Binary`, `buildid.Commit`; FE6-3, FE7-14); root `go.mod` `require` + `replace`; `GO_MODULE_DIRS := . core`; `internal/arch/layering_test.go` redesigned per 03 §3.5.1 (ratio floor, `. → cmd` positive control, classifier control, mapped core layers feeding the computed layer, `loadGraph` keeping mediacore imports; MOD3-5). The stage-visibility generator is committed as `scripts/ci/check_stage_visibility.py` with its mapping input (04 §2.1, MOD3-2). ABO's reload banner lands: `web/src/components/StaleBuildBanner.tsx`, mounted above the routes and fed by `apiFetch`'s own build-ID rule ("Build ID and reload check" below); from UI-0 the same component subscribes to `@mediacore/api-client`'s `onStaleBuild`, which replays reasons already detected on the page load, and at S9 the shell banner replaces it (FE8-3). |
| Gates (R3-2, OPS4-7, MOD4-8, OPS5-4, MOD5-7, OPS5-8) | The four named required checks of §9.2.1 (`ci.yml` jobs mirroring root `make ci`, `extraction-gates`, `docker-image`, `core-ci`) exist, with job-level `if:` skips that report success and trigger lists that cannot skip a PR changing their inputs; every PR to main passes them on an up-to-date branch or a merge queue; how that is enforced is 14 Q-I. `ci.yml`'s `changes` classifier (`ci.yml:92` treats `docs/*` and `*.md` as docs-only) gains explicit `go=true` cases **before** the docs glob for `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md`, `core/compat/*.md` and `docs/specs/platform-core/*.md` (the input of `DeferredMembersAbsent`), and the `ci.yml:46-48` comment ("no Go test reads a file under a docs glob") is corrected; `scripts/tests/test_ci_classifier.py` lists every file that a Go test or `//go:embed` reads under a docs glob and asserts each one sets `go=true`. The embed-context check `scripts/ci/check_embed_context.py` runs in root `make ci` on every PR (§9.2.1 table): `go list -json` over the root module and core for every (`GOOS`, tag set) CI builds, takes each package's `EmbedFiles`, and fails if any path matches `.dockerignore` (moby pattern semantics) **unless** it is listed in the committed `scripts/ci/embed_build_outputs.txt` as produced in-image by a declared earlier stage (`web/dist` from S0, because under `embed_frontend` `web_embed.go:11` embeds it while `.dockerignore:20` excludes it on purpose and `Dockerfile:68` copies it `--from=frontend-builder`; each app's `web/dist` in the ABO image). Hello's `core/examples/hello/web/dist` is **not** on this list: no ABO Dockerfile produces it, so it is on core's own list, `core/scripts/ci/embed_build_outputs.txt`, read by `check_embed_outputs.py` in `make -C core ci` with no Dockerfile cross-check, because core has no image (MOD7-6; 03 §3.2 item 4, 07 §7.6.3). The script checks the root list against the `COPY --from` lines **into a stage that runs `go build`** (the `go-builder` stages) whose destination is under that stage's `WORKDIR`: a listed path with no such line, or such a line with no listed path, fails, and the runtime-stage copies of the binary (`Dockerfile:90`, `Dockerfile.build-cgo:75`) are out of scope (MOD7-5); it ignores a `go list` "no matching files" error only for a listed path (MOD6-3; 03 §3.2 item 4). A flip PR is the PR whose diff changes `shipped_stage:` (S0 commits the initial value `pre-S0`; 04 §2.1, OPS7-8), and `extraction-gates`' red-rehearsal rule — on flip PRs and on the state-writing PRs of §9.2.1 (OPS7-6) — reads both rows of the most recent `compat-rehearsal` run on `main` (OPS7-9), tested in `scripts/tests/test_run_gates.py`. `ci/state_writers.txt` (the red-rehearsal rule's fifth criterion, §9.2.1; OPS8-9) and `scripts/ci/check_rollback_rows.py` (every §9.2.1 row listed in its "From" stage's overview and Rollback rows; OPS8-6) are committed and run in `extraction-gates`. `prerelease.yml` gains a Docker build without push (OPS2-14). |
| Docker (MOD-1, OPS-1, OPS2-14, MOD3-13, MOD4-1, OPS4-2) | Both Dockerfiles: `COPY go.mod go.sum ./` **and** `COPY core/go.mod core/go.sum ./core/` before `go mod download && go mod verify`, then `COPY . .` as today. The Go stage takes no `BUILD_ID` and no Go link step receives one; the frontend stage may declare an optional `ARG BUILD_ID`, which its `npm run build` reads when set (07 §7.3.2; FE4-1, FE5-2, MOD5-12, "Build ID"). `.dockerignore` adds `**/node_modules`, `core/web/packages/*/dist`, `core/examples/*/web/node_modules` and otherwise keeps every exclusion, `docs` included (R4-4). **Every `//go:embed` input of the root module and core lies inside the Docker build context or is a listed in-image output** (`scripts/ci/embed_build_outputs.txt`, MOD6-3); `scripts/ci/check_embed_context.py` in root `make ci` is the check, on every PR (a path filter on changed files cannot see a new embed directive, which changes only a `*.go` file; MOD5-7), and the image job confirms it, which is why the compat ledger is embedded from `internal/books/compat-surfaces.md` (§9.2.9). The frontend stage that copies `core/web` manifests and builds them lands in the **UI-0** PR that creates `core/web` (MOD3-13); S0 changes only the Go stage and `.dockerignore`. The **`docker-image` PR job** (§9.2.1: every non-doc PR, OPS5-3), with layer caching, builds the **full** image (and `Dockerfile.build-cgo` with `--output type=local`), runs it with a seeded volume, waits for `"ready":true` on `/health` and for `--print-storage-levels` inside the container, and asserts that `X-App-Build` equals the embedded `<meta name="mediacore-build">` (`TestBuildID_HeaderEqualsEmbeddedMeta`). A **second run** starts the image with `--db /config/x.pebble` on a named `/config` volume (an overridden `command:`), prepared as a user must prepare it (the image creates and chowns only `/data`, `Dockerfile:93-96`, and runs as uid 1000: `docker run --rm --user 0 --entrypoint chown -v cfg:/config <abo-image> 1000:1000 /config`, which needs nothing beyond the image under test, and which the README's compose notes, delivered in S0 "Deploy", give too; OPS6-10); **from S0 on** it seeds, boots, recreates the container and asserts that `.encryption_key` sits beside the database on `/config` and that a secret set through `PUT /api/v1/config` decrypts after the recreate, and from S3 also that `secrets.keyring` is there (OPS5-7, OPS4-2, R3-4, OPS3-1); the default run asserts the same for `/data`. The ledger records which Dockerfile `reusable-release.yml` builds and whether the release publishes the raw updater asset (OPS2-4). |
| Web build entry points (MOD-9, FE-13, MOD2-16, MOD2-17) | One POSIX `sh` script, `scripts/ci/build_web.sh` (the frontend stages run on `node:26-alpine`, which has no `bash`): build `core/web` when present with an explicit workspace order (`npm run build -w <pkg> …`, never `--workspaces`, whose order is not topological; 07 §7.3.2), then `npm ci` in `web/`; behind `make web-deps`; every `npm ci` site of §9.1 calls it (§9.1 is the one site list, MOD5-13); it generates no build ID and only leaves a caller's `BUILD_ID` in place, because `npm run build` in `web/` stamps the ID itself on every path (07 §7.6.3, R5-7; FE5-1); `frontend-ci.yml` and `e2e.yml` path filters gain `core/web/**`, `core/go.mod`, `core/go.sum`; `ci.yml`'s `changes` classifier sets `web=true` for `core/web/*` and `scripts/ci/build_web.sh`; Dependabot npm `directories` gain `/core/web`; the Dependabot gomod entry gains `directories: ["/", "/core"]` under the existing `multi-ecosystem-group: dependencies` (`.github/dependabot.yml:3-16,59-64`), which gives one PR for version updates, while security PRs for `/core` are made consistent by the root-tidy step and caught by the blocking `check_module_skew.py` (MOD2-6, MOD3-16, MOD4-15); every `cache-dependency-path` lists `core/web/package-lock.json` once it exists. **Precondition of UI-0 (MOD4-10):** the three frontend jobs that run `npm ci` inside external reusable workflows (`ci.yml:127` `reusable-ci-minimal.yml`, `frontend-ci.yml:64` and `nightly.yml:31` `reusable-ci.yml`) cannot call `build_web.sh`, so their callers set `run-frontend: false` and the frontend half runs in ABO-local jobs that call `make web-deps` (as `ci.yml:372-383` already does); 07 §7.3.2 lists all three. The two release callers of `reusable-release.yml` (`release-prod.yml:45-58`, `prerelease.yml:34-51`, both `frontend-enabled: true`) are a further, **unverified** case (MOD6-5): whether that workflow's frontend half installs or builds `web/` itself is not known, so UI-0's preconditions start with reading it (UI-0 "Preconditions"). CLAUDE.md's worktree recipe becomes `make web-deps`. |
| Module coupling (MOD2-6) | A change to `core/go.mod` carries the root `go mod tidy` result in the same PR; per-module `tidy-check` (`go mod tidy -diff` in `.` and `core`) in root `make ci`; `core-ci.yml` also runs the root `go build ./...` when `core/go.mod` or `core/go.sum` changes. |
| CI classification (MOD-8, MOD4-14) | The table below; per-module baseline files; a per-stage checklist (shard weight files `short_test_weights.json`, `fixture_weights.json`, `go_test_timings.json`; `.mockery.yaml`; `tygo.yaml`; `ci/gates.txt` rows) run in every PR that moves a package. Every commit of a move PR builds (the repo merges by rebase, so an unbuildable rename-only commit would land on `main`); the move PR appends `old-path ==> new-path` lines (12 §12.8.1's separator, one format in 04, 09 and 12; MOD5-14) to `core/scripts/split/origins.txt`, the authoritative origins for 12's history path file, and carries its `go fix`, R1 and R5 rewrites (04 §2.2). Move PRs that rename tagged files' imports rewrite the build-tagged files too (`cmd`'s `dedup_bench*.go`, `internal/server/bench.go`, `static_embed.go`; MOD4-7). |
| Data/keys | Register reserved families `c/` (core) and `m/` (modules) in `keyfamilies.go`. Ship the per-owner guard (§9.2.2) in `internal/database` (S2b moves it into core `storage`); every owner is served at level 0. The first guard-aware boot writes **`c/storage/install` before any other `c/` key** (DATA2-6); each boot then writes the served-ledger entry — `last` only after books' store has finished its startup steps, with owed duties in `owed` and the new sidecar identity staged as `pending` until then (R5-5, SEC5-5; 05-storage §5S.3.7) —, the applied-steps sidecar `<db>.storage-applied` (OPS3-7), the foreign-open record holding the sidecars' identity (DATA3-20) and the Pebble open stamp (the highest `OPTIONS-<n>` before its own open, staged as `pending`; SEC6-4), the keys encoded with `storage/keys` (05-storage §5S.3.3), so the first core-storage boot (S2b) reads S0–S2 history and does not halt with `ErrForeignKeys`; HEAD-era builds ignore them (`TestLegacyToleratesNamespacedKeys`). No config data change (no secret is moved or blanked before S3; OPS3-2) and no op-ID teach (the previous build never sees a `books.*` kind before S5). |
| Hotfixes (independent, early) | **The six early fixes of 05-authn §11.1** (the single list; SEC5-11, DATA5-12; early fix 6, R6-5: every `apikey:` writer — `SetAPIKeyStatus`, `SetAPIKeyExpiry` and the rest — takes `apiKeyMu`, so a last-use touch can never write back a pre-revoke row, `TestLegacyRevokeRacesTouch_NeverUnrevokes`), tracked in `todo.d/2026-11-04_platform_core_followups.md`, ship here or earlier as ordinary fixes, not as a rollback floor; they include the Basic gate refusing an empty stored password. No offline secret-recovery command is needed: the Basic password is never moved (R5-1(b)), so every rollback target keeps working with the stored one. The revocation journal is **not** among them: its lines are MAC'd with a key from the S3 keyring (05-backup §5B.3.6; DATA3-19). |
| Endpoints | `/api/*` redirect excludes `^/api/(core\|[a-z][a-z0-9]{1,15})/v[0-9]+(/\|$)` (`server_lifecycle.go:1798`); `TestNativePrefixesNotRedirected`; `TestCollidingNamespacesStillRedirect` unchanged. Every response carries `X-App-Build: <build id>`, which ABO's `main` writes (before S2) from the meta tag of the bundle it embeds; new fast exits `--version-json` with exactly `{app, version, commit, ui_build_id, binary_id}` (FE6-4; the same list as §9.6 row 20 and 05-updater step 5): `app` the app ID; `commit` the `vcs.revision` of `debug.ReadBuildInfo`, suffixed `+dirty` when `vcs.modified`, or `""` when absent (the Docker context has no `.git`); `ui_build_id` the embedded bundle's UI build ID; `binary_id` the binary's own identity, the version plus the SHA-256 of the running executable, computed by core's dependency-free leaf `buildid.Binary`, which lands in this stage and which `app.BinaryID` delegates to from S2b, so the S0 guard's `build` fields are defined from S0 (R5-7, FE5-3, FE6-3). The same PR deletes goreleaser's dead `-X main.commit`/`-X main.date` (`.goreleaser.yml:33-34`; `main.go` declares only `version`), since no new link-time value is allowed and `--print-upgrade-floor` (an empty requirement until the floor release; OPS4-16); `/health` gains a permanent `version` field, which the `deploy` template's post-check reads (OPS4-14; §9.6 row 20). |
| Build ID and reload check (OPS2-13, FE2-5, FE3-2, FE3-3, FE4-1, FE4-3, MOD4-9, FE5-1, FE5-2, MOD5-12, R5-7) | **One source: the embedded bundle's meta tag** (05-http-api §4.2 stage 5; FE7-3). The UI build ID is generated by `npm run build` itself — ABO's `vite.config.ts` from S0, the `@mediacore/shell/vite` preset from S9 — evaluated once per `vite build` by 07 §7.6.3's one rule (the one text; this row quotes it): `BUILD_ID` from the environment when it is non-empty, and it must match `^[A-Za-z0-9._-]{1,64}$` or `vite build` fails (an empty value falls through, FE6-7), and `dev` (any case) is reserved and fails `vite build` too (a case in `buildId.test.ts`; FE7-6); otherwise, when `git status --porcelain --untracked-files=no` is empty (07 §7.6.3; FE8-4), `<short sha>-<commit unix time>`; otherwise (a dirty tree, or no `.git` as in the Docker context) `<UTC timestamp>-<8 random hex>`. the Vite config writes it into the `<meta name="mediacore-build">` tag only (07 §7.6.3), so every build path stamps it — `make build`, the Docker frontend stage, goreleaser's `pre-build-script`, a bare `npm run build` — with nothing to carry between recipes; `vite build` never writes `dev`. **No Go link step receives it**: there is no ldflags variable, no `app.Options.BuildID` and no boot-time comparison, and a pipeline slip cannot make an artefact unbootable. It is the **UI** build ID only: the binary's identity, which the updater, storage and backup use, is `app.BinaryID`, a separate value that is never the UI build ID (R5-7). The server reads the UI build ID from the bundle it embeds — before S2 ABO's `main`, from S2 to S9 httpapi stage 5 from the mount's `LegacyMount.BuildID`, from S9 from `ui.Bundle.BuildID()` — and answers `dev` when nothing is embedded (an API-only build); `dev` never triggers the check. The SPA's `apiFetch` offers a reload when `X-App-Build` differs from the build ID it read once from that meta at page load, from S0, and when the header is **missing** on a 2xx/4xx response that carries a `Reporting-Endpoints` header naming a `csp` group — a pre-S0 server, because HEAD sets it globally (`internal/server/server_middleware.go:102-109`) — while a proxy's 502 or an Access interstitial carries neither and never counts (FE4-3). From S2 the core perimeter writes `X-App-Build` on every response, legacy mount, 404 and 410 included (05-http-api §8.1). The rule is never deleted: from UI-0 it is `@mediacore/api-client`'s `checkBuild(response, { buildId, shell })`, which ABO's `apiFetch` calls with the build ID it read once at page load from the served `<meta name="mediacore-build">` — a runtime input, never a define baked into the separately built library, whose own `vite build` would carry another ID (FE6-1) — and it runs in both the pre-shell and the shell bundle (07 §7.5.2); an e2e cell runs a production `dist/` build against the image (07). `TestBuildID_HeaderEqualsEmbeddedMeta` also asserts, for every `embed_frontend` artefact, that the meta is neither `""` nor `dev`. |
| Updater hotfix (OPS-6, OPS2-4, OPS2-5, OPS3-12) | The HEAD updater's restart exits **75**, not 0 (`updater.go:331-334`), so `Restart=on-failure` restarts it; it keeps the previous binary as `<exe>.prev` instead of deleting `.old` (`:278`), so the updater and `make rollback` share one slot (05-updater). It compares versions with `golang.org/x/mod/semver` and refuses a target that is not newer (today any difference counts, `:153`, so a main build would be offered a "downgrade"); a current version with a `git describe` suffix (`-<n>-g<hex>` or `-dirty`, `Makefile:9`), which semver would order **below** its base tag, counts as a **local build**: the check reports "local build, not compared" and a manual apply refuses unless the operator names the version explicitly (OPS4-17); before the swap it runs `<exe>.new --print-storage-levels` (falling back to `--print-storage-format`) against the sidecar with the guard table S0 ships, and `<exe>.new --print-upgrade-floor` against `<db>.storage-applied`, and refuses with the reason — `stepping_stone_required`, naming the release the new binary prints, for a store below the binary's floor (§9.2.2; OPS6-1). Because this updater has no backup or boot-counter rollback, **both** unattended apply paths become check-only until S8 — the scheduled apply and the maintenance-window op's step 1 (`scheduler_maintenance_window_op.go:72-91`): it notifies, health is Degraded `auto_update_paused_until_core_updater`, and the Settings UI shows a banner; a manual apply stays available behind the credential guard and the same checks (owner may instead choose a minimal boot-counter rollback in the HEAD updater; §9.12 O9). |
| Deploy (OPS2-17, OPS3-9, OPS4-10) | `DEPLOY_READY_TIMEOUT` (§9.3). `make rollback` calls `scripts/deploy-wait-ready.sh $(DEPLOY_URL) $(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) audiobook-organizer.service`, so the ssh polling below applies (without the host and unit it would fall back to URL-only polling). `scripts/deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]` polls `ssh $SSH_HOST systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus $UNIT` (and reports `ExecMainStatus=78` by name as "install the stepping-stone release first", OPS6-5): `activating` counts as progress (a long, extending start is not a failure); `failed`/`inactive` or a rising `NRestarts` fails at once, so a crash loop is reported in seconds; `active` starts `DEPLOY_READY_TIMEOUT` for the URL check. Without `SSH_HOST` (Docker, launchd) it polls the URL only, with a documented larger timeout. `scripts/tests/test_deploy_wait_ready.py` uses a fake `ssh`. `DEPLOY_STATE_DIR` and the owner actions of §9.2.2 (sudoers lines for both sidecars and the restore probe; the `deploy` recipe copied into `Makefile.local`); `scripts/deploy-preflight.sh` warns when `Makefile.local`'s `deploy` recipe lacks `DEPLOY_STATE_DIR` and refuses an S0+ deploy unless `sudo -n -l` lists the sudoers lines of §9.2.2 (OPS5-16). S0 delivers `scripts/staging/provision.sh` (offline archive restore through the ABO subcommand `staging-restore --archive <file> --db <empty path>`, which ships in this stage, egress rules before the first boot, the offline scrub through the ABO subcommand `staging-scrub`, both run with the binary of the release that wrote the archive; §9.3 "Staging"; R8-3) and the hand-written `scripts/staging/secret_fields.txt` (OPS5-11, R6-7; R7-6, OPS7-2, OPS7-4). The `deploy` recipe template and `scripts/deploy-preflight.sh` run `<new-bin> --print-upgrade-floor` against `<db>.storage-applied` (and the levels check) before `cp … .prev`, through the helper shared with `storage_format_guard.py`, and refuse a store below the binary's floor (OPS6-1). The README's compose notes gain the `/config` volume preparation (OPS6-10). |
| Rollback | Binary swap; the only state written is `c/storage/install`, the served ledger, the foreign-open record, the Pebble open stamp and the two sidecars, all ignored by older builds. |
| Done when | `audiobook-organizer --print-storage-levels` prints every owner at 0; the S0 image job green (full image, run, ready, levels inside the container, meta = header); `make ci` exits 0 with the classification below, extraction gates included; `make -C core exclude-check` and root `genproto-exclude-root` pass (03 §3.2 item 1, MOD3-12) and the throwaway `go work init . ./core && go build ./... ./core/...` step passes; `TestLegacyToleratesNamespacedKeys` (seeds `c/x`, `m/y` into a HEAD store and runs census, retention sweeps, backup, restore, boot) and `TestS0LedgerKeysRoundTrip` (the install marker precedes every `c/` key, every S0 key round-trips through `storage/keys`, and the guard's `build` fields written at S0 equal `buildid.Binary(version)`) pass, `TestVersionJSONFields` passes (06; FE7-14), and an S0-written fixture store is committed as the input of S2b's `TestS2bOpensStoreWrittenByS0` (OPS6-11); `scripts/test_storage_format_guard.py` covers the compat, restore-probe (over `restore/`, OPS7-11; the override applying to a pending intent only, OPS8-7) and `--print-upgrade-floor` cases of §9.2.2, the empty-floor and missing-sidecar cases included (OPS7-10); `curl -s -o /dev/null -w '%{http_code}' <abo-url>/api/core/v1/ping` returns 404, not 301; the updater test (fake release server serving the HEAD asset name to a binary in a temp directory) asserts exit 75, a kept `.prev`, refusal of an older tag, "local build, not compared" for a describe-suffixed current version with an older-or-equal base release (OPS4-17), refusal of a binary whose levels are below the store's, refusal (`stepping_stone_required`, nothing swapped) of a fake new binary whose `--print-upgrade-floor` the store's `.storage-applied` does not meet (OPS6-1), and that neither the scheduled apply nor the maintenance-window step applies (OPS2-4, OPS3-12); `make rollback` on staging (§9.3), run as the deploy user, S0 → HEAD passes the guard, with the restore probe passing on a store that never staged a restore; then the `deploy` recipe HEAD → S0 passes its restore probe and the S0 build boots on the store HEAD wrote (`make rollback` copies `.prev` over the current binary and leaves `.prev` unchanged, so a second run cannot go back; OPS5-10), and `deploy-match-unit-type.sh` runs (OPS2-16); `scripts/ci/check_embed_context.py` passes on HEAD (with `web/dist` listed in `scripts/ci/embed_build_outputs.txt`) and fails on a fixture embedding a `docs/` path and on a `go-builder` `COPY --from` destination missing from the list, while a fixture with a runtime-stage copy of the binary passes (MOD5-7, MOD6-3, MOD7-5); `scripts/tests/test_staging_scrub.py`'s S0 cases (§9.3) and `test_staging_egress.sh` pass (R7-6, OPS8-11); `check_rollback_rows.py` passes and fails on a fixture row listed under a stage earlier than its "From" (OPS8-6); `test_run_gates.py` has the `ci/state_writers.txt` case (OPS8-9); the `deploy` recipe refuses a fake binary whose floor the store does not meet (OPS6-1); `test_deploy_wait_ready.py` covers `ExecMainStatus=78` (OPS6-5); `scripts/tests/test_ci_classifier.py` and the flip-PR rule's case in `scripts/tests/test_run_gates.py` pass (OPS5-4, OPS5-8); the image job's second run asserts `.encryption_key` and decryption on `/config` (OPS5-7); `scripts/test_storage_format_guard.py` covers the unreadable-`.storage-levels` case and the preflight refuses a host without the sudoers lines (OPS5-16); a 12-minute fake migration under the unit is not killed and is not reported failed by `deploy-wait-ready.sh` (OPS2-17, OPS3-9). |

`make ci` prerequisites (`Makefile:785`) and CI jobs after S0 (MOD-8, MOD2-18):

| Target / job | Runs in | Notes |
|---|---|---|
| `staticcheck`, `fmt-check`, `test-short`, `go vet`/`go build`, `tidy-check` | per Go module (`GO_MODULE_DIRS`) | `fmt-check` excludes `web/`, `core/web/` and `core/examples/*/web/` |
| `fix-check` (`go fix -diff`) | **core only** (03 §3.2 item 4; MOD3-6) | the root has no `go fix` gate: `go fix -diff ./...` exits 1 at HEAD with pending fixes in 245 root files; a move PR applies `go fix` to the files it moves, in the same PR (04 §2.2) |
| `web-test` (was half of `test-all-short`) | per npm workspace: `web`, `core/web` (the latter also in `make -C core ci`, MOD5-15) | `test-all-short` = `test-short` + `web-test`, so neither runs twice |
| `lint-errcheck-ratchet`, interface-width job | per module, **separate baselines** (`core/.errcheck-baseline`, `core/.interface-width-baseline`); core's run from `make -C core ci` with scripts under `core/scripts/ci/` (MOD5-15) | one integer over root `./...` today; a move must not silently re-baseline |
| `lint-deadcode-ratchet` | root, with ABO's and hello's mains and `-filter 'github.com/falkcorp/mediacore/(internal\|kernel)'` (MOD2-19) | exported SDK packages are out of scope (as `pkg/` is today, `scripts/deadcode-ratchet.py:64`); no separate core baseline |
| `coverage-check-short` | per module: root floor `.ci/coverage-floor.txt`; core: a **ratchet floor** in `make -C core ci` from S2b; 12's SC5 thresholds run report-only in `make -C core readiness` (MOD2-8) | |
| `mocks-check` | per module: root `.mockery.yaml`; `core/.mockery.yaml` from the first core interface with a mock, run by `make -C core ci` (MOD5-15) | |
| `pprof-check` | per module from S1 (`internal/diag/flight` tests move to `mediacore/health`); core's in `make -C core ci` (MOD5-15) | |
| `oplint`, `sdkguard`, `ops-check`, `types-check`, `bench-check` | root only (`tools/cmd/oplint`, `tygo.yaml:28`) until their subject moves (oplint → `archtest/tasklint` at S5; sdkguard deleted with `pkg/plugin/sdk`) | no file of `cmd` moves (MOD2-4), but `cmd`'s `bench`-tagged `dedup_bench*.go` and `internal/server/bench.go` import `internal/database` (renamed at S2b), `internal/config`, `internal/ai` and `internal/httputil`, so every PR that moves or renames one of those rewrites them and `bench-check` is that PR's check (MOD4-7) |
| `go-test-short-shards` (`scripts/ci/short_test_shards.py`) and Woodpecker `test-rest`/`test-native-taglib` | root module, plus one core shard | the weight files are updated in the PR that moves a package (stale entries fail, `short_test_shards.py:257-258`) |
| `nightly.yml` (`test-nightly`) | per module | core's non-short and property tests run nightly |
| `ratchet-lower.yml` | per module | learns core's baselines |
| `codeql.yml`, `memory-leak-scan.yml`, `ci.yml` `fixture-tests`, Woodpecker `test-database`, `test-fixtures-{1,2,3}`, `test-server-scanner`, `checks-build`, `checks-lint` | root (fixtures, scanner, database) / per module (CodeQL Go, leak scan, build, lint) | each row added to `ci/gates.txt` with its classification |
| `ci/gates.txt` `store-width`, `store-references` | root, until the S2b+ split deletes `database.Store`; retired in that PR with the reason recorded | `scripts/tests/test_run_gates.py` validates every go-suite gate |
| core layering gate, `TestCoreImportsOnlyCore` | core only, registered in `ci/gates.txt` with `core-job`; core-owned check scripts live under `core/scripts/ci/` (MOD2-9) | the core guard's positive control is staged: S0 the engine's self-test on a synthetic graph, from S1 a named real edge, from S2 `app` → `kernel` (03 §3.5.1 item 2; MOD3-8) |
| `exclude-check` (core mode, and the "present/misplaced" scan of `core/`) | core only (`make -C core exclude-check`) | pass rule: load-bearing if at least one applicable probe shows the edge and fails with `ambiguous import`, stale if none does (03 §3.2 item 1; MOD3-12) |
| `genproto-exclude-root` (root mode, and the tree-wide "no other `go.mod`" scan) | root only, in root `make ci` | the only mode that applies at S0–S2 (MOD3-12) |
| `tools/stagecheck` (the type-checking half of the stage-visibility gate) | root `make ci`, explicitly, because it is its own module outside `GO_MODULE_DIRS`: `go -C tools/stagecheck vet ./...` and `go -C tools/stagecheck test ./...` | fixture tests for each negative control (an unlisted residue → taskstore reference, an unlisted test file, an unlisted (h) hit, a non-foldable key), so a helper regression cannot turn gate E or (h) silently green; registered in `ci/gates.txt` (MOD7-15; 03 §3.5.1 item 4) |
| `check_module_skew.py` | root only, **blocking**, in root `make ci` | no skew, the empty `core/go.sum` exists, the two exact `--pin`s hold (03 §3.2 item 6; MOD3-16) |
| stage-visibility gate (`scripts/ci/check_stage_visibility.py`) | root only, in root `make ci` and `extraction-gates`, every PR | evaluates **every remaining stage point** after the committed marker `shipped_stage:` (set by each flip PR); non-test and test imports of every package, `cmd/*` and the root `main` included, the file set being the union of `go list` over 03 §3.5.1 item 4's one tag-set list (MOD7-14), with no `IgnoredGoFiles` parsing (MOD5-16), `ForTest` variants and `.test` mains dropped; also fails on an ABO key name or op ID in a core-bound file under 04 §2.1 (h) (MOD5-3, MOD5-4, MOD8-4), and on an import cycle or a residue → taskstore reference (04 §8.6 gate E; R5-2) (04 §2.1 (c), (h); R3-3, MOD3-2, MOD4-7); its gate E also scans `internal/database`'s test files (MOD6-2), (h) uses 03 R1(c)'s one match rule (MOD6-7), the type-checking half is the `go run`-able `tools/stagecheck` in its own module (MOD6-13), it fails a PR that sets `shipped_stage:` to an unknown value or to a stage whose table names no flip PR (OPS6-8, OPS7-8), and a core-bound file that is not a move under 04 §2.1 (i) and has no 04 §8.6 row (R7-8, R8-6, MOD8-1), and a split that separates build-tag twins (04 §2.1 (b); MOD8-3) |
| `archtest.DeferredMembersAbsent` | root module, from S2 | the list is generated from every `Deferred-build members` heading under `docs/specs/platform-core/` (03 §3.4) and matched as package-qualified identifiers (`download.Pool.Grab`, `download.Event`; COR4-19); the PR that builds a deferred member deletes **that member's** entry, and the test is deleted when the list is empty (SIMP3-2, SIMP4-1) |
| prev-build one-format tests | root, in `extraction-gates` on every non-doc PR (§9.2.1) | `prev` built from `PREV_REF` and cached by tag (OPS4-6) |
| `compat-rehearsal` | root; nightly and in every stage's done-check; registered in `ci/gates.txt` | a two-row matrix: `PREV_REF` = the latest production tag, and `PREV_REF` = the committed `ci/prev_ref.txt` (the reference host's `.prev`, updated after each reference-host deploy from P10's output; OPS7-9); a red nightly run opens or updates one issue, and `extraction-gates` fails a stage's flip PR and every state-writing PR of §9.2.1 unless both rows of the most recent run on `main` are `success` and less than 36 h old (§9.2.1, OPS4-6, OPS5-8, OPS7-6); `PREV_REF` per §9.10 (OPS2-25) |
| `check_core_version.py` | core only, in `make -C core ci` (`core-ci`) | `version.Version` ≥ the newest checkpoint, hello's `MinCoreVersion`, npm lockstep (12 §12.4.3; MOD4-13) |
| `TestBooksMinCoreVersion` | root (`internal/books`), in root `make ci` | books' `Manifest.MinCoreVersion` ≤ core's `version.Version`; root-side, because a core script cannot read books' manifest from the `core/` export (MOD4-13) |
| `core-checkpoint` (`release-prod.yml`, `permissions: contents: write`) | prod release, a separate job that no other job `needs`, `continue-on-error` (MOD3-7) | when `core/` changed since the newest checkpoint and `version.Version` was bumped above it, records `refs/core-checkpoints/v0.Y.Z`; otherwise records nothing and opens or updates one `core-version-bump-needed` issue; the ABO release never fails because of it (12 §12.4.1, MOD2-14) |

### S1 — Core module skeleton, leaf packages, staged restore

| Aspect | Specification |
|---|---|
| Goal | Prove the nested module with real code; move dependency leaves that have no data or API surface; fix the one defect every later rollback argument relies on. |
| Flip PR | None: S1 leaves `shipped_stage:` unchanged (OPS6-8). |
| Moves | **04 §8.1 row S1 and §8.2 S1 rows** (authoritative): `internal/logging` → `mediacore/log`; no file of `internal/logger` moves (its `sanitize.go`, `attrs.go` and `retention.go` share unexported names and the `Logger` type with the staying façade, and `retention.go` prunes ABO families; MOD4-6); `internal/telemetry`'s provider construction, endpoint validator and resource are re-implemented in `mediacore/metrics` with namespace and scope as constructor options, which only **returns** providers — the process-global installs (`otel.SetTracerProvider`/`SetMeterProvider`, the error handler, the SDK logger, the default-registry Prometheus exporter) stay in the ABO façade at the `internal/telemetry` path until S2, so the six ABO packages that create instruments through the globals keep their series (MOD3-3, 05-metrics §13 item 1), and the attribute vocabulary, views, scope helper, version global and config readers stay there **permanently** as books telemetry (MOD4-4); its `ratchet_test.go` and `internal/telemetry/contract` stay in ABO as `internal/telemetrypolicy` (MOD-6); `internal/lifecycle` → public `mediacore/lifecycle`; `internal/security/{safepath,pathvalidation}`, `internal/libroot`, `internal/boundedcall`, `internal/diskstats`, `internal/util` `path.go`/`perms.go` → `mediacore/library`; `internal/diag/flight` → `mediacore/health`, **rewritten to take a `*slog.Logger` through its constructor** (it imports ABO's `internal/logger` today, `flight.go:108`; MOD2-11); `version` lands as `"0.1.0"`. **Stay in ABO** (04 §2.3): `internal/keyedmutex`, `internal/errhandling`, `internal/flight`, and `internal/logger/logtest`, which swaps `slog.Default` (R5 forbids that in core; `coretest` captures logs through an injected handler, MOD3-3). `internal/sdnotify` moves at S2 and `internal/startup` at S8 (MOD3-1). |
| ABO-local fixes | 05-config stage 0: the 8 bypassing `config.AppConfig` writes go through `Mutate`, `TestNoDirectAppConfigWrites`. **Staged restore** (OPS-15): restore stops extracting over the live store (`backup.go:777-870`) and stages the archive for the next boot, per 05-backup §5B.3.4, implemented in `internal/backup` before it moves at S8; the intent becomes an in-flight journal when the swap starts and is removed only when the restore is applied or swapped back (DATA3-6); `make rollback` and the updater refuse while either exists. From S2b the **kernel's journaled swap** runs at P0a (resume, swap, swap-back and completion of 05-backup §5B.3.4) over ABO's staged intent, and reports a restore boot, so P4d runs the post-restore hooks (from S4) on that boot; ABO keeps only the staging until S8 (06 §6.5.1; DATA3-7, DATA4-6). |
| Deploy (OPS2-26, OPS3-13, OPS4-3, OPS4-10) | The `deploy` recipe in `Makefile.local.example` refuses while a restore is staged or in flight (override `ROLLBACK_IGNORE_RESTORE=1`, the one override of §9.2.2, for a pending intent only; R8-1, OPS8-7), with the one ssh probe of §9.2.2, whose sudoers line is an S0 owner action; owner action: copy the updated recipe into `Makefile.local` again. |
| Data/keys, config, endpoints, permissions, UI | None changed. Metric and scope names byte-identical (namespace injected). |
| Rollback | Binary swap; no state. Older builds ignore a pending staged restore, so `make rollback` refuses while one is pending instead of leaving it to apply by surprise later. |
| Done when | `archtest.TestCoreImportsOnlyCore` (`go list -deps -test`) green; the seeded series contract test's golden diff against the S0 build is empty and the credentialed smoke scrape passes (05-metrics §6, §9.2.7; OPS5-9, OPS6-3); `go build ./... && (cd core && go build ./...)` exit 0 with **the pinned toolchain** (OPS-24); restore rehearsal on a copy: stage, boot, store equals the archive, and a crash between the swap and the hooks resumes them (`TestCrashBetweenSwapAndHooksResumesHooks`, 05-backup); under the committed unit (`Restart=on-failure`), staging a restore brings the service back without operator action, because ABO's staged restore requests its restart by exiting **75**, the S0 updater hotfix's restart code (from S2 through 06's `Lifecycle.RequestRestart`, which exits `core.lifecycle.restart_exit_code`, default 75; OPS8-12); the S1 boot measurement is written into the ledger as the first data point of §9.3's A/B budget. |

### S2 — Composition root; core perimeter; `books` wraps the server

| Aspect | Specification |
|---|---|
| Goal | ABO's `main` becomes `app.New(ctx, opts, books.Module(store))` (at S2 books receives the store `serve` opened; from S2b the kernel's handle — 06's `books.Module(…)`). Core owns process lifecycle, listeners, the typed services struct and the **perimeter**; ABO's whole gin engine is the books module's legacy mount, permanently. **Authentication and authorization do not change** (OPS-11, SEC-13). |
| Releases (OPS2-15) | **r1** composition root, lifecycle, listeners and supervisor timeouts (perimeter still in gin); **r2** perimeter move (05-http-api H0), except the Basic gate; **r3** logging stages 1–2. Each release carries the done-checks of its own rows below. |
| Flip PR | One per release (OPS6-8): the PR that switches `main.go` to `app.New` (r1), the PR that removes the perimeter from gin (r2), and the PR that deletes the telemetry façade's exporter and sets `app.Options.PromRegisterer` (r3); each sets `shipped_stage:` to `S2r1`, `S2r2`, `S2r3`, values of 04 §2.1's ordered list (OPS7-8). |
| Moves | 04 §8.1 row S2: `internal/serviceregistry` registrations → `module.Registrar` calls inside `books.Module()` (N4; the package itself is deleted at S10); listener code (`server_lifecycle.go:903-1078`: TLS, HTTP/2, HTTP/3, port-80 redirect, peer allow-list) → `core/httpapi` with `internal/httpmw` and `internal/openapi` (`httpapi` → `internal/openapi`, never the reverse; MOD3-4) and `internal/jsonschemax`; `internal/sdnotify` → `mediacore/internal/sdnotify`, **with every ABO importer rewired in the same stage** (04 §8.3, MOD2-1): `.` (the `--print-capabilities` fast exit) → `app`; `internal/server` (READY/STOPPING) → kernel lifecycle; `internal/database`'s `startup_notify.go` → the stateless `app.ExtendStart`, injected by `serve` (the store opens before `app.New` at S2); `internal/search` → its `IndexService` takes an injected `func(step string)`, wired by `books.Module()` from `Deps.ExtendStart`, because below layer 6 an ABO package never imports `module` or `app` (05-health §5H.3.4; MOD4-18); `internal/operations/registry` → `lifecycle.NotifySocketEnv`. **`internal/startup` stays in ABO until S8** (MOD3-1, R3-3): `cmd/root.go:274` runs seven books steps through it and the S2 kernel supervisor uses its own fixed core step list. `internal/config`'s loader for the core sections → `mediacore/config`, with the `config.OverrideStore` interface's read half (`Load`; no `storage` or `secrets` type in it; COR5-14) — the legacy capabilities `Open` and `Changed` live in core's `legacy` package (06 §6.4.10's S2 row; R6-3, SIMP6-9), and the basis mechanism (`Suspended`, `Confirm`) is a deferred-build member (SIMP6-1) — reading the settings blob through `app.Options.LegacyConfigStore` — the S2-only bridge, deleted at S2b — books' legacy config store over the store `serve` opened, which lives in the new `internal/books/configstore` (it imports `internal/config` and the store residue, and only `internal/books` and, for this bridge, `cmd` import it, so no cycle forms; R5-2, MOD5-2) (the façade comes at S3; COR3-1, R4-1); `app.Options.LegacyConfig` carries only the legacy key names. The **install code** of `internal/telemetry`'s façade is deleted: `app.InstallProcessDefaults` takes over the logger, error-handler, meter-provider, tracer-provider and propagator installs, and the App provider's Prometheus exporter registers on `app.Options.PromRegisterer` (ABO passes `prometheus.DefaultRegisterer`), so gin keeps serving `/metrics` from one registry until S8 (MOD3-3, MOD4-3); the books telemetry vocabulary stays at that path (MOD4-4). `compat` does not land at S2: nothing is aliased at S2, and the whole package (headers, counters, persistence, ledger) lands at S2b (05-http-api §3; MOD4-11). `ui.ReservedSegments()` and the dependency-free leaf types `ui.ModuleManifest`, `UIIDs` and `Redirect` land in one file, so `module.ReservedIDs()` reserves the shell segments from S2 and `internal/modulehttp` has a type for `modules[].ui` at S4 (FE3-8, FE4-6). The composition root `internal/books` lands (`books.Module()`, the one constructor name in 03, 04, 06 and 09; the `Registrar` calls; imported only by `.` and `cmd`, checked over non-test imports; MOD5-11), together with `internal/books/configstore`; each enters `layerOf` in this PR. The helper package `internal/books/contract` below it (the books `Writer` contract, `PageBooks`, selection helpers and task-kind aliases at S5, `Resource` declarations and resolvers at T8), which plugins import without a cycle, is created at **S5**, the first stage that moves content into it, and enters `layerOf` then (R4-5, MOD5-9; 04 §2.4). The `rbac` and `auth` leaf types are 04 §8.1's (05-rbac §3.1 decides; COR4-11). **No file of `cmd` moves** (MOD2-4): `serve`'s OTel/slog bootstrap in `cmd/root.go` is replaced by `app.New`/`app.InstallProcessDefaults` calls, its flag wiring moves onto `app.Options`, it takes the kernel-resolved state directory instead of calling HEAD's `EnsureSecureStateDir` (one resolver, OPS4-8), and it keeps opening the store and running books' migrations and settings steps **before** `app.New`, in HEAD's order (06 §6.5.1 "Stages"; 04 §8.3 per-step table); the cobra tree stays in ABO. `main.go` stops calling the process-global setters `server.SetVersion`/`SetEmbeddedFS` (`main.go:32-34`). `examples/hello` skeleton lands (module, app, one route, `openapi` subcommand emitting `core/openapi/core.json` with a core-CI freshness gate; 04 §8.1, MOD2-10, FE2-9, MOD3-4). `archtest.DeferredMembersAbsent` runs in the root module from here (SIMP3-2). Until S2b the kernel runs **without storage** (no P2/P4; `Deps.Storage` absent; 06 §6.5.1, COR2-9). |
| HTTP | `books` calls `httpapi.MountLegacy` with `Auth: LegacyAuthGin` (allowed only through `app.Options.LegacyAuthGinUntil`, which ABO sets here and removes in the S4 PR; SEC2-17), `ServesSPA: true` (FE-1: `ui.Handler` is **not** mounted; `app.Options` has no `UI` field until S9, 06 §6.4.10; gin's `NoRoute` keeps serving the SPA at `/`; MOD3-15) and the route manifest (every gin route its own mux pattern; the 36 permission-less authenticated routes annotated `AnyAuthenticated`, R6, and every `s.perm` route recording `rbac.LegacyAtom(atom)`, an S2 leaf value that `httpapi` accepts at S2 only in `MountLegacy` manifests under `LegacyAuthGin` and whose catalog resolution lands at S4 (COR6-1; `TestS2Manifest_SPermRoutesCarryLegacyAtom`); the legacy handler wrapper passes `r.Pattern` to gin and returns 404 when gin's matched route is not that manifest entry, SEC2-22). The perimeter moves to core and is removed from gin **in the same PR**: logger, recovery, security headers, CORS, gzip (with HEAD's per-path exclusions, 05-http-api §4.2), tracing, the `/api` rate limiter with per-route `RateClass` (05-http-api §8.1). **The Basic gate stays in gin until S3:** it needs the Basic password, a legacy secret that core config cannot read before `secrets` exists (05-config §1.12.2), so its per-route exemption stays gin's at S2. **CSP (FE2-1):** the books mount supplies its CSP per request (`DefaultCSP func(*http.Request) string` = today's `contentSecurityPolicy()`, so the inline-script hash and the Access team host stay; `csp.go` stays in ABO, 04 §7), and stage 5 writes `Reporting-Endpoints: csp="/api/v1/csp-report"`. **New**: cross-origin protection for cookie-authenticated unsafe requests (SEC-10); non-browser clients (no `Sec-Fetch-Site`/`Origin`) are unaffected and **no credential type is exempt** (FE2-23); refusals before S4 go to the access log with a `security_event` attribute, the audit write starts at S4 (SEC2-20). `client_trust.go` stays in ABO until S4 (gin's bootstrap, invite and ABS limiters keep calling `trustedClientIP`; SEC2-19). Gin's `RequireAuth`, permission checks and credential guards stay unchanged inside gin. |
| Lifecycle (OPS-13, OPS2-23) | Stop order of 06 §6.5.2 with one budget `core.shutdown.budget` = 25 s (05-health §5H.4; restart-bound, not hot-reloadable). Behaviour change listed: HTTP drain 30 s → `core.shutdown.http_drain` 10 s (`server_lifecycle.go:582`). Supervisor settings ship in this stage: `TimeoutStopSec=30` and **`KillMode=mixed`** in the committed unit (so operation-runner children are not signalled at t = 0), `stop_grace_period: 30s` plus `ABO_CORE_SHUTDOWN_BUDGET=25s` in `docker-compose.yml`, `ExitTimeOut` 30 in the plist, documented `docker run --stop-timeout 30`; the same unit change adds `RestartPreventExitStatus=78`, so systemd does not restart a floor-release refusal; launchd cannot be told so, and relaunches it throttled (§9.2.2; OPS5-15, OPS6-5); in a container with the key unset the default budget is 8 s (under plain `docker run`'s 10 s). |
| Logging (MOD4-5) | 05-logging stage 1 as split: `app.InstallProcessDefaults`, file output and deletion of the second `slog.SetDefault`/`log.SetOutput` only; the activity `Writer`'s line parsing, `StandardLogger`'s `syslog:` writes and HEAD's `syslog:` pruner stay until the **S5** release that starts `logjournal`, which removes them; stage 2 (books shim over core handles). |
| Data/keys | None. Books migrations 1..66 keep their bookkeeping (`db_version`, `migration_<n>`) as the books legacy track; the store is still opened by ABO's `serve`. |
| Config (COR3-1, COR4-2, COR4-20) | The core config loader reads the **same** sources as HEAD: YAML, env, and the settings blob through `app.Options.LegacyConfigStore` (05-config §1.12.2), attached at P3 with the precedence of the DB override layer, unlocked keys only. A write through ABO's legacy config path (`PUT /api/v1/config`) makes that store's `Changed` fire, and the loader republishes the changed live keys — at S2 and, through `Manifest.LegacyStores.Config`, at S2b — so a UI-saved perimeter value (rate limiter, CORS origins, log level) holds at boot and at runtime, as at HEAD; legacy env names and file keys are `Alias.BelowStored` from here (§9.2.5; OPS6-7). Canonical `core.*`/`modules.books.*` keys are accepted but undocumented. Flags `--port --host --tls-cert --tls-key --http3-port --external-url --read-timeout --write-timeout --idle-timeout --workers` (`cmd/root.go:526-535`) feed `core.http.*`/`core.tasks.workers`. |
| Deploy (OPS2-23, R3-4, OPS4-2) | Core's state directory keeps HEAD's fallback next to the database (05-config C15; 06 §6.4.5 `StateDir`), and **no shipped file sets the canonical state-directory variable**: with an `ENV` in the Dockerfile the directory would be explicit and never fall back, so a container whose `--db` is on another volume would put its keyring on an anonymous volume, lose it on recreate and refuse to boot with `ErrKeyMissing`; HEAD's fallback already yields the database's directory for the default `CMD` (`/data`), and the plist's `DATABASE_PATH` stays the one source of that fact. **One resolver from S2** (05-config §1.7): `ResolveStateDir` takes the database path from flags, env **and** the operator YAML file, as HEAD's fallback does (`internal/config/state_dir.go:143-153`), so a `database_path` given only in YAML keeps booting (OPS5-13). The S0 image job's second run (`--db /config/x.pebble`) checks it, running since S0 (OPS5-7). Owner action: install the updated unit/plist on each host and `systemctl daemon-reload` (a unit change reaches a host only when copied, `deploy/audiobook-organizer.service:8-10`). |
| Rollback | Binary swap; no state written. The previous build serves the same paths; the previous UI bundle is served by whichever build runs. |
| Done when | `main.go` contains no `server.NewServer`; `grep -rn 'serviceregistry.Register' --include=*.go internal \| grep -v _test` lists none of the services 04 moves; `coretest` boots core with zero modules (G7); the stage-visibility gate shows zero ABO users of the S2 rows of 04 §8.3; `archtest.DeferredMembersAbsent` green; **perimeter tests** (05-http-api §8.1): CSP header goldens per owner (`TestCSP_DefaultPerOwner` asserts the legacy SPA response carries the inline-script hash and the configured team host), `X-App-Build` on every response, the rate-limit class golden, compression exclusions equal HEAD's, the cross-origin test, `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash`, `TestPerimeterHonoursBlobOnlyValues` (rate limit disabled only in the blob → stage 10 off; a runtime edit through `PUT /api/v1/config` applies without restart; its runtime half runs again at S2b, COR4-2), at S2 r1 and r2 the credentialed smoke scrape (§9.2.7) passes and the seeded series contract test shows no removed family (05-metrics §6; OPS6-3) — `app.Options.PromRegisterer` stays nil (a private, unserved registry) in r1 and r2 and is set in r3, the release that deletes the façade's exporter, so two exporters never share one registry (OPS5-14, 05-metrics §6) — and at S2 r3 — the PR that switches the contract test onto the served `Provider.Handler()` and adds `mediacore_` to its in-scope prefixes (05-metrics §6; OPS7-1) — the contract test's golden diff adds only the kernel's own `mediacore_*` series (MOD4-3), the guard assertion that only `.`, `cmd` and `internal/books` import `mediacore/module` or `mediacore/app` (R4-5, MOD4-18), and the invite and bootstrap limiter tests unchanged; `TestS2Manifest_SPermRoutesCarryLegacyAtom` (COR6-1); `TestStateDirFallbackMatchesHEAD` and `TestStateDir_OneResolverYAMLDBPath` (05-config's name; OPS5-13), as unit tests and inside the image (OPS5-7); **wire goldens**: the 20 sampled routes (one per top-level group, `raw/09` §1.1) match `internal/wirecontract` goldens with volatile fields (timestamps, uptime, versions, request IDs) normalised — not byte equality across builds (OPS-24); **ABS** cell c; `systemctl stop` during a running op, in-process and subprocess mode, stamps it interrupted within the budget with no SIGKILL in the journal. |

### S2b — Core opens the store; books on claims

| Aspect | Specification |
|---|---|
| Goal | Core `storage` opens the one Pebble store; books reaches its families only through declared claims (R8); the guard, census, checkpoint and compat bookkeeping are core's (OPS-12 item 1). |
| Moves | 04 §8.1 row S2b: `internal/database` storage files → `mediacore/storage` + `internal/pebble` (`keyfamilies.go` stays ABO as the claim generator's input); `internal/compactprogress` → `storage`; Pebble collector → `internal/pebble` (gate S2b: ABO's Open Library store opens through `storage.OpenAuxiliary`), whose series are renamed to `mediacore_storage_pebble_*` in this stage with their gather-time aliases, ledger rows and the `deploy/` dashboard and rule switch in the same PR (§9.2.7; OPS5-6); `internal/database/dbtest` `newstore.go` → `coretest`; `compat` lands whole (headers, counters, usage persistence and the ledger from `app.Options.CompatLedger`, which `main` fills from `internal/books`' embedded copy, §9.2.9; MOD4-11, R4-4). The residue becomes `internal/books/store` on `storage.LegacyRange`; books' legacy guard projection, version-source adapter and exact census stay in it (04 §4.5). Storage opens at `core.storage.path` resolved per 05-storage §5S.4 step 1 from flags/env/file (Docker's `--db`, the unit's `DATABASE_PATH`), not under a state-directory root (COR2-8, DATA2-19). The kernel's journaled restore swap runs at P0a over ABO's staged intent (S1 row; DATA4-6). `config.OverrideStore` (its read half since S2) gains its native implementation over `c/config/` (hello's) and `Manifest.LegacyStores.Config`, which carries books' legacy one from `internal/books/configstore`; the write methods come at S3 (COR5-14); `app.Options.LegacyConfigStore` is deleted in this PR (SIMP4-11). Until `tasks` lands, books' start hook runs HEAD's every-boot `ReconcileOpsV3WithV2` before it builds its ops registry (it becomes `RunStore.Recover` at S5), and until S4 r2 `ReconcileVerifiedEmails` (it becomes part of `auth.BootWrites`), so neither every-boot writer of HEAD's store open is lost while `serve` runs no startup step (COR5-7). `examples/hello` gains its keyspace and one migration. |
| Releases | **Release 1 — shadow** (05-storage §5S.9.4): claims enforced in log-only mode, `core.storage.claims.mode=shadow`; every violation is fixed by registering or correcting the writer. **Release 2 — strict**, after release 1 served production with zero violations for 14 days. |
| Flip PR | Release 1's first core-storage PR (the one in which core `storage` opens the store; it sets `shipped_stage:` to `S2b`, OPS7-8), because the state core writes first (`c/storage/`, `c/compat/`, `c/storage/boot` and its state-directory mirror) starts there, so the red-rehearsal gate lands on it; strict mode in release 2 is a dial step under the persisted-violation check, which `core.storage.claims.mode=shadow` undoes without a rollback (OPS6-8). S2b **lands live PR by PR** (OPS7-6): the compat ledger, the Pebble rename and the reset change (both legacy reset routes, R8-1) are separate state-writing PRs, each under `extraction-gates`' red-rehearsal rule (§9.2.1). |
| Data/keys | No family moves, nothing is copied, no claim changes owner or mode and **no grant exists**: books holds every family (§9.2.3). Core config reads the blob through books' legacy config store over the kernel's one handle, and ABO's settings load, settings-encryption init and key-regeneration guard (`loadConfigFromDB`, `initEncryption`, `guardAgainstKeyRegeneration`, then `syncConfigFromEnv` and `config.Validate`) run **inside that store**, called by core config at the boot phase 06 assigns: never a second Pebble open, never defaults written over the stored blob, and each failing exactly where HEAD's step class is Fatal (R4-6, COR4-1; 04 §8.3 per-step table). Core config writes nothing to the store before S3, and books' legacy config `Open` writes nothing either (COR6-5, DATA6-11): HEAD's load-time writes get the home of HEAD's other init-phase writers — the idempotent reshaping `migrate*Blob` functions become pure read transforms, the marker-guarded one-shots (`log_retention_zero_migrated`, `embed_cover_art_default_on_migrated`, `maintenance_window_migrated`) become books versioned `Migration`s (`Additive` and `Seed: true`, P4b, writing HEAD's marker so an older build skips them; a fresh install runs them and so writes HEAD's three markers, and a rollback to a pre-S2b build never rewrites a stored `embed_cover_art=false` or `log_retention_days=0`; DATA7-13), each read transform consults the marker or that step's applied record, and the corrupt-secret repair is not run inside `Open` (05-config §1.12.2), so a stored `embed_cover_art=false` survives every restart. **HEAD's blind writers stay writers until S3** (DATA5-11, COR5-1, R5-3): `saveRawBlob` (`internal/config/persistence.go:883-885`), `SaveConfigToDatabase`'s secret loop (`:1989-2006`, which only warns on failure), and the import-root writers (`internal/database/pebble_store_importpaths.go:166,199-208`, until S7) write without an `Expect`; their claims are `Serialized` from S2b, `CheckedWrites` only from the stage that converts them (the blob and secret rows at S3, `import_path:` at S7; §9.2.3), so neither release of S2b refuses them and shadow mode records no violation for them. New core keys only under `c/storage/` (level stamps, served ledger, core migration bookkeeping `c/storage/ks/<owner>/`) and `c/compat/` (usage records). Books migrations 1..66 keep `db_version`/`migration_<n>` through 05-storage's version-source adapter and run first in the DAG, on books' own claims, through books' `LegacyRange` (claim-checked like every other access, so the shadow release sees the HEAD-era writers of steps 62/65/66; 05-storage §5S.3.4; COR6-12). **Both resets from S2b only stage** (R7-2, R8-1; DATA6-6, COR7-1, COR8-1, SEC7-4, SEC7-5, DATA7-6, DATA7-17): books' gin handlers for `POST /system/factory-reset` (kind `ResetFactory`) and `POST /system/reset` (kind `ResetStore`: store and settings only, no file effects, HEAD's `ResetSystem`), behind HEAD's own admin and owner guards (gin's chain decides until S4 r2), call `legacy.Maintenance.StageFactoryReset(ctx, requestedBy, kind)`, a narrow kernel-built handle whose one method is the kernel's staging function `kernel.stageReset` (it calls `legacy.ConfigResetArgs` for `ResetFactory`, then storage's Admin method with the captured hook arguments; COR8-2), handed to the module that declares legacy claims as `module.StorageHandles.Maintenance` (nil offline; deleted with the gin handlers in the S4 r2 PR, SIMP8-4); its `UpgradeFloors()` is gone, and ABO's `internal/backup` reads the floors from books' manifest `Storage.UpgradeFloor` (SIMP8-4); **there is no core route before S4 r2**. A reset is `KindReset` of the one restore swap (R8-1, SIMP8-1): one intent slot `restore/pending.json` with `kind`, created atomically (a second staging of any kind gets `storage.ErrSwapPending`, across processes), one rejection rule for every kind (`expires_at`, `staged_by_build`, a moved Pebble open stamp), one journal and one token pair. Staging pre-validates every target and refuses a dangerous `RootDir` with 400, nothing staged. Nothing destructive runs in the request and the handler writes no setting. The next boot sets each target aside beside itself, on its own filesystem (a `Recreatable` auxiliary DB on another filesystem, such as books' Open Library store under `<RootDir>/openlibrary-dumps`, is handed to the hook for deletion; COR8-3, DATA8-1), and is a fresh install with a new install ID whose fresh-install batch stages HEAD's factory defaults (`factoryDefaultConfig`, `root_dir` empty, `setup_complete` false; SEC8-5); a set-aside that fails moves back what it moved, journals `rejected` and boots the untouched store `degraded` (`factory_reset_failed`). For `ResetFactory`, HEAD's side effects — the gap-fill config file reset, the Open Library data and, unless 14 Q-K drops it, the library `RootDir` wipe — run in books' post-reset hook on the reset boot, after the swap journal reads `applied`, stated as not undoable (§9.2.6); the hook never fails boot: per-entry errors are logged, audited and reported as `factory_reset_cleanup_incomplete`, it always records `hook_done` (only a crash reruns it), its runtime is bounded and exempt from the 60 s start limit, it skips kept paths (the DB parent, the state directory, the set-aside destinations), and when no app hook exists the kernel records `hook_done` itself (COR8-4, OPS8-5). While a reset is staged or in flight, `CheckReader` reports `restore_pending` or `restore_inflight` (for a reset kept until `hook_done`), so `make rollback`, `deploy` and the updater refuse (the one override `ROLLBACK_IGNORE_RESTORE=1`, a pending intent only; §9.2.2, OPS8-7); from this PR ABO's `internal/updater` reads them through core `storage`'s `CheckReader` via the kernel binding (OPS8-8). Staging either reset requests a restart through the lifecycle supervisor, which exits 75, so the committed unit brings the service back (OPS8-12). The swap's intent TTL, `previous/` retention and the post-reset hook's runtime bound are core keys that land in this stage, `core.storage.swap.intent_ttl`, `core.storage.swap.keep_previous` and `core.storage.swap.reset_hook_timeout` (05-storage; backup reuses the first two from S8), and P4d sweeps expired `previous/` directories after readiness (SIMP8-1). The manual undo (moving `previous/` back) also renames the old install's set-aside revocation journal back and restores the keyring header's install ID with the offline command `<app> secrets set-install-id` (05-storage §5S.3.15; DATA8-12). The keyring is handled per 14, and the swap journal stays `set_aside` until P4d step 1 has rewritten the keyring header; an empty store is exempt from the upgrade-floor check, and its fresh-install batch records every books legacy step 1..`LegacyCap` as applied (§9.2.2; DATA7-7). `PebbleStore.Reset`, whose whole-range delete strict claims refuse and the shadow release would let wipe `c/` under a live kernel, is deleted with both of its callers (`handler.go:479,551`). **Offline opens (R7-4, R8-2; DATA7-11, DATA8-3, DATA8-4, SEC8-4):** from this stage's PR every ABO subcommand that writes the store (`scan`, `playlist`, `tag`, `organize`, `seed` under 05-storage §5S.4's empty-store rule, `diagnostics cleanup-invalid`, `staging-scrub`, `staging-restore`'s target, and the `Isolate` child mode) opens it through `app.OpenOffline`, the public app wrapper over `kernel.OpenOffline`: guard, floor and claims checked, migrations refused, and the boot verdicts computed — when it finds a foreign open, a missing or unreadable record or a behind lineage, its close batch first stages the owed entries a serve boot would stage, and only then promotes its own open stamp and sidecar identity, so an offline open never launders a foreign open (R8-2); `diagnostics query` and the `bench`-tagged `dedup-bench` open read-only through it; `diagnostics reserved-prefs` stays HEAD's break-glass direct open beside `pebble-inject-skip` (it exists for stores the guard refuses) and from this stage also repairs core's reserved records (05-storage); `pebble-inject-skip`, `reserved-prefs` and `pid-census` remain foreign opens, costing one round of fallbacks (05-storage). |
| Rollback | Binary swap (no data shape change; `c/` keys ignored), with §9.2.1 rows 12, 14 and 16: a reset staged on S2b takes effect at the next restart, `make rollback` refuses while it is staged or in flight, and undoing a factory reset does not bring deleted files back (OPS8-6). The dial `core.storage.claims.mode=shadow` undoes strict mode without a rollback. |
| Done when | The persisted claim-violation record of 05-storage lists no writer for ≥ 14 days of production on release 1 (not a process-local counter, OPS-16); `TestBooksClaimsGenerated` (books' claim list = 05-storage §5S.9.1; fixture with books writing every family, DATA2-1); `TestS2bOpensStoreWrittenByS0`; `TestS2bAppConfigFromBlob` (a value set only in the blob is effective in `AppConfig` after boot, and a `PUT /api/v1/config` changing one field leaves every other non-default blob field byte-identical; COR4-1); the runtime half of `TestPerimeterHonoursBlobOnlyValues` (COR4-2); the boot log names books' legacy config store as the `config` backend; free-space check before checkpoints and the checkpoint metric present (OPS-25); staged-restore rehearsal on core storage: the kernel's journaled swap runs at P0a, a crash mid-swap resumes or swaps back, and the boot reports a restore boot (DATA3-7, DATA4-6); the ledger-row check of config and env aliases starts here (COR4-20); `TestS2bBooksWritesEveryFamily` in strict mode includes a scan's root update (DATA5-11, COR5-1; the secret-save check moved to S3, where the flag exists, DATA6-12); `TestFactoryResetUnderClaims` (shadow and strict: no `ErrOutsideKeyspace`, the next boot is fresh) and `TestFactoryResetPostFloorBoots` (DATA6-6); `TestFactoryResetFromLegacyRoute_S2b` (gin admin + owner guards, intent written, next boot fresh, the Open Library store gone or moved; COR7-1); `TestSystemResetStagesStoreOnlyReset_S2b` (`POST /system/reset` under HEAD's guards stages a `ResetStore` reset: no file under `RootDir` or the Open Library dump directory is deleted and the next boot is fresh; COR8-1); `TestFactoryResetRejectedLeavesStoreAndSettings` and `TestFactoryResetIntent_RejectedAfterInterveningOpen` (a rejected intent leaves the store and the live blob untouched; DATA7-6, SEC7-5); `TestCheckReaderRefusesPendingReset` (`restore_pending` naming the reset kind; R7-2, R8-1); `TestSwapSlotExclusive` (two concurrent stagers of any kinds, in one process and in two: one wins, the other gets `storage.ErrSwapPending`; R8-1); `TestStageFactoryReset_DangerousRootRefused`, `TestFactoryResetCrossDeviceTargets`, `TestFactoryResetSetAsideFailureKeepsStore`, `TestPostResetHook_RemoveErrorsStillDone`, `TestFactoryResetReadOnlyRootDirCompletes`, `TestPostResetHookSkipsKeptPaths` and `TestFactoryResetNativeAppCompletes` (05-storage §5S.3.15; COR8-3, COR8-4, DATA8-1, OPS8-5); `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn` (SEC8-5); `TestHotfixUpdater_RefusesWhileResetPending` (OPS8-8); under the committed unit, staging a reset brings the service back without operator action (`TestRestartRequest_ExitsRestartCode`; OPS8-12); `TestOfflineOpenAfterForeignOpenKeepsEvidence` (R8-2); `TestFreshInstallPostFloorBootsTwice` (DATA7-7); `TestFreshInstallWritesHEADMarkers` (DATA7-13); `TestOfflineOpenIsNotForeign` and `TestCLISubcommandsUseOfflineOpen` (R7-4, DATA7-11); `TestS2bOpensStoreWrittenByS0` against the S0-written fixture store (OPS6-11); `TestFreshInstall_EmbedCoverArtFalseSurvivesRestart` (COR6-5); a store with a stale opv3 record and an unstamped email row is repaired on the first S2b boot (COR5-7); the seeded series contract test's golden diff shows only the renamed Pebble families and their aliases, and the smoke scrape passes (05-metrics §6; OPS5-6, OPS6-3). |

### UI-0 — UI groundwork (after S2, before S4; FE-2, FE-12)

| Aspect | Specification |
|---|---|
| Preconditions (MOD4-10, MOD6-5) | The reusable-workflow callers `ci.yml:127`, `frontend-ci.yml:64` and `nightly.yml:31` set `run-frontend: false`, with ABO-local frontend jobs calling `make web-deps` (S0 "Web build entry points"), so no job runs `npm ci` in `web/` before `core/web` is built. **Unverified, with a verification step first (MOD6-5):** the release callers `release-prod.yml:45-58` and `prerelease.yml:34-51` call `reusable-release.yml` with `frontend-enabled: true`, and that workflow lives in another repository the spec review could not read. Before the UI-0 PR, the plan reads `reusable-release.yml` at `.github/ghcommon-ref.txt`'s SHA and records what `frontend-enabled` does; then either sets it `false`, keeping Node set up for `pre-build-script` (stating where Node comes from), or adds a pre-install input to that workflow in the same PR as the ref bump. Both `pre-build-script`s become `sh scripts/ci/build_web.sh deps && cd web && npm run build`. |
| Flip PR | None: UI-0 leaves `shipped_stage:` unchanged (OPS6-8). |
| Goal | Remove the two UI ordering hazards before any UI call moves: the shell needs react-router 8, and S4–S6 must call native routes through the generated client, not hand-written code rewritten twice. |
| Moves | 07 §7.19 steps 1–3: react-router-dom 7 → react-router 8 in ABO with the declarative `<BrowserRouter>` unchanged — an **import swap only** (52 source + 70 test files, `vite.config.ts:81-82` chunk regex); the data router and the test harness rewrite follow at S9 (FE2-19). `core/web` workspace with `@mediacore/api-client` (runtime and generator) only — `ui-kit` and `shell` are created at S9 (04 §8.1, 07 §7.19 step 2; FE2-4) — `make generate-api-client`, consumed by ABO via `file:` and generated from `core/openapi/core.json`, which the S2 hello skeleton emits (MOD3-4); `@mediacore/api-client` exports `checkBuild(response, { buildId, shell })`, which ABO's `apiFetch` calls from this PR on with the meta tag's build ID and `shell: false` (07 §7.5.2, FE4-9; FE5-12, FE6-1). The PR that creates `core/web` adds `core/web/.gitignore` (`node_modules/`, `dist/`, `*.tsbuildinfo`, `coverage/`, `test-results/`), which travels with `core/web` into the split repo, so a build leaves no untracked file and Go's `vcs.modified` stays false; the UI build ID's "clean checkout" test is `git status --porcelain --untracked-files=no` (07 §7.6.3; FE7-1). This PR also adds the Docker frontend stage that copies the manifests and lockfiles of `core/web` and its members, runs `npm ci`, copies their sources and builds them in an explicit order before `web/` (`file:` deps resolve to `dist/`; MOD3-13). The JSON field naming of native routes is the one rule of 05-http-api §4.3 (FE-12a). |
| Data, config, endpoints, permissions | None. |
| Rollback | Binary swap (the UI is embedded per build). |
| Done when | `! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts` (FE2-19); vitest and Playwright e2e green; the generator produces a typed client from `core.json` in `core/web` CI; every build entry point of S0 builds `core/web` first (the image job green with `core/web` present); a dry-run RC build through `prerelease.yml` is green (MOD6-5) and leaves `git status --porcelain` empty after the rewritten `pre-build-script`, as does `make web-deps && make web-build` locally (FE7-1); the production-`dist/` e2e cell of `checkBuild` against the image (FE6-1); R-test cells a/b. |

### S3 — Config and secrets

| Aspect | Specification |
|---|---|
| Flip PR | The façade PR of the one release (Release row; OPS6-8); it sets `shipped_stage:` to `S3` (OPS7-8). |
| Goal | `core/config` and `core/secrets` become truth for reads and writes; every secret the new build writes is encrypted at rest (N10, G12), in the one stored format of §9.2.3. |
| Releases | **r1** — the flip (05-config §1.12.1 stage 1 plus §1.12.2's secret rule) — and **r2**, the enforcing release of §9.2.1 (OPS8-13), which turns the blob and secret rows' `CheckedWrites` from count-only to enforced after ≥ 14 days of r1 with zero persisted blind-write violations; r1: ABO's `internal/config` becomes the `Facade` over core handles (the `Facade` type of ABO's `internal/config`; MOD6-11); viper leaves the boot path, replaced by core `Load` + aliases (every implicit `AutomaticEnv` name registered as an alias, so nothing is dropped silently); every write goes through `Store.Apply`, which books' legacy config store renders into the blob in place, and the blob and secret rows become `CheckedWrites` in this release, count-only (a blind write is recorded, not refused) and enforced from r2 (R5-3, DATA6-12, OPS8-13); legacy env names and flags that HEAD let the blob override stay below the stored value (`Alias.BelowStored`, §9.2.5; R5-1(a)); `app.Options.LegacyConfig` **stays** (it carries only the legacy key names, which the aliases need until C3; COR4-3), and books' legacy config store stays until the settings family's cutover (§9.13). It is the stage's flip PR. |
| Mechanism | **05-config §1.12.2 over books' legacy `config.OverrideStore`** (SIMP-15, SEC-20, R4-1, R4-2): a key with a legacy blob name is read and written in place in `setting:config_blob`, in the legacy JSON shape; a key with no legacy name is a record in books' extension family; the four secret rows HEAD already encrypts (OpenAI key, Google Books key, Hardcover token, Basic password) are read and written in place in HEAD's encryption and never moved (R5-1(b)); a **plaintext** secret's **first write by the new build** stores it encrypted in `c/secrets/` and, in the same batch, replaces every plaintext copy — the legacy row **and** the gap-fill copy in `<db parent>/config.yaml` (DATA8-13) — with an unguessable, never-matching sentinel, never at boot and never because an unrelated key was saved (§9.2.1 row 1). `POST /operations/set-internal-flag` refuses any key of a store-interface family (`setting:config_blob`, the secret rows), so no dynamic key can bypass the store (DATA4-4). From S3 every integration-bound blob field (URL, host, credentials, headers) is `ProtectAdmin` on the façade, and a URL-origin change clears that instance's stored secret unless the same request re-supplies it, so URLs written on S3+ are admin-written (SEC4-7, 05-config §1.3.7); S7 still grants no `Local` without an admin's confirmation, because its input also holds pre-S3 URLs (S7 "Data/keys"; DATA5-9). There is no import, mirror, level step or contract for config. 09 schedules it; it does not restate it. |
| Basic gate (COR4-9) | The Basic gate (stage 8) moves from gin into the core perimeter with HEAD's **whole** exempt set: `/api/health` and `/api/v1/health` (`LegacyRoute.BasicExempt`, generated from `internal/server/middleware/basicauth.go:34`), the `/` owner's static assets by HEAD's prefix/suffix rule (`:40-46`, `LegacyMount.BasicExemptAssets`) and the ABS routes that run `ABSRequireAuth` (`:69`, per-route `OwnAuth`, SEC-14); its password is a `secrets.Ref` resolved from HEAD's encrypted row through books' legacy store (05-http-api §8.1; R5-1(b)). It **refuses "enabled with an unset or empty password"**: 401 on every non-exempt route and health `basic_gate_no_password` (05-http-api stage 8; R5-1(c), SEC5-4, DATA5-12, OPS6-6; `TestBasicGate_EmptyPasswordRefuses`). |
| Moves | 04 §8.1 row S3: `mediacore/secrets` lands as a native keyring; HEAD's cipher in `settings.go` stays books' as the row format of the four encrypted rows (04 §4.5; R4-1, R5-1(b)), and the `setting:` row methods stay books (cross-cutting 7); config's write path and `config.OverrideStore`'s write methods (COR5-14); `core/internal/keyedmutex` (04 §8.1, COR2-16); `internal/server/middleware/basicauth.go` → the `httpapi` perimeter (04 §7). |
| Config compatibility | All flat keys remain valid in YAML and env (§9.2.5); the blob keeps its field names (stored format). Canonical keys documented. Env canonical form per 05-config §1.3.6. |
| Deploy files (OPS2-12) | The shipped unit, plist, compose file and `local.conf.example` keep **only the permanent legacy names** for keys that have one (`DATABASE_PATH`, `BASIC_AUTH_*`, `FP_PARALLEL_WORKERS`, …) and use canonical names only for keys without a permanent alias, so a drop-in that overrides a legacy name can never be shadowed by a canonical twin in the base file; the four unread names (`AUDIOBOOK_ROOT_DIR`, `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL`) are deleted from them or, for `OLLAMA_BASE_URL`, replaced by the name ABO reads (owner decides); P4 scans the plist and compose files too. The compose file passes the Basic variables through only when the operator set them (`- BASIC_AUTH_ENABLED`, `- BASIC_AUTH_USERNAME`, `- BASIC_AUTH_PASSWORD` instead of `${…:-false}` defaults; OPS5-1); existing users keep their copied compose files, which `Alias.BelowStored` covers (§9.2.5). |
| Endpoints | `GET/PUT /api/v1/config` unchanged (write-only secret status per 05-config §2.10). |
| Rollback | Binary swap, per 05-config §1.12.2's rollback matrix: the blob is current on every older build (written in place); keys with no legacy name do not exist there; **plaintext secrets written on S3+ read as their sentinel there, so their consumers fail closed until they are re-entered** (release note, §9.2.1 row 1), while the four HEAD-encrypted secrets, the Basic password among them, are current there (R5-1(b)); a legacy copy that differs from its sentinel on roll-forward (re-entered, cleared or reset on the older build) is re-imported and re-sentinelled. §9.2.1 rows 12 (settings reset), 15, 17, 19 (Basic gate, rate limit) and 21 (canonical-only names) apply too (OPS8-6, R8-4). |
| Done when | The effective-config golden equals **HEAD's** for two fixtures: the production env, YAML and blob — run **on the staging host** (§9.3) against the scrubbed copy with the production unit's env names (values from P4, secret-class values replaced by placeholders), reporting only the diff of key names and value hashes, with nothing derived from production committed (OPS7-12) — and the **compose environment** (the one committed golden) — `BASIC_AUTH_ENABLED=false`, `BASIC_AUTH_USERNAME=admin`, `BASIC_AUTH_PASSWORD=` as compose sets them, with the blob holding `basic_auth_enabled: true` and a stored password (COR3-1, OPS5-1); `TestComposeEnvKeepsUIBasicGate` (that fixture on S3: stage 8 is on, `<username>:` gets 401 and the stored password gets 200); `TestBasicGate_EmptyPasswordRefuses` (OPS6-6); `TestRollback_OperatorEnabledAuthStaysOn`'s Basic-gate and rate-limit cells, with the stored-password, canonical-only-password and env-only-password cases, each asserting the gate enabled on `prev` (SEC8-1, SEC8-6); `TestRollback_CanonicalOnlyValuesReported` and `TestOperatorNames_NamesOnly` (`operator-names.json` lists names, never values, and `make rollback` prints the token; SEC8-6); the stage is done when r2 has served 14 days with zero refusals (OPS8-13); a settings save that changes a secret persists with no `ErrBlindWrite` (moved from S2b, DATA6-12), and the blob and secret flags are enforced only after ≥ 14 days of zero persisted blind-write violations; the `config_legacy_name_shadowed` health case (SEC6-1); `TestEveryLegacyConfigKeyHasCanonical`; `TestShippedDeployEnvNamesArePermanent`; `TestAliasConflictOnProtectedKeyRefusesBoot`; `TestBasicGate_ExemptSetEqualsHEAD` and the Basic-gate half of `TestPerimeterHonoursBlobOnlyValues` (gate enabled only in the blob → stage 8 on); the image job asserts the keyring on the data volume and decryption after the container is recreated (R3-4); R-test cells of 05-config §1.12.2: "save a new Basic password on cur, roll back: the new password gets 200 on prev and `<username>:` with an empty password gets 401 (HEAD's encrypted row, written in place, R5-1(b)); change it on prev, roll forward: the changed value is effective" (`TestRollback_BasicGateNeverOpenOnPrev`; OPS5-2), "rotate a gap-fill key on cur, roll back, save an unrelated setting on prev, roll forward: cur uses the rotated key" (`TestRollback_RotatedKeyNotResurrected`), "clear a secret on prev that cur had written, roll forward: it is cleared on cur", "edit a key on prev, roll forward, it is effective"; `TestPreS7Facade_IntegrationURLIsProtectAdmin` (SEC4-7); the image job's second run also asserts `secrets.keyring` on `/config` (OPS5-7); the Basic-gate half of `TestRollback_OperatorEnabledAuthStaysOn` (gate enabled through `ABO_CORE_HTTP_BASIC_GATE_ENABLED` over a stored `basic_auth_enabled: false` with a stored password; roll back to S2: the gate is on; R7-1). |

### S4 — Identity: auth engine, RBAC, audit

| Aspect | Specification |
|---|---|
| Flip PR | The r2 PR (Releases row; OPS6-8); it sets `shipped_stage:` to `S4` (S4 r1 is never a marker value; OPS7-8). |
| Goal | One core identity resolver and authorizer for every surface (web, API keys, CF Access, OAuth, ABS); permissions namespaced; admin is `core:admin`; audit trail (G13). **The one stage in which the auth engine changes** (cross-cutting 2). |
| Releases (OPS2-15, COR4-4, DATA4-2) | **r1 — dark**: `auth` and `rbac` land with `auth.Store` and `rbac.Store`, their native implementations (hello's) and books' legacy implementations (`Manifest.LegacyStores.Auth`, `.Roles`), while gin's auth still decides every request. At P4d step 1c (06's boot table; COR5-15), a read-only **decode census** reads every identity row through books' legacy stores (undecodable rows counted and listed for P1); the journal re-apply's auth kinds, `auth.PostRestore`, `auth.BootWrites` and P7's default-grant seeding are skipped and logged, **core** writes no identity row, and r1 does **not** list the revocation `journal` duty (gin revokes without journaling), so its interval becomes a `gap` line at the first r2 boot (05-authn §5, 05-backup). **r2 — flip** (the stage's flip PR): those P4d steps and P7 seeding start, the `journal` duty is listed, `LegacyAuthCore`, native routes, aliases, UI; ABO removes `app.Options.LegacyAuthGinUntil` in this PR. **HEAD's Start-time identity writers** (COR6-3): through r1, books' start hook (Run step 2) keeps HEAD's `seedRolesAndTokens` sequence over books' own code — `auth.SeedRoles` (the four seed roles), `auth.SeedSystemUser` (`_system`), the never-expiring-key stamp, `InitBootstrapToken` (a new token file and hash on every start) and the optional start-up read-only key (`internal/server/server_lifecycle.go:355,1098-1140`) — exactly as for `ReconcileVerifiedEmails`, so a fresh r1 install has its seed roles and every r1 restart mints a bootstrap token; the r2 flip PR deletes it as P7 seeding and `auth.BootWrites` take over, and moves `seed.go` and `bootstrap.go` (04 §8.1 S4, §8.5: dated `S4 r2`; COR7-11). **r3** is the enforcing release of §9.2.1 for `apikey:` (OPS8-13). Because books supplies its legacy stores in both releases, the claim ledger's `backends` record is the same, and rollback from r2 to r1 is a binary swap with the costs of §9.2.1 (never `ErrLegacyStoreMissing`). |
| Moves | 04 §8.1 row S4: `internal/auth` → `rbac` + `auth`; `internal/oauth` → `mediacore/auth/oidc`; the gin auth middleware is replaced by the read-only `legacyauth` bridge (`LegacyAuthCore`: core stage 12 binds the principal, stage 13 enforces each manifest route's permission; bridge adapters still call `rbac.Require`); `internal/database`'s identity files **stay** in the books residue as books' legacy `auth.Store` and `rbac.Store` (R4-1; 04 §4.5) — core's `auth` and `rbac` define their own types, interfaces and errors; auth/user/API-key/invite handlers → `internal/authhttp` (gate S4; `user_merge.go`'s methods move onto an ABO-owned merge handler type first, 04 §8.6, MOD2-3); `internal/uihttp` and `internal/modulehttp` (FE3-16); `events` (bus) lands. **In the r2 PR** the core factory-reset route lands in the new `internal/storagehttp` (L6, imported only by `kernel`): `POST /api/core/v1/system/factory-reset`, `core:admin` + `Guards.Interactive` + `Guards.Owner`, one route that receives the kernel's staging function as the storagehttp-local interface `storagehttp.Stager`, never `*storage.Admin` (COR8-2); its request takes `reset_kind` (`factory` by default, or `store`) and requires `{"confirm":"RESET"}` (422 otherwise), and success answers 200 `{reset_id, message}`; books' two legacy reset paths become its `Alias`es with HEAD's response shape, `POST /api/v1/system/factory-reset` and `POST /api/v1/system/reset`, which passes `reset_kind=store` (R8-1, COR8-1; §9.2.6 records the 422); the same PR deletes `legacy.Maintenance` and `module.StorageHandles.Maintenance` with the gin handlers, their only callers (SIMP8-4); through r1 the gin routes keep calling `legacy.Maintenance.StageFactoryReset` (S2b; R7-2, COR7-1, SEC7-4). `TestFactoryResetCoreRouteRecordsHookArgs` and `TestFactoryResetAliasKeepsConfirmAndShape` join the done-check. **Stay in ABO** (G11, G15): owner proof and CF Access glue (`internal/cfaccess`), `internal/cfaccesspolicy`, `internal/cfservicetoken`, guest tokens, ABS JWT/sessions (compat surface with per-route `OwnAuth`, which still refuses a confined worker principal, SEC2-4), `internal/usermerge` (rewired onto `auth` for its identity writes, §9.2.3 rule 4), and `internal/server/apikey_expiry_stamp.go`, which becomes a boot write of books' legacy auth store over HEAD's one-shot done flag (DATA4-12). The facade-only key machinery is not built (SIMP2-6). |
| Data/keys | §9.2.3 identity, session and invite rows: legacy rows read and written in place by books' legacy stores, after books track ≥ `LegacyCap`; native-only data (hashed sessions, native one-time tokens and invites, the canonical scopes of `mck_` keys — whose legacy row holds only admitted atoms, or a scope that grants nothing on an older build, never an empty list (R5-6) — role scopes, restriction fields) in books' extension family with fingerprints of the security-relevant fields of the legacy row (never `last_used_*` or `use_count`, R5-6; DATA5-2, SEC5-12), never additively in a legacy row (R4-1, SEC4-3); `apikey:` writes stage `Expect` (a last-use write is dropped on a conflict) and the family is `CheckedWrites` from S4 r2, count-only first (R6-5, SEC6-3); no raw session in any family the build writes; legacy invites read in place; no grant. New `c/audit/` (append-only, no legacy twin). User IDs unchanged, so books rows keyed by user are untouched. |
| Config | `oauth_*`, `cf_access_*`, `basic_auth_*`, `enable_auth`, `owner_email` aliases → `core.auth.*` (owner/CF keys → `modules.books.identity.*`). |
| Endpoints | Native `/api/core/v1/auth/*`, `/me` (05-authn §4.13), `/users/*`, `/roles/*`, `/api-keys/*`, `/audit`, `/permissions` (05-rbac §4.7), `/settings` and `/settings/schema` (05-config §1.3.4; FE3-11), `/modules` (`internal/modulehttp`) and `internal/uihttp`'s `/ui/diagnostics` and `/ui/csp-report` (07 §7.5.1; FE2-25), served by `internal/<capability>http` packages imported only by `kernel` (03 §3.3). Aliases (r2): `/api/v1/auth/*` (22), `/api/v1/users/*` (15), `/api/v1/me`, `/api/v1/setup`, `/api/v1/config` (2), `/api/v1/preferences` (3); the gin routes they replace are deleted in the same PR, except the unmapped ones below. Every `core.json` regeneration lands in the PR that adds the route. |
| Alias targets (SEC2-5, COR3-12) | Legacy paths that select their target by a field (05-http-api §4.6; strict decoding, `legacy_field_unsupported`): `POST /api/v1/auth/api-keys` → own `POST /api/core/v1/me/api-keys`, or with body `user_id` → `POST /api/core/v1/users/{id}/api-keys` (selector `user_id`); `GET /api/v1/auth/api-keys` → own `GET /api/core/v1/me/api-keys`, or with query `all=true` → the admin key list of 05-authn §4.13 (selector `all`). `PUT /api/v1/auth/me/password` is **not aliased**: it stays one gin route of the legacy mount, with and without `user_id`, calling the dominance-checked `auth.Service` (05-authn §4.13). Every other alias of this stage has exactly one target. Tests `TestAlias_UnknownLegacyFieldRefused`, `TestAlias_CreateKeyForOtherUserKeepsTarget`. |
| Invite and temp-login links (SEC2-6, FE2-8, FE3-5, SEC3-16, FE4-2) | **Invite links keep the `/invite/<token>` path form until S9**: the legacy SPA enters its public invite branch only for `pathname.startsWith('/invite/')` and routes only `/invite/:token` (`web/src/App.tsx:201-206`), so a fragment link would land the invitee on `/login` (signed out) or `NotFound` (signed in). From S4 core issues `/invite/<token>` links, which the legacy SPA's `AcceptInvite` opens and accepts through the aliased invite API. **The same S4 SPA change adds a reader only** for the fragment form (FE5-4, option (a); 07): the public-branch guard becomes `pathname === '/invite' || pathname.startsWith('/invite/')`, and `<Route path="/invite" element={<AcceptInvite />} />` prefers `#token=` and removes it with `history.replaceState`, so an invite S9 issues in the fragment form still opens after a rollback to S4 r2–S8, whose builds run core auth and read the native invite (reader before writer, §9.2.1; S4 r1 is dark and has neither the reader nor core auth, FE6-5). This reader ships in r2 with the other UI changes. The fragment form (`/invite#token=`), books' 302 from `/invite/{token}` and `invite` in `LegacyMount.SPAShared` start with the shell at S9. Temp-login links move now: core issues `/login/temp#token=…`, books' legacy manifest rewrites the existing `GET /auth/temp-login?token=` to a 302 to `/login/temp#token=<token>` with `Referrer-Policy: no-referrer`, and the same build adds a `/login/temp` route to the legacy SPA's unauthenticated and authenticated branches that reads `#token=`, removes it with `history.replaceState` and calls `POST /api/core/v1/auth/temp-login/consume` (05-authn §4.13). The temp-login 302 is the only implementation for its window (S4 + max(W, temp-login TTL), §9.6 row 34). |
| Permissions (FE3-7) | 05-rbac §6.1 mapping live; legacy atoms accepted on API input for W (surface `rbac_atom`); native routes output canonical names only; the `/api/v1/auth/me` alias keeps `permissions` in legacy atoms (reverse-mapped, the legacy wire shape, so previous-release tabs keep their controls) and adds `canonical_permissions` during W (05-rbac §6.3). `RequireAdmin` role-name checks (`middleware/auth.go:522-537`, `handlers/user.go:285,441,453`, `wire_master_list_routes.go:41`) → permission checks per 05-rbac §6.2. Legacy routes require the full legacy reach of their atom (R4-3, §9.2.6); the auth-disabled principal is judged against the atom's expansion; ABO sets `core.auth.disabled_allow_remote=true` by default (SEC4-8). A legacy `abk_` key whose scopes come from its legacy row (no extension record with `scopes_source: canonical`, §9.2.3 rule 1) keeps HEAD's intersection of key atoms and role reach on legacy routes, also after a restore stamped it and an admin re-enabled it (SEC5-7, SEC6-2); `rbac.LegacyAtom` requirements, recorded since S2, are resolved through the catalog from here (COR6-1); the auth-disabled principal is judged against the expansion through the atom-carrying requirement, and the deliberate auth-off tightenings start (§9.2.6; COR5-4, SEC5-8, SEC5-9). `/metrics`: the S4 r2 PR changes the legacy manifest entry from `AnyAuthenticated` to `core:metrics.read` (§9.2.6; OPS5-5). From S4 r2 books' start hook no longer runs `ReconcileVerifiedEmails`; `auth.BootWrites` does (COR5-7). |
| UI (FE-11, FE-12, FE2-17) | Auth, users and config calls switch to generated client operations in the **same** build. The 5 hard-coded permission constants and the `APIKeysTab` scope list are replaced by canonical permissions read from `GET /api/core/v1/permissions` (titles from 05-rbac); an ESLint `no-restricted-syntax` rule fails on any string literal passed to `usePermission`/`useAction`/`hasPermission`, assigned to a `permission`/`*_PERMISSION` property or constant, or used in the API-key scope list, unless it comes from `manifest.permissions` or the generated client. The Users page gains role management. |
| Rollback | Binary swap (§9.2.1 rows 2–5): one re-login per browser, and native sessions from before the rollback are revoked on roll-forward; temp-login links issued on S4 void on the older build (resend); invite links keep working there only if the older build still has the invite (legacy invites are read in place; native invites are void); `mck_` keys refused there, legacy keys keep working; a role edited on S4 grants there only the legacy atoms the row rule admitted, and its scopes are suspended on roll-forward if the older build edited the row; seed roles were never edited (refused, §9.2.3 rule 2). Deactivations, revocations and password resets made on either build are in the one stored row, so both builds honour them. Rows 13, 18, 19 (auth, from S4 r1) and 20 (an ABO archive restored on S4 r2–S7) apply too (OPS8-6). |
| Done when | `TestLegacyRoleReachUnchanged` green over 572 routes × 4 seed roles + scoped keys + auth-off (with §9.2.6's recorded changes) + its custom-role half (R4-3), wired as a permanent CI gate; `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` (SEC4-2); `TestS4_AuthOffDockerBoots` (image job, SEC4-8); `TestLegacyInviteLink_EndToEnd` (an invite created on S4 r2 is a `/invite/<token>` link; opened signed out it is accepted and the invitee signed in; opened signed in it shows the invite page, not `NotFound`; FE4-2); `TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev` (SEC4-3); `TestDarkReleaseWritesNoIdentityRow` (narrowed: **core** writes no identity row at r1) and `TestDarkReleaseRecordsJournalGap` (r1, COR4-4); on r1 a fresh store has the four seed roles and `_system`, and every r1 restart writes a new bootstrap token file (COR6-3); `TestAuthOn_LegacyAliasEnablesOverStoredOff` (blob `enable_auth=false`, env `ENABLE_AUTH=true`: auth is on after restart; R6-6, SEC6-1); `TestRestoredLegacyKey_ReenabledKeepsLegacySemantics` (SEC6-2); `TestRevokeRacesLastUse_NeverUnrevokes` (R6-5); `TestRollbackR2ToR1Boots` (DATA4-2); `TestAPIKey_StoredScopesNeverEmpty`, `TestOneFormat_LegacyRowsReadByPrevBuild` (run against the previous release's real binary, 05-authn §10), `TestRowRuleDominatesLegacyReach` (05-rbac), `TestMeAlias_LegacyShape` (FE3-7), `TestLegacyTempLoginLink_EndToEnd` (admin reset → link → logged in), `TestConfining_WorkerKeyRefusedOnABS`, `TestNoRoleNameAuthz` (AST, OPS-24), `TestLegacyFamiliesWrittenOnlyByLegacyStores` at its target for the identity families; the P4d decode census on r1: every `u:`, `apikey:`, `role:` row decoded by books' legacy stores, undecodable rows listed and 0 on production; `TestLegacyMetricsRoute_RequiresScopeFromS4` (OPS5-5); `TestLegacySPA_InviteFragmentReader` (an `/invite#token=` link opened signed out on the S4 r2 SPA shows the invite page and strips the fragment; FE5-4; one test name, the one 07 and 05-authn use, FE6-5, FE7-10); `TestFirstR2BootKeepsLegacySessions` (the first S4 r2 boot writes the session cut-off and deletes no legacy session or open invite: `DeleteLegacySignIns` runs only when the cut-off exists and predates `Owed.Since`, so the upgrade signs no one out and §9.2.1 row 3 is unchanged; R7-5, DATA7-9); the `apikey:` blind-write gate: zero persisted `op=blind` violations for row 3 for ≥ 14 days of production before the flag is enforced, and `TestS4r2APIKeyCheckedWrites` green in count-only and strict mode (DATA7-15), and the stage is done when r3 has served 14 days with zero refusals (OPS8-13); `TestUserMerge_ScopedAdminKeyRefused` (the ABO merge handler checks `core:admin` with key scoping; SEC8-7); `TestRollback_OperatorEnabledAuthStaysOn` (blob `enable_auth: false`, `ENABLE_AUTH=true` on cur; roll back S4 r1 → S3, and S4 r2 → S4 r1 and → S3: auth required on each; R7-1); `GET /api/core/v1/audit?kind=auth.login` shows the rehearsal login; `core.json` contains the stage's native routes; R-test incl. "deactivate on prev, roll forward, login refused", "create a key on cur, roll back: refused on prev, legacy keys work; roll forward: the key works", "seed-role edit refused on cur; a custom role narrowed on cur stays narrow on prev" (DATA3-16), "a custom confining role created on cur is confined on prev" (SEC4-3), "sign in on cur, roll back, roll forward: that session is revoked" (SEC4-9), "roll back and return with no restore: no API key is set `needs_confirmation` and no epoch is bumped" (SEC4-5), "re-enable a key on cur, roll back, use it on prev, roll forward: it is not `needs_confirmation`" (DATA5-2), "create a metrics-only key on cur; on prev the owner sets it active: it grants nothing there; roll forward: an admin re-enable keeps `core:metrics.read` only" (SEC5-1), "a key scoped `["settings.manage"]` held by an admin passes a `settings.manage` legacy route on cur as on prev" (SEC5-7), "roll back to a pre-S0 `prev` and open the store there twice, roll forward: the foreign open is detected, sessions are revoked and a `gap` line is written" (SEC5-5), "back up on cur, revoke a key on cur, roll back, restore that archive in place with prev; roll forward: the journal head is below the anchor (from S8 also the content marker is older, because until S8 ABO's own backup writes the archives and they carry the current pair), so the store is replaced, the revocation is re-applied, keys need confirmation and legacy sessions are deleted" (SEC5-6, DATA6-4, DATA7-16; 05-backup `TestOlderBuildInPlaceRestore_SameDirDetected`; an archive the older build itself made after the last journaling boot is 05-backup's stated residual), "an invite link created on cur opens and is accepted in cur's UI and in a previous-release tab (cell a)" (FE3-5, FE4-2), and "a previous-release tab still shows the delete button for a user holding `library.delete`" (FE3-7); cells a, b, c. |

### S5 — Tasks

| Aspect | Specification |
|---|---|
| Flip PR | The T0+T1 PR (Moves and batches row; OPS6-8); it sets `shipped_stage:` to `S5` (T8 is never a marker value; OPS7-8). |
| Goal | One task system (N9): v3 authoring on the v2 engine becomes `core/tasks`; required parallelism (N8, R7); `/api/core/v1/tasks` is the one API. |
| Mechanism | **05-tasks 5T.6.2 and §5T.8**: the ops families keep HEAD's stored format; core `tasks` reads and writes run records and pages, clocks, the pause and scan stand-down markers (`StandDownStore`, MOD6-4) and `Requires` stamps through `tasks.Stores`, which books implements with HEAD's own opv2/opv3 store code, including its dual write, moved unchanged in format to `internal/books/taskstore` (`Manifest.LegacyStores`; R4-1), over HEAD's run-state vocabulary `internal/operations/state`, which stays in ABO beside it (R6-2, MOD6-1): core's executor state machine is a re-implementation over `tasks.RunState`, and taskstore maps `RunState` ↔ `state.V3`. No store code is re-implemented in core and no grant exists. Run-store writes are checked per guard row (R7-3, §9.2.3): `CheckedWrites` on `opv3:run:`, `opv2:op:` and `opv3:meta:counts` only; every other run write — progress ticks, chunk-ledger commits, checkpoints, row marks — is legal only in a batch carrying the run guard's `Expect`; a ledger commit or row mark is dropped only under HEAD's closed-ledger rule, a bare tick on any state change, and `Transition`/`Insert` recommit under `storage.RetryOnConflict` while `want` still matches. Rows carry the legacy definition ID of every kind that has one (`LegacyDefID`), so the previous build resumes those runs; native-only data goes to books' extension family. There is no copy-and-mark, no `migrated_to`, no new reconcile, no clock, pause or stamp mirror, no adopt step and no contract for tasks; HEAD's every-boot `ReconcileOpsV3WithV2` becomes `RunStore.Recover`, run by `Executor.Run` (books' start hook ran it until T0+T1; COR5-7). Taskstore imports the store residue and is never imported by it (R5-2, MOD5-1): the T0+T1 PR moves legacy step 66's body (`pebble_store_plan_legacy.go`), the bodies of steps 62 and 65 and the ops part of `InitializeStore` with it, the legacy step list calls them through funcs the composition root wires, every capability assertion on a moved method becomes a direct call, and `TestLegacyStepPreconditionsResolve`, which lives in `internal/books` (the one package that imports both the step list and taskstore), fails on any "nothing to do" path for a production store type; the ops tests move with taskstore per gate E's test half (04 §2.4, §4.5, §8.6 gate E; MOD6-2). Core-owned kinds that replace an ABO op ID get their former ID from `app.Options.LegacyTaskAliases`, which lands by T4 (05-tasks; MOD6-6). |
| Moves and batches | 04 §8.1 row S5 and §8.3 gates: core's executor `internal/executor` is **written fresh** at T0 against `tasks.RunStore`/`RunState`, a re-implementation with a 04 §8.6 row, because the engine files use HEAD's v2/v3 row types and run-state vocabulary, which stay in ABO (R7-8, MOD7-1; 03 R1(c)'s move rule); the `internal/operations/registry` files stay at their path as the `opsbridge` v2 adapter until T8, with no `origins.txt` line (§8.3 counts its remaining importers; T8 ships at zero); `childop` (→ `tasks`' `rc.Await`), `opsmetrics` (→ the executor's instruments), `opsv3` (→ `internal/taskhttp`) and `pkg/ops/opstest` (→ `taskstest`) are re-implementations too; ABO's legacy formats stay ABO: `internal/operations/state.go`'s v1 checkpoint helpers → `internal/books/contract`, `BulkMetadataFetchParams` → `internal/server`, the v1 status bridge (`legacy_op_status.go`, `legacy_backfill.go`) stays in opsbridge as a books subscriber on `core.task.finished.v1` (05-events 5E.13), and the activity mirror becomes a books subscriber to the executor's log-line events (MOD7-2); core's scheduler is a **re-implementation** against `tasks.Schedule` (04 §8.6; MOD5-4), not a copy: the ABO formats in `internal/scheduler` stay books — the clock-key codec of `interval_clock.go` is taskstore's `ClockStore`, the pause marker of `pause_gate.go` taskstore's `PauseStore`, `maintenance_window_last_run` taskstore's `ClockStore` for the maintenance-window kind with HEAD's date codec (R7-8, MOD7-3), `taskV2DefIDs` and `pausableDefs` books data — and the ABO path is deleted at T7; from T0+T1 to T7 ABO's scheduler **keeps ticking** the 38 `TaskDefinition`s, enqueuing through `Deps.Tasks` and advancing their clocks through books' `ClockStore`, while core's scheduler polls only kinds that declare a `Schedule`, and T7 converts each definition and deletes its books ticker entry in one PR, so no kind ever has two schedulers (`TestOneSchedulerPerKind`; DATA4-5); the **readiness core of `health`** lands with `tasks` (`ComponentID`, `Reporter`, the `Ready(id)` view dispatch waits on; `core.storage` reported by storage, `books.memdb` by books' warm-up; COR4-8); the scheduler-admin handler → `internal/taskhttp`; `pkg/ops` → `tasks`; run fencing (`opswriter/fence.go`) → `tasks`; `storage/datamigrate`, `internal/storagemigrate` and `internal/logjournal` land (COR3-3); `internal/books/taskstore` and `internal/books/contract` are created and enter `layerOf` (MOD5-9); the ops series are renamed to `mediacore_tasks_*` in the T0+T1 release with their gather-time aliases, ledger rows and `deploy/` switch in the same PR (§9.2.7; OPS5-6); **the same PR deletes** the five `operations_*` client_golang families of `internal/metrics` (`operationStarted`, `operationCompleted`, `operationFailed`, `operationCanceled`, `operationDuration`, `metrics.go:20-47`), their setters `IncOperationStarted`/`Completed`/`Failed`/`Canceled` and `ObserveOperationDuration` (`:351-358`) and their calls in `registry/worker.go`, and replaces their seeding rows with alias rows (`_operation_duration_seconds` aliases `mediacore_tasks_run_duration_seconds`, a histogram, never the runs counter), otherwise their names would be live when the alias rows reach `AddAliases` and the flip build would not boot (OPS8-1, the S5 instance of OPS7-5). `childop` and `opsmetrics` keep their ABO paths until their last ABO user is gone, `opsmetrics`' one core successor being the executor's instruments in `internal/executor` (04 §8.3; R8-6, MOD8-2), and the activity mirror moves to `internal/activity`, a books subscriber that outlives T8 (MOD8-8). A later **enforcement release** ("S5 rE", §9.2.1) enforces the run guard rows' `CheckedWrites` (OPS8-13). 05-tasks batches, as releases: **T0+T1 in one release (the flip)**; T2–T7 in later releases of the stage, mechanically similar, suited to `/parallel-sweep` partitioned by package; **T8 ships in the floor release**, which names the stepping-stone release to install first (§9.2.2, §9.4.1). `examples/hello` gains `hello.count`; G6/G7 are proven from here (`TestSecondModuleNoAudiobookEdit`, which lives in `internal/books`; MOD5-11). |
| Data/keys | §9.2.3 `tasks.Stores` row. From T0+T1 every runtime write of the ops, clock and pause families goes through books' `internal/books/taskstore`, enforced by the ABO archtest `TestOpsFamiliesWrittenOnlyByTaskstore`; the only other writers are books' legacy steps in P4b and the v1 carve-outs of 05-tasks. T8 deletes, in the floor release, only the code that legacy steps 62, 65 and 66 call; books' legacy migrations up to `LegacyCap` run before core `tasks` calls the store (§9.2.2). `internal/logjournal` starts with `log.JournalStore` over `syslog:`, and **the same release** removes the activity `Writer`'s line parsing, `StandardLogger`'s `syslog:` writes and HEAD's `syslog:` pruner (`internal/logger/retention.go`; MOD4-5, MOD4-6). |
| Config (R2-7, COR3-11) | `scheduled.*` (~40), `maintenance.*` → schedule keys `modules.books.<subsystem>.schedules.<name>` declared under books' namespace and registered through `tasks.ScheduleFromConfig` (05-tasks §5T.3.8, the one schedule mechanism) with window aliases; worker count → `core.tasks.workers`. Dial: `core.tasks.kinds` (§9.2.8). |
| Endpoints (FE2-18) | Native `/api/core/v1/tasks/` routes of 05-tasks §5T.3.10: `definitions`, `definitions/{kind}`, `definitions/{kind}/runs`, `runs`, `runs/{id}` (+ `rows`, `items`, `log`), `runs/{id}/cancel`, `runs/{id}/approve`, `runs/{id}/decide`, `counts`, `timeline`, `schedules`, `schedules/{kind}`, `pause`, `unpause`; there is no tasks events route (run changes are bus topics streamed by `/api/core/v1/events`). Aliases (W): `/api/v3/ops/*` (12), `/api/v1/operations/*` (38), `/api/v1/op-defs` (2), `/api/v1/tasks` (3); legacy status vocabulary kept on aliased responses via `LegacyPath.Shape` (FE3-6). |
| Legacy streams until S6 (FE2-3, COR3-9, FE3-12, SEC3-9) | Until the S6 projections exist, a **books-side bridge** keeps HEAD's streams working: a bus subscriber on `core.task.*`, registered by books with `Registrar.Subscribe`, feeds stream A (`/api/events`, hub A's `operation.*`), stream B (`/api/v1/operations/events`, `registry.EventHub`) and stream C (`/api/v3/ops/events`, the v3 broker). It forwards **only kinds that have a `LegacyDefID`, under that ID**, so HEAD's per-event filters (`opEventFilter`, the v3 re-check) see what they see today; it never forwards a `core.*` kind or a kind without a legacy ID, and blanks run labels on stream A (05-realtime §4.9). Core never calls ABO code (R1). |
| Permissions | Per-definition `Permissions.Start` (05-tasks §5T.3.7); the implicit `settings.manage` default (`operations_v2.go:781-800`) becomes explicit on every definition that had none, as the atom-carrying legacy requirement, so the auth-disabled principal still starts those ops (no auth-off tightening of op starts) and a scoped legacy key keeps HEAD's intersection (§9.2.6; COR5-4, SEC5-7, SEC5-8). |
| UI (FE2-3, FE3-6) | **No change to the Activity page and `useOperationsStore`**: from S5 to S9 they keep their hand-written `api.ts` calls on the legacy task paths, now served by the aliases in the legacy shape, with the status enums of `web/src/generated/ops.ts`; `tools/cmd/opsgen` stays until the last `ops.ts` importer is native (04, FE4-7). They move to native REST and stream together at S9 (07 §7.19 step 4). Other UI calls move per 07 §7.19 step 6 as their native routes ship. |
| Rollback | Binary swap: the previous build reads every run, clock, pause marker and stamp in its own format (no stampede, no double fire, no library-wide prerequisite re-run); a run of a kind with a `LegacyDefID` is resumed by it under that ID; a run of a kind it does not register is dropped by its resume sweep (`resume.go:29`), as on any restart, and runs again on its schedule after roll-forward. |
| Done when | `opscatalog`'s 233 defs run through `core/tasks` with declared `Parallelism`; the G5 rollback test of 05-tasks §5T.9 and the resume rehearsal (kill -9 mid `books.acoustid.backfill`, restart, checkpoint resumes) green; `TestLegacyOpsWriters` and `TestOpsFamiliesWrittenOnlyByTaskstore` at their targets for each finished batch (COR4-22); `TestOneSchedulerPerKind`; `TestScheduleParity_AllDefinitions` (root CI, fake clock: for each of the 38 definitions, `cur` — core scheduler plus books ticker — and HEAD produce the same sequence of enqueue or no enqueue, start-run, maintenance-window reachability and clock advance, from the definition's `IsEnabled`, interval and `RunOnStart` for enabled and disabled config, and the two manual-only definitions, transcode and series-normalize, never self-start; OPS8-4, DATA5-8) and, as a smoke check, the staging boot logs the scheduler's table (name, enabled, interval, next due) and every enabled definition with an interval of 24 h or less fires within one interval, the rest listed with their next due time; `TestS5_FullAliasSetBoots` (the T0+T1 build's whole alias table plus every legacy collector `metrics.Register()` still registers: `AddAliases` returns nil, so the five `operations_*` families are gone; OPS8-1); the stage is done when rE has served 14 days with zero refusals (OPS8-13); `TestLegacyStepPreconditionsResolve` and gate E green (R5-2); the seeded series contract test's golden diff shows only the renamed ops families and their aliases, and the smoke scrape passes (05-metrics §6; OPS5-6, OPS6-3); `TestLegacyProgressVsCancelKeepsState` and `TestLedgerCommitAfterTerminalRefused` (R6-1); the run-row blind-write gate: zero persisted `op=blind` violations for rows 11–13's guard rows (`opv3:run:`, `opv2:op:`, `opv3:meta:counts`; R7-3) for ≥ 14 days of production before the flags are enforced, and `TestT0T1RunRowsCheckedWrites` — which covers the per-guard-row rule — green in count-only and strict mode (DATA7-15); a `books.*` run that reads books is not dispatched before `books.memdb` reports Up (COR4-8); the journal shows ABO records and the Activity page's system view is unchanged (MOD4-5); `TestS5Bridge_LegacyStreamsShowCoreRuns` and `TestS5Bridge_RepairsRunHiddenFromViewer` (streams A, B and C, 05-realtime); an e2e run with a request log shows the first-party UI enqueuing nothing on `task_kind` beyond the Activity page's aliases (OPS3-19); the Activity page shows live progress; `core.json` contains the stage's native routes; the boot log names books' legacy store as the `tasks` backend; R-test incl. "a run started on cur with a `LegacyDefID` resumes on prev", "a `Requires` stamp written on cur is honoured on prev", "a schedule clock advanced on prev neither double-fires nor stampedes on cur" and "store at books v64 → cur" (DATA2-2); `opsgen` keeps its HEAD input `internal/operations/state`, which stays in ABO, and its freshness gate shows `ops.ts` byte-identical (FE4-7, R6-2). |

### S6 — Events, outbox, realtime

| Aspect | Specification |
|---|---|
| Flip PR | The PR that deletes the S5 bridge, `registry.EventHub`, hub A and the v3 broker (Endpoints row; OPS6-8); it sets `shipped_stage:` to `S6` (OPS7-8). S6 lands live PR by PR: the `c/outbox/` PR is a state-writing PR under the red-rehearsal rule (§9.2.1, OPS7-6). |
| Goal | One notify-only bus with one topic catalog (SIMP-5), one durable outbox, one resumable permission-filtered SSE hub (G4). Webhooks move at S7 with the instance store and `providerhttp` they need (SIMP2-4). |
| Moves | 04 §8.1 row S6: `internal/realtime` → `realtime` (+`internal/sse`), `internal/eventhttp`; `internal/operations/registry/bus.go` → `events` with topics such as `books.book.imported.v1` and `books.read-status.changed.v1` (12 legacy event types mapped one-to-one in 05-notify §10); books' `BookChangeCoalescer` is rewritten to publish `books.book.changed.v1`, and HEAD's 5-second `system.status` ticker (`internal/server/server_lifecycle.go:404-441`) moves into a books worker that publishes `books.system.status-ticked.v1` (`Realtime`) with HEAD's payload `{books, folders, memory_alloc, goroutines, timestamp}` (COR4-7). The plugin `EventBus` (`internal/plugin/events.go`) is **not** moved: it keeps feeding the webhook plugin until S7 (SIMP3-10). The tag write-back outbox `pref:_system:outbox:writeback:` stays a books claim. |
| Data/keys | New `c/outbox/` (core-owned, no legacy twin). |
| Endpoints (COR2-1) | Native `/api/core/v1/events` (SSE with `id:`, `Last-Event-ID`, caps). Legacy streams `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events` are **core projections** of the same hub with their legacy framing: books obtains each handler from `Registrar.Realtime().Projection(p)` during P6 and mounts it in its gin manifest at the legacy path (`AnyAuthenticated`, `NoCompress`, per-event topic filtering inside the hub; 05-realtime), counted on surface `sse`. Projection A's topic list is `core.task.*`, `books.book.changed.v1` and `books.system.status-ticked.v1` (its `system.status` frame) from S6 on and **does not change at S8**: `core.system.status-changed.v1`, declared by `health`'s service from S8, is native-stream only and is never added to projection A, so stream A's `system.status` payload never changes and needs no compat-ledger row (05-realtime §4.9; COR5-9, COR4-7); the S5 bridge, `registry.EventHub`, hub A and the v3 broker are deleted in this PR. |
| UI (FE-10) | **No change.** The SPA keeps using the legacy streams through the projections; the shell's realtime client replaces them at S9. |
| Switches | Dial `core.outbox.relay_enabled`. |
| Rollback | Binary swap. Outbox rows are left unprocessed by S5; nothing correctness-critical is carried only by the outbox before S7 (the stage ships no such path; webhooks are not on it until S7). |
| Done when | `grep -rln 'text/event-stream' --include=*.go internal core \| grep -v _test` lists only the core hub and its projections; `TestProjectionTopicsExistAtStage` (P7 with the S6 and S8 catalogs; projection A's topics equal at both); legacy-stream goldens per event type, `system.status` frames byte-shape-equal to S5's; cells a, b (previous-release UI against cur and cur UI against prev stream events) and c (ABS). |

### S7 — Integrations, library, search index, notify

| Aspect | Specification |
|---|---|
| Flip PR | The PR that deletes `internal/plugin` (Moves row; OPS6-8); it sets `shipped_stage:` to `S7` (OPS7-8). S7 lands live PR by PR: the `Local` migration, `c/integrations/instances/` and the AI rename are state-writing PRs under the red-rehearsal rule (§9.2.1, OPS7-6). |
| Goal | Capability contracts in core with ABO's adapters behind them; one integration-instance registry (SIMP-4, SIMP4-12) over the one stored format; per-instance HTTP resilience (R5); the search index behind `storage/index`; notify v1 (mail and webhooks) built in one stage. |
| Moves | 04 §8.1 row S7: `internal/metadata/{providerhttp,dailyquota}`, `internal/security/safehttp` → `integrations/providerhttp` (instance-scoped); `internal/ai/aijobs`, generic `internal/aidispatch` → `integrations/ai` (`internal/metadata/dailyquota` stays ABO as the quota half of books' legacy `providerhttp.GateStore`, and `internal/ai/resultjournal` stays ABO as plain books code over `aijournal:`, served by no core store; 04 §2.3; COR5-10, SIMP5-3); the AI series are renamed to `mediacore_ai_*` in this stage with their aliases, ledger rows and `deploy/` switch in the same PR (§9.2.7; OPS5-6); Deluge client → `integrations/download/deluge` + books glue; `internal/mail` → `integrations/notify/mail`; `internal/plugins/webhook` → `integrations/notify/webhook`; **`internal/plugin` is deleted** with the webhook plugin: its event publishers publish books' 12 legacy event types through `module.Deps.Notify`, and its registry, route mount and handlers go (04 §8.3 deletion gate: 20 non-test files in 10 packages at HEAD; SIMP3-10); generic `internal/fileops`, `internal/watcher`, `internal/pathutil` parts → `library`; search engine lifecycle → `internal/bleveindex`, dirty set and reconciler → `storage/index`. Metadata sources stay books, implementing `metadata.Provider[Q,R]`. **No books row rewrite**: books keeps `BookVersion.TorrentHash`/`BookFile.DelugeHash` and builds `download.ItemRef{Instance: <the Deluge instance ref>, ID: …}` at the call site; the `ItemRef` migration is phase 2 with its own stage entry (SIMP2-7). |
| Data/keys (R3-1, R4-1, SEC3-5, SEC4-7) | **No import, no mirror and no grant.** `integrations` serves ABO's existing instances — Deluge (`download/deluge`), AI endpoints (`ai/<id>`), metadata sources, SMTP (`notify/email`) and each configured webhook URL (`notify/webhook-<n>`, `legacy_headers` on, keeping `X-Audiobook-Event`/`X-Audiobook-Signature-256`, plus a global `Rule` for the configured event list mapped to `books.*` topics; 05-notify §10) — through books' legacy store over their blob fields and secret rows, in `internal/books/configstore` (`Manifest.LegacyStores`, 05-metadata M1, 05-config C11; R5-2). Instances with no legacy form live in core's `c/integrations/instances/`; native-only fields (`Local`, limits) in books' extension family, with the hash of the legacy row they extend. **`Local` (egress trust; DATA5-9, DATA5-17, SIMP5-11):** books ships one versioned `Migration` (05-storage §5S.3.5; `Additive`, P4b, writing only `m/books/ext/integrations/`, so it raises no level and is not a legacy step under `TestNoNewLegacyMigrations`) that grants nothing: for each legacy-backed instance whose URL core's `legacy.IntegrationsLocalCandidate` accepts — an **IP literal** in the loopback or private ranges, bound to that URL origin — it records the origin as `PendingLocal` (a native-only, non-Widening extension member listed on the Integrations page), and `Local` is set only when an admin confirms it (audited; accepted only while it equals the current legacy URL origin, 05-metadata M1.10). The same S7 migration sets each instance's native-only `OperatorSecretOrigin` from the stored destination (at HEAD only `settings.manage` holders could write it), so an operator-sourced secret keeps being served to the destination an administrator configured; a later `Put` sets it only when it passes ProtectAdmin or the confirm route, and until the integrations cutover a non-admin `Put` that changes a destination origin of a legacy-backed instance with an operator-sourced secret answers 403 `ErrOperatorSecretBound` (§9.2.6; R8-5, SEC8-2, SEC8-3). A private host given by **name** gets no candidate and reports "private host needs Local or lan_allow" until an admin sets `Local` or lists it in `core.integrations.egress.lan_allow`. `ProtectAdmin` since S3 covers only URLs written on S3–S6, and the step's input also holds URLs written under HEAD's rule, a HEAD store that skips S3–S6 and a pre-S7 archive restored on S7+ (after which the step runs again, under the same rule), so it never grants by itself (`TestLocalStepSkippedStoreGrantsNothing`, `TestLocalStepAfterOldArchiveRestore`). Secrets follow config's rule (§9.2.1 row 1). Env-bound values ABO treats as authoritative today (Deluge, OpenAI) are an **in-memory overlay** over the stored row, never persisted (05-metadata M1.4 step 2, SIMP2-5). `library`, `integrations/ai`, `integrations/providerhttp` and `storage/index` start calling books' legacy `legacy.LibraryRootSource` (R6-3), `ai.JobStore`, `providerhttp.GateStore` and `index.DirtySet` over `import_path:` (and the books root from the blob), `aijob:`/`aijob_batch:`/`aijob_payload:`, `provider_throttle:`/`provider_daily_budget:` and `idx:sidx:dirty:` (§9.2.3; the dirty set per index through `index.Spec.Dirty`, SIMP5-2). Books' scanner, autoscan and filesystem handlers update a root's `LastScan`/`BookCount` through the root store's field-merge method (`GetForUpdate` + retry), never a whole-struct rewrite, and the root store keeps books' memdb `import_paths` table current in the same code (DATA4-4); that PR turns `import_path:`'s `CheckedWrites` on (R5-3; COR5-1). The search index format is unchanged (or rebuilt; it is derived data). **Release note:** plaintext secrets saved on S7+ must be re-entered after a rollback (the older build refuses them until then); an admin confirms `Local` for each LAN integration listed as pending; LAN hosts given by name need `Local` or `lan_allow`; integrations created on this release do nothing after a rollback (§9.2.1 row 9). |
| Notify (COR2-3, SIMP3-7) | Books emits its 12 legacy event types as notifications through `module.Deps.Notify` (`Publish(ctx, b, n)`, outbox-backed, refused for topics books does not own); undelivered entries older than `core.notify.max_age` (default 24 h; 05-notify §7) are dead-lettered and counted (OPS2-20). |
| Config | `deluge_*`, `download_client`, `openai_*`, `ai_endpoints*`, `smtp_*`, `metadata_sources`, `plugins.webhook.*` stay in the blob as the instances' stored fields; their YAML and env names get window aliases; `deluge_move_enabled` etc. → `modules.books.download.*`. `OPENAI_API_KEY`/`OPENAI_BASE_URL` stay permanent env aliases (§9.2.5). |
| Endpoints (SIMP2-17) | Native `/api/core/v1/integrations/{kind}/...` and `POST /api/core/v1/integrations/{kind}/test`; aliases (W): generic parts of `/api/v1/ai` (14), `/api/v1/deluge` (6), `/api/v1/metadata-sources`. Books Deluge behaviour routes stay on the legacy mount (cross-cutting 2, §9.2.4); only generic client routes become core instance routes with aliases. |
| Rollback | Binary swap: S6 reads Deluge/AI/SMTP/webhook settings from the blob, which S7 writes in place; plaintext secrets S7 wrote read as their sentinel on S6 and must be re-entered there (§9.2.1 row 1); instances S7 created natively do not exist on S6 (§9.2.1 row 9); a URL S6 changes is effective on roll-forward, and suspends that instance's `Local` until an admin re-confirms it (§9.2.1 row 10; 05-metadata, SEC3-5). Webhook deliveries pending in `c/outbox/` at rollback are not sent by S6; on roll-forward they are delivered once, or dead-lettered past `core.notify.max_age` (OPS2-20). |
| Done when | `plugin.DownloadClient` (0 implementors, `raw/11` §4) deleted with `internal/plugin` (deletion gate green); `download.Client` has 1 production implementor (Deluge) passing `coretest` conformance; no package-level throttle/quota map (`archtest.TestNoGlobals` on moved code); **deferred-build packages do not exist** (SIMP-1): `test ! -e core/integrations/indexer && test ! -e core/arrfacade && test ! -e core/acquire`, `integrations/download` contains no grab, monitor or pending-recovery code, and `archtest.DeferredMembersAbsent` is green over the generated, package-qualified list (`FacadeWriter`, `download.Pool.Grab`, `download.Event`, …; SIMP3-2, SIMP4-1, COR4-19); webhook golden (legacy headers and body) byte-equal to S6 for a `legacy_headers` instance; search results for the fixture query set equal S6's; the `Local` migration records candidates and grants nothing (`TestLocalStepSkippedStoreGrantsNothing`, `TestLocalStepAfterOldArchiveRestore`); the seeded series contract test's golden diff shows only the renamed AI families and their aliases (the AI families get no live sample on a staging copy, so no scrape can show them), and the smoke scrape passes (05-metrics §6; OPS5-6, OPS6-3); the root and AI-job blind-write gate: zero persisted `op=blind` violations for row 19's `import_path:` and `aijob*` families for ≥ 14 days of production before **S7 r2**, the enforcing release of §9.2.1, enforces the flags, and `TestS7RootStatisticsCheckedWrites` green in count-only and strict mode (DATA7-15); the stage is done when r2 has served 14 days with zero refusals (OPS8-13); `TestOperatorSecret_StoredEqualOriginServed`, `TestOperatorSecret_AdminStoredHostServed` and `TestRollback_NonAdminURLNeverCarriesEnvSecretOnPrev` (R8-5); `test_staging_scrub.py`'s S7 cases (§9.3; OPS8-11); R-test incl. "a Deluge URL edited on prev is effective on roll-forward and suspends `Local`", "a Deluge password saved on cur reads as its sentinel on prev (Deluge refuses it); re-entered there, it is effective on roll-forward", "a rename by `library` during a scan survives the scan's end" (DATA4-4), and "a webhook pending at rollback is delivered once on roll-forward or dead-lettered after the max age". |

### S8 — Backup, updater, health, metrics registry

| Aspect | Specification |
|---|---|
| Flip PR | The PR that mounts the kernel's `/metrics` and deletes gin's route and `internal/backup`'s staging (Moves row; OPS6-8); it sets `shipped_stage:` to `S8` (OPS7-8). S8 lands live PR by PR: the backup catalogue and `updater.Boot` are state-writing PRs under the red-rehearsal rule (§9.2.1, OPS7-6). |
| Goal | Platform operations owned by core; per-App metrics registry. |
| Preconditions (OPS-23, OPS2-4, OPS3-15) | A signed release (`release-manifest.json.sig`) is already published and its public key is compiled into the S8 build; installs migrated from `auto_update.enabled=true` get `core.updater.allow_unsigned_legacy=true` until their first signed apply (05-updater); `no_trusted_keys` and `unsigned_updates` are Degraded with a UI banner. **Owner action — service-owned binary directory** (OPS4-4), as a checklist: the directory is **outside the state directory** (which is 0700 and holds credentials, `internal/config/state_dir.go:112-116,135`), owned by the service user, mode 0755, binaries 0755 — e.g. a service-owned `bin` under the install prefix — so `make rollback`'s unprivileged probes (`test -f`, `.prev --print-storage-levels`, `--print-upgrade-floor`, `--print-capabilities`; `Makefile:899-902`) can still read it; S8 commits the unit with that layout as default (on macOS a user-writable location, and the plist's `ProgramArguments` path, `deploy/com.audiobook-organizer.plist:30-33`, 05-updater §5U.5); the recipes keep unprivileged probes and copy through literal sudoers lines (`cp` as the service user or root); update the drop-in `ExecStart`, the sudoers template, `DEPLOY_BIN` and `deploy-match-unit-type.sh`; copy both the binary and `.prev`; verify with a dry-run `make rollback` **run as the deploy user** that prints the `.prev` levels, and with the updater check's `binary_not_writable` clearing. Otherwise the updater disables itself with `binary_not_writable` (component Degraded), never failing half-way. |
| Moves | 04 §8.1 row S8: `internal/backup` → `backup` (ABO's staging code goes; the kernel's journaled P0a swap has served it since S2b); `internal/updater` → `updater` (release source from `app.Options`, not the slug `updater.go:72`); host parts of `internal/sysinfo`, flight-recorder wiring, trace download → `health`; **`internal/startup` → `kernel` + `health`** with `Registrar.StartupSteps`: `internal/server`'s start steps become books startup steps, and the path is deleted; `cmd/root.go`'s seven steps already left `serve` at S2 and S2b (04 §8.3 per-step table; MOD3-1, MOD4-17); `internal/metrics` registrations onto the App registry: the kernel mounts `Provider.Handler()` at `/metrics`, the legacy client_golang families move to `Provider.Registerer()` in the same PR, and `app.Options.PromRegisterer` is removed (MOD4-3), and the seeded contract test's registry becomes the private one in that PR (05-metrics §6; OPS7-1). `legacy.Maintenance` and `module.StorageHandles.Maintenance` are already gone (deleted in the S4 r2 PR with the gin reset handlers, their only callers; SIMP8-4), and ABO's `internal/backup` staging, which read books' manifest floor directly, goes now. |
| Backup (cross-cutting 8, SIMP2-2, SIMP4-5) | Archive format, retention floor, staged restore, provenance check and catalogue location are **05-backup's** (one format, DATA-21, OPS-14). S8 builds **plaintext** archives with the B5 provenance HMAC over a keyring-derived key, the catalogue and staged restore; **encryption is not built during the extraction**: it lands with the first app or cutover that can activate it (05-backup B3), because books' legacy restore bound `{core, 0}` holds for the whole extraction (no step raises the `core` level), so every earlier ABO release, HEAD included, can open every store and restore every archive. Secret-class items and the config file are handled as 05-backup states (no plaintext secrets in an archive, DATA2-17). The **05-config keyring export is the only export**. Restore staging checks the build's upgrade floors (§9.2.2; DATA3-9). A staged restore records the staging build and an expiry; a stale or foreign intent is rejected (moved aside, Degraded); `make rollback`, the updater and the `deploy` recipe refuse while one is pending or in flight; applying a restore rewrites the **three** sidecars (`.storage-levels`, `.storage-applied`, `.storage-format`) from the restored store (OPS4-15); the revocation journal is re-applied and post-restore revocations are written through books' legacy auth store into the legacy rows (05-authn §4.14). |
| Updater (OPS-6, OPS2-3, OPS2-6, OPS3-7) | `updater.Boot` runs in boot phase **P−1**, before the restore swap and config (06 §6.5.1), so a refusal in P0 (retired alias, invalid config) counts as a failed boot; automatic rollback runs `ReaderCheck(<exe>.prev)` (the same guard decision as `make rollback`); if refused, no rollback: `rollback_blocked`, Degraded, boot continues. One slot, `.prev`. A rolled-back version is recorded and skipped by scheduled apply until a manual apply or a newer release (05-updater). The stepping-stone check reads `<db>.storage-applied` (§9.2.2). Scheduled apply (check-only since S0) resumes with this build. |
| Health (FE2-6, FE3-13) | `/health` with its `ready` field is **permanent** (`deploy-wait-ready.sh`, Docker HEALTHCHECK; 05-health OPS-27b); core adds `/livez`, `/readyz`; `health`, `metrics`, `livez` and `readyz` are non-SPA segments, so `/health/` and `/metrics/` never answer HTML. Gin's `NoRoute` still owns `/` until S9, so the same PR adds `livez` and `readyz` to gin's `nonSPASegments` (`internal/server/spa_fallback.go:74-83`): gin's SPA fallback refuses them, and `/livez/` and `/READYZ` answer 404, not `200 text/html`. |
| Metrics | The two process gauges move to the Go collector with gather-time aliases whose targets `go_goroutines`/`go_memstats_alloc_bytes` are in `metrics.RuntimeFamilies()`, so `AddAliases` accepts them (§9.2.7; OPS6-2; the Pebble, ops and AI renames shipped at S2b, S5 and S7, OPS5-6). **The same PR deletes** `memoryAllocGauge`, `goroutinesGauge`, `SetMemoryAlloc` and `SetGoroutines` (`internal/metrics/metrics.go:188-197,302-303,422-423`) and their two calls in the books status worker — otherwise `audiobook_organizer_process_goroutines` would be live on the private registry and `AddAliases` would refuse its alias with `ErrAliasConflict`, so the S8 build would not boot — and replaces the two gauges' seeding rows with the alias rows; the `books.system.status-ticked.v1` payload keeps its `memory_alloc` and `goroutines` fields, read from `runtime` (OPS7-5); books names unchanged; `mediacore_build_info{version}` (the binary's version, never the UI build ID, R5-7) with the supported level vector; `deploy/prometheus` gains alert snippets for store level > supported (`mediacore_storage_level`), undecodable legacy rows > 0 (OPS-27d), and `rollbacks_total{outcome="done"}` (OPS2-6). |
| Config | `backup_*` → `core.backup.*`; `auto_update*` → `core.updater.*`. |
| Endpoints | Native `/api/core/v1/{backup,update,system,health}`; aliases (W): `/api/v1/backup` (4), `/api/v1/update` (3), `/api/v1/system` (7), `/api/health`, `/api/v1/health`. Permanent: `/health`, `/metrics`. |
| Rollback | Binary swap; archives written on S8 are restorable by every older build (unencrypted, ABO's legacy layout, 05-backup B1), with §9.2.1 row 15's costs for an archive written on S3+ (DATA5-19). |
| Done when | `TestS8_FullAliasSetBoots` (ABO's whole metric-alias table plus ABO's legacy collectors on the S8 Provider: `AddAliases` returns nil; OPS7-5; its S5 instance is `TestS5_FullAliasSetBoots`, OPS8-1); the seeded series contract test (05-metrics §6; OPS6-3), scraping the private registry's `Provider.Handler()` (OPS7-1): every books series name equals S7's; the golden diff adds `mediacore_build_info` and the listed additions of the PR — not `go_memstats_alloc_bytes` and `go_goroutines`, which the default registry has carried since HEAD (OPS6-2); every aliased core family answers under both names and the two retired process gauges only through their aliases (OPS5-6, MOD4-3); the credentialed smoke scrape passes (OPS5-9); `promtool test rules deploy/prometheus/alert-rules.test.yml` green; the stage-visibility gate shows zero ABO users of `internal/startup`; restore matrix (archive from S0 × S8 build, archive from S8 × S7 build and × HEAD, archive below the upgrade floor refused at staging, staged intent across a rollback) green; R-test cells d (failed boot → `Boot` rolls back, or `rollback_blocked` when `.prev` cannot read the store) and e; the updater refuses to apply a release whose `min_upgrade_from` the store does not meet (`TestApply_SteppingStoneRequired`); a staging install of the signed S8 release candidate *n*, prepared with `provision.sh --updater-check` (§9.3 step 5: egress to the release host names only), with `core.updater.channel=prerelease`, `auto_update.enabled=true` and a service-owned binary directory outside the state directory, applies the real signed RC *n+1* and `Boot` confirms it, so the shipped binary's trusted key, source wiring, apply and `Boot` are what is measured (OPS7-3, OPS8-3, SIMP8-10), and a dry-run `make rollback` as the deploy user prints the `.prev` levels (OPS4-4); cells a/b for the moved system/backup/update UI calls. |

### S9 — UI shell

| Aspect | Specification |
|---|---|
| Flip PR | The steps 4–5 release PR that deletes gin's `NoRoute`/`setupStaticFiles` SPA serving (Moves row; OPS6-8); it sets `shipped_stage:` to `S9` (OPS7-8). |
| Goal | `@mediacore/shell` + books UI contribution (D6, N6). |
| Preconditions (FE2-25, FE3-11) | UI-0 shipped (react-router 8, generated client); **S3–S8 done** — the shell's System and Integrations pages call S7 and S8 native routes, and `/api/core/v1/modules`, `/settings`, `/settings/schema` (the settings schema and `KeyState`), `/ui/diagnostics`, `/ui/csp-report` exist since S4. |
| Moves | 07 §7.19 step 4 (one PR series): create `@mediacore/ui-kit` and `@mediacore/shell` (with `/vite` and `/testing`) and add hello's web package to the workspace; shell-classified files move into `core/web` with audiobook imports cut into books slots; `main.tsx` → `createShell` with the data router; **then** `renderWithProviders` onto `renderInShell` (it needs the providers to have moved, FE2-4); `useUnsavedChangesBlocker` deleted; the shell's realtime client replaces `eventSourceManager` and the operations stream, and the Activity page moves to native REST and stream together (FE-10, FE2-3) and stops importing `ops.ts`; `tools/cmd/opsgen` stays until the last `ops.ts` importer is native (FE4-7, FE5-12); the preset writes the build-ID meta tag, the `mediacore-app` meta (`appId: 'abo'`, equal to `createShell`'s `app.id`; FE7-8, FE8-8), and the theme meta and boot script from `mediacore({ appId: 'abo', themeStorageKey: 'app-theme-mode', defaultMode: 'dark' })`; ABO's `vite.config.ts` adopts `mediacore(…)` in the same PR as, or a PR before, the one that passes `UIBundle()` to `app.Options.UI`, and the image job of that PR boots the embedded bundle, because `MustFromFS` fails closed on a bundle without the preset's metas (FE7-7, FE8-8); and ABO's hand-written inline boot script in `web/index.html:13-30` is deleted in the same PR (FE7-9); the PR adds `core/examples/hello/web/.gitignore` like `core/web`'s (FE7-1); the stale-build rule keeps running in `@mediacore/api-client`'s runtime (since UI-0), where both bundles run it (FE3-3, OPS3-11; 07 §7.5.2); ABO adopts the `UIBundle()` pattern (an API-only build gets a placeholder, never a panic, FE2-10) and `main.go` passes it to `app.Options.UI`, with `app.Options.UIOptions` carrying ABO's own contributions (FE3-4): as `PageMiddleware`, Cloudflare service-token recovery from `internal/cfaccess`; as `ConnectSrcFrom`, its Access team host re-read per response (what `csp.go` does today). Both read an `atomic.Pointer` that books fills in its Start hook and are a no-op (or an empty list) until then, so a page load during boot serves `index.html` with no panic (FE4-5). `ui_route` recording is **not** ABO's: the kernel registers one `compat.Entry` per `UIDescriptor.LegacyRedirects` entry (§9.2.9), and books declares those redirects in the release that moves ABO URLs to `/books/...`; steps 4 and 5 **ship in one release**, because `createShell` (step 4) already mounts books' routes under `/books/`, and a step-4-only release would move every URL with no redirect (07 §7.19; FE5-13), so C9's clock starts at the release in which something moved (FE4-8); the list `manifest.gen.ts` exports is the same list (FE3-17). Books drops `ServesSPA` and gin's `NoRoute`/`setupStaticFiles` SPA serving is deleted **in the same PR** (FE-1), with the gin `/api/v1/csp-report` handler; `ui.Handler` mounts from this build. Step 5 (server manifest and intersection) ships in the same release; per native route, step 6 follows. `examples/hello` gains `ui_embed.go` (UI half of G6). |
| UI routes | Module pages under `/books/...`; shell pages per `ui.ReservedSegments` (`/settings`, `/system/...`, `/users`, `/activity`, `/invite` and `/login/temp` with the token in the URL fragment, SEC2-6). From this build core issues invite links in the fragment form `/invite#token=…`, which the legacy SPA has read since S4 r2, so they still open after a rollback to S8 (§9.2.1 row 3; FE5-4); books' legacy manifest serves `GET /invite/{token}` (Public, `RateAuth`) with a 302 to `/invite#token=<token>` and `Referrer-Policy: no-referrer`, and `invite` joins `login` in `LegacyMount.SPAShared` (07 §7.4.3); that 302 is the only implementation for its window (S9 + max(W, invite TTL), §9.6 row 31; FE4-2). Legacy SPA paths (`/library`, `/authors`, `/series`, `/works`, `/playlists`, `/collections`, `/review`, `/versions`, `/fingerprints`, `/dedup*`) → client redirects to `/books/...`, plus the pages the shell absorbs: `/dashboard → /`, `/files → /books/files`, `/diagnostics → /system/diagnostics`, `/operations → /activity`, `/maintenance → /system` (07 §7.11, FE2-16; pattern of `web/src/utils/absRedirects.ts:17-25`); the legacy `/invite/{token}` and `/auth/temp-login` 302s are books' routes. `localStorage` keys are **read in place, never moved, renamed or copied** during the extraction (07 §7.7; FE4-4): the books keys keep their literal names in books' module, and the shell keeps the legacy names of the keys it reads: the seven keys of shell-bound files through `createShell({ legacyStorageKeys })`, typed by `LegacyShellKey` — table layouts, dismissed banners, the welcome wizard, the operations indicator and the advanced-settings switch included — and `app-theme-mode` through the preset's `themeStorageKey` (07 §7.4.2; FE5-5, FE7-9) — so a rollback to S8 keeps every preference. |
| Rollback | Binary swap; the UI is embedded per build. Bookmarks to `/books/...` 404 on the older build's SPA (accepted, cosmetic); S9 tabs left open are offered a reload by the api-client's stale-build rule, because an S8 server reports `ui_build_id: ""` with a non-empty `X-App-Build` (07 §7.5.2 case 2; FE3-3, OPS3-11). |
| Done when | `App.tsx` has no hard-coded module routes; `createShell.meta.test.tsx` and `TestBundle_MissingMeta` (FE8-8); a dummy second contribution renders with no edit to books UI (G6, UI half); G3: the shell rejects a module descriptor with unknown version; an e2e run with a request log shows no first-party request to the legacy streams (OPS3-19); `static_embed_recovery_test.go`'s port boots through `app.New`, not a hand-built `ui.Handler` (FE3-4); `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder (FE2-10); R-test rows "S9 tab, roll back to S8, reload offered", "a preference changed on S9 is kept after a rollback to S8" (FE4-4, FE5-5) and "an invite minted on S9, roll back to S8, opened signed out → the invite page, and accepted" (FE5-4); a page load before books' Start serves `index.html` (FE4-5); `TestLegacyInviteRedirect_SPASharedSegment`; `theme-boot.spec.ts`, and the built `index.html` has exactly one inline script (FE7-9); cells a, b, c. |

### S10 — Final cleanup (was S11)

**Flip PR:** the PR that deletes `internal/serviceregistry`, the first S10 deletion (OPS6-8); it sets `shipped_stage:` to `S10` (OPS7-8). Delete after their windows: removed flags `--db-type` and the SQLite no-op flag (`cmd/root.go:489,508`);
`internal/serviceregistry` and `pkg/plugin/sdk` (`internal/plugin` went at S7); the **alias code** of
every surface contract that has run; the legacy-session fallback (from the first release after S4 + 7
days, 05-authn §11); from that release books' `sess:` and `idx:sess:user:` claim is `AgingOut` and a books retention sweep keeps deleting expired raw `sess:` rows an older build writes during a rollback, which the current build otherwise removes only through the restore hook (DATA5-18). There is no mirror, rebuild or reconcile code to delete (R3-1), and S10 sets no
`min_upgrade_from`: it removes nothing an older store needs (§9.2.2). Compat-ledger rows closed. Not
deleted: the legacy mount, the `/api/*` → `/api/v1/*` redirect, ABS paths, `/health`, `/metrics`,
permanent env names, books' legacy store implementations (they serve the one stored format until each
family's cutover, §9.13), the `config.AppConfig` and logger façades (optional ratchets, SIMP-3; 11 §11.5
checklist). Done when §9.6 has no row in state "aliased" except those marked permanent and
`TestCompatLedgerMatchesCode` shows only permanent entries.

### 9.4.1 Sub-spec batch → stage map (OPS-12)

| Batch (owner) | Stage | Runtime behaviour change? | Rollback mechanism | Done-check |
|---|---|---|---|---|
| Toolchain bump to `go1.27.3` (13) | S0, first PR | no | revert PR | 13's pin check |
| Build paths, CI classification, Docker PR job, the named required checks (03 §3.2, 07 §7.3.2, R3-2) | S0 | no | revert PR | S0 done-checks |
| The six early fixes of 05-authn §11.1 (independent, early; SEC5-11; fix 6 the API-key revocation race, R6-5) | S0 or earlier | yes (refusals on legacy key paths, session view, empty Basic password; no lost revocation) | binary swap | 05-authn §11.1 tests |
| Updater hotfix (exit 75, `.prev`, semver with local-build detection, reader check, the binary's own upgrade floor against `.storage-applied` (OPS6-1), both unattended paths check-only) | S0 | yes (no unattended apply) | binary swap | updater test |
| 05-config stage 0 (8 bypassing writes) | S1 | no | binary swap | `TestNoDirectAppConfigWrites` |
| Staged restore, ABO-local (05-backup §5B.3.4) | S1 | yes (restore applies at next boot) | binary swap; pending or in-flight restores refused by rollback and deploy | S1 restore rehearsal |
| `internal/telemetry` construction to core, global installs and books telemetry vocabulary in the ABO façade (05-metrics §13) | S1 | no | binary swap | series contract-test diff empty (05-metrics §6) |
| Composition root, lifecycle, listeners; 05-health budget; supervisor stop settings | S2 r1 | yes (stop order, budget, drain 10 s) | binary swap | `systemctl stop` check |
| 05-http-api H0: `MountLegacy`, perimeter to core (Basic gate excepted), cross-origin check; config loader over `LegacyConfigStore` | S2 r2 | yes (perimeter owner, new cross-origin check) | binary swap | S2 perimeter tests, ABS, wire goldens |
| 05-logging stage 1 as split (`app.InstallProcessDefaults` with the meter, tracer and propagator installs, file output) and stage 2; telemetry install code deleted (MOD4-3, MOD4-5) | S2 r3 | yes (log file path, levels now honoured) | binary swap | level change applies; series contract-test diff adds only the kernel's `mediacore_*` series (05-metrics §6) |
| 05-storage rollout 1–2: core opens the store; books on `LegacyRange`, shadow claims; core config over books' legacy config store (R4-6); the stage's flip (OPS6-8) | S2b release 1 | no (log-only) | binary swap | zero persisted violations ≥ 14 days; `TestS2bAppConfigFromBlob` |
| 05-storage rollout 3: strict claims (a dial step, OPS6-8) | S2b release 2 | yes (refused writes) | dial `core.storage.claims.mode`, binary swap | strict on production 14 days, zero refusals |
| Reset staging (R8-1): both legacy reset routes stage `KindReset` (`ResetFactory`, `ResetStore`) of the one restore swap through `legacy.Maintenance`; post-reset hook; the `core.storage.swap.*` keys (05-storage §5S.3.15) | S2b release 1 (its own state-writing PR) | yes (a reset applies at the next restart; dangerous root refused at staging) | binary swap; `make rollback` refuses while a reset is staged or in flight (`restore_pending`/`restore_inflight`) | S2b done-checks; §9.2.1 row 12 |
| `database.Store` split: storage files (04 §4.5) | S2b | no (pure moves and 04 §8.6 rewrites) | binary swap | `make ci` both modules |
| 07 §7.19 steps 1–3; Docker frontend stage for `core/web` | UI-0 | no | binary swap | UI-0 done-checks |
| 05-config §1.12.1 stage 1 (façade) and §1.12.2 (writes through books' legacy config store, sentinels on a plaintext secret's first write; the four HEAD-encrypted rows in place; `Alias.BelowStored`); Basic gate to core | S3 | yes (config write path; secrets encrypted) | binary swap; re-enter moved secrets (the older build refuses them until then) | S3 done-checks |
| Enforcing release for the blob and secret rows' `CheckedWrites` (OPS8-13) | S3 r2 | yes (refused blind writes) | binary swap, or the claims dial | 14 days with zero refusals |
| 05-config optional ratchet (accessor) | none (optional) | no | — | `TestAppConfigRatchet` ceilings only go down |
| Identity services and books' legacy identity stores, dark (read-only decode census at P4d) | S4 r1 | no (gin still decides) | binary swap | decode census |
| 05-http-api S4: `LegacyAuthCore` with the full-legacy-reach rule, native core auth routes, P4d identity steps; UI auth/users/config calls and permission constants | S4 r2 | yes (auth engine, UI) | binary swap; §9.2.1 rows 2–5 | `TestLegacyRoleReachUnchanged` (with its custom-role half), permission-literal rule |
| Enforcing release for `apikey:` `CheckedWrites` (OPS8-13) | S4 r3 | yes (refused blind writes) | binary swap, or the claims dial | 14 days with zero refusals |
| 05-tasks T0+T1 (flip: executor, books' `internal/books/taskstore`, legacy-stream bridge, `health` readiness core); 05-logging journal over `syslog:` | S5 release 1 | yes (executor) | binary swap | 05-tasks §5T.9 G5 test; journal shows ABO records |
| 05-tasks T2–T7 (native definitions; T7 moves schedule keys) | S5 later releases | T2–T6 no; T7 yes (schedule keys, aliases) | binary swap | per-batch gate in 05-tasks §5T.8.2 |
| Enforcing release for the run guard rows' `CheckedWrites` (OPS8-13) | S5 rE | yes (refused blind writes) | binary swap, or the claims dial | 14 days with zero refusals |
| 05-tasks T8 (delete `OperationDef`, `opsbridge` and the code only legacy steps 62, 65 and 66 call; also `storage.LegacyMigration`, `storage.Spec.Legacy`, the runner's legacy phase and `TestNoNewLegacyMigrations`, which lose their last consumer here — `storage.UpgradeFloor`, `storage.LegacyGuardProjection` and the version-source adapter stay; SIMP7-7) | the floor release | yes (floor) | binary swap to a release at or above the stepping stone; older ones refused by `ErrUpgradeFloor` | 04 §8.3 registry count 0; `TestUpgradeFloor_RefusedBelowBooks66`; `TestStepSteppingStoneValid` |
| 05-events/05-realtime hub, projections, outbox | S6 | yes (stream server) | binary swap | S6 goldens |
| 05-metadata M1 instance registry over books' legacy store, books' versioned migration recording `PendingLocal` candidates (`legacy.IntegrationsLocalCandidate`; grants nothing) and setting `OperatorSecretOrigin` from the stored destination (R8-5); 05-download/05-ai adapters; library, AI-job, gate and dirty-set stores; 05-notify mail + webhook instances; `internal/plugin` deleted | S7 | yes (settings home, webhook delivery path) | binary swap; re-enter moved secrets | S7 done-checks |
| Enforcing release for `import_path:` and `aijob*` `CheckedWrites` (OPS8-13) | S7 r2 | yes (refused blind writes) | binary swap, or the claims dial | 14 days with zero refusals |
| 05-backup format and catalogue (plaintext archives; encryption not built); 05-updater signed apply; process-gauge renames and kernel `/metrics` (the Pebble, ops and AI renames ship at S2b, S5 and S7 with their emitters, §9.2.7); `internal/startup` to `Registrar.StartupSteps` | S8 | yes | binary swap; archives restorable by older builds | S8 done-checks |
| 07 §7.19 steps 4–5, in one release (the shell release starts fragment invite links and registers the `ui_route` entries; FE5-13) | S9 | yes (UI routes, stream client) | binary swap | S9 done-checks |
| 07 §7.19 step 6 (remaining `api.ts` sections) | S4–S9, per native route | no for users | binary swap | generated client covers the route |
| 05-logging optional ratchet (injection); logger façade removal | none (optional) | no | — | slog ratchet ceilings only go down |
| Mock conversion (`internal/database/mocks`, 04 §4.6) | S2b–S4 (owner decides: in-stage or deferred behind `storetest`) | no | — | 04 open item 1 |

## 9.5 Surface contracts

Every contract is a PR gated by `TestNoContractBeforeSunset` and P8 and merged through `make ci`
(§9.2.1). A contract retires **surfaces** only: no contract deletes, rewrites or re-levels stored data
during the extraction (R3-1), so there are no store-side contract preconditions, no persisted contract
phases and no contract checkpoints. On a store whose own usage record shows a removed surface used
within 14 days, the contract build reports Degraded `legacy_surface_removed_in_use` (§9.2.1).

| Contract | Earliest | Usage precondition | Action |
|---|---|---|---|
| C3 config/env aliases | R(S3) + W | P8 shows no use on the `config`/`env` window aliases (boot-read rule, §9.2.1) | YAML legacy keys refused at boot for one minor, then their alias rows deleted; retired env names per §9.2.5. The blob's field names are untouched (stored format) |
| C4 identity | R(S4 r2) + W | P8 zero for the `http` auth/users aliases and `rbac_atom` | legacy atoms on API input → 400 naming the canonical permission; HTTP aliases → `Gone` one minor → delete; the `/api/v1/auth/me` alias's `canonical_permissions` goes with it. Atoms stored in role and key rows are untouched (§9.2.6) |
| C5 tasks | R(S5) + W | P8 zero for `task_kind` input and the task `http` aliases | aliases → `Gone` → delete; stored-row kind resolution through `FormerKinds`/`LegacyDefID` is **kept** |
| C6 events | R(S6) + W and after S9 | P8 zero on `sse` | legacy streams → 410 (EventSource stops on non-200) → delete projections |
| C7 integrations | R(S7) + W | P8 zero on the generic `/api/v1/{ai,deluge,metadata-sources}` aliases | aliases → `Gone` → delete; webhook instances keep `legacy_headers` as instance data; the operator overlay and its permanent env names are unaffected (05-config C11) |
| C8 backup/update/system/health/metrics | R(S8) + W | P8 zero on `/api/v1/{backup,update,system}` and the health aliases; P6 shows no external dashboard on aliased core metric names | aliases → `Gone` → delete; metric aliases removed by PR (gate P6) |
| C9 UI routes | R(S9) + W | P8 zero on `ui_route` | client redirects removed; the legacy invite and temp-login 302s removed at the end of their own window (§9.6 rows 31, 34); `localStorage` keys are untouched (read in place, FE4-4) |

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
| 10 | Permission atoms in API input/output | 13 | 05-rbac §6.1 | W after S4 | input counted → 400 naming canonical (C4) | `permissions.go:24-85` |
| 11 | Permission atoms in stored roles/API keys | `role:`, `apikey:` rows | legacy atoms only in the rows; canonical scopes in books' extension family (05-rbac §6.3, R4-1) | stored format until the identity cutover (§9.13) | none in the extraction | `store.go:968-994` |
| 12 | Role-name admin check | `"admin"` | 05-rbac §6.2 | none (internal) | replaced in S4 | `middleware/auth.go:522-537` |
| 13 | Config keys in YAML | ~251 flat keys | `core.*`, `modules.books.*`, instance fields | W after S3 (S7 for instance keys) | refuse-to-start naming canonical (one minor) → alias deleted (C3, C7) | `config.go:2621` |
| 14 | Config in DB (`config_blob`) and legacy secret rows | 1 blob + 4 rows | read and written in place by books' legacy config store (05-config §1.12.2) | stored format until the config cutover (§9.13); a secret's first write leaves sentinels | none in the extraction | `persistence.go:359-884,1996-2008` |
| 15 | Env vars, window | `BindEnv` names + implicit `AutomaticEnv` names + `ABK_*` (counts: 05-config §1.12.4) | 05-config §1.3.6 | W after S3 | not read; `ProtectAdmin`+ and secret-class keys refuse to start (§9.2.5) | `cmd/root.go:625` |
| 16 | Env vars, permanent and runtime | the single list of 05-config §1.12.4 (permanent aliases, including every read name a shipped deploy file sets; runtime/ecosystem names config does not read) | unchanged | **permanent** (OPS-5) | none | 05-config §1.12.4; §9.2.5 |
| 17 | CLI commands | `serve scan playlist tag organize inspect-metadata seed diagnostics{cleanup-invalid,reserved-prefs,query} fp-worker dedup-bench` (bench tag) | unchanged; added: `staging-restore` and `staging-scrub` (S0, §9.3; R8-3) and core's `secrets set-install-id` (S3, when the keyring lands — a reset undone before S3 has no keyring header to restore; the manual reset undo, R8-1) | **permanent** | none | `cmd/*.go` `Use:` |
| 18 | CLI flags in shipped artifacts | `--host --port --db --config --dir --log-level --playlists --tls-cert --tls-key --http3-port --external-url --workers --read-timeout --write-timeout --idle-timeout` | same flags feed canonical keys | until next **major** (1.0) | release note at the major; not usage-gated | `cmd/root.go:486-535`; `Dockerfile:104` |
| 19 | Removed/no-op flags | `--db-type`, SQLite flag | — | W after S2 | deleted S10 | `cmd/root.go:489,508` |
| 20 | Fast-exit protocol | `--print-storage-format`, `--print-capabilities`, new `--print-storage-levels`, `--version-json` (`{app, version, commit, ui_build_id, binary_id}`, FE6-4) and `--print-upgrade-floor` (all three from S0) | unchanged | **permanent** | none (rollback and stepping-stone protocol) | `main.go:40,52`; `Makefile:892` |
| 21 | Child-mode sentinel/env | `--operation-runner`, `registry.EnvChild*` | unchanged | none (same binary) | n/a | `cmd/child_mode.go:53-96` |
| 22 | Other binaries in `cmd/` | `itl-*`, `mtls-bridge`, `pebble-inject-skip`, `pid-census`, `verify-suite` | stay ABO | permanent (books tools) | n/a | `cmd/` |
| 23 | Metric names, books | 73 `audiobook_organizer_*` families | unchanged | **permanent** (SIMP-13) | none | `internal/telemetry/contract/testdata/series.golden` |
| 24 | Metric names, core subsystems | Pebble (S2b), ops (S5), AI (S7), two process gauges (S8) | `mediacore_*` (05-metrics §13) | W from each alias's own release, gate P6 (OPS5-6) | gather-time alias → removed by PR (C8) | `metrics.go:15-300`; `opsmetrics.go:12-16` |
| 25 | Pebble identity families | `u: role: apikey: inviteallow: oauthid:` + their `idx:` sub-prefixes (read and written in place); `invite:`, `sess:` (read and deleted) | served by books' legacy identity stores | stored format until the identity cutover (§9.13) | none in the extraction | `keyfamilies.go:63-71`; 05-authn §11 |
| 26 | Pebble books families | the rest, incl. `setting:`, `preference:`, `pref:`, `act:` | books claims | **permanent** (02 #13) | none | 05-storage §5S.9.1 |
| 27 | Pebble ops families and clocks | `opv2:`, `opv3:`, `op:`, settings clock keys, `Requires` stamps | read and written in place by books' `internal/books/taskstore` | stored format until the tasks cutover (§9.13); history ages out | 05-tasks retention | 05-tasks §5T.6 |
| 28 | Task kinds (op def IDs) | 233 | `books.<id>`, `core.<id>` | input W after S5; stored-row resolution **permanent** | `FormerKinds`/`LegacyDefID` + persisted usage on `task_kind` (C5) | `aliases.go:16-52` |
| 29 | Webhook headers and event names | 2 headers, 12 event names | `books.*.v1` topics, core notify headers | per instance: `legacy_headers` is instance data, kept while set | operator clears it per instance | 05-notify §10; `webhook/plugin.go:101-126` |
| 30 | Backup archives | tar(+codec) of DB dir | 05-backup format (ABO's legacy layout plus a manifest; unencrypted during the extraction) | read: **permanent for archives at or above the build's upgrade floor**; older archives through the named release (§9.2.2, DATA3-9) | 05-backup | `backup.go:283-860` |
| 31 | SPA routes and the invite link | 14 module paths + 5 absorbed pages (`/dashboard`, `/files`, `/diagnostics`, `/operations`, `/maintenance`) + `/invite/:token` | `/books/...`, shell pages, `/invite#token=` | module paths and pages: W after S9; `/invite/{token}` 302: S9 + max(W, invite TTL) (FE3-18, FE4-2) | client redirect → removed (C9); the 302 route deleted at the end of its window | `App.tsx:204,253-448` |
| 32 | Wire shapes of aliased routes | hand-written TS interfaces | generated | = route window | `LegacyPath.Shape` goldens (FE3-6) | `raw/13` §8 |
| 33 | Upgrade floor | binary-carried from the floor release (first T8 release, `--print-upgrade-floor`), naming the stepping-stone release; also the signed `min_upgrade_from` from S8 (OPS7-10) | — | per release | install the stepping-stone release and let the store apply it first (`stepping_stone_required`, `ErrUpgradeFloor`) | §9.2.2; 02 N14 |
| 34 | Temp-login link `/auth/temp-login?token=` | the admin reset and temp-login link | `/login/temp#token=` | S4 + max(W, temp-login TTL) (FE3-18) | 302 route deleted at the end of its window | `internal/server/auth_temp_login.go:156-224`; `auth_reset_password.go:97` |

## 9.7 Production-data unknowns (read-only commands, both branches designed)

Run these against production before the stage that needs them; paste counts only, never values.
`<abo-url>`, `<redacted-token>` (an admin API key from `server-bootstrap`), `<unit>`, `<scratch>`,
`<host-a>`, `<bin>`, `<new-bin>`, `<db>` are placeholders. Offline commands run on a **restored backup
copy** so production is never stopped. Never print `sess:` or secret values (they hold credentials);
count keys only.

| ID | Question | Command | Branch A | Branch B |
|---|---|---|---|---|
| P1 | Key counts/bytes per family (S2b, S4) | `curl -sS -H "Authorization: Bearer <redacted-token>" "<abo-url>/api/v1/diagnostics/db-census?fresh=true" \| jq '.data.families[] \| select(.prefix \| test("^(u\|role\|apikey\|invite\|inviteallow\|oauthid\|sess\|idx\|setting\|opv2\|opv3\|act):")) \| {prefix,keys,raw_key_bytes,raw_value_bytes}'` (estimated, read-only, `handlers/diagnostics.go:1195-1220`); at S4 r1 also the P4d decode census through books' legacy stores | every row decodes: flip | undecodable rows: listed by key, fixed or restricted before r2 (§9.2.3 rule 3) |
| P2 | Custom (non-seed) roles exist? | `audiobook-organizer diagnostics query --raw --db <scratch>/audiobooks.pebble --prefix role: --limit 1000 \| grep -c '"is_seed":true'` vs total `^Key:` | none: only seed roles are translated | some: each translated through 05-rbac §6.1; an atom outside it grants nothing and the role reports Degraded naming it (§9.2.3 rule 3; SEC4-13); the owner maps it before r2 |
| P3 | API keys with empty scopes (run before S4 r2, when no row has an extension record; from S4 the count covers rows whose extension record is absent or `scopes_source: legacy` — 05-authn §4.4's one predicate, which the S4 r1 decode census reports (SEC6-2, SEC7-9)) | same with `--prefix apikey:` and `grep -c '"scopes":\[\]'` | none: no `LegacyFullScope` rows | some: read as `LegacyFullScope` and kept as written; the native API refuses new empty-scope keys and never writes an empty legacy list |
| P4 | Env names actually set | `systemctl show <unit> -p Environment \| tr ' ' '\n' \| cut -d= -f1`; `systemctl cat <unit> \| grep -o 'Environment="[A-Z_0-9]*'`; Docker: `docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' <container> \| cut -d= -f1`; launchd: `plutil -extract EnvironmentVariables xml1 -o - <plist> \| grep -o '<key>[A-Z_0-9]*'`; compose: `docker compose config \| grep -oE '^\s+[A-Z_0-9]+:'` | only bound or permanent names: alias table suffices | an implicit name in use: add an explicit alias row (and a permanent one if a deploy file sets it) before S3 |
| P5 | Invite TTLs outstanding; external API callers | `... --prefix invite: ... \| grep -o '"expires_at":"[^"]*"' \| sort \| tail -1`; callers: `journalctl -u <unit> --since -30d -o cat \| grep -oE '(GET\|POST\|PUT\|PATCH\|DELETE) /api/[a-z0-9/_-]*' \| sed -E 's#/[0-9A-Za-z]{20,}#/:id#g' \| sort \| uniq -c \| sort -rn \| head -50` | only the SPA calls `/api/v1` core routes: W floor applies | scripts/clients found: list them in the ledger; the window stays open until P8 shows no use |
| P6 | Metric consumers | operator's Grafana/Prometheus: `grep -rhoE '(audiobook_organizer\|ai_dispatch\|ai)_[a-z_]+' <dashboards-and-rules-dir> \| sort -u` | only repo templates: updated in S8 | private dashboards on aliased core names: C8 waits until they move |
| P7 | ABS surface in use | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.abs_api_enabled'`; `... --prefix abs_sess: ... \| grep -c '^Key:'` | off: conformance fixtures suffice | on with sessions: fixtures **plus** live login and progress sync with a real ABS client, before and after rollback, gate S2, S4, S6 and S9 (§9.3) |
| P8 | Legacy use (removal gate, reference store) | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/core/v1/compat/usage \| jq '.[] \| select(.gate_met == false) \| {surface,legacy,count,gate_reason,last_used_at,last_seen_set_boot}'` (OPS3-18) | empty for the contract's surfaces: contract may run | entries: window extends; `legacy` names the caller to fix, `gate_reason` why |
| P9 | Webhooks configured | `curl … /api/v1/config \| jq '.plugins.webhook // empty \| keys'` | none: S7 serves no webhook instance | some: one instance per URL with `legacy_headers` |
| P10 | Rollback inventory | `ssh <host-a> '<bin>.prev --print-storage-levels \|\| <bin>.prev --print-storage-format'`; `ssh <host-a> '<bin>.prev --version-json'` and `ssh <host-a> '<bin> --version-json'` (S0 on; no route serves `/api/v1/system/version`, OPS4-14) | `.prev` ≥ S0: it gives the R-test's `PREV_REF` for the next flip, committed as `ci/prev_ref.txt` after each reference-host deploy (OPS7-9), in this order: `commit` with a trailing `+dirty` stripped (recorded in the rollback inventory as "dirty: the rehearsal builds the committed tree, not the deployed one"); else the `g<hex>` part of a describe-form `version`; else a manual `PREV_REF` — never `version=dev` (a Docker build) or an unresolvable ref, which fail the R-test loudly instead of defaulting (FE6-4, FE7-2; `scripts/tests/` covers the `+dirty` and `version=dev` cases) | `.prev` older than S0: `make rollback` uses `--print-storage-format`, which the projection keeps at 1, so it still passes; deploy S0 once so later rehearsals have a `--version-json` |
| P11 | (withdrawn in round 3: the copy-and-mark over-cap preview served an adoption that no longer exists, R3-1) | — | — | — |
| P12 | Disk headroom | `df -B1 --output=avail <db-dir-parent>` vs `du -sb <db-dir>` | free ≥ store size: checkpoints fit | less: free space before any boot that runs a migration step |
| P13 | Stepping stone (before deploying the floor release) | `ssh <host-a> 'sudo -n cat <db>.storage-applied'` beside `<new-bin> --print-upgrade-floor` (OPS3-7) | the store meets the floor (books ≥ 66): deploy | below: install and run the stepping-stone release first; the S0+ updater and the `deploy` recipe refuse the same way, from the binary's own floor (OPS6-1) |

## 9.8 Error semantics (compatibility machinery)

| Error path | Behaviour | Why |
|---|---|---|
| Store required level > build level | **fail-closed**: refuse to open, name both levels and the release to install | an old build on a store a real format change raised would misread it (not reached in S0–S10, §9.2.2) |
| Store below the release's `min_upgrade_from` | **fail-closed** (`ErrUpgradeFloor{InstallFirst}`, exit 78 — 75 while an S8+ updater's pending marker names the running binary, so `Boot` can roll back, 05-updater §5U.4 —, last stderr line `install <release> first`): refuse to start before any write | the legacy migrations it needs are gone (R2-4, OPS3-21) |
| Updater (S0 on: the new binary's `--print-upgrade-floor`; S8 on: also the signed `min_upgrade_from`) or `deploy` recipe: store does not meet a non-empty floor, or the floor is non-empty and `<db>.storage-applied` cannot be read | **closed**: apply refused, `stepping_stone_required` ("boot any release ≥ S0 once" when the sidecar is missing; OPS6-1, OPS7-10); an **empty** floor passes without reading the sidecar | never apply a release that would only refuse the store at boot (OPS3-7); a HEAD store has no sidecar, and the S0 binary's floor is empty |
| Restore staging of an archive below the build's upgrade floor | **closed**: refused before extracting, `ErrUpgradeFloor{InstallFirst}` | a fresh host must not end with an empty store (DATA3-9) |
| Migration step error (books legacy or versioned, core versioned) | **fail-closed**: boot halts, checkpoint kept unconsumed for the offline restore | G2 |
| Build whose store backend differs from the claim ledger's `backends` record | **fail-closed** boot (`ErrLegacyStoreMissing`, `ProblemLegacyStore`) | a subsystem must not switch silently between books' legacy store and its native keyspace (05-storage; R4-1) |
| ABO code writes a store-interface family outside books' legacy store | **refused in CI** (`TestLegacyFamiliesWrittenOnlyByLegacyStores` and its instances); `set-internal-flag` refuses those keys at runtime | one write path per family (§9.2.3 rule 4) |
| Extension record whose legacy row an older build changed | **closed**: a Widening extension is suspended until an admin re-confirms it; Derived fields reset (DATA6-14) | no silent widening on roll-forward (R4-1) |
| Older build reads a moved plaintext secret | reads the sentinel; its client **fails closed** (the Basic password and the other HEAD-encrypted secrets are never moved, R5-1(b)) | no value an older build reads may relax a check when blank (R4-2) |
| Core Basic gate enabled with an unset or empty password | **closed**: 401 on every non-exempt route, health `basic_gate_no_password` (OPS6-6) | HEAD's comparison would admit `<username>:` (R5-1(c), SEC5-4) |
| Legacy step whose method is missing on the store type | **fail** in CI (`TestLegacyStepPreconditionsResolve`); there is no "nothing to do" path for a production store | a skipped step recorded as applied never runs again (R5-2, MOD5-1) |
| Legacy row a legacy store cannot decode | **closed per principal**: the row is treated as restricted (user inactive, key refused), Degraded naming the key, counted; boot continues | one bad row must not crash-loop the unit (OPS-19) |
| New API key with empty or wildcard scopes | **refused** (400) | an empty list means "all role permissions" to every build (SEC-1) |
| Seed-role edit during the extraction | **refused** (`ErrSeedRoleLocked`) | older builds rewrite seed roles at boot (§9.2.3 rule 2) |
| Stored role holds an unknown atom | **closed per role**: the atom grants nothing, Degraded naming the role; boot continues | §9.2.3 rule 3, P2 branch B (SEC4-13) |
| Legacy alias request | served (**open**), recorded, headers | compatibility is the point |
| Legacy request field an alias cannot map | **refused** 400 `legacy_field_unsupported` | a dropped selector retargets the action to the caller (SEC2-5) |
| Legacy key in YAML after W | **fail-closed** refuse to start, names canonical | silently ignoring `root_dir` would point the library at nothing |
| Retired env name of a `ProtectAdmin`+ or secret-class key after W | **fail-closed** refuse to start, names canonical | ignoring `DATABASE_PATH` would open or create the wrong store (`config.go:2519-2544`); an ignored secret alias would disable a feature (`ABS_JWT_SECRET`, OPS2-7) |
| Legacy and canonical name both set with different values, `ProtectAdmin`+ key | **fail-closed**, names both | a base-unit canonical twin would shadow an operator's drop-in (OPS2-12) |
| Other legacy env name after W | ignored + one WARN (**open**) | env carries unrelated names (`HOST`, `PORT`) |
| `BelowStored` legacy name of a `ProtectAdmin`+ key set and differing from the stored value | stored value wins + WARN, health `config_legacy_name_shadowed` naming the canonical variable (**open**); an operator value that enables auth wins instead (R6-6) | HEAD's precedence, made visible (SEC6-1) |
| Progress tick against a run whose state changed since it was read; ledger commit or row mark against a run that is terminal or under another `Attempt`/`FenceEpoch` | the write is dropped (`ErrRunConflict` on a terminal run), never retried blindly (**closed**); a commit to a `stopping` run under the same attempt is accepted, as at HEAD | a tick must never revert a cancel (R6-1, R7-3) |
| Same key in legacy and canonical form, other keys | canonical wins + WARN (**open**) | deterministic, visible |
| Usage cannot be recorded | ignored (**open**); the flush retries | telemetry must not fail requests |
| Checkpoint without disk headroom | **fail-closed** before the migration | a full pool is a known outage (`config.go:2521-2528`) |
| Staged restore intent stale or from another build | rejected, moved aside, Degraded (**closed**) | a late restore would discard writes made in between (OPS-14) |
| Rollback, deploy or updater apply while the swap slot holds a restore or a reset, or a swap is in flight, or the probe cannot tell | **refused** (`restore_pending`, `restore_inflight`, naming the kind; or the probe or the missing sudoers line); `ROLLBACK_IGNORE_RESTORE=1` overrides `restore_pending` only, never `restore_inflight`, and the updater has no override | a swap must not discard or half-apply a restore or an audited reset, or land an older build on a half-moved store (OPS2-26, OPS3-13, R8-1, OPS8-7) |
| Swap intent (restore or reset) that expired, was staged by another build or whose recorded Pebble open stamp moved; a second staging while the slot is taken | **rejected** (closed), store and live settings untouched; a second staging gets `storage.ErrSwapPending` | one rejection rule for every kind; another binary opened the store in between; the slot is created atomically, so a restore and a reset never race (R7-2, R8-1, SEC7-5, DATA7-17) |
| Reset staging with a dangerous `RootDir` | **refused** (400), nothing staged | HEAD refused only after wiping the store (OPS8-5, COR8-4) |
| Reset set-aside rename that fails at P0a (another filesystem, a permission) | moves back what it moved, journals `rejected`, boots the untouched store `degraded` (`factory_reset_failed`) (**closed**) | a reset never leaves a half-moved store (COR8-3, DATA8-1) |
| Post-reset hook cannot delete an entry | logged, audited, health `factory_reset_cleanup_incomplete`; the hook still records `hook_done` (**open**) | HEAD warned and continued; a hook that never finishes would block every swap (COR8-4, OPS8-5) |
| Backup without a manifest (old archive) | restore proceeds as legacy (**open**) | read support is permanent |

## 9.9 Concurrency contract

Books legacy migrations and core versioned steps run in the kernel's migration phase (06 §6.5.1
`P4a`–`P4d`), before listeners and the executor start (03 §3.7), and nothing writes the store before the
pre-migration checkpoint (`P4a`). A library-scale books rewrite is a `DataMigration` with its own
`tasks.Parallelism` (05-storage §5S.3.6), never a sequential loop. Books' legacy store implementations write in the
caller's batch (participant binding); their correctness comes from 05-storage's `Expect` preconditions under its commit
stripes, and a `Serialized` family (the blob row, the identity rows, the run rows) is read with
`GetForUpdate`, not guarded by a store mutex (DATA2-29). **Every** write derived from such a read is checked
at commit (R6-1, DATA6-1), per guard row for the run store (R7-3, §9.2.3): progress ticks, chunk-ledger commits,
checkpoints and row marks ride in a batch carrying the run guard's `Expect`, a conflicting tick is dropped, not
retried blindly, and `Transition`/`Insert` return `ErrRunConflict` at staging when their read no longer matches `want`, while the caller that builds the batch re-runs build-and-commit under `storage.RetryOnConflict`, re-staging every participant (05-tasks 5T.7; COR8-7); books' API-key last-use write
stages `Expect` on the row it read and drops itself on a conflict, so it can never undo a revocation (R6-5).
At HEAD, until early fix 6 ships, `apiKeyMu` must cover every `apikey:` writer (§9.1). Alias handlers inherit the canonical handler's concurrency.
`compat.Handle.Use` is lock-free on the request path (atomic counters); the flusher is one goroutine per
App, owned by the kernel lifecycle. All migration steps take `ctx` and honour cancellation between
batches.

## 9.10 Config keys, metrics, tests

Config keys this file adds: none beyond the operational dials of §9.2.8, which their owning specs
define; the reader level is compiled into the build.

| Metric | Type | Labels |
|---|---|---|
| `mediacore_compat_legacy_use_total` | counter | `surface`, `name` (bounded: ledger entry), `entry` — dashboards only |
| `mediacore_compat_legacy_last_use_timestamp_seconds` | gauge | `surface`, `name` — from the persisted record |
| `mediacore_storage_level` (05-storage §5S.8; the only level metric) | gauge | `owner`, `kind` (supported, store) |
| `mediacore_build_info` | gauge | `version` |
| `mediacore_kernel_boot_phase_seconds` (06 §6.9) | gauge, per boot | `phase` and `step` exactly as 06 §6.9 defines them (ASCII phase values; OPS3-16) |

| Test | Proves |
|---|---|
| `TestLegacyToleratesNamespacedKeys` (S0) | HEAD-era code paths ignore `c/`, `m/` keys |
| `TestS0LedgerKeysRoundTrip` (S0), `TestS2bOpensStoreWrittenByS0` (S2b, on the S0-written fixture store) | the install marker precedes every `c/` key and S0 keys round-trip through `storage/keys`; S2b reads S0–S2 ledger history (OPS6-11) |
| `TestNativePrefixesNotRedirected` (S0) | `/api/core/vN/`, `/api/<module>/vN/` bypass the 301 |
| `TestCompatGuard*` + `scripts/test_storage_format_guard.py` | open refusal, rollback refusal, sidecar repair after a commit error, both values printed on disagreement, the restore probe and its refusal when it cannot tell |
| `TestUpgradeFloor_RefusedBelowBooks66`, `TestApply_SteppingStoneRequired`, `TestRestoreBelowUpgradeFloorRefusedAtStaging` | the store-based stepping stone at boot, in the updater and at restore staging (OPS3-7, DATA3-9) |
| `TestLegacyFamiliesWrittenOnlyByLegacyStores` (ABO) and its instances (05-tasks' `TestOpsFamiliesWrittenOnlyByTaskstore`) | one write path per store-interface family (§9.2.1) |
| `TestCompatLedgerCopyFresh`, `TestBooksMinCoreVersion`, `TestStepSteppingStoneValid` | the embedded ledger copy equals `docs/compat-surfaces.md`; books' `MinCoreVersion` ≤ core's version; the stepping-stone tag is valid (R4-4, MOD4-13, OPS4-5) |
| `TestLegacyStoreSuites` (books' legacy stores run core's conformance suites `<pkg>test.<Interface>Suite` with HEAD fixtures; the one ABO umbrella name, SIMP5-9) | each legacy implementation satisfies its store interface (03 §3.3) |
| `TestLegacyStepPreconditionsResolve` (in `internal/books`); 04's gate E (stage-visibility gate), test half included | no legacy step can be skipped; taskstore → store only, no import cycle among ABO packages, every split test file compiles where it lands (R5-2, MOD6-2) |
| `TestUsageGateRestartsAfterUnrecordedInterval`, `TestS2Manifest_SPermRoutesCarryLegacyAtom`, `TestFactoryResetUnderClaims`, `TestAuthOn_LegacyAliasEnablesOverStoredOff`, `scripts/tests/test_staging_scrub.py`, `scripts/tests/test_staging_egress.sh` | W restarts at `detected` (DATA6-7); every S2 `s.perm` route carries `rbac.LegacyAtom` (COR6-1); a core build's factory reset under claims (DATA6-6); enabling auth through an operator layer beats a stored `false` (R6-6); a staging copy is inert at every boot and every stage, and its egress is denied while ssh and the API port answer (R6-7, R7-6) |
| `TestRollback_OperatorEnabledAuthStaysOn`, `TestFirstR2BootKeepsLegacySessions`, `TestFactoryResetFromLegacyRoute_S2b`, `TestFactoryResetRejectedLeavesStoreAndSettings`, `TestFactoryResetIntent_RejectedAfterInterveningOpen`, `TestCheckReaderRefusesPendingReset`, `TestFreshInstallPostFloorBootsTwice`, `TestFreshInstallWritesHEADMarkers`, `TestOfflineOpenIsNotForeign`, `TestCLISubcommandsUseOfflineOpen`, `TestS8_FullAliasSetBoots`, `TestSystemResetStagesStoreOnlyReset_S2b`, `TestSwapSlotExclusive`, `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`, `TestOfflineOpenAfterForeignOpenKeepsEvidence`, `TestS5_FullAliasSetBoots`, `TestScheduleParity_AllDefinitions`, `TestOperatorNames_NamesOnly` | operator-enabled auth and Basic gate survive a rollback (R7-1); the S4 r2 upgrade signs no one out (R7-5); both resets stage only, as kinds of the one swap, from the legacy routes, and a rejected intent changes nothing (R7-2, R8-1, COR8-1); the reset boot keeps auth on under a stale `ENABLE_AUTH=false` (SEC8-5); an offline open never launders a foreign open (R8-2); the S5 alias set boots and the 38 schedules keep HEAD's parity (OPS8-1, OPS8-4); `operator-names.json` holds names only (R8-4); a floor build boots its fresh store twice and writes HEAD's markers (DATA7-7, DATA7-13); offline opens are not foreign (R7-4); the S8 alias set boots (OPS7-5) |
| `TestComposeEnvKeepsUIBasicGate`, `TestBasicGate_EmptyPasswordRefuses`, `TestLegacyMetricsRoute_RequiresScopeFromS4`, `TestLegacySPA_InviteFragmentReader`, `TestStateDir_OneResolverYAMLDBPath`, `TestLocalStepSkippedStoreGrantsNothing`, `TestLocalStepAfterOldArchiveRestore` | HEAD precedence on compose (OPS5-1); fail-closed Basic gate (R5-1(c)); `/metrics` scope from S4 r2 (OPS5-5); invite reader before writer (FE5-4); YAML database path (OPS5-13); `Local` never granted unconfirmed (DATA5-9) |
| `TestOneFormat_LegacyRowsReadByPrevBuild` (05-authn) and the 05-storage / 05-config / 05-tasks one-format tests, in `extraction-gates` on every PR | the previous release's real binary reads what this build wrote (R3-1, OPS4-6) |
| `TestLegacyRoleReachUnchanged` (permanent CI gate) | §9.2.6 invariant and its recorded changes |
| `TestEveryLegacyConfigKeyHasCanonical`, `TestShippedDeployEnvNamesArePermanent`, `TestAliasConflictOnProtectedKeyRefusesBoot`, `TestPerimeterHonoursBlobOnlyValues`, `TestStateDirFallbackMatchesHEAD` | config/env completeness, conflicts, blob-only values, the state-directory fallback |
| `TestCompatLedgerMatchesCode`, `TestNoContractBeforeSunset` | the ledger is the code |
| `TestAliasHeadersAndShape`, `TestAlias_UnknownLegacyFieldRefused` | headers, legacy-shape goldens, strict legacy fields |
| **R-test (rollback rehearsal), per stage**, `make compat-rehearsal` | in CI: build `prev` from `PREV_REF` in a two-row matrix — the latest production release tag, and the committed `ci/prev_ref.txt`, which P10's output updates after each reference-host deploy, because deploys build main and `.prev` is usually not a tag (OPS2-25, OPS7-9); a `PREV_REF` that does not resolve fails the job loudly instead of defaulting (FE7-2) — and `cur`; boot `cur` on a seeded store, write through every surface the stage touches, stop; boot `prev` on the same store, assert it reads every row in its one format and that the costs and consequences are exactly the rows of §9.2.1's table, rows 16–18 included (one re-login, plaintext secrets written on `cur` read as their sentinel and refused, `mck_` keys refused, temp-login links issued on `cur` void, system-log entries written on `cur` not shown, natively created integrations inert, …), write (password reset, deactivate, key revoke, config edit, secret change and clear, enqueue), stop; boot `cur`, assert `prev`'s writes are effective. The **DATA-6 cases** are explicit rows: a run enqueued, re-queued or interrupted on `prev` is seen and run once on `cur`; a schedule clock advanced on `prev` neither double-fires nor stampedes; a `Requires` stamp written on `cur` is honoured on `prev`; a config value changed or cleared on `prev` is effective on `cur`; a secret re-entered on `prev` wins on `cur`; an integration URL changed on `prev` is effective on `cur` and suspends its `Local`; a key rotated on `cur` is not resurrected by an unrelated save on `prev`; native sessions and one-time tokens from before a rollback are revoked on roll-forward, also when the first roll-forward boot fails after the served-ledger step and a second boot follows (R5-5), and also after a pre-S0 `prev` opened the store twice (SEC5-5); a Basic password changed on either build is effective on the other (R5-1(b)); auth enabled through `ENABLE_AUTH=true` over a stored `false` stays required after a rollback to S4 r1 and S3, and a Basic gate enabled through its canonical name stays on after a rollback to S2 (`TestRollback_OperatorEnabledAuthStaysOn`, R7-1); an invite minted on S9 opens on S8 (FE5-4); an archive written on S3+ restored on an older build has rows 1–5's costs (DATA5-19); an archive written on `prev` (pre-S8) and restored on `cur` (S4 r2+): every API key reads `needs_confirmation` and legacy sessions are deleted (row 20; OPS8-14); a Basic gate whose password is set only canonically or only through `BASIC_AUTH_PASSWORD` stays enabled on `prev` (SEC8-1); a canonical-only operator value is reported as `canonical_only_operator_values` before the rollback (`TestRollback_CanonicalOnlyValuesReported`, `TestOperatorNames_NamesOnly`; row 21, SEC8-6); a URL a non-admin changed on a legacy-backed instance with an env secret never carries that secret on `prev` (`TestRollback_NonAdminURLNeverCarriesEnvSecretOnPrev`; SEC8-3); a factory reset staged on `cur` with `ENABLE_AUTH=false` in the environment boots with auth on (`TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`; SEC8-5). Matrix cells (OPS-20): **(a)** the previous release's UI bundle against `cur` (old tabs during W); **(b)** `cur`'s bundle against `prev` after rollback (new tabs; the stale-build rule offers a reload, FE3-3, OPS3-11); **(c)** ABS conformance against `prev` and `cur`; **(d)** the updater's `Boot` rollback (and `rollback_blocked` when `.prev` cannot read the store); **(e)** restore across builds (archive from `prev` onto `cur`, from `cur` onto `prev`, a staged intent across a rollback, a revocation made after a restore surviving a restart and a rollback, and §9.2.1 row 20's `needs_confirmation` outcome; OPS8-14). Cells a/b are mandatory in every release that moves a UI call, cell c in S2, S4, S6 and S9 (§9.3). The job runs nightly and in every stage's done-check; a red row in the nightly run fails the next flip PR and every state-writing PR through `extraction-gates` (§9.2.1; OPS7-6, OPS7-9). |

## 9.11 Risk table

| Stage | Risk | L | I | Detection | Mitigation |
|---|---|---|---|---|---|
| S0 | Redirect exclusion too broad: an unversioned client path `/api/x/v2/...` stops redirecting | L | M | `TestCollidingNamespacesStillRedirect`; P5 | exclusion requires `/vN/` and a module-ID shape |
| S0 | A build path missed by the entry-point sweep breaks once `core/web` exists | M | M | the image job on every non-doc PR; UI-0 done-check | one script, every site listed in §9.1 |
| S0 | A gate runs only after the code shipped (ungated merges, auto-promoted minors, admin merges) | H | H | 14 K20 | every PR passes `make ci` with the extraction gates (R3-2); enforcement 14 Q-I |
| S0–S7 | The HEAD updater applies a release unattended with no backup or rollback | M | H | Degraded `auto_update_paused_until_core_updater` | both unattended paths check-only until S8, semver and reader checks on manual apply (OPS2-5, OPS3-12) |
| S1 | Moving telemetry changes metric/label output or drops series that read the globals | M | M | S1 name diff | construction only; global installs stay in the ABO façade until S2 (MOD3-3) |
| S2 | Perimeter move changes CSP, compression or rate classes | M | H | S2 perimeter tests | manifest-driven per-route data copied from HEAD; per-request CSP from the mount; goldens; Basic gate stays in gin until S3 |
| S2 | UI-saved perimeter values ignored by core | M | H | `TestPerimeterHonoursBlobOnlyValues` (S2 and S2b) | books' legacy config store at every stage, its `Changed` signal until S3 (COR3-1, COR4-2) |
| S2 | Listener move changes TLS/H3/peer-allowlist behaviour | M | H | smoke on `--http3-port`, peer test from a disallowed address | move code verbatim first; `peer_allowlist` tests move with it |
| S2 | Supervisor kills the process or its runner children mid-shutdown | M | M | journal SIGKILL check, subprocess-mode stop check | stop timeouts and `KillMode=mixed` shipped in S2; budget below them |
| S2 | Docker/launchd keyring in the ephemeral layer or on an anonymous volume | L | H | image job keyring assertion, both runs (`/data`, `--db /config/…`) | HEAD's state-dir fallback kept; no shipped file sets the state-directory variable (R3-4, OPS4-2) |
| S2b | Settings load or encryption init skipped, defaults written over the blob | L | H | `TestS2bAppConfigFromBlob` | the steps run inside books' legacy config store over the one handle (R4-6) |
| S2b | Strict claims refuse a live books writer | M | H | shadow release with persisted violations | shadow ≥ 14 days; dial back to shadow |
| S3 | Rollback leaves plaintext secrets written on S3+ unusable on the older build | H | M | R-test | sentinel, never empty: the older build fails closed (R4-2); stated cost; release note; re-enter there; the Basic password and the HEAD-encrypted keys are never moved, so the gate never locks the older build out (R5-1(b); `TestRollback_BasicGateNeverOpenOnPrev`) |
| S3 | Compose's always-set `BASIC_AUTH_ENABLED=false` switches off a Basic gate enabled in the UI | H | H | compose fixture of the S3 golden; `TestComposeEnvKeepsUIBasicGate` | HEAD's precedence kept for legacy names (`Alias.BelowStored`, R5-1(a)); compose passes the variables through only when set |
| S5 | A legacy step is skipped and recorded as applied once its method leaves `*PebbleStore` | M | H | `TestLegacyStepPreconditionsResolve`; 04 gate E | taskstore → store only; steps reached through wired funcs, direct calls (R5-2) |
| S3 | Keyring missing on restore to a new host | M | H | restore matrix | the 05-config keyring export (one export covers secrets and the derived backup identity, SIMP2-2) |
| S4 | Privilege change from atom expansion | M | H | `TestLegacyRoleReachUnchanged` | permanent gate |
| S4 | A row the new build wrote widens access on an older build | L | H | `TestOneFormat_LegacyRowsReadByPrevBuild`, `TestRowRuleDominatesLegacyReach`, `TestRowRule_ConfinedRoleStaysWorkerOnlyOnPrev` | legacy atoms only in legacy rows, scopes in books' extension family (R4-1, SEC4-3); row rule over the frozen snapshot; `mck_` keys never in a legacy row |
| S4 | A bounded custom role passes a legacy `settings.manage` route | M | H | custom-role half of `TestLegacyRoleReachUnchanged` | full legacy reach (`AllOf`) on legacy routes (R4-3) |
| S4 | Docker installs with auth off crash-loop | M | H | `TestS4_AuthOffDockerBoots` | ABO's `disabled_allow_remote` default `true`, Degraded banner (SEC4-8) |
| S4 | ABO code writes an identity row outside books' legacy store (`usermerge` undo, admin debug edit) | M | H | `TestLegacyFamiliesWrittenOnlyByLegacyStores` | rewired onto `auth` in S4 |
| S4 | Rollback signs everyone out; keys created on S4 refused on the older build | H | L | R-test | stated costs; release note |
| S4 | Session fallback lets a revoked legacy session live | L | H | test | fallback applies epoch, status, revoked and expiry checks and the cut-off (05-authn) |
| S5 | Runs of new kinds dropped on rollback | M | L | R-test | stated consequence; scheduled kinds re-run; `LegacyDefID` for every ported kind |
| S5 | Parallelism declarations change runtime behaviour | M | M | duration metrics per kind | carry today's `Concurrency` values; dial `core.tasks.kinds` |
| S5 | Two schedulers tick one kind | L | M | `TestOneSchedulerPerKind` | ABO's scheduler keeps a kind until T7 converts it (DATA4-5) |
| S5 | A skipped store below books `LegacyCap` hits the floor release | L | H | `ErrUpgradeFloor`; `stepping_stone_required` | store-based `min_upgrade_from`; `TestStepSteppingStoneValid` (§9.2.2) |
| S6 | Legacy stream projection drops events the UI relied on | M | L | per-type goldens | projections built from HEAD frames |
| S7 | Webhook receivers break | L | M | P9 | `legacy_headers` per instance |
| S7 | Instance-scoped throttles double outbound rate | M | M | provider 429 counts | one `providerhttp` instance per provider per App, asserted in kernel |
| S7 | A LAN host given by name loses reach | M | L | health "private host needs Local or lan_allow" | admin sets `Local` or `lan_allow`; release note |
| S7 | An unvetted private URL gets egress trust | L | H | `TestLocalStepSkippedStoreGrantsNothing` | the S7 migration only records `PendingLocal`; an admin confirms (DATA5-9) |
| S8 | Auto-update users stuck on S8 | M | H | staging install of signed RC *n* with `provision.sh --updater-check` (§9.3 step 5): RC *n+1* is applied and `Boot` confirms it (OPS7-3, OPS8-3) | signed-release precondition; `allow_unsigned_legacy`; service-owned binary directory checklist |
| S8 | The binary directory breaks `make rollback`'s unprivileged probes | M | H | dry-run `make rollback` as the deploy user | directory outside the 0700 state directory, 0755 (OPS4-4) |
| S8 | Updater rolls back onto a store it cannot read | L | H | R-test cell d | `ReaderCheck`; `rollback_blocked` |
| S9 | Bookmarks/old tabs hit removed SPA paths | H | L | `ui_route` usage | client redirects through W; reload prompt |
| C* | Contract merged before window | L | H | `TestNoContractBeforeSunset`; P8; `make ci` | ledger dates + persisted usage |

## 9.12 Open items

| # | Item | Owner |
|---|---|---|
| O1 | (closed by 05-config §1.12.2, R4-2) Plaintext secrets in `config_blob`: encrypted on first write by the new build, legacy copies sentinelled, re-entered after a rollback. | — |
| O2 | (closed by 05-rbac §6.1) Final `core:` permission names. | — |
| O3 | `.encryption_key` lives outside the archived DB dir (`settings.go:44-52`, `backup.go:283-428`): restore on a new host needs the 05-config keyring export (SIMP2-2). | 05-config, 05-backup |
| O4 | Env grammar and prefix (`ABO_`) | 05-config §1.3.6 |
| O5 | (closed by 05-storage §5S.9.1 and R4-1) Exact books claim list; no grants during the extraction. | — |
| O6 | (closed by 03 §3.3 `log` row, 05-logging §5) `act:` stays books; audit gets new rows only. | — |
| O7 | CLI flag window "until 1.0" deviates from the two-minor floor for surfaces baked into users' compose files | owner |
| O8 | Pilot cost before any books route group is rewritten to native `httpapi` (SIMP-2 item 4): measure one group (e.g. playlists, 10 routes) | owner, when a group is first rewritten |
| O9 | S0–S8 auto-update: unattended apply check-only (specified) or a minimal boot-counter rollback in the HEAD updater instead (OPS2-5) | owner |
| O10 | How the release-gating requirement is enforced (R3-2) | owner (14 Q-I) |

## 9.13 Deferred: per-family cutover

Moving a family from its legacy keys to a native core keyspace and deleting the legacy rows — identity
to `c/auth/`/`c/rbac/`, the settings blob to `c/config/`, the ops families to `c/tasks/` — is **not part
of S0–S10** (02 N15). Each is a later, per-family, **one-way** step, decided on measured data (key
counts, row sizes, the native features that need a different shape), run behind the mandatory
pre-migration checkpoint and the single per-owner level guard (05-storage §5S.3.7), which it raises so
that older builds are refused; its rollback floor is the cutover release itself, and only a restore of
the pre-cutover checkpoint or backup goes back. A cutover gets its own stage entry, its own
`min_upgrade_from` if it deletes code older stores need, and its own ledger rows; a cutover also brings the
storage grants (deferred-build members of 05-storage §5S.3.13), because it is the only step in which core
reads books' rows; the first one is also the level step past books' legacy restore bound with which
backup encryption is built and activated (05-backup B3, SIMP4-5). Until then books' legacy store
implementations of §9.2.3 stay, and the surfaces that depend on the stored format (§9.6 rows 11, 14, 25,
27) stay with them.

## Objections to 03

None open. 03 v1.9 carries everything this file relies on: one stored format per family with store
interfaces and books-supplied legacy implementations, no grants during the extraction (§3.3 "Store
interfaces", `storage`, R1(c), §3.7), the extension family with legacy-row hashes, the floor and
stepping-stone releases (§3.7), reserved compat namespaces (§3.6), the `compat` package with the
app-supplied ledger copy outside `docs/` (§3.3, R4-4), `.dockerignore` as it is (§3.2 item 4), the
composition root and helper package split (§3.5.1 item 1, R4-5) and the stage-visibility gate (§3.5.1
item 4), and from v1.9 the round-6 count (49 leave / 5 new / 169 remain, R6-2), the `legacy` package (R6-3), the
in-image embed list (MOD6-3), `tools/stagecheck` (MOD6-13) and (h)'s match rule, and from v1.11 the one restore swap with the
reset kinds `ResetStore`/`ResetFactory`, its one token pair and `storagehttp.Stager` (R8-1), `operator-names.json` (R8-4),
the struct-tag clause of (h) (MOD8-4) and the move rule that cites 04 §2.2's enumerated mechanical rewrites (R8-6).

## Out-of-scope defects found

1. Shipped deploy files set env names nothing reads: `AUDIOBOOK_ROOT_DIR` (`deploy/audiobook-organizer.service:97`,
   `deploy/com.audiobook-organizer.plist:38`; deliberately unread, `internal/config/config.go:2569-2575`),
   `AO_DIR` and `AO_DB` (`docker-compose.yml:18-19`; the Docker `CMD` supplies `--db`, `Dockerfile:104`),
   `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`). They mislead operators; S3 removes them.
2. `--db-type` help still says "pebble (default) or sqlite" (`cmd/root.go:489`) although SQLite is
   rejected (`internal/database/store.go:1840-1843`).
3. The HEAD updater exits 0 to restart (`internal/updater/updater.go:331-334`), so under the committed
   unit's `Restart=on-failure` every auto-update leaves the service stopped; it treats any version
   difference as an update (`:153`); S0 fixes both. It also cannot write next to the executable in the
   committed install layout and looks for a raw asset name the release may not publish
   (`updater.go:254-258,318-326`; `deploy/audiobook-organizer.service:41-43,88,122`).
4. A revoked API key can be re-activated: `PATCH /api/v1/auth/api-keys/:id {"status":"active"}`
   (`internal/server/handlers/apikeys.go:336-372`) reaches `SetAPIKeyStatus`, which sets `Status` without
   checking `RevokedAt` (`internal/database/pebble_store_auth.go:1226-1247`); filed as a live hotfix in
   `todo.d/2026-11-04_platform_core_followups.md`, shipped independently and early (05-authn §11.1).
5. The `deploy` template's post-check calls `/api/v1/system/version` (`Makefile.local.example:103`), which
   no route serves (`internal/server/wire_system_routes.go:28-48`), so its "deployed version matches"
   step can never pass; S0 moves it to `/health`'s `version` field (OPS4-14).

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- Cross-cutting 2 / SIMP-2 / OPS-11 / SEC-13 / FE-1 — no gin retirement (old S10 removed, old S11 → S10); `MountLegacy` permanent; S2 moves only the perimeter, S4 is the one auth-engine stage; SPA stays on gin until S9.
- Cross-cutting 5 / DATA-3 / DATA-4 / OPS-19 — identity: legacy authoritative, rebuild every boot, side records (05-storage mechanism); lossy "legacy wins" reconcile dropped; per-principal quarantine. (Superseded in round 3 by R3-1: one stored format, no rebuild or side records; per-principal fail-closed kept.)
- Cross-cutting 6 / OPS-2 — store-side records (adopt-step applied record, served ledger); contracts declare `Adopts` and `RequiresServed`, adopt first or defer; only a build without the adopt step refuses. (Superseded in round 3 by R3-1: no adopt steps or data contracts; the served ledger stays for W.)
- Cross-cutting 7 / DATA-1 — `setting:`/`preference:`/`pref:` stay books.
- Cross-cutting 8 / DATA-21 / OPS-14 / OPS-15 — backup format owned by 05-backup; window policy, staged-restore intent rules; staged restore pulled to S1; C3/C4 precondition. (C3/C4 precondition superseded in round 3: contracts no longer touch data.)
- Cross-cutting 9 / MOD-1 / OPS-1 / MOD-8 / MOD-9 / FE-13 — S0 Docker layers, `.dockerignore`, PR image job, one web build script for every `npm ci` site, Dependabot, CI classification table.
- Cross-cutting 10 / MOD-2 / MOD-6 — move lists referenced to 04 §8 (stage-keyed); S1 list reconciled; `sdnotify`/`startup` move at S2. (`startup` refined in round 3 by MOD3-1: S8.)
- Cross-cutting 11 / MOD-3 — genproto exclude only in `core/go.mod`.
- SIMP-3 — config and logger façades kept; removal optional ratchets, not stages.
- SIMP-4 — integration settings to the one instance store (S7), webhooks as notify instances (S6). (Webhooks superseded in round 2 by SIMP2-4: S7; the store's legacy instances read in place since round 3, R3-1.)
- SIMP-6 — schedule keys via `ScheduleFromConfig` (S5).
- SIMP-7 / OPS-10 / OPS-16 — one window in §9.2.1; no clock-based removal; persisted usage record; `compat.Ledger` defined (§9.2.9).
- SIMP-13 — books metric names unchanged, emitted once; core renames via gather-time aliases, gate P6.
- SIMP-15 / SEC-20 / OPS-9 / OPS-3 — S3 references 05-config §1.12.2 (T in S0, X in S3, C3); reader-capability levels. (Phases T/X and C3's data step superseded in round 3 by R3-1: the legacy-format adapter.)
- SIMP-18 / OPS-8 / DATA-7 — one per-owner level guard (05-storage §5S.3.7), policy here; contracts are `Breaking` steps with `RaisesLevel`; legacy restore bound `core` 4, lifted by C8. (Contract levels superseded in round 3 by R3-1; the restore bound is now `{core, 0}`, so archives stay unencrypted during the extraction.)
- SEC-1 / OPS-4 — exact-or-disabled key mirror; mirror-disabled keys survive the rebuild (`MirrorDisabled` + `Fresh`). (Superseded in round 3 by R3-1: never-empty namespaced scopes, `mck_` keys.)
- SEC-19 — no raw session mirror; one re-login on rollback.
- OPS-5 — permanent env aliases for read names in shipped deploy files; retired DB-path name refuses to start; unread names are deploy-file defects.
- OPS-6 — S0 updater exit-code hotfix and `.prev` slot; S8 `ReaderCheck`.
- OPS-7 — 05-tasks' model referenced; S5 level 4 and C5; S0 op-ID teach dropped. (Level and C5 superseded in round 3 by R3-1.)
- OPS-12 — S2b storage stage; §9.4.1 batch → stage map incl. 05-tasks T0–T8.
- OPS-13 — S2 stop sequence, one budget, supervisor stop timeouts.
- OPS-17 — operational dials listed (§9.2.8).
- OPS-18 — one flip PR per stage + ratchet.
- OPS-20 / OPS-21 — R-test with last production tag, UI and ABS cells at S2/S4/S6/S9; `X-App-Build` teach in S0.
- OPS-22 — boot budget per stage, `DEPLOY_READY_TIMEOUT`, P11, boot-phase metric. (P11 withdrawn and the budget redefined in round 3.)
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

- R2-1 / SEC2-1 / SEC2-2 / DATA2-3 / DATA2-5 — Teach build per adopt/expand sequence (§9.2.1, §9.2.2 table): legacy-side guards, journal, rollback floor via adopt-step `Floor`; identity Teach build = S4 r1 (05-authn §11.1); levels renumbered (S4 r1 3, S4 r3 4, S5 5, S8 6); live hotfixes may ship from S0. (Superseded in round 3 by R3-1: no Teach builds, floors or renumbering; hotfixes ship independently.)
- R2-2 / SEC2-3 / DATA2-4 — revocations, including post-restore re-application, reach the legacy row in the same batch (§9.2.3, S8, R-test cell e). (Kept in substance: the legacy row is now the only row.)
- R2-3 / DATA2-1 — per-stage ownership view referencing 05-storage §5S.9.1; claim-mode changes are stamped level steps; S2b claim test per stage. (Superseded in round 3 by R3-1: no claim changes; grants per stage.)
- R2-4 / DATA2-2 / OPS2-11 — minimum-upgrade-from release (02 N14) replaces "no stepping stone"; legacy migrations before claims; T8 and S10 bounded by `min_upgrade_from`; adopt steps stay registered. (Refined in round 3 by OPS3-7: store-based check; the S10 bound and adopt steps are gone.)
- R2-5 / MOD2-1 / MOD2-2 / MOD2-3 / MOD2-4 / MOD2-11 — S2 rewires all five `sdnotify` importers; no file of `cmd` moves; `flight` rewritten onto slog; references to 04's per-stage gate.
- R2-6 / OPS2-1 — §9.1 releases row corrected (ungated merges, auto-promotion every 10 RCs); release gating in §9.2.1 (required check + promotion gate, 14 Q-I); W counts minors and 30 days. (Gating superseded in round 3 by R3-2: one requirement.)
- R2-7 / SIMP2-1 / COR2-6 — S5 schedule keys `modules.books.<subsystem>.schedules.<name>` via `ScheduleFromConfig`.
- R2-8 / COR2-7 — boot phases per 06 §6.5.1 (P−1 … P8) in the boot metric and §9.9.
- DATA2-6 — S0 writes `c/storage/install` before any `c/` key, with `storage/keys`; `TestS2bOpensStoreWrittenByS0`.
- DATA2-8 — invites read in place, not mirrored (§9.2.3, §9.6 row 25).
- DATA2-9 — `RequiresServed` uninterrupted-run rule; blanking uses the `.prev` probe. (Superseded in round 3 by R3-1.)
- DATA2-10 — S7 import with M1.4 step 2a re-import and blob mirror; S7 rollback row corrected. (Superseded in round 3 by R3-1: no import or mirror.)
- DATA2-13 / DATA2-14 — C5 final copy-and-mark before the raise; contracts in three persisted phases. (Superseded in round 3 by R3-1.)
- DATA2-18 / DATA2-20 / DATA2-28 / DATA2-29 — checkpoint consumption wording; seed-role edits on rollback (S4); fresh-install level rule; stale cross-references (rule 2 note, §9.9 mutex, bookkeeping keys and step names).
- SEC2-4 / SEC2-5 / SEC2-6 / SEC2-17 / SEC2-19 / SEC2-20 / SEC2-21 / SEC2-22 / SEC2-25 — ABS confinement test; S4 alias target/selector table; fragment invite links; `LegacyAuthGinUntil`; `client_trust.go` stays until S4; access-log refusals before S4; auth-off tightening recorded (§9.2.6); mux/gin agreement test; stale note deleted.
- OPS2-2 … OPS2-26 — `RequiresSelfServed` and cell d'; updater `Boot` in P−1; updater layout and test-based done-check; scheduled apply check-only until S8; rolled-back version skipped; boot-read usage gate, recording-build rule, reference-store rule; blanking probe; `Requires` stamps copied not deleted; storage-efficiency ordering rule; canonical names only where no permanent alias; one build-ID mechanism; Docker job on every non-doc PR with a full image run; S2/S3/S4 releases; per-stage done-check rule and one ABS stage list; `deploy-wait-ready.sh` polls while activating; reference-store gate; webhook dead-letter on rollback; 05-config single env list; `KillMode=mixed`, container budget, unit install owner action; one level metric and phase labels; `PREV_REF` and the `compat-rehearsal` job; deploy refuses a staged restore. (`RequiresSelfServed`/cell d', the blanking probe and the stamp copy superseded in round 3 by R3-1.)
- MOD2-5 / MOD2-6 / MOD2-8 / MOD2-9 / MOD2-10 / MOD2-13 / MOD2-15 / MOD2-16 / MOD2-17 / MOD2-18 / MOD2-19 / MOD2-21 — genproto check as build pair; tidy coupling; coverage ratchet (SC5 report-only); core scripts under `core/scripts/ci/`; hello from S2; go1.27.3 bump as S0's first PR; empty `core/go.sum`; POSIX build script, Docker frontend stage, triggers, classifier; explicit workspace build order; CI table completed; deadcode from the root; SC numbering.
- COR2-1 / COR2-3 / COR2-8 / COR2-9 — projections mounted from `Registrar.Realtime().Projection`; books notifies through `Deps.Notify`; storage opens at `core.storage.path`; kernel without storage until S2b, hello and G6 from S5.
- FE2-1 / FE2-3 / FE2-5 / FE2-6 / FE2-8 / FE2-9 / FE2-10 / FE2-16 / FE2-17 / FE2-18 / FE2-19 / FE2-23 / FE2-25 — per-request CSP and `Reporting-Endpoints`; Activity on task aliases until S9 with the S5 legacy-stream bridge; build ID; non-SPA infra segments; invite fragment and R-test cell; `core.json` per stage; API-only placeholder; five extra redirects; ESLint permission rule; owner route names; UI-0 import swap and grep; no credential exempt from cross-origin; S9 after S3–S8, `/modules` at S4.
- SIMP2-2 / SIMP2-4 / SIMP2-5 / SIMP2-6 / SIMP2-7 / SIMP2-12 / SIMP2-14 / SIMP2-15 / SIMP2-16 / SIMP2-17 — one keyring export; webhooks at S7; operator overlay, never persisted; facade key machinery deferred; no books row rewrite at S7 and a `FacadeWriter` check; ledger row holds class and dates; level steps (floors and contracts); `parallelism_cap`; stale note deleted; Deluge behaviour routes stay on the legacy mount. (Level steps superseded in round 3 by R3-1; `parallelism_cap` refined by COR3-11.)
- integration pass (round 2, lead): legacy-side guard list includes the integration URL/secret bound. (Superseded in round 3 by R3-1.)

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — one stored format per family through S0–S10: removed Teach builds and rollback floors, adopt steps, mirrors, side records, rebuild-every-boot, mirror-disabled keys, claim hand-overs, the level renumbering and contracts C3–C8's data steps; §9.0, §9.2.1 (rollback = binary swap with stated costs: one re-login, re-entering moved secrets, `mck_` keys refused on older builds), §9.2.2 (no extraction level raise), §9.2.3 rewritten as adapters and grants per stage (`storage.Grant` modes), §9.3, every stage, §9.4.1, §9.5, §9.6, §9.8, §9.9, §9.11; S8 states that ABO archives stay unencrypted during the extraction (restore bound `{core, 0}`); new §9.13 "Deferred: per-family cutover".
- R3-2 / OPS3-3 / OPS3-4 / OPS3-5 / OPS3-6 — release gating is one requirement (every PR passes `make ci` with the extraction gates); promotion gate, label, path list and `check-rc-ordinal` mechanics removed; enforcement 14 Q-I; K19 references dropped.
- R3-3 / MOD3-1 — `internal/startup` stays in ABO until S8 (`Registrar.StartupSteps`); the stage-visibility gate reads non-test imports of every package (S0 table, S2, S8).
- R3-4 / OPS3-1 — state directory keeps HEAD's fallback; S2 sets the variable in the shipped files; image job asserts the keyring on the volume (§9.1, S0, S2, S3).
- COR3-1 — config reads the blob from S2 (`LegacyConfig`, then the adapter); S3 golden against HEAD; `TestPerimeterHonoursBlobOnlyValues`; Basic gate stays in gin until S3 (lead relay of R-E).
- COR3-3 — S2 surface: `compat` header/counter API, `internal/openapi`, `internal/httpmw`, `internal/jsonschemax`, `auth` leaf types; `uihttp`/`modulehttp` S4, `datamigrate`/`storagemigrate`/`logjournal` S5, `eventhttp` S6 (via 04 §8.1).
- COR3-9 / FE3-12 / SEC3-9 — S5 bridge is a books bus subscriber feeding streams A, B and C, forwarding only kinds with a `LegacyDefID` under that ID.
- COR3-11 — `core.tasks.kinds` map-typed dial (§9.2.8, S5, §9.11).
- COR3-12 — `PUT /api/v1/auth/me/password` stays one gin route, no alias.
- COR3-14 — `core/integrations#1` is now the only core versioned step of the sequence and writes only `c/integrations/ext/`; its DAG tie-break is 05-storage's.
- DATA3-1 / DATA3-22 / OPS3-14 — checkpoint reuse restricted to the same attempt and store identity; record order; deferred steps take no checkpoint (§9.2.2).
- DATA3-2 / DATA3-3 / DATA3-4 / DATA3-8 / DATA3-10 / DATA3-11 / DATA3-14 / DATA3-15 / DATA3-23 / DATA3-24 — superseded by R3-1 (no hand-overs, no C3/C5/C8, no tasks or logging Teach build, no levels, no contract phases).
- DATA3-6 / DATA3-7 — in-flight restore journal; `PreBootRestore` from S2b to S8 (S1, S2b).
- DATA3-9 — restore staging checks the upgrade floors; §9.6 row 30.
- DATA3-16 / SEC3-17 — seed-role edits refused (no clone alternative); Teach-build tests dropped from S4.
- DATA3-19 — revocation journal dropped from the S0 hotfixes.
- DATA3-20 — S0 records the foreign-open identity of the sidecars.
- DATA3-21 — fresh-install stamping in one batch.
- SEC3-1 / SEC3-2 / SEC3-3 / SEC3-11 — superseded by R3-1 (no role mirror, no Teach-build assignment check or restores, no decoy hash or `MirrorDisabled`); row rule cited (§9.2.3).
- SEC3-5 — `core/integrations#1` grants `Local` only to IP-literal private hosts; named LAN hosts need `Local` or `lan_allow` (S7, release note; lead relay of R-F).
- SEC3-13 — `/metrics` scoped-key change recorded (§9.2.6).
- SEC3-16 — `invite` in `SPAShared`; legacy 302 routes (S4).
- OPS3-2 — S0 writes no config data (no blanking before S3).
- OPS3-7 — store-based `min_upgrade_from` (`<db>.storage-applied`, `stepping_stone_required`, P13); only the T8 release sets one.
- OPS3-8 / OPS3-20 / OPS3-22 — superseded by R3-1 (no C5/C7 data steps, no Teach table).
- OPS3-9 — `deploy-wait-ready.sh` polls `systemctl` over ssh (S0).
- OPS3-10 / OPS3-16 — boot budget as a per-stage A/B; labels from 06 §6.9.
- OPS3-11 / FE3-3 — the stale-build rule is never deleted (S0, S9, R-test cell b).
- OPS3-12 — S0 updater hotfix: semver, reader check, both unattended paths check-only.
- OPS3-13 — `DEPLOY_STATE_DIR`, literal `test -e` sudoers lines, refuse when undetermined.
- OPS3-15 — S8 binary-directory checklist.
- OPS3-18 / OPS3-19 — server-computed gate fields; origin-dependent checks as e2e assertions.
- OPS3-21 — `ErrUpgradeFloor` exit 78 and last stderr line are the Docker message.
- MOD3-2 — stage-visibility generator committed and run in root `make ci` (S0).
- MOD3-3 — telemetry: construction at S1, global installs in an ABO façade until S2; `logtest` stays in ABO (lead relay).
- MOD3-4 — `internal/openapi` at S2; UI-0 justified by `core/openapi/core.json`.
- MOD3-5 / MOD3-8 — S0: the ABO guard's computed layer follows mapped core layers; the core guard's positive control is staged (S0 self-test, S1 real edge, S2 `app` → `kernel`).
- MOD3-6 — `fix-check` core only.
- MOD3-7 — `core-checkpoint` non-blocking.
- MOD3-12 / MOD3-16 — `exclude-check` (core) and `genproto-exclude-root` (root) rows; `check_module_skew.py` blocking.
- MOD3-13 / MOD3-14 — Docker frontend stage at UI-0; `ARG BUILD_ID` in every stage, meta = header assertion.
- MOD3-15 — no `app.Options.UI` until S9.
- MOD3-17 — rename-only first commit in move PRs.
- FE3-2 — one `BUILD_ID` per release into `app.Options.BuildID`; missing-header rule needs `X-Request-ID`.
- FE3-4 / FE3-17 — `app.Options.UIOptions` with ABO's `PageMiddleware` and `ConnectSrcFrom`; Go-side `UI.LegacyRedirects`.
- FE3-5 / FE3-18 — legacy `/login/temp` page, the 302 routes, one window each, §9.6 rows 31 and 34; `localStorage` copied until C9.
- FE3-6 — `LegacyPath.Shape`; Activity page on hand-written calls, `opsgen` until S9.
- FE3-7 — `/api/v1/auth/me` alias keeps legacy atoms, adds `canonical_permissions`.
- FE3-8 / FE3-16 — `ui.ReservedSegments()` at S2; `internal/modulehttp` serves `/modules` from S4.
- FE3-11 — S4 endpoints `/settings`, `/settings/schema`; S9 preconditions.
- FE3-13 — gin's SPA fallback refuses `livez`/`readyz` at S8.
- SIMP3-2 — `archtest.DeferredMembersAbsent` from S2 (lead relay).
- SIMP3-3 — app-supplied compat ledger (§9.2.9).
- SIMP3-6 — S5 → S6 → S7 → S8 strictly ordered.
- SIMP3-7 — `core.notify.max_age`.
- SIMP3-10 — `internal/plugin` deleted at S7 (one stage, checked by 04's deletion gate); S6 does not move its bus.
- SIMP3-12 — superseded history lines marked.
- integration pass (round 3, lead): `syslog:` grant moved from S2b to S5 (its reader `logjournal` lands at S5).

Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- R4-1 / SIMP4-2 / SIMP4-3 / COR4-4 / DATA4-2 / SIMP4-7 / DATA4-20 / COR4-22 — §9.2.3 rewritten: store interfaces with core native implementations and books-supplied legacy implementations (`module.Manifest.LegacyStores`), no grant during S0–S10, native-only data in books' extension family `m/books/ext/<store>/` with legacy-row hashes; the per-stage grant table replaced by the store table; §9.2.1 ratchet `TestLegacyFamiliesWrittenOnlyByLegacyStores`; S2b–S8, §9.4.1, §9.6 rows 11/14/25/27, §9.8, §9.9, §9.10, §9.11, §9.13, S10 restated; S4 r1/r2 per 05-authn.
- R4-2 / DATA4-1 / SEC4-1 / OPS4-1 / DATA4-3 — a secret's first write leaves a never-matching sentinel in the legacy row and the `config.yaml` copy; the older build's gate refuses until re-entry (§9.2.1 row 1, §9.2.3 rule 1, S3, S7, §9.8, §9.11); R-test cells `TestRollback_BasicGateNeverOpenOnPrev`, `TestRollback_RotatedKeyNotResurrected`.
- R4-3 / SEC4-2 — §9.2.6: legacy routes require the full legacy reach; custom-role half of `TestLegacyRoleReachUnchanged`; auth-off principal judged against the expansion (recorded).
- R4-4 / MOD4-1 — §9.1 quotes `.dockerignore` as it is; §9.2.9 ledger copy `internal/books/compat-surfaces.md` with `TestCompatLedgerCopyFresh`; S0 Docker row: every `//go:embed` input in the build context.
- R4-5 / MOD4-2 — S2: composition root `internal/books` vs helper package `internal/books/contract`; `module`/`app` importer assertion.
- R4-6 / COR4-1 — S2b: ABO's settings load, encryption init and key-regeneration guard run inside books' legacy config store; `TestS2bAppConfigFromBlob`.
- COR4-2 / COR4-20 — the legacy config store's `Changed` is the change source at S2 and S2b; `TestPerimeterHonoursBlobOnlyValues` runtime half at S2b; ledger-row check from S2b.
- COR4-3 — S3 keeps `app.Options.LegacyConfig` (legacy names only).
- COR4-7 — S6: stage-dependent projection A topics, books status ticker and `BookChangeCoalescer` rewrite; `TestProjectionTopicsExistAtStage`.
- COR4-8 — S5: `health` readiness core lands with `tasks`; dispatch done-check.
- COR4-9 — S3 Basic gate keeps HEAD's whole exempt set (health paths, static assets, ABS).
- COR4-18 / FE4-1 / MOD4-9 — S0 build ID: meta-only, no ldflags, no Docker `ARG BUILD_ID`, `build_web.sh` generates the ID; `main` writes `X-App-Build` before S2.
- COR4-19 / SIMP4-1 — S0 table and S7 done-check: generated, package-qualified `DeferredMembersAbsent` list, per-member deletion.
- DATA4-4 — S3: `set-internal-flag` refuses store-interface keys; S7: field-merge root writes, memdb kept current by books' root store.
- DATA4-5 — S5: ABO's scheduler keeps ticking until T7, one scheduler per kind (`TestOneSchedulerPerKind`); `TestOpsFamiliesWrittenOnlyByTaskstore`.
- DATA4-14 / SEC4-9 — §9.2.1 row 2: native sessions from before a rollback revoked on roll-forward.
- DATA4-18 / DATA4-19 / OPS4-5 — §9.2.2: floor release vs stepping-stone release, `LegacyCap`, S0+ updater scope, "boot any release ≥ S0 once"; `TestStepSteppingStoneValid`; S5, §9.4.1, §9.6 row 33, P13.
- SEC4-3 / SEC4-4 / SEC4-13 — §9.2.3 rule 1 (confined roles atoms-only, frozen reach snapshot) and rule 3 (unknown atom fails closed per role); §9.8 row and P2 branch B aligned.
- SEC4-5 — S4 R-test cell "roll back and return with no restore: no key confirmation".
- SEC4-7 — S3: integration-bound blob fields `ProtectAdmin`; S7 `Local` grant sees admin-written URLs only.
- SEC4-8 — §9.2.6 records ABO's `disabled_allow_remote=true` default and turning auth on through the operator layers; `TestS4_AuthOffDockerBoots`.
- MOD4-3 / MOD4-4 — S1/S2/S8: telemetry vocabulary stays as books telemetry; `InstallProcessDefaults` with meter, tracer and propagator; `PromRegisterer` until S8; series-set done-checks at S2 r3 and S8.
- MOD4-5 / MOD4-6 — S2 logging stage 1 split; `syslog:` writer and pruner leave at S5, where "journal shows ABO records" is checked; S1: `internal/logger` not split.
- MOD4-7 — S0: stage-visibility gate over every remaining stage point with `shipped_stage:`, tagged files read; `bench-check` note corrected.
- MOD4-8 / OPS4-7 / OPS4-6 — §9.2.1 named required checks (`ci.yml`, `extraction-gates`, `docker-image`, `core-ci`), up-to-date branches, prev-build one-format tests on every PR, nightly-red rule; S0 table rows.
- MOD4-10 — UI-0 precondition: `run-frontend: false` in the three reusable-workflow callers.
- MOD4-11 / COR4-11 — S2: no `compat` until S2b; leaf types per 05-rbac §3.1 (via 04 §8.1).
- MOD4-13 — S0 table: `check_core_version.py` in `core-ci`, root `TestBooksMinCoreVersion`.
- MOD4-14 — S0: every commit of a move PR builds; `core/scripts/split/origins.txt`.
- MOD4-15 — S0: Dependabot via the existing `multi-ecosystem-group`, no `applies-to`.
- MOD4-17 / MOD4-18 — S2/S8 `internal/startup` rewire per 04 §8.3's per-step table; `internal/search` takes an injected func.
- FE4-2 — invite links keep the path form until S9; fragment form and 302 at S9 (window S9 + max(W, invite TTL)); `TestLegacyInviteLink_EndToEnd`.
- FE4-3 — S0 stale-build case 3 keyed on `Reporting-Endpoints`.
- FE4-4 — S9/C9: `localStorage` read in place, no copies.
- FE4-5 / FE4-8 — §9.2.9 and S9: `ui_route` core-generic, registered in the step-5 release; CF recovery and `ConnectSrcFrom` read an atomic pointer filled at Start.
- FE4-7 — S5/S9: `opsgen` repointed at S5, retired with the last native `ops.ts` importer.
- OPS4-2 — S2/§9.2.5: no shipped file sets the state-directory variable; S0 image job second run.
- OPS4-3 / OPS4-16 — §9.2.2: one `find` restore probe, its sudoers line an S0 owner action; `--print-upgrade-floor` from S0.
- OPS4-4 — S8 binary directory outside the state directory; dry run as the deploy user.
- OPS4-10 — S0 `rollback` recipe passes host and unit; `Makefile.local` owner actions; preflight warning.
- OPS4-11 — §9.2.1 one table of costs and consequences (with 05-config's matrix consequences), asserted by the R-test.
- OPS4-12 / OPS4-13 — §9.3 `scripts/boot_budget.py` and its threshold; "staging" defined.
- OPS4-14 — S0 `/health` `version` and `--version-json` with the build ID; P10; out-of-scope defect 5.
- OPS4-15 — S8: three sidecars rewritten on restore.
- OPS4-17 — S0 updater hotfix: describe-suffixed versions are local builds.
- SIMP4-4 — served ledger `last` with duties; W rule reads the `usage` duty.
- SIMP4-5 — S8: backup encryption not built during the extraction.
- SIMP4-10 — §9.2.9: core's ledger fragment holds only permanent `OTEL_*` names.
- SIMP4-12 — §9.2.3 and S7: `integrations` is the one registry of instances.
- DATA4-6 — S1/S2b/S8: the kernel's journaled P0a swap from S2b replaces `PreBootRestore`.
- DATA4-12 — S4: `apikey_expiry_stamp.go` becomes a boot write of books' legacy auth store.
- Lead relays (round 4): S7 `Local` grant by a books legacy step calling `integrations.InitialLocalGrant` (R-F); S5 ops store in `internal/books/taskstore` (R-C).
- integration pass (round 4, lead): store interface names aligned (`rbac.Store`, `log.JournalStore`, `library.RootSource`, `tasks.Stores`, `module.Manifest.LegacyStores`).

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`):

- R5-1 / OPS5-1 / OPS5-2 / SEC5-4 / DATA5-12 — §9.1 HEAD precedence and the four encrypted secret rows; §9.2.1 row 1 (only plaintext secrets move; the Basic password never does), §9.2.3 config row, §9.2.5 `Alias.BelowStored`; S0 hotfixes need no recovery command; S3 mechanism, Basic gate refuses an empty password, compose pass-through, compose fixture of the golden and `TestComposeEnvKeepsUIBasicGate`, rewritten Basic-gate R-test cell; §9.8 rows; §9.11 risks.
- R5-2 / MOD5-1 / MOD5-2 — §9.2.2 legacy steps reached through wired funcs, direct calls, `TestLegacyStepPreconditionsResolve`; §9.2.3 layout (`internal/books/configstore`, taskstore → store only); S2 and S2b moves; S5 mechanism; S0 stage-visibility row (gate E); §9.8, §9.10, §9.11.
- R5-3 / COR5-1 / DATA5-11 — `CheckedWrites` starts at the stage that converts the last blind writer (blob and secret rows S3, `import_path:` S7); §9.2.3 ownership sentence; S2b lists HEAD's blind writers; S2b done-checks; S3 and S7.
- R5-4 / DATA5-1 — §9.2.3: families with commit-time checks are `Serialized`.
- R5-5 / COR5-3 / DATA5-6 — served ledger `last` replaced only in the served-ledger commit, `owed` duties (§9.2.1 W rule, §9.2.2 records, S0 data row); R-test cell for a failed roll-forward boot.
- R5-6 / DATA5-2 / DATA5-3 / DATA5-5 / SEC5-1 / SEC5-2 / SEC5-3 / SEC5-12 — `mck_` keys are legacy rows with admitted atoms or a grants-nothing scope; fingerprints cover security-relevant fields; monotone revocation; §9.2.1 rows 2 and 4, §9.2.3 rule 1, S4 data and R-test cells, P3.
- R5-7 / FE5-1 / FE5-2 / FE5-3 / MOD5-12 — S0 build ID generated by `npm run build` by 07 §7.6.3's one rule; Docker `ARG BUILD_ID` in the frontend stage only; `--version-json` prints `ui_build_id` and `binary_id` (`app.BinaryID`); §9.6 row 20.
- COR5-4 / SEC5-7 / SEC5-8 / SEC5-9 — §9.2.6: atom-carrying requirement for the auth-off principal (op starts not tightened), HEAD intersection for scoped legacy keys, credential guard pass-through corrected, tightenings (a) S4 `Guards.Interactive` and (b) S8 backup routes, `auth_off: pass|deny` column; S4 and S5 permissions.
- COR5-5 — §9.2.6: aliases declared through `app.Options.LegacyPermissionAliases`.
- COR5-7 — books' start hook runs `ReconcileOpsV3WithV2` until S5 and `ReconcileVerifiedEmails` until S4 r2 (S2b, S4, S5; §9.2.3 tasks row).
- COR5-9 — S6: projection A's topics do not change at S8.
- COR5-10 / DATA5-15 / SIMP5-3 — no result-journal store; one AI family list with `aijob_payload:` (§9.2.3, S7).
- COR5-11 — definition snapshots and idempotency records in core's `c/tasks/def/` and `c/tasks/idem/`.
- COR5-13 — §9.2.1 ratchet allow-list per family names the four legacy store packages.
- COR5-14 — `config.OverrideStore` read half at S2, native implementation at S2b, writes at S3 (S2, S2b, S3).
- COR5-15 — S4 r1 decode census is P4d step 1c.
- COR5-8 — §9.2.2 fresh installs: seed steps run in P4b (consequence).
- DATA5-8 — S5 done-check: all 38 schedule definitions fire within one interval.
- DATA5-9 / DATA5-17 / SIMP5-11 — S7 `Local`: a versioned `Migration` that records `PendingLocal` via `legacy.LocalCandidate` and grants nothing until an admin confirms; reruns after an old-archive restore; tests; S3 note corrected; §9.4.1.
- DATA5-10 — §9.2.1 row 12: an older build's factory reset wipes core records; orphan sentinels read unset.
- DATA5-18 — S10: `sess:` aging-out claim and a books retention sweep.
- DATA5-19 — §9.2.1 row 15 and S8 rollback: restoring an S3+ archive on an older build.
- FE5-4 — the S4 legacy SPA gains the `/invite#token=` reader (option (a)); §9.2.1 expand rule and row 3; S4, S9; R-test cell.
- FE5-5 — S9: all eight shell-bound `localStorage` keys kept by name.
- FE5-12 — S9 keeps `opsgen` and the stale-build rule in the api-client since UI-0; UI-0 exports `checkBuild`.
- FE5-13 — S9: steps 4 and 5 ship in one release (§9.4.1).
- MOD5-3 / MOD5-4 — S0 stage-visibility row: (h) per 04 §2.1; S5: core scheduler is a re-implementation, ABO formats stay books.
- MOD5-7 — `scripts/ci/check_embed_context.py` in root `make ci` is the embed check (S0, §9.2.1).
- MOD5-8 / OPS5-4 — `changes` classifier `go=true` cases for the ledger, its copy, core's fragment and the spec directory; `test_ci_classifier.py`; `extraction-gates` trigger.
- MOD5-9 / MOD5-11 — S2: composition root and configstore enter `layerOf` at S2, contract and taskstore at S5; `books.Module()`; guard over non-test imports; G6 test in `internal/books`.
- MOD5-13 — §9.1 is the one `npm ci` site list.
- MOD5-14 — `old-path ==> new-path` origins separator.
- MOD5-15 — `core-ci` runs core's errcheck, interface-width, mocks, pprof and `core/web` tests; trigger includes the workflow file.
- MOD5-16 — stage-visibility file set from `go list` per (`GOOS`, tag set).
- OPS5-3 — `docker-image` runs on every non-doc PR.
- OPS5-5 / SEC5-13 — `/metrics` scope enforced from S4 r2 through the legacy manifest entry; `TestLegacyMetricsRoute_RequiresScopeFromS4`.
- OPS5-6 / OPS5-9 — renames in the stage that moves each emitter (Pebble S2b, ops S5, AI S7, gauges S8) with aliases, dashboards and per-stage checks; one credentialed scrape command (§9.2.7, S1, S2, S2b, S5, S7, S8, §9.6 row 24).
- OPS5-7 — image job second run from S0, volume preparation, state-dir tests inside the image.
- OPS5-8 — flip PR = changes `shipped_stage:`; latest `compat-rehearsal` run rule.
- OPS5-10 — S0 done-check: `make rollback` one way, the `deploy` recipe back.
- OPS5-11 — S0 delivers `scripts/staging/provision.sh`; no production key on staging.
- OPS5-12 — §9.2.1 rows 7, 9 and 14.
- OPS5-13 — one resolver reads the YAML database path; `TestStateDir_OneResolverYAMLDBPath`.
- OPS5-14 — S2 r1/r2: `PromRegisterer` nil until r3; scrape checks.
- OPS5-15 — exit 78 not restarted (`RestartPreventExitStatus=78`, plist, Docker note, `deploy-wait-ready.sh`).
- OPS5-16 — preflight sudoers check; guard treats unreadable `.storage-levels` with format 1 as all zero.
- SEC5-5 / SEC5-6 — §9.2.2 full-stat foreign-open identity and boot lineage records; R-test cells.
- SEC5-11 — "the five early fixes of 05-authn §11.1" in §9.1, S0 and §9.4.1.
- SIMP5-2 — the dirty set per index through `index.Spec.Dirty`.
- SIMP5-8 — §9.2.3 cites 03 §3.3's list and adds the `notify.RuleStore` row.
- SIMP5-9 — `TestLegacyStoreSuites` is the one ABO umbrella name.
- integration pass (round 5, lead): S2 constructor `books.Module(store)`; 04 objection reference updated.

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-1 / DATA6-1 — every write to a serialized family is checked at commit; a stale tick is dropped (§9.2.3 ownership, §9.8, §9.9, S5).
- R6-2 / MOD6-1 — `internal/operations/state` stays in ABO; taskstore reads it, core's machine is a re-implementation; S5 opsgen check no longer says "repointed" (§9.2.3 tasks row, S5).
- R6-3 / SIMP6-2 / SIMP6-3 — the root source is `legacy.RootSource`; `legacy.LocalCandidate` (§9.2.3, S7, §9.4.1).
- R6-5 / SEC6-3 — early fix 6 (API-key revocation race) in §9.1, S0 and §9.4.1 ("six early fixes"); books' last-use write stages `Expect`, `apikey:` `CheckedWrites` from S4 r2 (§9.2.3, S4, §9.9).
- R6-6 / SEC6-1 — an operator value that enables auth beats a stored `false`; canonical names for other protected keys; `config_legacy_name_shadowed` (§9.2.5, §9.2.6, §9.8, S4).
- R6-7 / OPS6-4 — the staging copy is scrubbed offline and booted with egress denied; integrations disabled (`core.integrations.disabled` from S7, URLs cleared before); `test_staging_scrub.py` (§9.3).
- COR6-1 — `rbac.LegacyAtom(atom)` is the one requirement value, recorded in the S2 manifest (§9.2.6, S2, S4).
- COR6-2 / DATA6-7 — W restarts at `detected` through the duty handle and `c/compat/recording_since/<owner>`; `since` zero without a previous `last` (§9.2.1, §9.2.9).
- COR6-3 — books' start hook keeps HEAD's `seedRolesAndTokens` through S4 r1; r1 done-checks; narrowed dark-release test (S4).
- COR6-5 / DATA6-11 — books' legacy config `Open` writes nothing; the corrupt-secret repair dropped from S2b's writer list; marker one-shots become books `Migration`s (S2b).
- COR6-12 — books' legacy migrations run on claim-checked handles (S2b).
- DATA6-4 — S4 R-test cell reworded to 05-backup's redesigned content marker (an archive of the current build, also one taken during the last journaling boot).
- SEC6-4 — S0's guard records the Pebble open stamp (§9.2.2 records table, S0 data and rollback rows).
- COR6-2 — owed-duty entries are store-wide at `c/storage/owed/<duty>` (§9.2.2 records table).
- DATA6-6 — factory reset from S2b is storage's staged reset; `PebbleStore.Reset` deleted (§9.2.1 row 12, S2b).
- DATA6-12 — one count-only release per `CheckedWrites` flag; secret-save check moved to S3; flag tied to the flip PR (§9.2.3, S2b, S3, S7).
- DATA6-14 — §9.8: only Widening extensions are suspended; Derived fields reset.
- FE6-1 — `checkBuild(response, { buildId, shell })` with the build ID read from the meta tag (S0, UI-0).
- FE6-3 / FE6-4 — `--version-json` = `{app, version, commit, ui_build_id, binary_id}`, `binary_id` from `buildid.Binary` at S0, `commit` from `vcs.revision`; goreleaser's dead `-X` values deleted; P10 fallback (S0, §9.6 row 20, §9.7).
- FE6-5 — "S4 r2–S8" in the three invite places; one test name (`TestLegacySPA_InviteFragmentReader`, corrected in round 7 by FE7-10).
- FE6-7 — non-empty, charset-checked `BUILD_ID` (S0).
- MOD6-2 — `TestLegacyStepPreconditionsResolve` lives in `internal/books`; gate E's test half (S0 gate row, S5, §9.10).
- MOD6-3 — embed-context check accepts listed in-image outputs (`scripts/ci/embed_build_outputs.txt`) (§9.2.1 gating table, S0).
- MOD6-4 — `StandDownStore` (§9.2.3, S5).
- MOD6-5 — the release callers of `reusable-release.yml` are an unverified precondition with a verification step first; `pre-build-script`s; dry-run RC done-check (S0, UI-0).
- MOD6-6 — `app.Options.LegacyTaskAliases` by T4 (S5).
- MOD6-7 / MOD6-13 — stage-visibility row: (h)'s one rule, `tools/stagecheck` (S0 CI table).
- MOD6-11 — the S3 façade is the `Facade` type of ABO's `internal/config` (S3).
- OPS6-1 — the S0 updater and the `deploy` recipe check the binary's own floor; done-check case (§9.2.2, S0, §9.4.1, §9.7 P13, §9.8).
- OPS6-2 / OPS6-3 — process-gauge aliases target `metrics.RuntimeFamilies()`; `go_*` not S8 additions; every series done-check is 05-metrics §6's seeded contract test, the scrape a smoke check (§9.2.7, S1, S2, S2b, S5, S7, S8).
- OPS6-5 — exit 78: no plist claim, launchd note, `ExecMainStatus` in the wait script, the exit-75 exception (§9.2.2, S0, S2, §9.8).
- OPS6-6 — `basic_gate_no_password`, `TestBasicGate_EmptyPasswordRefuses` (S3, §9.8, §9.10).
- OPS6-7 — `BelowStored` covers env names and file keys wherever the legacy config store attaches (S2 on); flags recorded in §9.2.6 (§9.2.5, §9.2.6, S2).
- OPS6-8 — "Flip PR" row in every stage table; one flip per stage release; S2b's flip is release 1 (§9.2.1, §9.3, every stage, §9.4.1).
- OPS6-9 — §9.2.1 rows 16–18 (metric renames, settings with no legacy name, scrape key); R-test.
- OPS6-10 — the `/config` volume `chown` command against the ABO image; README note in S0 "Deploy".
- OPS6-11 — S0 runs `TestS0LedgerKeysRoundTrip` and commits an S0 fixture store; `TestS2bOpensStoreWrittenByS0` only at S2b (S0, S2b, §9.10).
- SEC6-2 — §9.2.3 rule 1 and §9.2.6: scopes come from the extension record iff it holds canonical scopes (`scopes_source: canonical`) (S4).
- SIMP6-1 — §9.2.1 row 13 drops the config example (now from S4).
- integration pass (round 6, lead): `legacy.SecretsStore`.

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`):

- R7-1 / SEC7-1 / SEC7-2 — §9.2.1 row 19 (operator-enabled auth from S4 r1 and the Basic gate from S3 survive a rollback through the audited narrowing write); §9.2.6; `TestRollback_OperatorEnabledAuthStaysOn` in S3 (Basic gate), S4 and the R-test.
- R7-2 / COR7-1 / SEC7-4 / SEC7-5 / DATA7-6 / DATA7-17 — S2b: books' gin handler stages through `legacy.Maintenance` (`module.StorageHandles.Maintenance`), no core route before S4 r2, side effects in books' post-reset hook after `applied`, intent bound to the Pebble open stamp, restore/reset mutually exclusive, `TestFactoryResetFromLegacyRoute_S2b` and the rejection tests; S4 r2: `internal/storagehttp` with the legacy path as its `Alias`; S8 deletes the handle; §9.2.1 row 12; §9.2.6 Q-K bullet; §9.8 rows.
- R7-2 / OPS7-11 — `reset_pending`/`reset_inflight` refuse `make rollback`, `deploy` and the updater (`ROLLBACK_IGNORE_RESET=1`); the one S0 sudoers probe covers `restore/` (including `inflight-checkpoint.json`) and `reset/` (§9.2.2, S0, §9.8).
- R7-2 / DATA7-7 — empty store exempt from the floor; fresh-install batch records legacy steps 1..`LegacyCap`; versioned steps from `LegacyCap+1`; `TestFreshInstallPostFloorBootsTwice` (§9.2.2, S2b).
- R7-3 / DATA7-1 / DATA7-2 / DATA7-3 — run-store writes checked per guard row (§9.2.3, S5, §9.8, §9.9).
- R7-4 / DATA7-11 — ABO subcommands that write open through `app.OpenOffline` from S2b, `diagnostics query` read-only; `TestOfflineOpenIsNotForeign`, `TestCLISubcommandsUseOfflineOpen` (S2b, §9.3).
- R7-5 / DATA7-9 — row 3 unchanged; `TestFirstR2BootKeepsLegacySessions` in S4 r2's done-check.
- R7-6 / OPS7-2 / OPS7-3 / OPS7-4 / OPS7-7 / SEC7-6 / SEC7-10 — §9.3 is the one staging procedure: archive restore only (no `config.yaml`), nftables egress drop before the first boot with ssh and the API port open, every boot egress-limited, `staging-scrub` with `secret_fields.txt` from S0, URLs cleared at every stage (native instances and metadata `base_url` included, R-F request), schedules run, password hashes replaced and keys revoked, `provision.sh --updater-check`; S0 Deploy, S5, S8, §9.11.
- R7-8 / MOD7-1 / MOD7-2 / MOD7-3 — S5: executor written fresh, registry files stay as opsbridge until T8, re-implemented helpers, ABO formats stay ABO (v1 status bridge on `core.task.finished.v1`), `maintenance_window_last_run` in `ClockStore` (§9.2.3, S5); S0 CI table: the generator's move check.
- OPS7-1 — §9.2.7 points at 05-metrics §6 (served `Provider.Handler()`, `mediacore_` from S2 r3, private registry at S8); S2, S8.
- OPS7-5 — S8 Metrics: the same PR deletes the two books process gauges and their setters; `TestS8_FullAliasSetBoots`.
- OPS7-6 — §9.2.1: S3–S5 dark, S2b and S6–S8 live PR by PR; the red-rehearsal rule covers every state-writing PR (S2b, S6–S8 flip rows).
- OPS7-8 — every flip row names the `shipped_stage:` value it writes; S0 commits `pre-S0` (04 §2.1).
- OPS7-9 — `ci/prev_ref.txt` and a two-row `compat-rehearsal` matrix; the flip-PR rule needs both rows (§9.2.1, S0 CI table, §9.7 P10, §9.10).
- OPS7-10 — an empty floor passes without the sidecar; §9.6 row 33; §9.8 (§9.2.2, S0).
- OPS7-12 — S3's production golden runs on the staging host, names and value hashes only, nothing committed.
- DATA7-13 — the three marker one-shots are `Seed` steps; `TestFreshInstallWritesHEADMarkers` (§9.2.2, S2b).
- DATA7-15 — the ≥ 14-day blind-write gate in S4 r2's, S5's and S7's done-checks.
- DATA7-16 — §9.2.1 row 20 (an archive taken during S2b–S7 needs every API key re-confirmed, R-B request); S4 R-test cell reworded.
- FE7-1 — UI-0 adds `core/web/.gitignore` and the clean-tree done-check; S9 adds hello's.
- FE7-2 — P10 strips `+dirty` with a stated fallback order; an unresolvable `PREV_REF` fails loudly (§9.7, §9.10).
- FE7-3 / FE7-6 — S0 build ID: the meta tag only, read once at page load from S0; `dev` reserved.
- FE7-9 — S9: seven `legacyStorageKeys` plus the preset's theme key, theme meta and boot script, ABO's inline script deleted, `theme-boot.spec.ts`.
- FE7-10 — one fragment-cell test name, `TestLegacySPA_InviteFragmentReader` (S4, §9.10, round-6 history line).
- FE7-14 — S0 Moves include `buildid`; `TestVersionJSONFields` in S0's done-check.
- MOD7-5 / MOD7-6 — S0: the `COPY --from` cross-check reads only `go-builder` stages; hello's dist on core's own list (`core/scripts/ci/embed_build_outputs.txt`, `check_embed_outputs.py`).
- MOD7-14 / MOD7-15 — S0 CI table cites 03 §3.5.1 item 4's tag-set list; a `tools/stagecheck` vet/test row with negative-control fixtures.
- COR7-11 — S4: `seed.go`, `bootstrap.go` dated S4 r2 (04 §8.5).
- SEC7-9 — §9.7 P3 uses 05-authn §4.4's one predicate.
- SEC7-12 — `auth_disabled_remote` and `config_legacy_name_shadowed` carried by HEAD's token list until S8 (§9.2.6).
- SEC7-13 — §9.2.5: the compose example precedes the one exception.
- SIMP7-7 — §9.4.1 T8 row deletes `storage.LegacyMigration`, `Spec.Legacy`, the legacy phase and `TestNoNewLegacyMigrations`.
- SIMP7-8 (03 v1.10) — `legacy.LibraryRootSource`, `legacy.IntegrationsLocalCandidate`.
- Consistency — S2's `config.OverrideStore` read half is `Load` only (legacy capabilities in `legacy`, basis deferred), as 04 §8.1 and 06 state.

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`):

- R8-1 / SIMP8-1 / COR8-1 / COR8-2 / COR8-3 / COR8-4 / DATA8-1 / SEC8-5 / OPS8-5 — both legacy reset routes stage `KindReset` (`ResetFactory`, `ResetStore`) of the one restore swap through `legacy.Maintenance` from S2b, and are `Alias`es of the one `internal/storagehttp` route (which receives `storagehttp.Stager`) from S4 r2; staging pre-validates targets and refuses a dangerous `RootDir`; set-aside beside each target, failed set-aside boots the untouched store degraded; the hook never fails boot (`factory_reset_cleanup_incomplete`); the reset boot stages HEAD's factory defaults; `TestSystemResetStagesStoreOnlyReset_S2b` and the reset tests in S2b's done-check (§9.2.1 row 12, §9.2.6, S2b, S4, §9.8, §9.10).
- R8-1 / OPS8-7 / OPS8-8 — one token pair `restore_pending`/`restore_inflight` and one override `ROLLBACK_IGNORE_RESTORE=1` (pending only) in `make rollback`, `deploy` and the S0 probe; `reset_pending`, `reset_inflight`, `reset/`, `ROLLBACK_IGNORE_RESET` and `DEPLOY_DISCARD_RESTORE` deleted; the S0–S7 updater runs the decision in process without `sudo`, through `CheckReader` from S2b (§9.2.2, S0, S1, S2b, §9.8).
- R8-1 / OPS8-6 — §9.2.1 row 12 dated S2b (factory and store reset) and S3 (settings reset), with split release-note text; S2b, S3 and S4 overview and Rollback rows list rows 12, 19, 20, 21; `scripts/ci/check_rollback_rows.py` (§9.2.1, §9.3, S0, S2b, S3, S4).
- R8-1 (R-B request) — the `core.storage.swap.*` keys land at S2b; the manual undo uses `<app> secrets set-install-id` (S2b).
- R8-2 / DATA8-3 / DATA8-4 / SEC8-4 — `OpenOffline` stages owed entries before promoting; the offline-open user list corrected (`seed`, `dedup-bench`, `Isolate`, `reserved-prefs` as a break-glass direct open) (S2b).
- R8-3 / OPS8-2 / SIMP8-2 / SEC8-11 / OPS8-11 — §9.3 steps 1 and 3: `staging-restore --archive --db` (an S0 subcommand) and `staging-scrub` run with the binary of the release that wrote the archive; clause (c) clears legacy URLs only (native instances covered by the switch and the firewall); `test_staging_scrub.py` asserts the first serve boot finds everything scrubbed, cases dated S0/S2b/S7 (§9.3, S0, S7).
- R8-4 / SEC8-1 / SEC8-6 / SEC8-8 — row 19: the Basic gate and rate limit narrow whenever `true`, release note names `BASIC_AUTH_PASSWORD`; row 21 for canonical-only names with `operator-names.json` and the advisory token; §9.2.6 advice and the S3/S4 r1 reading; R-test cells (§9.2.1, §9.2.2, §9.2.6, S3, §9.10).
- R8-5 / SEC8-2 / SEC8-3 (R-F request) — §9.2.6 bullet on operator-secret withholding by origin plus provenance; `OperatorSecretOrigin` set by the S7 migration; `TestRollback_NonAdminURLNeverCarriesEnvSecretOnPrev` in the R-test (§9.2.6, S7, §9.4.1, §9.10).
- OPS8-1 — S5: the T0+T1 PR deletes the five `operations_*` families and their setters; `TestS5_FullAliasSetBoots` (S5, S8, §9.10).
- OPS8-3 / SIMP8-10 — the S8 updater check runs signed RC *n* applying RC *n+1* with egress to the release hosts only; the fake server and test key are gone (§9.3 step 5, S8, §9.11).
- OPS8-4 — S5's done-check: `TestScheduleParity_AllDefinitions` plus a staging smoke table (§9.3 (d), S5).
- OPS8-9 — fifth red-rehearsal criterion, `ci/state_writers.txt` committed at S0 (§9.2.1, S0).
- OPS8-12 — S1 and S2b: staging a restore or reset exits 75, so the committed unit restarts the service (S1, S2b).
- OPS8-13 — enforcing releases S3 r2, S4 r3, S5 rE, S7 r2, each with a §9.4.1 row; a stage is done when its enforcing release has served 14 days with zero refusals (§9.2.1, S3, S4, S5, S7, §9.4.1).
- OPS8-14 — row 20 reworded to ABO archives restored on S4 r2–S7; R-test row and cell (e) (§9.2.1, §9.10).
- DATA8-11 — a host with no DB directory and no sidecars passes the floor checks (§9.2.2).
- DATA8-13 — the boot-lineage row adds the restore boot's `marker_written` batch; fresh installs record steps below the lowest `Seed` step; `<db parent>/config.yaml` (§9.2.1 rows 1, 12, §9.2.2, S3).
- MOD8-1 / MOD8-4 — S0's stage-visibility row cites 04 §2.1 (h) and (i) instead of restating them, and names the twin check (MOD8-3).
- MOD8-2 / MOD8-8 — S5: `childop` and `opsmetrics` keep their ABO paths until their last ABO user is gone; the activity mirror moves to `internal/activity`.
- FE8-3 / FE8-4 — S0: `StaleBuildBanner`, and the clean-tree rule quoted as `git status --porcelain --untracked-files=no`.
- FE8-8 — S9: the `mediacore-app` meta, the preset-before-`UIBundle()` order, `createShell.meta.test.tsx` and `TestBundle_MissingMeta`.
- SIMP8-4 — `legacy.Maintenance` keeps only `StageFactoryReset` and is deleted in the S4 r2 PR; `UpgradeFloors()` deleted (S2b, S4, S8).
- SEC8-7 — S4's done-check adds `TestUserMerge_ScopedAdminKeyRefused`.
- COR8-7 (consistency) — §9.9: the caller that builds the batch retries build-and-commit (05-tasks 5T.7).
- Integration pass (round 8, lead) — row 17: `secrets set-install-id` lands at S3 with the keyring; §9.2.7: the golden diff names S2 r3's `mediacore_*` families and S2b's kernel families.
