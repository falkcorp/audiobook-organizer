<!-- file: docs/specs/platform-core/09-abo-on-core.md -->
<!-- version: 1.13.0 -->
<!-- guid: 3c82c917-2fc4-4385-b085-73d143a2374f -->
<!-- last-edited: 2026-11-05 -->

# 09 — ABO on core: releases and the cutover

Evidence commit: `748fd787a`. Raw data: [`raw/`](../../plans/platform-core/raw/00-index.md).
Names, paths and rules come from [03-architecture](03-architecture.md) (normative). The release keys R0–R3, the flip
labels F2–F9 and the migration steps M1–M8 are 03 §3.8's; D12's four owner answers are cited as D12 (a)–(d). Package
moves are **not restated here**: [04-package-mapping](04-package-mapping.md) §8 keys every move by release; its rows
are the planning list (informative, A-4), and each PR's build and the guards of 03 §3.5 decide what it may move. This
file owns **sequencing**: what each release ships, the order of the R2 flips and migration steps, how each release is
verified and rolled back, and what the owner does. Where a 05-* "Migration from ABO" section and this file disagree on
*when* something happens, this file wins; on *how* a mechanism works, the owning 05-* file wins.

## 9.0 Purpose & scope

ABO becomes **app = core + module `books`** in four releases (§9.3): **R0** the hotfixes; **R1** the core runtime,
with no change to user data; **R2** the one-way cutover of every core-service family, with its UI, as one release;
**R3** cleanup and the one upgrade floor. Four owner decisions set the shape ([02](02-decision-record.md)):

- **D10, no backward compatibility.** Data formats, storage layouts, key names, HTTP paths, metric names, config keys,
  env names, CLI flags, the webhook format, the OAuth callback path and the fingerprint-worker API may change. A rename
  updates its dashboards, rules, scripts and skills in the same PR (§9.2.9). A thing stays unchanged only where that is
  cheaper, and the extraction builds no compatibility machinery: no alias, window, `Gone` step, compat ledger, legacy
  store, `legacy` package, HEAD-parity golden, legacy env decoding or lock parity.
- **D11, no soaks.** Code lands dark on `main`, one flip PR switches each subsystem on, and every merge passes
  `make ci` and the e2e suite (§9.2.1). The owner's work is decisions, deploys and an optional 1-day smoke run.
- **D12, the cutover's scope.** (a) R2 is **one** release: automatic promotion pauses from the first R2 flip until
  R2 ships; a lead extension, which the owner kept (D15), pauses it from R1's first core PR until R1 ships too
  (§9.2.1; both freezes are the repository variable `vars.RELEASE_FREEZE`). (b) API keys and the Audiobookshelf (ABS) phone apps' sessions survive it; web sessions and one-time
  tokens are reset; task-run and log history are converted; throttle and quota holds, indexes and caches are reset
  or rebuilt. (c) The only frozen external contract is the ABS-compatible API (§9.6). (d) The pre-migration
  checkpoint is deleted automatically 7 days after it stops being needed (§9.2.3).
- **D13, identity rows and merges.** A user, or one of its keys or identities, that the cutover cannot convert stops
  the upgrade, and an account merge in progress refuses it, until the owner fixes the cause (§9.2.3); finished
  merges become permanent. The UI shell builds no old-URL redirects. Core is `github.com/falkcorp/mediacore`, public,
  under Apache-2.0.

Kept from before: D1–D9 (02); the six early fixes of 05-authn §11.1, which ship first (R0) and whose tests are ported
onto core auth in R2; data migrated forward one-way by in-binary steps and never dropped, a row that does not decode
being quarantined and reported, except an identity row, which stops the upgrade (§9.2.3); rollback by an offline restore plus the previous binary (§9.2.1); and the
hardened identity, secrets and precedence design, which lands directly in R2.

Out of scope: TV/Movies (10), multi-app (11) and the repo split (12). The deferred-build packages (`acquire`,
`arrfacade`, `integrations/indexer`, the download grab/monitor pipeline, non-Deluge adapters; 03 §3.4) and the
facade-only key machinery (`PurposeFacade`, query-key extraction, `FacadeGroup`; SIMP2-6) are built in **no** release.
Their contracts are non-normative design sketches (A-15), and a package built too early is caught by review and by
Go's `internal/` rule (A-4). The storage-efficiency program that owns `storage_format` 2
(`internal/database/storage_format.go:44-50`) is a separate track. Its release B ships only as a books
`DataMigration` on core storage (R1 or later); if it ships earlier, 05-storage's legacy projection constant
(`LegacyMaxFormat + 1`) moves to cover it in the same release, so R2's projection still refuses every older build
(OPS2-10).

## 9.1 Current state at HEAD (facts the releases depend on)

| Fact | Evidence |
|---|---|
| One Pebble store, one WAL, one backup unit; 258 registered key families (243 namespaced, 2 bare, 10 legacy, 3 v1) | `internal/database/keyfamilies.go:37-345`; `raw/05-storage.md` §2–§3 |
| No family starts with `c/` or `m/`; a key outside every family is counted as `(unregistered)`, never deleted | `internal/database/census_families.go:128`, `keyfamilies.go:19-26` |
| `idx:` is ONE registered family shared by auth (`idx:user:`, `idx:role:name:`, `idx:sess:user:`, `idx:apikey:`, `idx:invite:username:`, `idx:oauth:`) and books (`idx:bv:`, `idx:col:name:`, `idx:ubs:status:`, `idx:upl:`, `idx:sidx:dirty:`, `idx:abs_sess:`) | `keyfamilies.go:64`; `pebble_store_auth.go:67,142,966,1126,1131,1335,1670`; `oauth_identity.go:32,78` |
| `setting:` is books' runtime-marker store (56 non-test `SetSetting` call sites), not only config | `internal/database/settings.go:316`; review DATA-1 |
| The storage-format guard refuses a store **newer or older** than the build; `make rollback` asks `.prev --print-storage-format` and reads `<db>.storage-format` before it swaps, so a raised projection makes it refuse | `pebble_store.go:670-680`; `Makefile:892-917`; `scripts/storage_format_guard.py` |
| The run dispatcher skips, and the resume sweep drops, a run whose definition ID the build does not register | `internal/operations/registry/dispatcher.go:113-118`; `resume.go:29` |
| Nothing scrapes `/metrics` in this repo; `/metrics` accepts any authenticated credential (an API key in practice) | `deploy/prometheus/README.md:9-10`; `internal/server/server_lifecycle.go:1735-1747` |
| Unversioned `/api/*` is 301-redirected to `/api/v1/*` unless excluded (v1, v3, health, events, metrics, ABS-reserved) | `internal/server/server_lifecycle.go:1790-1822`; `wire_abs_routes.go:308-345` |
| 13 permission atoms; 4 seed roles **rewritten every boot**; admin checked by role **name**; permissions resolve by **exact string match** over the role's stored list, so an unknown (namespaced) string grants nothing; an API key with **empty** scopes holds all of its owner's role permissions; key auth accepts only the `abk_` prefix | `internal/auth/permissions.go:24-85`; `seed.go:80-129` (called at `server_lifecycle.go:1102`); `middleware/auth.go:76-99,405-408,430-455,522-537` |
| Live access-widening defects, fixed by **the six early fixes of 05-authn §11.1** (SEC5-11): a key-row write without `apiKeyMu` can undo a revocation (early fix 6); a revoked key can be re-activated; the session list returns bearer tokens; a scoped key can mint an empty-scope key; `RequireAdmin` ignores key scopes; the Basic gate admits `<username>:` when the stored password is empty | `handlers/apikeys.go:336-372` → `pebble_store_auth.go:1222-1247`; `pebble_store_auth.go:1226-1248,1254-1268,1284-1302`, `middleware/auth.go:382`; `middleware/auth.go:335-344`; `handlers/auth.go:525-536`; `handlers/apikeys.go:199-210`; `middleware/auth.go:522-537`; `middleware/basicauth.go:81-87` |
| Config: ~251 viper defaults, `AutomaticEnv` with no prefix, the extra `BindEnv` names; the whole config persisted as one flat `config_blob` + 4 encrypted secret rows (OpenAI key, Google Books key, Hardcover token, Basic password); every other secret sits in the blob in plaintext | `internal/config/config.go:1048-1912,2621`; `cmd/root.go:625`; `persistence.go:884,1955-1958,1996-2008` |
| Precedence: an env name that HEAD reads through `AutomaticEnv`/`BindEnv` is only a **default**: the blob is laid over it and the encrypted secret rows over that, except the names in `envLockedSettings`, `applyEnvAuthoritativeConfig` and `SyncConfigFromEnv`; an env value set to `""` counts as unset. Compose always sets `BASIC_AUTH_ENABLED=${BASIC_AUTH_ENABLED:-false}`, `BASIC_AUTH_USERNAME` and `BASIC_AUTH_PASSWORD`, so a UI-enabled Basic gate works on compose only because of this order (OPS5-1) | `cmd/root.go:625`; `persistence.go:1131-1170,1186-1199,2023-2057`; `config.go:2241-2253,2435`; `docker-compose.yml:20-22` |
| State directory: with no explicit state dir set, `EnsureSecureStateDir` falls back to the database's directory when the compiled default cannot be created, so on Docker (unprivileged user, one data volume) and launchd installs `.encryption_key` lives beside the database | `internal/config/state_dir.go:118-155`; `Dockerfile:93-96,104`; `docker-compose.yml:17-22`; `deploy/com.audiobook-organizer.plist:36-43` |
| Shipped deploy files: unit `Type=notify`, `TimeoutStartSec=300`, **no `TimeoutStopSec`, no `KillMode`**, `Restart=on-failure`, `StartLimitBurst=5`/60 s, DB path only from `Environment=DATABASE_PATH`; compose sets `BASIC_AUTH_*`; plist sets `DATABASE_PATH`; Docker has no entrypoint script, `CMD … --db /data/audiobooks.pebble` and HEALTHCHECK `/health` | `deploy/audiobook-organizer.service:81-112`; `docker-compose.yml:18-22`; `deploy/com.audiobook-organizer.plist:36-43`; `Dockerfile:100-104` |
| Env names in shipped files that nothing reads: `AUDIOBOOK_ROOT_DIR` (unit, plist; deliberately unread), `AO_DIR`, `AO_DB` (compose), `OLLAMA_BASE_URL` (`deploy/local.conf.example:40`) | `internal/config/config.go:2569-2575`; no Go reader at the evidence commit |
| Warmup is 130–200 s against a hard-coded 300 s in `TimeoutStartSec`, `deploy-wait-ready.sh` default and `make rollback`; HEAD extends the start timeout per startup step (store open, each migration, index open); `deploy-wait-ready.sh` takes only `BASE_URL [TIMEOUT]` and runs on the operator's machine | unit `:28,87`; `scripts/deploy-wait-ready.sh:8-13`; `Makefile:916`; `internal/database/startup_notify.go:15-27`, `internal/search/register.go:55` |
| Both Dockerfiles copy only `go.mod go.sum` before `go mod download && go mod verify`; `.dockerignore` excludes `.git`, **`docs`**, root-level `*.md` (Markdown below the root stays in the context), `.github`, `testdata` and `dist`, among others, so a `//go:embed` of anything under `docs/` fails inside the image build (MOD4-1) | `Dockerfile:54-55`; `Dockerfile.build-cgo:52-53`; `.dockerignore:5-32` (`docs` `:11`, `*.md` `:30`) |
| Prereleases skip Docker; no PR job builds the image; the first image of a version is built by the production release | `.github/workflows/prerelease.yml:48`; `release-prod.yml:53` |
| `npm ci` runs in 12 places, none of which builds anything first (this is the one list of build sites; 07 §7.3.2 cites it, MOD5-13) | `Dockerfile:18`, `Dockerfile.build-cgo:17`, `prerelease.yml:51`, `release-prod.yml:58`, `ci.yml:379`, `e2e.yml:167`, `binary-smoke.yml:55`, `security.yml:82`, `vulnerability-scan.yml:62`, `.woodpecker/checks-build.yaml:85`, `scripts/ci_remote.py:516`, `Makefile:194` (`web-install`) |
| Updater: downloads `releases/latest`, treats any `latestVersion != currentVersion` as an update, deletes `.old` after the swap and restarts with `os.Exit(0)`, which `Restart=on-failure` does not restart; the maintenance-window op's step 1 applies too; under the committed unit (`NoNewPrivileges=yes`, root-owned binary directory) it cannot write next to the executable | `internal/updater/updater.go:153,254-258,278,295,318-326,331-334`; `internal/server/scheduler_maintenance_window_op.go:72-91`; `deploy/audiobook-organizer.service:41-43,88,122` |
| Live restore extracts over the running store | `internal/backup/backup.go:777-870` |
| Web client: `API_BASE = '/api/v1'`, no build or version check; 5 hard-coded permission constants and a legacy-atom scope picker; 52 source files import `react-router-dom` | `web/src/services/api.ts:24`; `hooks/useLibraryPermission.ts:9,11`, `hooks/useCanOrganizeLibrary.ts:9`, `hooks/useCollections.ts:24`, `components/system/ThrottlePanel.tsx:24`, `components/settings/APIKeysTab.tsx:49-61` |
| **Releases and merges are not CI-gated (OPS2-1).** Main merges without a pre-merge gate (CI runs afterwards and auto-reverts); an RC is cut on every push to main; `check-rc-ordinal` **auto-dispatches the production release** at 10 RCs of a base version, which builds the image and moves `releases/latest`, which the updater and `:latest` users follow | `.github/workflows/auto-revert.yml:8-11`; `prerelease.yml:7-13,59-131`; `release-prod.yml:43-65`; `updater.go:295` |

## 9.2 The R2 cutover model

Five rules, used by every release (03 §3.7, §3.8): (1) a core service never runs over HEAD's rows: in R2 a
books-owned step decodes them, writes them through the subsystem's importer and deletes them (§9.2.3); (2) every boot
that will migrate takes the pre-migration checkpoint first, and it is deleted 7 days after it stops being needed (§9.2.3); (3) R2 raises
the level stamps before its first rewritten row, so older binaries refuse a migrated store instead of misreading it
(§9.2.2); (4) nothing is dropped in silence: an undecodable row is quarantined, an unconvertible identity row stops
the upgrade (D13), and what D12 resets is in the release note (§9.6); (5) rollback across R2 is an offline restore
plus the pre-freeze release (§9.2.1).

### 9.2.1 Release safety: the merge gate, no soaks, rollback by restore

**How work lands (D11).** Trunk-based (D4: branch → PR → rebase merge). Native code lands **dark** on `main` across
many PRs, and **one flip PR** per subsystem makes it truth: it wires the subsystem in, deletes what it replaces,
carries the migration step of its families and switches every first-party caller in the same PR (§9.2.9), so every
`main` boots and passes e2e. Nothing records a flip: there is no stage marker, release-ID grammar, soak ledger,
release-gating commit trailer or stage record. There are no soaks. After a deploy the owner may run a 1-day smoke run
(R2's list is in §9.4 R2).

**The release freezes (D12 (a)).** From the merge of R2's first flip PR (F3) until the R2 release, automatic promotion is
off: `check-rc-ordinal` dispatches no production release (§9.1), so `releases/latest` and the `:latest` image stay on
the pre-freeze release and no install runs a half-flipped `main`. RCs are still cut, and none is promoted. Unattended
updates are check-only from R0 on (05-updater). The freeze is the repository variable `vars.RELEASE_FREEZE`, which
`prerelease.yml` reads when each run executes, not a value committed to the tree: the owner sets it to `R2` before F3
merges and clears it with the R2 release, and records each toggle in the tracking issue (D15, advisor S3 and S5). No
PR toggles it. F3-08 still commits `ci/prefreeze_release.txt`, the pre-freeze tag the cutover build embeds. Feature PRs keep merging and ship with R2. F2's foundation PRs land dark
before F3 and are outside the freeze. **R1 freeze (lead extension of D12 (a), IMPL-9; kept by the owner,
D15).** The same variable, set to `R1` by the owner before R1's first core PR merges and cleared with the R1 release,
pauses promotion from R1's first core PR until R1 ships, so no production minor carries part
of R1 (its perimeter move, the deleted reset routes, the first checkpoint) without R1's P12 check and release note:
R1 and R2 each ship as exactly one production release; a defect found during it is fixed the same way, from the last
production release before R1's first core PR. Between R1 and F3, production minors ship as usual.

**The pre-freeze release** is the last production release before F3 merges; once R2 is deployed it is also the `.prev`
binary. A production defect found during the R2 freeze is fixed by default in a release cut from the pre-freeze
release plus the fix, never from the R1 tag, because a minor after R1 may have run a books step that the R1 tag cannot
open (exit 78); the owner may decide otherwise for a given defect (§9.11). `TestCutoverRefusedByOlderBuilds` and the
restore drill (§9.3) run that release's binary.

**Release gating is a list of named required checks (R3-2; OPS4-7, MOD4-8, OPS4-6).** Every PR to `main` passes, on
GitHub's merge queue (rebase merges otherwise put an untested tree on `main`; D15), these required
checks:

| Check | What it runs | Trigger |
|---|---|---|
| `ci.yml` jobs mirroring root `make ci` | the per-module targets of R1's classification table (§9.4 R1), including the always-run embed-context check `scripts/ci/check_embed_context.py` (MOD5-7), which accepts an embed input excluded from the Docker context only when `scripts/ci/embed_build_outputs.txt` lists it as produced in-image by an earlier stage (MOD6-3); root `make ci` stays the local mirror (`/parallel-sweep` and developers run it) and needs no Docker | every PR; a path-skipped job skips through a job-level `if:` on the `changes` classifier, so a skip reports success instead of leaving the check "expected"; the classifier sets `go=true` **before** its docs glob for every Markdown file a Go test or `//go:embed` reads (OPS5-4) |
| `e2e` | the Playwright suite of `e2e.yml` against the built binary | every PR that touches a non-doc file (D11) |
| `extraction-gates` | ABO's crash-matrix instance `TestBootCrashMatrix_ABO` (core's `TestBootCrashMatrix` runs in `core-ci`); from the F3 PR on, the R2 cutover suite (§9.4 R2 "Verification"), whose production-shaped-store run executes only on `unit-lifecycle`'s triggers and nightly; the dashboard-reference test with `promtool test rules` (05-metrics §12); `scripts/ci/check_commits_build.py` on a PR whose diff renames a file into `core/` (MOD9-5); parallel jobs, each with `timeout-minutes` ≤ 30 (§9.2.10) | every PR that touches a non-doc file |
| `vet-darwin`, `vet-integration` | `go vet` over both modules, once with `GOOS=darwin CGO_ENABLED=0` **without** `embed_frontend` (its `//go:embed web/dist` fails on a tree with no built frontend, and `go vet` has no `-e`; the `docker-image` job compiles the tagged files, and no placeholder `web/dist` is ever created) and once with `-tags integration`, so every (`GOOS`, tag set) of 03 §3.5.1 item 4's list that a PR could break compiles in it; both in `ci/gates.txt` (R9-10, MOD9-3, R10-9) | every PR that touches a non-doc file |
| `unit-lifecycle` | `scripts/tests/test_unit_lifecycle.py` on GitHub's Ubuntu runner VM, where systemd is PID 1 (OPS10-7): it installs the committed unit with a drop-in that overrides only `ExecStart` and the database path and adds `PrivateNetwork=yes`, and runs **two cells**: a 12-minute fake migration under the committed unit is not killed (`EXTEND_TIMEOUT` per step) and `deploy-wait-ready.sh` does not report it failed; after a restore is staged, the service comes back | every PR that changes `deploy/**`, `cmd/**`, `main.go`, `internal/backup/**`, `internal/updater/**`, `internal/server/server_lifecycle*.go`, `internal/books/cutover/**`, `core/kernel/**` or `core/internal/restoreswap/**` (every R2 flip changes `main.go` or the kernel wiring), and nightly; outside the budget of §9.2.10 |
| `docker-image` | the PR image job of R1 (build, run with a seeded volume, probe), with layer caching | every PR that touches a non-doc file (OPS5-3) |
| `core-ci` | `make -C core ci` (`fix-check`, core-mode `exclude-check`, coverage ratchet, `replace-check`, `toolchain-check`, `check_core_version.py`, and core's own `errcheck-ratchet`, `interface-width`, `mocks-check`, `pprof-check` and the `core/web` `web-test`, with their scripts under `core/scripts/ci/`, so the gate an export of `core/` runs is whole; MOD5-15) | every PR that changes `core/**`, `go.mod`, `go.sum` or `.github/workflows/core-ci.yml` (one list; 12 SC4(b) cites it; MOD5-15) |

A release then contains only commits that passed. The list is enforced by a ruleset on `main` with **no bypass actors**
(`enforce_admins: true`) and **GitHub's merge queue**, whose `merge_group` trigger every required workflow carries; merge
tooling (`/parallel-sweep`, CLAUDE.md's Quick Fix flow) waits for the checks and never merges with `--admin` (D15, 14 Q-I;
K20). Tests are now the main safety (D11), so nothing bypasses the queue.

**Rollback (D12).** Rollback is an **offline restore plus the previous binary**. What there is to restore depends on
the release:

- **R0, R1 and R3 change no user data in the store**, so the restore part is empty and `make rollback`, HEAD's binary
  swap to `.prev` (`Makefile:892-917`) with R1's in-flight probe, is the whole rollback. R0 writes no new state. R1
  writes only core's `c/storage/` bookkeeping, which R0 ignores (`TestR0OpensR1Store`, 05-storage's one previous-build
  test), after the checkpoint its first boot takes on a non-empty store. R3 adds a boot-time floor and deletes two
  files beside the store through one named start hook, `.encryption_key` and HEAD's `library.bleve` directory, which R2
  no longer reads on a migrated store (§9.4 R3). Nothing written after the upgrade is lost.
- **Across R2 the store is migrated and stamped**, so HEAD's storage-format guard refuses `make rollback` (it reads the
  raised projection, §9.2.2) and the rollback is **`make rollback-restore`** (05-storage §5S.3.12): stop the service;
  compare the source's levels (the checkpoint record's `from_vector`, or an archive's levels) with
  `.prev --print-storage-levels` and refuse, with a message naming the release to install, when they differ, so that
  after an R2 hotfix whose `.prev` is an R2 build the recipe never re-migrates (DATA-9). The release named is the
  checkpoint record's `from_release`: the pre-freeze tag the cutover build embeds (HEAD's store records no app
  version; the release freeze makes the embedded tag exact). On the archive branch no record exists, and the refusal
  names the release that produced the archive, read from the archive manifest (for a `legacy-abo/0` archive, the
  version string the archive carries; 05-storage §5S.3.12). **After an R2 hotfix** the
  owner runs `make rollback-restore RELEASE=<pre-freeze tag>`: it calls `<app> updater fetch-release --version <tag>
  --out <path>` (05-updater §5U.4), which fetches the named release from the configured release source, checks the
  fetched binary's levels against the source's levels (the checkpoint's `from_vector`, or the archive's levels) before
  renaming it to `.prev`, and then proceeds; without `RELEASE` it refuses and names the tag, and it takes no other
  binary (05-storage §5S.3.12). The fetch has apply's trust level: HTTPS from the configured release source, plus the
  checksum check apply performs if the source publishes one; it is not a signature check (05-updater §5U.4);
  run the current binary's `storage restore-offline --checkpoint <latest> --accept-loss` (the checkpoint holds the
  store and the state files outside the DB directory, `.encryption_key`, the gap-fill `<db parent>/config.yaml` and,
  once a boot has created it, the keyring, which the first R2 boot creates only after its checkpoint; it lives 7 days), or `--archive <file> --accept-loss` with the owner's off-host archive (a pre-R2 archive holds no state
  files: `.encryption_key` is still on the host until R3, and the gap-fill `config.yaml`, which M2 stripped, comes
  back from the owner's off-host copy, as both do after a host loss), and for an archive the recipe compares `.prev`'s levels with the levels read from the
  archived store itself, not with manifest stamps (05-storage §5S.3.12); a restore replaces only the state files its source
  holds and never removes a keyring the source lacks; reinstall the previous binary from `.prev`; start. Its one probe refuses while a
  swap is in flight (`restore_inflight`), and nothing overrides it: boot the current build once to finish the swap.
  The one exception is a checkpoint restore boot whose migration failed (journal `failed`, 05-backup §5B.3.4 step x):
  the runbook stops the service and runs the **previous** binary's `storage restore-offline --checkpoint <name>` (any
  release from R1), the one recipe admitted while `inflight.json` exists; on success it writes `applied_offline` with
  `HooksOwed` set when the store it restores still owes its hooks (DATA-7) and deletes `inflight.json`, and
  the previous binary is installed and started (05-storage §5S.3.12). A
  staged intent that has not started is rejected by the restore itself, which supersedes it.
- **What a restore costs.** It discards everything written after the upgrade: imports, edits and the listening progress
  the phone apps synced. It also undoes every revocation made on R2: an API key revoked, a user disabled or a password
  changed after the upgrade is live again on the pre-freeze release, so after a compromise the owner repeats those
  revocations once the rollback is up (SEC-10). Fixing forward is therefore the default, and the decision to roll back
  belongs to the smoke day. The runbook says both plainly.

### 9.2.2 The store guard: level stamps, the refusal and the R3 floor

There is **one** guard mechanism — per-owner reader levels, owned by [05-storage](05-storage.md) §5S.3.7: the level
stamp `c/storage/level/<owner>`, its sidecar `<db>.storage-levels`, the fast exit `--print-storage-levels`, and books'
**legacy projection** onto HEAD's format stamp (`preference:storage_format` and the sidecar `<db>.storage-format`),
which makes every pre-core build, HEAD's `make rollback` guard included, refuse a store once a level was raised. R1
ships it in core `storage`, with the re-home of `internal/database`.

**Policy.**

- **R0 and R1 raise no level.** Every owner is served at level 0 and the projection stays at HEAD's format 1, so the
  previous binary opens the store and `make rollback` passes. R1's first boot on a non-empty store still takes a
  checkpoint (§9.2.3), which its sweeper deletes after 7 days.
- **R2 raises `books` and `core`** in its level step M7, before the first rewritten row (§9.2.3), and sets the
  projection to `LegacyMaxFormat + 1`. From that batch on, every older binary refuses the store at open, before any
  write: HEAD and R0 through HEAD's storage-format guard, R1 through the level guard, which exits 78 and names both
  levels and the release to install (`TestCutoverRefusedByOlderBuilds`).
- **R3 raises nothing.** It declares books' one upgrade floor (below).
- Otherwise a level rises only for a real format change: books' own migrations (the storage-efficiency program's
  release B, §9.0). The single metric is `<namespace>_storage_level{owner,kind}` (05-storage §5S.8).

**What the store records** (written by its own boots, never by a person; mechanism 05-storage):

| Record | Written by | Read by |
|---|---|---|
| install marker `c/storage/install` | the first R1 boot, **before any other `c/` key** (DATA2-6); a fresh install writes it in its fresh-install batch | 05-storage §5S.3.4 rule 4: a later boot finds `c/` non-empty only with the marker |
| level stamp + sidecar `<db>.storage-levels` | a level step only (R2's M7); every boot rewrites the sidecar from the stamps (OPS-26b) | open, which refuses a store above the build's level; `--print-storage-levels` |
| legacy projection `preference:storage_format` + `<db>.storage-format` | M7, in the same batch as the stamps | HEAD's and R1's open; HEAD's `make rollback` guard |
| store generation `c/storage/epoch` | every swap, copy or in-place restore | derived on-disk indexes, which rebuild when it changes (05-storage §5S.3.9) |
| checkpoint record `<name>.json` and `<db path>.migration-checkpoint` | the boot that takes the checkpoint (§9.2.3) | `storage restore-offline --checkpoint`; the 7-day sweeper |

**Fresh installs (DATA2-28, DATA3-21).** On the boot that creates `c/storage/install` on an empty store, the marker,
every owner's level stamp at the build's level and the applied record of every registered step below each owner's
lowest `Seed` step are written in **one** batch, so a crash leaves either an empty store or a fully stamped one. Books'
fresh-install batch records its legacy steps 1..`LegacyCap` as applied and every step ID that a declared
`UpgradeFloor.Requires` names, M2–M6, as satisfied, whether or not the build still registers it, so a fresh install or
a factory reset is never migrated and passes R3's floor on every boot, in R3 too, which no longer registers M2–M6
(`TestFreshInstallPostFloorBootsTwice`; IMPL-7).

**The R3 floor (02 N14).** R3 deletes books' legacy steps 1..66 (`LegacyCap`), the HEAD-format readers and the cutover
steps, and declares `UpgradeFloor{InstallFirst: <R2 tag>}`:

- Boot refuses a store that has not run M7 with `ErrUpgradeFloor{InstallFirst}`, before any write. An empty store is
  exempt (above).
- The refusal exits **78** and prints `install <R2 tag> first` as its last stderr line, that is "install the R2 release (its tag)
  first": `InstallFirst` holds the R2 release's tag, written into books' floor declaration when R2 is tagged
  (05-storage §5S.3.5); under Docker (no entrypoint
  script, `Dockerfile:103`) that line in `docker logs` is the message. The committed unit carries
  `RestartPreventExitStatus=78` from R1, so systemd does not restart it. launchd has no per-exit-code setting, so the
  binary sleeps 60 s before exiting 78 when it detects launchd (`XPC_SERVICE_NAME` set), and the R3 release note tells
  macOS users to `launchctl bootout` the job and Docker users, whose `restart: unless-stopped` restarts it, to stop the
  container. `deploy-wait-ready.sh` reports `ExecMainStatus=78` as "install the release the log names first", not as a
  crash loop (OPS6-5). The level guard's refusal of an older binary uses the same exit code.
- `--print-upgrade-floor` (from R1; empty until R3) prints the requirement. Nothing checks the floor before a binary
  swap: neither the updater nor the deploy recipe reads the store (03 §3.7).
- A restore stages an archive only if it meets the build's floor: staging refuses a pre-R2 archive with
  `ErrUpgradeFloor{InstallFirst}` before it extracts anything (05-backup §5B.3.4), so a fresh host never ends with an
  empty store. The host-loss procedure for such an archive after R3: install R2 (the floor refuses the archive in R3),
  put the archive's off-host `.encryption_key` and gap-fill `config.yaml` into the state directory and beside the
  database, restore the archive with `storage restore-offline --archive`, start it once so that it migrates, then
  upgrade (CONFORM-4, DATA-16). Without the off-host `.encryption_key`, M2 refuses to start, because R3 deleted the host's copy.
- `LegacyCap` is the highest HEAD-style books step when R1's storage move lands (66 at the evidence commit). Every later
  books step is a versioned `Migration` or `DataMigration` numbered from `LegacyCap+1`, and `TestNoNewLegacyMigrations`
  pins the legacy track until R3 deletes it.

### 9.2.3 Data: the one-way migration

**Who does what.** Each core subsystem keeps its store interfaces with native implementations only (other
apps and `coretest` use them) and gains an **importer** (03 §3.3 "Store interfaces and importers"). The books package
`internal/books/cutover` holds one migration step per subsystem: it decodes HEAD's rows, which only books knows, and
calls the importer, so core still holds no ABO key name or row format. Each step is a books `BootBlocking` data
migration, chunked and resumable: in **one batch per chunk** it writes the importer's rows and deletes the HEAD rows it
decoded, so a killed boot resumes from the step's cursor and no record exists in both forms (05-storage §5S.3.13).
Books' own domain families, about 240 of them, stay in place under books' claims (05-storage §5S.9.1): renaming them
buys nothing.

**The cutover preflight (D13).** Before it takes the checkpoint, an R2 boot of a pre-R2 store whose cutover has not
started reads, without writing: every user, API key and OAuth identity row, which M3 must convert, and every
`usermerge:journal:` record. The cutover has started when books' level has reached M7's target or any cutover step
has a progress record in `c/storage/dm/`; the preflight runs if and only if it has not started, and checkpoint reuse
is not part of that condition (05-storage §5S.3.13 "When"). Invite allow-list rows are not checked: one that M3 cannot convert is quarantined. A boot
resuming a started cutover skips the preflight: M3 resumes from its chunk marker, and its own defensive check applies
only to the rows it has not yet imported; its refusal names `make rollback-restore` as the way out, and once the
cutover has started `storage cutover-blockers` no longer opens the store (05-storage §5S.3.13). The preflight refuses the boot, writing nothing, when an identity row cannot be converted or a merge journal is
`applying` or `undoing`; its report and the log name each row or merge. Because nothing was written, `make rollback`
to `.prev` is still a binary swap. The owner then finishes or undoes the merge on the pre-freeze release, and fixes
or deletes each named row with R2's offline command `storage cutover-blockers` (or on the pre-freeze release), and
starts R2 again: `storage cutover-blockers list` prints each blocking row's key and reason only, and
`list --out <file>` writes the raw row values to a file of mode 0600, because `fix --from` needs them, with a printed
warning that the file holds credential hashes and should be deleted after the fix; `storage cutover-blockers fix <key> --from <file>` replaces
one row with a corrected value; `storage cutover-blockers delete <key> --confirm` deletes it. Identity rows are never quarantined-and-continued. The dry run (below)
reports the same rows and merges before the deploy. Finished merges become permanent: their journals stay books'
records, and no merge is undone after R2: an undo of a merge finished before R2 answers 409 `merge_permanent`
(05-authn §11), and the release note says so. 05-authn §11 and 05-storage §5S.3.13 own the
checks.

**Order.** In the kernel's migration phase (06 §6.5.1 P4), after the checkpoint and `secrets.Open` (which creates
the keyring), every R2 boot of a pre-R2 store runs:

| Order | Step | What it does | Rules owned by |
|---|---|---|---|
| 1 | **M1** | Books' legacy steps 1..66. They run only on older stores, and they already exist. | 05-storage §5S.3.5 |
| 2 | **M7** | Level step: `books` and `core` raised (sidecar first) and the projection set to `LegacyMaxFormat + 1`, in one batch, **before M2's first chunk**. HEAD, R0 and R1 refuse the store from this batch on (§9.2.2). | 05-storage §5S.3.7 |
| 3 | **M2** | Config and secrets: the fields of `setting:config_blob` into `c/config/` under canonical keys (the rename table, §9.2.5); the plaintext blob secrets and HEAD's four encrypted rows re-encrypted into `c/secrets/` under the keyring; M2 moves only the blob's fields and rewrites the residual blob, which stays an active books claim until M6 retires it (DATA-1); the gap-fill `config.yaml` keys imported once, then the file stripped of its fifteen managed keys and deleted if empty (M2b; the checkpoint already holds a copy, 05-storage §5S.3.12). The blob's integration-instance fields stay for M6. A missing `.encryption_key` fails the boot before M2 writes anything (HEAD's guard); `.encryption_key` stays until R3. | 05-config §1.12.2 |
| 4 | **M3** | Identity: users, OAuth identities, the invite allow-list, API keys and roles into `c/auth/` and `c/rbac/`, every ID kept; `abk_` keys keep their SHA-256 hashes; atoms mapped to native scopes by books' table, with the ambient rule (§9.2.6); statuses normalised as HEAD's predicates read them (trimmed, lower-cased, an empty user status active; SEC-11); web sessions, invites and the bootstrap hash deleted. Only the auth sub-prefixes of `idx:` move; `abs_sess:` and the other ABS families stay books'. The preflight above has already refused, before the cutover started, a store with an unconvertible identity row or an unfinished merge (D13); on a resumed cutover M3's own check covers the rows it has not yet imported. | 05-authn §11; 05-rbac §6 |
| 5 | **M4** | Tasks: every run (open, paused and finished) with its checkpoint and HEAD's repairs rows (`opv2:row:`, `opv3:rowmark:`, imported with their run; DATA-3), the schedule clocks, the pause and stand-down markers and the `Requires` stamps into `c/tasks/`; stored kinds renamed by the kind-rename map, in the books families 05-storage §5S.9.1 lists as rewritten in place too (DATA-5). Only the run keys of `setting:` move. | 05-tasks §5T.8 |
| 6 | **M5** | The `syslog:` rows into `c/log/`, converted. | 05-logging §5 |
| 7 | **M6** | Integrations: the blob's instance fields with their secret refs into `c/integrations/instances/`, then the config blob deleted; `aijob*` rows into `c/ai/`; HEAD's webhook settings into native notify instances and their rule, the HMAC secret by value; import roots into `core.library.roots` entries, IDs kept; HEAD's search dirty-set rows deleted, because the full-text index is rebuilt at its new path; throttle and quota rows deleted. | 05-metadata M1.10; 05-ai §10; 05-notify §10; 05-library §5L.10; 05-storage §5S.3.8 |
| 8 | **M8** | The store rewritten into a fresh directory (full compaction, flush, then a Pebble checkpoint or copy) and swapped in through the journaled swap, the old directory deleted, so no file of the live DB directory, WAL, recycled log and MANIFEST included, holds a HEAD row or a raw token (SEC-5). | 05-storage §5S.3.13 |

M7 and M8 land with F3 and run in every build from then on; each later flip that adds a step raises M7's target in the
same PR, so a `main` refuses a store that a newer `main` migrated; M8 keeps one fixed ID, `books.rewrite`, and stays
last (03 §3.8; D15, advisor S2). The runner they use, `storage/datamigrate` and `internal/storagemigrate`
over `tasks.ForEach`, lands at F2 (§9.4 R2). M7 runs before M2 because a store that
stops between M2 and a later level step would hold deleted config and identity rows at level 0, which HEAD's
`make rollback` guard and the R1 binary would both accept (03 §3.8).

**What survives and what resets (D12 (b)).**

- **Kept, converted:** settings and every secret; users, roles, OAuth identities, the invite allow-list and API keys
  (the `abk_` tokens keep verifying); integration instances, import roots and AI jobs; every task run and its history;
  system-log rows. Library data and reading progress are books' families and do not move.
- **Untouched:** the ABS families (`abs_sess:` refresh sessions, progress, bookmarks) and the value of the ABS JWT
  secret, so the phone apps stay signed in.
- **Reset:** web sessions, invites and one-time tokens (everyone signs in to the web app once, and open invites are
  re-sent); throttle and daily-quota holds; the full-text index (rebuilt at its new path; HEAD's `library.bleve`
  stays for a rollback restore and is deleted in R3), derived indexes and caches, which rebuild after the boot.

**IDs and references.** Every user, role, key, import-root, instance and run ID is kept, because books' rows
reference them; a referential-integrity check over the migrated corpus proves that no reference dangles (§9.4 R2). The
referential `Verify` excludes quarantined IDs and reports each dangling reference in health instead of failing boot
(DATA-2); identity rows never reach it unconverted, because the preflight or M3's own check stops the upgrade first. The kind IDs that books'
rows carry are renamed by the same map as the runs, in exactly the families 05-storage §5S.9.1 lists (DATA-5).

**Shared families.** `idx:` and `setting:` hold both core-service and books rows (§9.1). Only listed sub-prefixes
move. The lists are generated from `keyfamilies.go` and the census, and each step checks that every range outside its
list is byte-identical before and after; `TestCutoverLeavesOtherFamiliesByteIdentical` excludes exactly the books
families and keys that the kind rename and M3's and M6's extra writes touch, which 05-storage §5S.9.1 names (DATA-5).

**The checkpoint and its 7 days (D12 (d)).** A boot takes the pre-migration checkpoint only when a step will
actually run (a legacy or versioned step, a cutover step, a level step, or the install marker on a non-empty store); it
is the boot's first store write, at P4a. Before taking it, the boot checks the disk: free space must cover the
checkpoint, the rewritten ranges and the compaction together, or boot halts with the numbers before any write (P12). A
checkpoint is reused only by a crash loop of the **same** upgrade attempt on the **same** store identity, and
`storage restore-offline --checkpoint` restores only the latest attempt's checkpoint (05-storage §5S.3.12). The
checkpoint is a full pre-R2 copy of the store and its state files beside the DB, holding plaintext third-party
secrets and raw session tokens, in a directory of mode 0700 with files of mode 0600 (SEC-9), so it is **deleted
automatically 7 days after it stops being needed** (CONFORM-2, DATA-8), by a sweeper that runs once at every boot, after readiness (05-storage §5S.4
step 15), and hourly while serving: a completed checkpoint 7 days after its attempt completed; an open checkpoint is kept while the live
store is still at its pre-migration levels, because it is the rollback, and its 7-day clock starts once the store has
completed a migration or been restored by any path (the checkpoint, an archive, a failed restore's `failed/` copy,
`restore_left`); health raises `storage_checkpoint_open` meanwhile. `storage prune-checkpoints` deletes one earlier.
The sweeper and the command exist from R1, whose first boot on a non-empty store takes a checkpoint, and the R1 and
R2 release notes say so. After that, only the off-host archive goes back.

**Quarantine.** A row that a step cannot decode, or that an importer rejects, is **quarantined**: its raw bytes are
copied under `c/storage/quarantine/`, its family and key are counted in the step's report, health reports it, and boot
continues, so one bad row never crash-loops the unit and nothing is dropped in silence. 05-storage §5S.3.13 owns the
format and how an admin clears an entry. **Identity rows are the exception (D13):** a user, or one of its keys or
identities, that cannot be converted is never quarantined; the preflight above stops the upgrade instead. A secret
row the key does not decrypt is quarantined, and its secret reads "re-enter" in health; a quarantined
row of a secret-bearing family is sealed with the keyring, never stored raw (05-storage §5S.3.13);
a missing `.encryption_key` is not quarantined but fails the boot before M2 writes anything (05-config §1.12.2).

**Dry run.** The preflight and every step run under `audiobook-organizer storage migrate --dry-run [--from-archive <file>]`,
the real runner on a throw-away copy in the scratch area `.mediacore-checkpoints/scratch/` under the checkpoint
directory (directory 0700, files 0600, deleted on completion, sealed with a throw-away keyring created and deleted
with it; 05-storage §5S.3.6), which reports the preflight's blocking rows and unfinished merges, per-step
counts, quarantined rows, bytes written and duration and writes nothing to the live directory (05-storage §5S.3.5; `TestDryRunMatchesLiveRun`).

**A long first boot.** The first R2 boot reports each step to systemd (`EXTEND_TIMEOUT` per step, as HEAD already
does per startup step), so `TimeoutStartSec` does not kill it, and every step resumes after a kill. The R2 release note
gives the measured duration (§9.3) and the Docker `start_period` to set.

### 9.2.4 HTTP surfaces

The mechanism is 05-http-api (§8 `MountLegacy`, §4.8 the collision rule). Policy:

- **Core routes exist only under `/api/core/v1`**, each from its R2 flip. The flip PR deletes the gin routes that the
  native ones replace and switches the SPA, `scripts/` and the agent skills in the same PR: there is no alias, `Gone`
  step or `Deprecation` header, and nothing serves the old path afterwards (§9.6 lists them).
- ABO's gin engine stays mounted **permanently** through `httpapi.MountLegacy`. Books' routes keep `/api/v1/...`; a
  books route group moves to `/api/books/v1/...` only when it is rewritten for its own reasons, with its callers in
  the same PR. HEAD's `/api/*` → `/api/v1/*` 301 stays, carried by the mount's `Redirects`; from R1 it excludes
  `^/api/(core|[a-z][a-z0-9]{1,15})/v[0-9]+(/|$)`.
- The ServeMux collision rule and the real-engine manifest test (`TestLegacyManifest_RegistersAsMuxPatterns`, ABS on
  and off) stay, with no exception for the operations routes: HEAD's task routes go with F5, the tasks flip.
- The ABS routes are the one kept contract (§9.6). The `oasdiff` breaking-change gate protects core's own OpenAPI
  within a major for other apps (05-http-api §4.6); it does not protect ABO's routes.

### 9.2.5 Config keys and environment names

The mechanism is 05-config (§1.3.5 precedence, §1.3.6 grammar, §1.12.3 the rename table, §1.12.4 the names after
the cutover). Policy:

- **Canonical names only.** One generated **rename table** maps every HEAD flat key, env name and blob field to its
  canonical key and env name; M2 uses it, and the R2 release note lists it. Ecosystem names (`OTEL_*`,
  `OPENAI_API_KEY`, `OPENAI_BASE_URL`) are canonical bindings and do not change. The shipped deploy files (unit,
  plist, compose, `deploy/local.conf.example`, Dockerfiles) use canonical names from F3's PR on, and a test asserts
  that they use no other; the names nothing reads (`AUDIOBOOK_ROOT_DIR`, `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL`) are
  removed in the same PR.
- **A retired name that is still set.** For a key at `ProtectAdmin` or higher (database path, sign-in, server paths) or
  a secret-class key, boot refuses and names the canonical variable; storage also refuses to *create* a store at the
  resolved path while a retired database-path name is set, because an ignored `DATABASE_PATH` would open an empty
  store at the default path (14 K15). Any other retired name logs one WARN at boot. `audiobook-organizer config check`
  lists every retired name it finds.
- **Strict decoding.** A value that does not parse under the canonical grammar is a boot error naming the variable;
  `ENABLE_AUTH=yes`, which HEAD read as `false`, is one.
- **Precedence (from F3).** An operator value locks its key. For the auth and Basic-gate keys one
  fail-closed rule applies: the more restrictive of the operator and the stored value wins, and health reports the
  disagreement, so compose's always-set `BASIC_AUTH_ENABLED=false` (§9.1) cannot switch off a gate enabled in Settings
  (05-config's `TestFailClosed_MoreRestrictiveWins`, §1.11). Compose passes the Basic-gate variables only when they are set.
- **Files beside the DB.** M2 imports the gap-fill `config.yaml` once and strips its managed keys (M2b), and core writes
  no gap-fill file. The state
  directory keeps HEAD's fallback beside the DB (`TestStateDirFallbackMatchesHEAD`), so on Docker and launchd
  `.encryption_key` and `secrets.keyring` sit beside the database.

### 9.2.6 Permission atoms

The mechanism is 05-rbac (§6 the migration, §6.1 the table) and 05-http-api §8.3 (the bridge). Policy:

- **Stored native scopes.** M3 stores native scopes on every role and key; HEAD's 13 atoms map through one books-owned
  table, which is code, not stored data. The gin routes keep calling `s.perm(atom)`, and from F4 stage 13 enforces the
  table's requirement over the stored scopes.
- **The ambient rule.** HEAD let any credential reach `/metrics`, the profile and reading state. M3 stores
  `core:self`, `core:metrics.read` and `books:reading` on a principal iff it is not `Confined()` (HEAD's worker
  confinement, `internal/server/middleware/auth.go:268-297`), so the scrape key keeps working and worker keys stay
  confined (`TestWorkerKey_Confined`, `TestIdentityCutover_WorkerKeyStaysConfined`).
- **Keys and roles.** An `abk_` key with empty scopes, which holds all of its owner's role permissions at HEAD (P3), is
  converted per 05-rbac §6 and never stored empty; a new key never has empty or wildcard scopes. HEAD's `admin`
  seed and every key whose owner holds HEAD's `admin` role (the bootstrap key, an admin's every-atom or empty-list key)
  map to `core:admin` plus every native scope of the 13 atom rows, task-run approval included, so the reach oracle
  shows no loss; every other holder of every atom (a custom role, or a key owned by a non-admin) gets the union of the
  13 rows, a key then intersected with its owner's role set, never `core:admin` (05-rbac §6.1; SEC-12). Core seeds the four
  seed roles, and their IDs (`admin`, `editor`, `viewer`, `fp-worker`) are kept, so `User.Roles` and invite role
  references need no rewrite. An atom outside the table maps to nothing, and its role converts and is reported (P2).
- **The reach oracle.** Allow and deny for every gin route (the ABS routes included), every seed and custom role,
  scoped keys and auth-off, computed by the R0 binary on its fixture corpus (with its hand-built overlay, §9.3) and by R2
  on the migrated corpus, are equal except the recorded changes below. The baseline is R0, that is HEAD plus the six
  early fixes, never HEAD itself (SEC-13); the auth-disabled principal is also checked against every core route, not
  only the HEAD router dump. It runs on F4's PR and stays as the table's regression test (05-rbac §12).
- **Recorded changes (release notes).** With auth off, every `Guards.Interactive` route (credential, key,
  temp-login and restore routes) refuses the auth-disabled principal, so do the API-key, session-list and audit routes
  (SEC-3), and the backup routes answer 401. The credential-metadata routes (`/me*`, `/api-keys*`,
  `/users/{id}/sessions*`, `/users/{id}/api-keys*`) and the audit routes answer 401 too, while the settings routes keep
  their auth-off reach (05-rbac §4.11): "With authentication off, credential, API-key, session, audit and backup
  routes now refuse requests; turn authentication on to use them." Playlist writes stay open to every signed-in user,
  as at HEAD: `books:playlists.create` is not enforced on them (SEC-4). ABO's
  `core.auth.disabled_allow_remote` defaults to `true`, with health Degraded `auth_disabled_remote` (05-rbac
  §4.10–§4.11).

### 9.2.7 Metrics

- Core takes its namespace as `app.Options.MetricNamespace`, which ABO sets to `audiobook_organizer`; new core series
  are `<namespace>_<subsystem>_*` (03 §3.6).
- A moved emitter may take core's name. Renames land only in R1 (the HTTP series), F5 (the ops families: HEAD's
  `operations_*`, `operation_*` and `op_*` deleted and the `ops_*` label changes) and F7 (HEAD's AI families, which
  core's AI families replace); F8 only moves the kernel `/metrics` mount (05-metrics §13 lists each; 05-ai §8;
  XFILE-1). Pebble's collector moves in R1 under the same names. Core's executor lands dark at F2 and creates no `ops`
  instrument before F5. Books' two process gauges stay books' series,
  unchanged. A rename and its edits to `deploy/grafana/dashboards/*.json` and
  `deploy/prometheus/{alert-rules,recording-rules,alert-rules.test}.yml` are one PR. The dashboard-reference test (every
  series those files name is served; 05-metrics §12) and `promtool test rules` gate it, and no series is renamed for
  nothing. Every family has one owner: a plain gather returns no error and no duplicate family (05-metrics §6).
- From R1 core's stage 2 records every HTTP request, gin's included, and `otelgin` leaves the mount;
  `httpapi.MatchedPattern` stays only for the post-match check (05-http-api §4.2 stage 2). The R1 release note says what
  moves: a request refused before a route (rate limit, body limit, CORS, the Basic gate and, from F4, authentication)
  carries no `http_route`; preflights and Basic-gate challenges are counted for the first time; `http_route` values
  follow core's route patterns; `http_server_response_body_size` measures bytes before compression.
- HEAD's AI families (`ai.*`, `ai_dispatch.*`) become core's `<namespace>_ai_*` series at F7, with their dashboards
  and alerts in the same PR (05-ai §8, §10).

### 9.2.8 Switches: none on state shape, a few operational dials (OPS-17)

**State-shape switches: none.** The cutover runs unconditionally at the first boot of a build that carries its steps,
and no flag selects a stored format.

**Operational dials** never change persisted shape; each is a declared config key owned by its spec:
`core.storage.claims.mode` (`strict` by default from R1; `shadow` is the emergency dial, and violations are logged,
not persisted; 05-storage), `core.tasks.kinds` (one map keyed by task kind, entry `parallelism_cap`; 05-tasks),
`core.outbox.relay_enabled`, per-provider throttle overrides (05-metadata M2), `core.updater.mode` (05-updater) and
`core.integrations.disabled`. ABO's own switches (`abs_api_enabled`, `enable_auth`) keep their meaning under their
canonical names.

### 9.2.9 Renames and the release note

- **One PR per rename.** The renamed thing and every first-party consumer — the SPA, `scripts/`, the repo's agent
  skills (`server-bootstrap`'s `POST /api/v1/auth/bootstrap` among them), the deploy files (the Dockerfile
  `HEALTHCHECK` and `deploy-wait-ready.sh` included), the dashboards and rules, the docs — change together, so
  nothing on `main` uses an old name (D10); no consumer waits for R2's last PR, which carries only the runbook and the
  release note.
- **The R2 release note** is assembled from the rename table (05-config §1.12.3), the kind-rename map (05-tasks §5T.8),
  the deleted routes (05-http-api §8.1), the metric renames (05-metrics §13) and §9.6, and it quotes each owner's text
  verbatim, 05-authn §11's identity text among them. R0, R1 and R3 carry the lines their tables give.
- Nothing counts or records the use of an old name. HEAD's `docs/compat-surfaces.md` is left as it is.

### 9.2.10 Feature work beside the extraction (A-11; ADV-7)

ABO keeps shipping features while the extraction runs (parallel sessions; a production minor every 10 RCs outside the
R1 and R2 freezes, §9.1, §9.2.1). Three rules keep the two from colliding; 14 K24 cites them.

- **Announced move windows.** A PR series that moves or re-homes packages (R1's leaf moves and its `internal/database`
  re-home, about 850 direct engine call sites at the evidence commit; R2's handler groups and integrations) runs inside
  a **move window** announced in the tracking issue at least one working day ahead: it names the 04 rows the window
  moves, which are **frozen** for its duration, and its end. A window lasts no longer than its move series and targets
  at most two working days. While it is open, feature PRs that touch a frozen package are held, not merged (agents and
  `/parallel-sweep` check the announcement), and the move PRs merge first. Packages outside the window are not frozen.
- **Rebase onto a move.** The move PR's description lists its `old ==> new` package paths, for people (12's history
  split uses git rename detection, A-4). A held feature branch then: (1) fetches `main` after the move merged; (2) runs
  `git rebase origin/main`; (3) rewrites its own new import paths from that list and runs the move's mechanical
  rewrites (04 §2.2) on code it added in a moved package; (4) runs `go build ./... && (cd core && go build ./...)` and
  `make ci`; (5) re-applies by hand, with a test, any code it added to a part 04 §8.6 lists as re-implemented. Feature
  PRs never edit or regenerate 04 (A-4).
- **CI-time budget per PR (OPS9-13).** The required checks of §9.2.1 on a non-doc PR finish within **35 minutes**
  wall-clock, the longest job timeout `ci.yml` sets today (`ci.yml:667,859`), which already holds a 25-minute
  `go test -timeout`: `extraction-gates` runs as parallel jobs, each with `timeout-minutes` ≤ 30, beside the `ci.yml`
  jobs, `e2e`, `docker-image`, `vet-darwin` and `vet-integration`, with image layers cached. `unit-lifecycle` and the
  production-shaped store run only on their triggers and are outside the budget. When the budget is exceeded, the
  remedy is sharding or caching; only a check that no rule here requires may move to the nightly run, and the crash
  matrices and the cutover suite never move. The owner may change the figure (§9.12 O11).

## 9.3 Release overview and the reduced rehearsal

| Release | Ships | Migrations | Rollback | Owner |
|---|---|---|---|---|
| **R0** | the hotfixes | none | `make rollback` | P14; deploy; smoke (optional) |
| **R1** | the core runtime, no user-data change | core's `c/storage/` bookkeeping only, behind the first checkpoint | `make rollback` to R0, nothing lost | the R1 freeze variable set (D15); deploy; smoke (optional) |
| **R2** | the foundation PRs F2, then every core-service family, its UI, the shell if ready | M1, M7, M2–M6, M8 (§9.2.3) | `make rollback-restore` to the pre-freeze release, losing every write since the upgrade | finished merges and a clean preflight (D13); off-host archive; deploy and its external steps; dry run and smoke (optional); keep or roll back |
| **R3** | cleanup and the floor | none; one start hook deletes `.encryption_key` and `library.bleve` | `make rollback` to R2 | keep the off-host `.encryption_key` with any pre-R2 archive; deploy; smoke (optional) |

**Order.** R0 → R1 → R2 → R3 are strictly ordered. R2's content lands dark on `main` while R1 is deployed. R2's flips
are serialized in the order of §9.4 R2, because they share `main.go`, the kernel wiring and `internal/server`, and the
release freeze covers them (§9.2.1); F2's foundation PRs land dark before them. R1's own PR series is covered by the R1
freeze (§9.2.1). The shell (F9) ships in R2 if it is ready, otherwise in R3.

**Every release's done-check includes** `make ci`, the e2e suite and the required checks of §9.2.1 on every PR; the
release tables repeat only release-specific checks. **Counts in done-checks are generated** (ADV-11): every literal
count (routes, seed roles, definitions, importing files, families) is informative, measured at the evidence commit,
and the check uses the value generated at its base commit. **Boot writers extend the crash matrix** (A-8): every PR
that adds a boot-time durable write, rename or fsync, a cutover step included, adds its row to 06 §6.5.1's writer
table and extends `coretest/crashmatrix` (05-storage §5S.3.16) in the same PR: `TestBootCrashMatrix` for a kernel
writer, `TestBootCrashMatrix_ABO` for a books one.

**Boot time.** One measurement, on the production-shaped store below and on the owner's dry run if it runs,
records the R2 binary's time per migration step
(`<namespace>_kernel_boot_phase_seconds{phase,step}`, 06 §6.9) and its steady boot (restart to `"ready":true` on
`/health`). The result sets the R2 release note's expected first-boot time and the `DEPLOY_READY_TIMEOUT` the runbook
gives (default 300 s, counted from when the unit leaves `activating`).

**The reduced rehearsal.** There is no staging host, scrubbed copy, staging marker or previous-build rehearsal. What
replaces them:

1. **In CI (agents), on every R2 PR.** The fixture corpus is written **by the R0 binary itself**: a committed script
   drives the R0 release binary through its API over a seeded store. A second, separate and labelled stage then adds a
   **hand-built overlay** for the cases R0's API cannot write, each case listed in the corpus' manifest: a program
   built at the R0 tag against HEAD's own `internal/database` writes corrupt rows, a blob field no rename-table row
   names, the custom role rows 05-rbac §6.3 and §12 use (one holding every atom, imported as `custom.<slug>` without
   `core:admin`; one holding a subset of atoms; one naming an atom outside the table, imported and reported; one whose
   every atom is unknown, quarantined with the role-list entries that name it), keys outside their owner's roles, user and key status edge values, an identity row M3 cannot
   convert and an unfinished merge journal (the last two in their own corpus, which the preflight must refuse;
   IMPL-8). The answers are still recorded by the R0 binary booted on the result (effective config,
   reach, schedules, search results, ABS responses), and the store and answers are committed as testdata. It is
   regenerated only if R0 is re-cut. Every R2 PR migrates it and checks the per-family counts, every ID and every reference, the
   quarantine report (exactly the overlay's quarantine cases), the oracles and the ABS suite (§9.4 R2). A **production-shaped store**,
   generated at the P1 counts and sizes, runs the same steps on `unit-lifecycle`'s triggers and nightly, for the
   per-family checks and the boot-time measurement. The **restore drill** restores the checkpoint of a migrated
   corpus offline and boots the pre-freeze release's binary on it, which must give the corpus' pre-migration answers.
2. **Optional, owner, before the R2 deploy.** With the R2 binary,
   `audiobook-organizer storage migrate --dry-run --from-archive <archive>` on the fresh off-host archive, on the
   production host or any linux/amd64 machine: it migrates a throw-away copy, reports per-step counts, quarantined rows and durations, and writes nothing to the live
   store. To also boot the result, the owner extracts the archive with
   `storage restore-offline --archive <file> --into <dir>`, `<dir>` a new directory inside the scratch area
   `.mediacore-checkpoints/scratch/`, and boots it in a container with `--network none` and
   `core.integrations.disabled=true`, reached through `docker exec`. The copy holds production data, plaintext secrets
   and session tokens, so it is written with directory mode 0700 and file mode 0600, the command prints a warning that it holds
   secrets, and the owner deletes it afterwards; the checkpoint sweeper removes it 7 days after its mtime otherwise
   (05-storage §5S.3.12).

## 9.4 Releases

Every table has the same rows: content, migrations, verification, rollback and the owner's part, which is decisions,
the deploy and the optional 1-day smoke run. 04 §8 keys the package moves of each release.

### R0 — Hotfixes

| Aspect | Specification |
|---|---|
| Content | (1) **The six early fixes of 05-authn §11.1** (the single list; §9.1 names the defects): among them every `apikey:` writer takes `apiKeyMu` (early fix 6), and the Basic gate refuses an empty stored password while `BASIC_AUTH_PASSWORD` applies in memory, logged, so setting it and restarting recovers (OPS9-7). (2) **The toolchain bump** (13 §13.2.2) as R0's first PR, moving every pinned copy together; later commands use `make -C core <target>` or `$(make -s -C core print-toolchain)`, never a literal (MOD2-13). (3) **The updater hotfix** (05-updater): a restart after an update exits **75**, so `Restart=on-failure` restarts it; the previous binary is kept as `<exe>.prev`, the slot `make rollback` uses; versions compare with `golang.org/x/mod/semver`, and a release that is not newer is refused; a current version with a `git describe` suffix counts as a local build, reported "local build, not compared", and a manual apply refuses unless the operator names the version; both unattended apply paths (the scheduled apply and the maintenance-window step) become check-only: they notify, Settings shows a banner, and the token `auto_update_paused` is shown in a `degraded` field that R0 adds to the authenticated `GET /api/v1/system/status` with that one token (R1 extends the field with the posture tokens; never on `/health`, 05-health §5H.3.1). Release note: "Automatic updates now only check for a new release and notify you; apply updates from Settings. A locally built binary is not compared with releases." and "Security fixes: a revoked API key can no longer be re-activated; the session list no longer shows session tokens; a limited API key can no longer create an unlimited one; admin-only routes honour API-key limits; Basic authentication with an empty saved password is refused; a revocation can no longer be undone by a request that was in flight. If Basic authentication is on without a saved password, save one before upgrading; if you are locked out, set `BASIC_AUTH_PASSWORD` and restart." |
| Migrations | None. |
| Verification | The fixes' own HEAD-code tests, one `TestLegacy…` test per fix as 05-authn §11.1 names them (`TestLegacyRevokeRacesTouch_NeverUnrevokes` among them; their ported core tests are R2's; CONFORM-13); the updater test (exit 75, a kept `.prev`, refusal of an older tag, "local build, not compared", no unattended apply); `make ci`; e2e. The R0 binary also writes R2's fixture corpus (§9.3), so its tag is kept. |
| Rollback | `make rollback`: R0 changes no stored data and takes no checkpoint, so there is nothing to restore. |
| Owner | Before the deploy, P14 (§9.7): a Basic gate that is on with no saved password gets one saved first. The deploy. The optional smoke run. |

### R1 — Core runtime, no user-data change

| Aspect | Specification |
|---|---|
| Content: build and CI | 03 §3.2, 13 and 07 §7.3.2 (MOD-1, MOD-8, MOD-9). `core/` with its `go.mod` (the `exclude` of the monolithic genproto **only in `core/go.mod`**, `ignore ./web`, `ignore ./examples/hello/web`) and an **empty committed `core/go.sum`**; root `go.mod` `require` + `replace`; `GO_MODULE_DIRS := . core`; the L0 leaves `archtest`, `storage/keys` and `buildid` (`buildid.Binary`, `buildid.Commit`); `internal/arch/layering_test.go` redesigned per 03 §3.5.1 (MOD3-5). Both Dockerfiles copy `core/go.mod core/go.sum ./core/` beside `go.mod go.sum` before `go mod download && go mod verify`; `.dockerignore` adds `**/node_modules`, `core/web/packages/*/dist` and `core/examples/*/web/node_modules` and keeps `docs` excluded (R4-4). Every `//go:embed` input of either module lies inside the build context or is a listed in-image output (`scripts/ci/embed_build_outputs.txt`; `web/dist`, MOD6-3); `scripts/ci/check_embed_context.py` in root `make ci` is the check on every PR, and this row owns its done-check (MOD5-7). One POSIX `sh` script, `scripts/ci/build_web.sh`, builds `core/web` when present in an explicit workspace order and then runs `npm ci` in `web/`, behind `make web-deps`; every `npm ci` site of §9.1 calls it; path filters, the `changes` classifier and Dependabot gain `core/web` and `/core` (MOD2-6, MOD3-16); the three frontend jobs inside external reusable workflows set `run-frontend: false` and run their frontend half in ABO-local jobs (MOD4-10); before R1's first UI PR (07 §7.19 step 1), `reusable-release.yml` is read at `.github/ghcommon-ref.txt`'s SHA to settle what `frontend-enabled` does, and both `pre-build-script`s become `sh scripts/ci/build_web.sh deps && cd web && npm run build` (MOD6-5). The **`docker-image` PR job** builds the full image (and `Dockerfile.build-cgo` with `--output type=local`), runs it with a seeded volume, waits for `"ready":true` on `/health` and for `--print-storage-levels` inside the container, and asserts that `X-App-Build` equals the embedded `<meta name="mediacore-build">` (`TestBuildID_HeaderEqualsEmbeddedMeta`); a second run with `--db /config/x.pebble` on a named volume asserts that `.encryption_key` (from F3 also `secrets.keyring`) sits beside the database and survives a container recreate (OPS5-7, OPS6-10). The reserved families `c/` and `m/` are registered in `keyfamilies.go`. Every PR that moves a package updates `ci/gates.txt`, the shard weight files, `.mockery.yaml` and `tygo.yaml` (the classification table below). |
| Content: endpoints and build identity | The `/api/*` redirect excludes `^/api/(core\|[a-z][a-z0-9]{1,15})/v[0-9]+(/\|$)` (`server_lifecycle.go:1798`). Every response carries `X-App-Build`, the UI build ID, which `npm run build` generates by 07 §7.6.3's one rule and writes only into the bundle's `<meta name="mediacore-build">`: no Go link step receives it, and the binary's own identity is `app.BinaryID`, a separate value (R5-7). R1 creates ABO's reload banner `web/src/components/StaleBuildBanner.tsx`, fed by `apiFetch`'s build-ID rule and, from R1's api-client step (07 §7.19 step 2), by `@mediacore/api-client`'s `onStaleBuild` (IMPL-14, XFILE-6). New fast exits: `--version-json` with exactly `{app, version, commit, ui_build_id, binary_id}` — `app` the app ID, `commit` the `vcs.revision` of `debug.ReadBuildInfo` with `+dirty` when `vcs.modified` or `""` when absent, `ui_build_id` the embedded bundle's UI build ID, `binary_id` the version plus the SHA-256 of the running executable (`buildid.Binary`) — the one field list, which 05-updater's step-3 self-test uses verbatim (FE6-4); `--print-storage-levels`; `--print-upgrade-floor`, empty until R3. The `deploy` template's post-check reads the running version by running `ssh $(DEPLOY_HOST) $(DEPLOY_BIN) --version-json` on the host (an R1 fast exit that needs no credential and no HTTP route), never from `/health`, which carries no version (05-health §5H.3.3; OPS4-14); `Makefile.local.example` carries the same command. The authenticated `degraded` field of `GET /api/v1/system/status`, which R0 added with `auto_update_paused` alone, carries every posture token from R1 (05-health §5H.3.1); `/health` carries none of them. goreleaser's dead `-X main.commit`/`-X main.date` are deleted. |
| Content: runtime | 04 §8.1's R1 rows (leaves with no data or API surface, `entity`, `coretest`); `internal/telemetry` construction moved to core, its global installs kept in the ABO façade until the composition root (05-metrics §13; MOD3-3); 05-config stage 0, the 8 bypassing `config.AppConfig` writes through `Mutate` (`TestNoDirectAppConfigWrites`). ABO's `main` becomes `app.New(ctx, opts, books.Module())` (`books.Module(store)` only on the intermediate `main`s before the storage PR, 06 §6.11.1; XFILE-11), with lifecycle, listeners and supervisor timeouts: the stop order of 06 §6.5.2 within one budget, `core.shutdown.budget` = 25 s (05-health §5H.4), below the unit's new stop timeouts and `KillMode=mixed` (OPS-13). `books` calls `httpapi.MountLegacy` with `Auth: LegacyAuthGin`, allowed until F4 through `app.Options.LegacyAuthGinUntil`; every manifest route registers as a mux pattern (05-http-api §4.8); the perimeter moves from gin into core's stages and leaves gin in the same PR, except the Basic gate, which moves at F3; `otelgin` leaves the mount (§9.2.7). 05-logging stages 1 and 2: `app.InstallProcessDefaults` with the meter, tracer and propagator installs, file output, and the second `slog.SetDefault` deleted (MOD4-5). |
| Content: storage | Core `storage` opens the one Pebble store: `internal/database`'s storage files re-home into core `storage` and `internal/pebble`, `keyfamilies.go` stays ABO, and `database.Store` splits (04 §4.5). Books reaches its families only through declared claims, **strict** from the first release, with the full suite and e2e running under strict claims; `core.storage.claims.mode=shadow` stays as the emergency dial. A restore stages as a kind of the one journaled swap and takes effect at the next restart, which the service requests by exiting 75 (05-backup §5B.3.4). The factory and store resets are the swap's `KindReset`, staged in R1 **only offline** by `storage reset` with the service stopped and applied at the next start; HEAD's two gin reset routes are deleted, and the SPA hides its reset control until the core route lands in F4 (05-storage §5S.3.15). Every target is pre-validated at staging, the set-aside stays on the store's own filesystem, and the post-reset hook never fails boot. CLI write subcommands open through `app.OpenOffline`. `storage restore-offline` (`--checkpoint <name>`, `--archive <file>`, `--into <empty dir>`), core code that ABO's cobra tree reaches through `app.OfflineCommands`, restores with the service stopped, in place through the journaled swap or into an empty path for a rehearsal; it never boots the app (05-storage §5S.3.12). The crash-matrix kit `coretest/crashmatrix` lands with core's `TestBootCrashMatrix` and ABO's `TestBootCrashMatrix_ABO`. The checkpoint sweeper and `storage prune-checkpoints` land here, because the first R1 boot on a non-empty store takes a checkpoint (§9.2.3; CONFORM-6). `LegacyCap` is fixed (§9.2.2). |
| Content: UI (07 §7.19 steps 1–2) | 07 §7.19 steps 1–2 (FE-2, FE-12; IMPL-11, XFILE-6): react-router-dom 7 → react-router 8 with the declarative `<BrowserRouter>` unchanged, an import swap only (52 source and 70 test files); the `core/web` workspace with `@mediacore/api-client` (runtime and generator) only, generated from `core/openapi/core.json` (MOD3-4) and consumed through `file:`; `apiFetch` calls its `checkBuild`; `core/web/.gitignore`; the Docker frontend stage that builds `core/web` before `web/` (MOD3-13). |
| Content: deploy tooling | `DEPLOY_READY_TIMEOUT` in `Makefile` (`rollback`) and `Makefile.local.example` (`deploy`), default 300 s, counted from when the unit leaves `activating`. `scripts/deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]` polls the unit over ssh: `activating` counts as progress, `failed`, `inactive` or a rising `NRestarts` fails at once, and `ExecMainStatus=78` is reported by name; without `SSH_HOST` (Docker, launchd) it polls the URL with a documented larger timeout. The committed unit gains `RestartPreventExitStatus=78` and its stop settings. The `make rollback-restore` target of §9.2.1 lands here, so that it is tested before R2 needs it, and it and `make rollback` share one probe that refuses while a swap is in flight (`restore_inflight`), with no override. The README's compose notes gain the `/config` volume preparation. |
| Content: release note | "A restore now takes effect at the next restart, which the service performs itself; a container started without a restart policy must be started again." — "In this release a factory reset or a database reset runs only from the command line, with the service stopped (`audiobook-organizer storage reset`); the reset button in Settings returns with <R2 release>. Undoing a factory reset restores the database but not the library files or Open Library data it deleted. A reset now leaves `config.yaml` beside the database unchanged; delete it by hand for a full reset." — "The first start after this upgrade keeps a full copy of the database, including saved passwords and sign-in tokens, beside it for 7 days; `audiobook-organizer storage prune-checkpoints` deletes it sooner." — "After an upgrade, open browser tabs offer to reload." — "`--version-json`, `--print-storage-levels` and `--print-upgrade-floor` print machine-readable build and store facts." — "Paths of the form `/api/<name>/v<N>/…` are reserved for versioned APIs and are no longer redirected to `/api/v1/`." — "Every response carries an `X-App-Build` header naming the build." Plus the HTTP-metrics lines of §9.2.7 (the `http_route` value change among them), whose dashboard edits ship in the same PR, and 05-logging's log-file and level lines. |
| Migrations | None on user data. The first R1 boot writes core's `c/storage/` bookkeeping (the install marker before any other `c/` key, every owner's level stamp at 0), which HEAD and R0 ignore; on a non-empty store it takes the pre-migration checkpoint first (§9.2.3). |
| Verification | `make ci` with every required check, under strict claims; e2e; the ABS conformance suite; the perimeter's own tests (05-http-api §12: per-owner CSP header goldens, compression and rate classes) and `TestLegacyManifest_RegistersAsMuxPatterns` on the real engine, ABS on and off; `TestNativePrefixesNotRedirected` and `TestCollidingNamespacesStillRedirect`; `TestVersionJSONFields`; the image job, both runs; the two `unit-lifecycle` cells; `TestBootCrashMatrix` and `TestBootCrashMatrix_ABO`; the restore and reset staging tests of 05-backup §5B.3.4 and 05-storage §5S.3.15; `TestR0OpensR1Store` (05-storage: the R0 binary opens a store the R1 binary wrote and reads every row; deleted in R3); `TestCheckpointAutoDeletedAfterSevenDays` over R1's first-boot checkpoint; every row of the classification table below, `genproto-exclude-root` running its workspace probe in a throwaway copy outside the repository, so nothing runs `go work init` in a checkout (CLAUDE.md); `scripts/tests/test_ci_classifier.py`; `scripts/tests/test_deploy_wait_ready.py` (exit 78 included) and `scripts/tests/test_make_rollback.py` on its fake-ssh harness (`make rollback`, and `make rollback-restore` with a checkpoint and with an archive); `check_embed_context.py` passes on HEAD and fails on a fixture that embeds a `docs/` path. The UI steps: `! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts`; vitest and Playwright green; the image job green with `core/web` present; a dry-run RC build through `prerelease.yml` green, with `git status --porcelain` empty after it. |
| Rollback | `make rollback` to R0, which loses nothing: R1 changes no user data, and R0 ignores core's `c/storage/` rows (`TestR0OpensR1Store`). It is refused while a swap is in flight (§9.2.1). An offline restore of the R1 checkpoint with R0 also works and discards the writes since. |
| Owner | Decisions before R1's first core PR are recorded (D15): the required checks are enforced by a no-bypass ruleset with the merge queue (14 Q-I), applied before R1's first core PR merges; the module name is decided (D13, `github.com/falkcorp/mediacore`); the R1 freeze is kept (§9.2.1): the owner sets `vars.RELEASE_FREEZE` to `R1` before R1's first core PR merges and clears it with R1's release. P12 before the deploy, because the first R1 boot takes a checkpoint. The deploy. The optional smoke run. |

**R1's CI classification (MOD-8, MOD4-14).** Root `make ci` (`Makefile:785`) and the CI jobs from R1 on:

| Target / job | Runs in | Notes |
|---|---|---|
| `staticcheck`, `fmt-check`, `test-short`, `go vet`/`go build`, `tidy-check` | per Go module (`GO_MODULE_DIRS`) | `fmt-check` excludes `web/`, `core/web/` and `core/examples/*/web/`; `tidy-check` is `go mod tidy -diff` in `.` and `core`, and a change to `core/go.mod` carries the root `go mod tidy` result in the same PR (MOD2-6) |
| `fix-check` (`go fix -diff`) | **core only** (03 §3.2 item 4; MOD3-6) | the root has no `go fix` gate: `go fix -diff ./...` exits 1 at HEAD with pending fixes in 245 root files; a move PR applies `go fix` to the files it moves, in the same PR (04 §2.2) |
| `web-test` | per npm workspace: `web`, `core/web` (the latter also in `make -C core ci`) | `test-all-short` = `test-short` + `web-test`, so neither runs twice |
| `lint-errcheck-ratchet`, interface-width job | per module, **separate baselines** (`core/.errcheck-baseline`, `core/.interface-width-baseline`); core's from `make -C core ci` with scripts under `core/scripts/ci/` (MOD5-15) | one integer over root `./...` today; a move must not silently re-baseline |
| `lint-deadcode-ratchet` | root, with ABO's and hello's mains and `-filter 'github.com/falkcorp/mediacore/(internal\|kernel)'` (MOD2-19) | exported SDK packages are out of scope (as `pkg/` is today, `scripts/deadcode-ratchet.py:64`); no separate core baseline |
| `coverage-check-short` | per module: root floor `.ci/coverage-floor.txt`; core: a **ratchet floor** in `make -C core ci` from the storage move; 12's SC5 thresholds run report-only in `make -C core readiness` (MOD2-8) | |
| `mocks-check`, `pprof-check` | per module: root `.mockery.yaml`, and `core/.mockery.yaml` from the first core interface with a mock; `internal/diag/flight`'s tests move to `mediacore/health` | |
| `oplint`, `sdkguard`, `ops-check`, `types-check`, `bench-check` | root only (`tools/cmd/oplint`, `tygo.yaml:28`) until their subject moves (oplint → `archtest/tasklint` at F2; sdkguard goes with `pkg/plugin/sdk` in R3) | `cmd`'s `bench`-tagged files import packages that move, so a PR that moves one rewrites them, and `bench-check` is its check (MOD2-4, MOD4-7) |
| `go-test-short-shards` (`scripts/ci/short_test_shards.py`) and Woodpecker `test-rest`/`test-native-taglib` | root module, plus one core shard | the weight files are updated in the PR that moves a package (stale entries fail, `short_test_shards.py:257-258`) |
| `nightly.yml` (`test-nightly`), `ratchet-lower.yml` | per module | core's non-short and property tests run nightly; `ratchet-lower` learns core's baselines |
| `codeql.yml`, `memory-leak-scan.yml`, `fixture-tests`, Woodpecker `test-database`, `test-fixtures-{1,2,3}`, `test-server-scanner`, `checks-build`, `checks-lint` | root (fixtures, scanner, database) / per module (CodeQL, leak scan, build, lint) | each in `ci/gates.txt` with its classification |
| `ci/gates.txt` `store-width`, `store-references` | root, until the split deletes `database.Store`; retired in that PR with the reason recorded | `scripts/tests/test_run_gates.py` validates every go-suite gate |
| core layering gate, `TestCoreImportsOnlyCore` (its file set is the tag union of 03 §3.5.1 item 4's list, R9-10) | core only, registered in `ci/gates.txt` with `core-job`; core-owned check scripts live under `core/scripts/ci/` (MOD2-9) | the positive control is staged: the engine's self-test on a synthetic graph in the first core PR, then a named real edge, then `app` → `kernel` (03 §3.5.1 item 2; MOD3-8) |
| `exclude-check` (core mode, and the "present/misplaced" scan of `core/`) | core only (`make -C core exclude-check`) | pass rule: load-bearing if at least one applicable probe shows the edge and fails with `ambiguous import`, stale if none does (03 §3.2 item 1; MOD3-12) |
| `genproto-exclude-root` (root mode, and the tree-wide "no other `go.mod`" scan) | root only, in root `make ci` | runs from the first core PR (MOD3-12) |
| `check_module_skew.py` | root only, **blocking**, in root `make ci` | no skew, the empty `core/go.sum` exists, the two exact `--pin`s hold (03 §3.2 item 6; MOD3-16) |
| `TestCoreHoldsNoAppKeyNames` | root only (`internal/arch`), in root `make ci` (MOD9-6) | reads `KeyFamilies()` until the storage move, then the claim generator's output, over the tag union (R10-9, MOD10-8) |
| `vet-darwin`, `vet-integration` | per module (root and `core`), required PR jobs, registered in `ci/gates.txt` (R9-10, MOD9-3) | as §9.2.1 states |
| `unit-lifecycle` | root, on the Ubuntu runner VM, registered in `ci/gates.txt` (R9-9, OPS9-6, OPS10-7) | the two cells and the triggers of §9.2.1 |
| `check_commits_build.py` | root, in `extraction-gates`, on a PR whose diff renames a file into `core/`; registered in `ci/gates.txt` (MOD9-5) | builds both modules at each commit of the PR, because rebase merges land every commit on `main` |
| `check_core_version.py` | core only, in `make -C core ci` (`core-ci`) | `version.Version` ≥ the newest checkpoint, hello's `MinCoreVersion`, npm lockstep (12 §12.4.3; MOD4-13) |
| `TestBooksMinCoreVersion` | root (`internal/books`), in root `make ci` | books' `Manifest.MinCoreVersion` ≤ core's `version.Version` (MOD4-13) |
| `core-checkpoint` (`release-prod.yml`) | prod release, a separate `continue-on-error` job (MOD3-7) | records `refs/core-checkpoints/v0.Y.Z` when `core/` changed and `version.Version` was bumped, else opens one `core-version-bump-needed` issue (12 §12.4.1, MOD2-14) |

### R2 — The cutover

| Aspect | Specification |
|---|---|
| Content | The foundation PRs F2, dark (below); then every core-service family, in the flip order below (03 §3.8), each flip with the UI calls its routes serve: config and secrets, with the operator precedence and the fail-closed rule (05-config §1.12.2); identity, with hashed sessions and one-time tokens, `mck_` keys and native scopes (05-authn §11, 05-rbac §6); tasks, with every definition native, the schedules and the log journal (05-tasks §5T.8, 05-logging §5); events, outbox and realtime, with `/api/events` deleted, the two operation streams having gone with the tasks flip (05-events, 05-realtime §11); integrations, AI jobs, provider gates, library roots, the rebuilt full-text index and notify (05-metadata M1.10, 05-ai §10, 05-library §5L.10, 05-storage §5S.3.8, 05-notify §10); backup in core's layout, the updater moved into `core/updater` with its R0 behaviour, health, and the kernel `/metrics` mount (05-backup B1, 05-updater, 05-health, 05-metrics §6); the metric renames land at R1 (HTTP), F5 (tasks) and F7 (AI), each with its dashboards and rules (05-metrics §13); the generated clients, the identity UI and the `api.ts` sections switched flip by flip (07 §7.19 step 3); the shell (07 §7.19 steps 4–5) if it is ready; the six early fixes' tests ported as property tests onto core auth (05-authn §11.1); the operator runbook (deploy, rollback, smoke run). Release note: assembled by §9.2.9 from §9.6 and the owners' texts; its identity text is 05-authn §11's. |
| Migrations | One ordered `BootBlocking` list behind the checkpoint: M1, M7, M2–M6, M8 (§9.2.3). |
| Verification | **Automated, on every R2 PR through `extraction-gates` (§9.2.1):** the fixture corpus written by the R0 binary and its answers (§9.3); the production-shaped store at the P1 counts; the per-family count, ID and reference checks and the byte-identity check outside the moved sub-prefixes; the config oracle (the effective config before = after, env, YAML and stored values included), the reach oracle (`TestIdentityCutover_ReachOracle`, 05-rbac §12), the schedule oracle (`TestScheduleParity_AllDefinitions` and a first-boot "no catch-up storm" test, 05-tasks §5T.9) and the search oracle; 05-tasks' resume, cancel and shutdown parity suites; the ABS conformance suite on the migrated corpus (`TestIdentityCutover_ABSSurvives`) and 05-authn §10's other `TestIdentityCutover_*` rows; the ported early-fix property tests; the webhook delivery test (05-notify §9); the crash matrix over every step and every chunk boundary; the canaries `TestCutover_NoRawTokenOnDisk` (05-authn) and `TestConfigCutover_NoPlaintextSecretOnDisk` (05-config) over every file of the live DB directory after M8, WAL, MANIFEST and OPTIONS files included, the retained checkpoint excepted until its 7 days pass; `TestCutoverRefusedByOlderBuilds`, with the pre-freeze release's binary, on a migrated store and on one stopped in the middle of M3; `TestDryRunMatchesLiveRun` (05-storage); the restore drill with the pre-freeze release's binary (§9.3; `TestRollbackRestoreBootsPreviousBuild`, 05-storage); the cutover preflight's refusal of the blocking-row and unfinished-merge corpus (D13); the deploy-files test (§9.2.5); the Basic gate's `TestBasicGate_ExemptSetGolden`, `TestBasicGate_EmptyPasswordRefuses` and `TestBasicGate_MoreRestrictiveWins` (the compose cell of `TestPerimeterHonoursBlobOnlyValues`; XFILE-8, CONFORM-7); the dashboard-reference test; e2e. **Optional, owner:** the dry run and the container boot of §9.3; the smoke run below. |
| Rollback | `make rollback-restore` (§9.2.1): the checkpoint, or the off-host archive with its state files, offline, plus the pre-freeze release, which the recipe checks against the source's levels, the checkpoint's `from_vector` or the archive's (after an R2 hotfix, `make rollback-restore RELEASE=<pre-freeze tag>` fetches the named release from the configured release source into `.prev`, §9.2.1). Everything written after the upgrade is lost, every revocation made on R2 included (repeat them after the rollback), so fixing forward is the default. |
| Owner | **Decisions:** D12 answered (a)–(d); what is left is whether to run the optional dry run and smoke run and, within the smoke day, whether to keep R2 or roll back. **The deploy**, by the runbook: (0) finish or undo every account merge in progress on the pre-freeze release, and fix or delete each identity row the dry run reports as blocking (D13); (1) take an archive, verify it and copy it off the host together with `.encryption_key` and `<db parent>/config.yaml`; (2) optionally, the dry run on that archive (§9.3); (3) rename the env names that the host's own unit drop-ins, compose `.env` and plist set, per the rename table (P4; a set retired protected or secret name refuses boot and names its successor); (4) deploy R2, with `DEPLOY_READY_TIMEOUT` at the measured first-boot time; (5) switch each webhook receiver to core's format (§9.6 row 5; the OIDC redirect URI is unchanged, row 4); (6) reload the Prometheus rules and Grafana dashboards that R2 ships; (7) after the first boot, `secrets export-keyring` to the off-host location (health warns `secrets_keyring_not_exported` until then); (8) re-send open invites (P5). **The optional smoke run:** sign in to the web app; an ABS phone app keeps its session and syncs progress (P7); a library scan and a scheduled task run once, with no catch-up storm; a webhook reaches its receiver; the Prometheus scrape answers and the alerts evaluate; health shows no quarantined row. |

**R2's flips (03 §3.8).** R2 opens with F2, the foundation PRs, landed dark before F3 so that each later flip only
switches ownership (IMPL-1–IMPL-5, IMPL-12, XFILE-4). The flips are serialized, each flip PR green on every required check, so every
intermediate `main` boots and passes e2e; the release freeze starts before F3 merges (§9.2.1):

| Flip | Subsystem | Step | Also in the flip PR |
|---|---|---|---|
| **F2** | the foundation PRs, dark: `storage/datamigrate` and `internal/storagemigrate`; `tasks` with its executor and `ForEach`, no books kind registered and no `ops` instrument created before F5; `archtest/tasklint` (from `tools/cmd/oplint`), run in CI from F2 with its shrink-only exemption list of not-yet-converted books task packages, which each conversion PR shrinks and F5 requires empty (05-tasks §5T.8.4); the `audit` store and the normative action table (`core.storage.*`, `core.settings.update`); the full `core:*` scope catalog; every core config section registered (backup, auth, tasks, updater and the rest); the health registry and its route, with the kernel's own components `core.storage` and `core.secrets` registered on it kernel-side (module handles at F5), so F3's tokens surface; the minimal owner registrations of `core.integrations.release-holds`, `core.diagnostics.export` and `core.ai.batch-poll` | — | nothing switches: no route, schedule or stored row changes (IMPL-1–IMPL-5, IMPL-12, XFILE-4) |
| **F3** | config and secrets; the Basic gate into the core perimeter (05-http-api §8.1) | M2, with M7 and M8 | the rename table; deploy files with canonical names; `basicauth.go` deleted from gin |
| **F4** | identity: `auth`, `rbac`, `audit` serving; the gin auth chain replaced by `legacyauth`; the core reset route; the `events` bus (IMPL-10) | M3 | the native auth, user, key, role and audit routes, and the gin routes they replace deleted (the OAuth/OIDC callback keeps HEAD's path, now served by core, with HEAD's native-app hand-off; HEAD's start path `/api/v1/auth/oauth/{provider}/start` kept at its exact path, where ABS clients such as AudioBooth start, as a second `CoreExactPatterns()` entry served by core's start handler with the `AppHandoff` deep-link rules; 05-http-api §8.1); ABS logins through books' `auth.Compat` handle (05-authn); the native settings routes (`/api/core/v1/settings`, `/settings/schema`) with the gin settings handlers of the `k` and `s` keys deleted and the SPA settings page switched (05-config §1.3.4; XFILE-7); the SPA's auth, user and key calls and its permission constants; `server-bootstrap` and `scripts/` on the new routes; the SPA's reset control back, on the core reset route |
| **F5** | tasks and the log journal | M4, M5 | every definition native and HEAD's engine deleted; the three late-owner core kinds registered from F2's minimal owner registrations (`core.integrations.release-holds`, `core.diagnostics.export`, `core.ai.batch-poll`; IMPL-5); HEAD's task routes, `/api/v3/ops/*` and the two operation streams deleted; the Activity page on core's task routes, polling `GET /runs` until F6, and no longer reading `ops.ts` (XFILE-2); HEAD's `operations_*`, `operation_*` and `op_*` families deleted and the `ops_*` label changes made, with their dashboards, rules and rule tests (05-metrics §13; XFILE-1) (05-tasks §5T.8, 05-realtime §11) |
| **F6** | outbox and realtime (the bus is live from F4) | — | the SPA's `eventSourceManager` rewritten onto `/api/core/v1/events` (05-realtime §4.8 items 1–6, per-tab mode, with the `useTopic`/`useResync` signature; `@mediacore/shell`'s client replaces it at F9 or R3; IMPL-6), which the Activity page uses instead of polling; `/api/events` deleted |
| **F7** | integrations, AI jobs, provider gates, library roots, notify | M6 | `internal/plugin` deleted; webhook and mail instances native, deliveries in core's webhook format; the AI series renamed with their dashboards and alerts; `integrations/providerhttp` and `integrations/ai` extend the registrations of `core.integrations.release-holds` and `core.ai.batch-poll` |
| **F8** | backup, the updater move, health, the kernel `/metrics` mount | — | the kernel `/metrics` mount replacing the gin route, with no metric rename (XFILE-1); `health/diagnostics` extends the registration of `core.diagnostics.export`; HEAD's `/api/health` and `/api/v1/health` deleted, every caller of them switched to `/health`, which becomes Basic-exempt in their place (`TestBasicGate_ExemptSetGolden`, 05-http-api §12; 05-health) |
| **F9** | the shell, if ready (otherwise R3) | — | 07 §7.19 steps 4–5 in one release; gin's SPA serving deleted |
| — | the last PR of R2 | — | the runbook and the release note; the owner clears `vars.RELEASE_FREEZE` with the release (D15) |

### R3 — Cleanup and the upgrade floor

| Aspect | Specification |
|---|---|
| Content | The shell flip F9 if it was not in R2. Deleted: `internal/serviceregistry`, `pkg/plugin/sdk`, the no-op `--db-type` and SQLite flags (`cmd/root.go:489,508`), books' legacy steps 1..66 and the code only they call, the HEAD-format readers, `internal/books/cutover`, `TestR0OpensR1Store`, and together `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state` (XFILE-2). Books' start hook `legacy-file-cleanup` deletes `.encryption_key` (M2 left it for a rollback-restore) and HEAD's `library.bleve` directory (left for the same reason), each deletion a labelled row of 06 §6.5.1's boot-writer table; while `secrets_keyring_not_exported` is raised it still deletes `library.bleve` but skips `.encryption_key`, logs why and retries the key at the next start, and when it deletes the key it logs that the off-host `.encryption_key` is now the only copy (05-storage §5S.3.8; CONFORM-4, DATA-16). `UpgradeFloor{InstallFirst: <R2 tag>}` (§9.2.2). Not deleted: the gin mount, the `/api/*` → `/api/v1/*` redirect, the ABS routes, `/health`, `/metrics`, and the `config.AppConfig` and logger façades (optional ratchets, SIMP-3; 11 §11.5 checklist). Release note: "This release needs a database that ran <R2 release>. An older database stops the service at start with the message `install <R2 release> first`: install that release, start it once, then upgrade." — "The first start of this release deletes the old settings key file `.encryption_key` and the old search index directory. Keep your off-host copy of `.encryption_key` with any backup taken before <R2 release>: restoring such a backup needs it, and needs <R2 release> installed first." — with the macOS and Docker notes of §9.2.2. |
| Migrations | No migration step; the floor is a boot-time check, and `legacy-file-cleanup` deletes the two files once. |
| Verification | `make ci`; e2e; the crash matrix over `legacy-file-cleanup`'s rows; a pre-R2 store is refused at boot, before any write, with exit 78 and `install <R2 tag> first`; restore staging refuses a pre-R2 archive (`TestRestoreBelowUpgradeFloorRefusedAtStaging`); a fresh install and a factory-reset store pass the floor on two boots (`TestFreshInstallPostFloorBootsTwice`). |
| Rollback | `make rollback` to R2: R3 changes no data in the store, and R2 reads neither deleted file on a migrated store, so there is nothing to restore. |
| Owner | Before the deploy: the keyring exported and an off-host `.encryption_key` kept with every pre-R2 archive. The deploy. The optional smoke run. |

### 9.4.1 Sub-spec batch → release map (OPS-12)

| Batch (owner) | Release | Behaviour change | Done-check |
|---|---|---|---|
| Toolchain bump (13) | R0, first PR | no | 13's pin check |
| The six early fixes (05-authn §11.1) | R0 | yes | their tests |
| Updater hotfix (05-updater) | R0 | yes (no unattended apply) | the updater test |
| Build paths, CI classification, image job, required checks (03 §3.2, 07 §7.3.2, R3-2) | R1 | no | R1's build checks |
| 05-config stage 0; `internal/telemetry` construction to core (05-metrics §13) | R1 | no | `TestNoDirectAppConfigWrites`; the plain gather test |
| Composition root, lifecycle, listeners, the shutdown budget (05-health) | R1 | yes (stop order) | `unit-lifecycle` |
| 05-http-api: `MountLegacy`, the perimeter in core, `otelgin` removed; 05-logging stages 1–2 | R1 | yes (perimeter owner, HTTP metrics, log file) | the perimeter tests; the ABS suite |
| 05-storage: core opens the store, strict claims, the one swap, offline opens, the crash matrix; the `database.Store` split (04 §4.5) | R1 | yes (a restore or reset applies at the next restart) | the crash matrices; the staging tests |
| 07 §7.19 steps 1–2; the Docker frontend stage | R1 | no | the UI checks of §9.4 R1 |
| The foundation PRs F2: `storage/datamigrate`, `internal/storagemigrate`, `tasks` and its executor, the `audit` store and action table, the `core:*` scope catalog, the core config sections, the health registry route | R2, F2 (dark) | no | builds and boots with nothing switched; `TestRenameTable_EveryKeyRegistered` from F3 against the build under test |
| 05-config §1.12.2 (M2), precedence, the rename table; the Basic gate to core | R2, F3 | yes | the config oracle |
| 05-authn, 05-rbac: identity (M3), native routes, `legacyauth`, the atom table; the identity UI | R2, F4 | yes | the reach oracle; `TestIdentityCutover_*` |
| 05-tasks: native definitions, schedules, the kind-rename map (M4), the Activity page on core's task routes; 05-logging's journal (M5) | R2, F5 | yes | the schedule oracle; the parity suites |
| 05-events, 05-realtime: hub, outbox, native SSE; the SPA's realtime client | R2, F6 | yes | the stream tests |
| 05-metadata M1, the 05-download and 05-ai adapters, library roots, AI jobs, gates, the index rebuild, 05-notify (M6) | R2, F7 | yes | the search oracle; the webhook delivery test |
| 05-backup (core layout), the updater move, 05-health, the kernel `/metrics` mount | R2, F8 | yes | the dashboard-reference test |
| 07 §7.19 step 3 (the `api.ts` sections, switched with each flip) | R2 | no | the generated client covers the route |
| 07 §7.19 steps 4–5 (the shell); step 6 | R2 (F9) or R3; after R2 | yes; no | the shell's checks |
| The R3 deletions and the floor | R3 | yes (the floor) | the exit-78 boot test |
| Optional ratchets (05-config accessor, 05-logging injection); mock conversion (`internal/database/mocks`, 04 §4.6) | none; R1–R2 as the owner decides | no | ceilings only go down; 04 open item 1 |

## 9.6 Changed in R2, and the kept contract

**Changed in R2.** This list is the source of the R2 release note (§9.2.9); its last column feeds the owner's deploy
steps (§9.4 R2).

| # | Surface | Before R2 | From R2 | Owner file | Outside the release |
|---|---|---|---|---|---|
| 1 | Core API routes: auth, users, keys, roles, audit, config, system, backup, update, tasks and definitions, the generic integration routes | `/api/v1/...`; `/api/v3/ops/*` | `/api/core/v1/...`; each old path deleted by the flip that replaces it | 05-http-api §8.1 | a caller outside the repo must move (P5) |
| 2 | Event streams | `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events` | `/api/core/v1/events`; the two operation streams deleted at F5, `/api/events` at F6 | 05-realtime | — |
| 3 | Extra health paths | `/api/health`, `/api/v1/health` | deleted; `/health` and `/api/core/v1/health` serve (F8) | 05-health | a monitor on one of them moves to `/health` |
| 4 | OIDC callback and start | `/api/v1/auth/oauth/:provider/callback` and `/api/v1/auth/oauth/:provider/start`, served by gin | the same callback path, served by core with HEAD's native-app hand-off (F4); the start path kept at its exact path, served by core's start handler with the `AppHandoff` deep-link rules exactly as `/api/core/v1/auth/oidc/{provider}/start` is (F4) | 05-authn §4.13; 05-http-api §8.1 | — (no provider re-registration; ABS clients such as AudioBooth keep starting at the old start path) |
| 5 | Webhook format | `X-Audiobook-Event`, `X-Audiobook-Signature-256` (an HMAC of the body only), HEAD's payload | core's format, `X-Mediacore-Delivery` and the timestamped `X-Mediacore-Signature`; the HMAC secret migrated by value (M6) | 05-notify §3, §10 | switch each receiver to the new headers and signing string (deploy step 5) |
| 6 | Stored config and secrets | `setting:config_blob` with plaintext secrets; 4 rows encrypted with `.encryption_key` | `c/config/` under canonical keys; every secret encrypted in `c/secrets/` under the keyring; the gap-fill file's managed keys stripped (M2) | 05-config §1.12.2 | export the keyring after the first boot (deploy step 7) |
| 7 | Config keys and env names | ~251 flat keys; the `BindEnv` and `AutomaticEnv` names; `ABK_*` | canonical keys and env names by the rename table; ecosystem names unchanged | 05-config §1.12.3–§1.12.4 | rename what the host's own files set (P4; deploy step 3) |
| 8 | Precedence | a stored value beats an env default | an operator value locks its key; for the auth and Basic-gate keys the more restrictive value wins, reported in health | 05-config §1.3.5 | — |
| 9 | Web sessions, invites, one-time tokens | HEAD's rows | deleted (M3): everyone signs in to the web app once | 05-authn §11 | re-send open invites (P5; deploy step 8) |
| 10 | API keys | `abk_` tokens holding atoms | the same tokens, with native scopes (M3); new keys are `mck_` | 05-authn §4.4; 05-rbac §6 | — |
| 11 | Roles and permissions | 13 stored atoms; seed roles rewritten at every boot | native scopes through the atom table; core seeds the seed roles | 05-rbac §6 | — |
| 12 | Auth-off behaviour | HEAD's pass-through | credential, key, temp-login and restore routes refuse the auth-disabled principal; the backup routes answer 401 | 05-rbac §4.10–§4.11 | turn authentication on to use them |
| 13 | Task kinds, runs and schedules | HEAD's definition IDs and ops rows | `books.<id>` and `core.<id>` by the kind-rename map; runs, clocks, markers and stamps in `c/tasks/` (M4) | 05-tasks §5T.8 | — |
| 14 | System log | `syslog:` rows | `c/log/`, converted (M5) | 05-logging §5 | — |
| 15 | Integrations, AI jobs, library roots, the webhook rule | blob fields, `aijob*`, `import_path:` | `c/integrations/instances/`, `c/ai/`, `core.library.roots` entries (IDs kept), native notify instances and their rule (M6) | 05-metadata M1.10; 05-ai §10; 05-library §5L.10; 05-notify §10 | — |
| 16 | Throttle and quota holds; the full-text index; derived indexes; caches | HEAD's rows; `library.bleve` | reset or rebuilt; a provider's daily budget restarts at zero; the index is rebuilt at its new path, and `library.bleve` is deleted in R3 | 05-metadata M2; 05-storage §5S.3.8 | — |
| 17 | Metric names | HEAD's `audiobook_organizer_*` names | moved emitters may take core's names, with the shipped dashboards and rules changed in the same PR: R1 (HTTP, in R1's note), F5 (tasks), F7 (AI); F8 only moves the kernel `/metrics` mount (CONFORM-11, XFILE-1) | 05-metrics §13 | reload the shipped rules and dashboards (deploy step 6) |
| 18 | `/metrics` reach | any authenticated credential | `core:metrics.read`, stored on every principal that is not `Confined()`, so the scrape key keeps working | 05-metrics §6; 05-rbac §6 | — |
| 19 | Backup archives | ABO's layout (`legacy-abo/0`) | core's layout (F8); an old archive still restores, and the next boot migrates it | 05-backup B1, B7 | take a new off-host archive after R2 |
| 20 | Factory and store reset | HEAD's two gin routes; in R1 the offline `storage reset` only | the core route `POST /api/core/v1/system/factory-reset` (F4) and the offline command | 05-storage §5S.3.15 | the R1 release note says the reset is offline-only in R1 |
| 21 | SPA paths and link forms | HEAD's paths, `/invite/:token`, `/auth/temp-login?token=` | the shell's paths (F9), with no old-URL redirect (D13): a former bookmark lands on the not-found page, and the release note lists the new paths; link forms change with the SPA | 07 §7.19 | update bookmarks |
| 22 | The pre-migration checkpoint | — (R1's first boot already took one, with its own release-note line) | a full pre-R2 copy beside the DB (directory 0700, files 0600), holding plaintext third-party secrets and raw session tokens, deleted 7 days after it stops being needed (§9.2.3) | 05-storage §5S.3.12 | guard the host's disk as the server is guarded; `storage prune-checkpoints` deletes it earlier |
| 23 | Pre-R2 backups | — | they still hold plaintext secrets and sign-in tokens; restoring one migrates it again and clears its sign-ins; after R3 it needs R2 installed first and its off-host `.encryption_key` | 05-authn §11; §9.2.2 | keep them as safe as the server, with their `.encryption_key`, or delete them |
| 24 | Account merges | a finished merge could be undone from its journal | permanent: no merge finished before R2 is undone after it (an undo answers 409 `merge_permanent`), and a merge in progress refuses the upgrade until it is finished (D13) | 05-authn §11 | finish or undo every merge before the deploy (deploy step 0) |
| 25 | Rollback and revocations | — | a `make rollback-restore` brings back every credential revoked, user disabled or password changed on R2 | 05-storage §5S.3.12 | repeat those revocations after a rollback (SEC-10) |
| 26 | Auth-off reach of credential metadata | HEAD bound no user with auth off, so these routes answered 401 or did not exist | with auth off, `/me*`, `/api-keys*`, `/users/{id}/sessions*`, `/users/{id}/api-keys*` and the audit routes answer 401; the settings routes keep their auth-off reach (SEC-3) | 05-rbac §4.11; 05-authn §4.13 | turn authentication on to use them |

**The kept contract (D12 (c)).** The **only** frozen external contract is the ABS-compatible API that the phone apps
use (03 §3.8): the unprefixed root routes `/ping`, `/status`, `/login`, `/logout`, `/auth/refresh`, `/auth/openid*`,
`/api/authorize`, `/api/me*`, `/api/libraries*`, `/api/items*`, `/api/session*`, `/public/session/:id/track/:index`,
`/api/playlists*`, `/api/collections*` and the authors, series and narrators routes (`absRouteList()` lists 74 at
HEAD); the value of its JWT signing secret, whose env name may change, a set retired name refusing boot; and its auth
semantics (HS256 JWTs, refresh sessions in `abs_sess:`, logins by password and by API key). HEAD's OAuth start path `/api/v1/auth/oauth/{provider}/start` is kept at its exact path as part of it, because
ABS clients such as AudioBooth start a sign-in there (05-http-api §8.1; row 4). It is pinned by the **ABS
conformance suite**, the tests of `internal/server/handlers/abs/` and `internal/server/absauth/` (05-authn §4.10),
which runs in every `make ci` and, from R2, on the migrated corpus too, because ABS logins then resolve users through
core auth through books' `auth.Compat` handle (`TestIdentityCutover_ABSSurvives`; 05-authn). The ABS families stay books' and are not migrated. Webhooks are not
part of it.

**Unchanged because that is cheaper** (not contracts; each keeps only its tests):

- the OAuth/OIDC callback path `/api/v1/auth/oauth/:provider/callback`, served by core from F4 with HEAD's native-app
  hand-off (deep-link start parameters, books' hook that mints the app authorization code, the exact-match redirect
  allowlist, S256-only PKCE; SEC-2), so no identity provider needs re-registering (row 4);
- books' routes on `/api/v1/...` (the permanent gin mount) and HEAD's `/api/*` → `/api/v1/*` 301;
- the remote fingerprint-worker API (`/api/v1/fingerprint/worker/*`, a books route) and its `abk_` key, so the worker
  on its other host needs no upgrade (`TestIdentityCutover_WorkerKeyStaysConfined`);
- `/health` with `ready` (it carries no version, which the authenticated `GET /api/v1/system/status` reports; 05-health
  §5H.3.3), and the `/metrics` path;
- the 15 shipped CLI flags, the CLI commands and the fast-exit flags (the no-op `--db-type` and SQLite flags go in R3);
- the Cloudflare Access assertion header, and the protocols ABO speaks outward (iTunes library XML, Deluge JSON-RPC,
  OpenAI-compatible APIs, metadata providers, SMTP), which the extraction does not touch.

## 9.7 Production-data unknowns (read-only commands, both branches designed)

Run these against production before the release that needs them; paste counts only, never values. `<abo-url>`,
`<redacted-token>` (an admin API key from `server-bootstrap`), `<unit>`, `<scratch>` and `<db>` are placeholders.
Offline commands run on a **restored backup copy** (`storage restore-offline --into`), so production is never
stopped. Never print `sess:` or secret values, which hold credentials; count keys only.

| ID | Question | Command | Branch A | Branch B |
|---|---|---|---|---|
| P1 | Key counts and bytes per family (before the R2 flips: they size the production-shaped store) | `curl -sS -H "Authorization: Bearer <redacted-token>" "<abo-url>/api/v1/diagnostics/db-census?fresh=true" \| jq '.data.families[] \| select(.prefix \| test("^(u\|role\|apikey\|invite\|inviteallow\|oauthid\|sess\|idx\|setting\|opv2\|opv3\|act):")) \| {prefix,keys,raw_key_bytes,raw_value_bytes}'` (estimated, read-only, `handlers/diagnostics.go:1195-1220`) | every key is in a registered family: the generator takes the counts | `(unregistered)` keys: list them by prefix before F3, so that no cutover step meets an unknown key |
| P2 | Custom (non-seed) roles exist? | `audiobook-organizer diagnostics query --raw --db <scratch>/audiobooks.pebble --prefix role: --limit 1000 \| grep -c '"is_seed":true'` vs total `^Key:` | none: M3 converts the seed roles only | some: each converts through the atom table; an atom outside it grants nothing, and the role is reported (§9.2.6) |
| P3 | API keys with empty scopes | the same with `--prefix apikey:` and `grep -c '"scopes":\[\]'` | none | some: M3 converts each per 05-rbac §6, never empty, and the reach oracle's corpus carries one |
| P4 | Env names actually set | `systemctl show <unit> -p Environment \| tr ' ' '\n' \| cut -d= -f1`; `systemctl cat <unit> \| grep -o 'Environment="[A-Z_0-9]*'`; Docker: `docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' <container> \| cut -d= -f1`; launchd: `plutil -extract EnvironmentVariables xml1 -o - <plist> \| grep -o '<key>[A-Z_0-9]*'`; compose: `docker compose config \| grep -oE '^\s+[A-Z_0-9]+:'` | only names the rename table maps: the owner renames them at the R2 deploy | a name the table does not map: add it before F3 |
| P5 | Open invites; external callers of core routes | `... --prefix invite: ... \| grep -c '^Key:'`; callers: `journalctl -u <unit> --since -30d -o cat \| grep -oE '(GET\|POST\|PUT\|PATCH\|DELETE) /api/[a-z0-9/_-]*' \| sed -E 's#/[0-9A-Za-z]{20,}#/:id#g' \| sort \| uniq -c \| sort -rn \| head -50` | no open invite; only the SPA and the repo's scripts call core routes | open invites: the owner re-sends them after R2; a caller outside the repo: the R2 release note and the runbook name it, because its route is deleted |
| P7 | ABS surface in use | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.abs_api_enabled'`; `... --prefix abs_sess: ... \| grep -c '^Key:'` | off: the conformance suite suffices | on with sessions: the suite on the migrated corpus, plus a live phone-app login and progress sync in the optional smoke run |
| P9 | Webhooks configured | `curl … /api/v1/config \| jq '.plugins.webhook // empty \| keys'` | none: M6 creates no instance | some: M6 migrates each URL with its secret by value, and the owner switches each receiver to core's format at the deploy (05-notify) |
| P12 | Disk headroom (before R1 and R2) | `df -B1 --output=avail <db-dir-parent>` vs `du -sb <db-dir>` | free space covers the checkpoint, the rewritten ranges and the compaction: deploy | less: free space first, or the boot halts before writing |
| P14 | Basic gate on without a saved password (before R0; OPS9-7) | `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/api/v1/config \| jq '.data.config \| {enabled: .basic_auth_enabled, password_set: ((.basic_auth_password // "") != "")}'` (HEAD masks a set password and returns `""` for an empty one, `internal/config/update_service.go:102-104`) | the gate off, or a password set: nothing to do | the gate on with no password: save one before deploying R0, or after it set `BASIC_AUTH_PASSWORD` and restart |

## 9.8 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Store stamped above the build's level (R1 on an R2 store) | **fail-closed**: refuse to open before any write, exit 78, name both levels and the release to install; HEAD and R0 refuse through HEAD's storage-format guard | it would misread the store (§9.2.2) |
| Store below the upgrade floor (R3 on a pre-R2 store) | **fail-closed**: `ErrUpgradeFloor{InstallFirst}`, exit 78, last stderr line `install <R2 tag> first` (install the R2 release, its tag, first) | the cutover steps are gone |
| Restore staging of an archive below the build's floor | **closed**: refused before extracting | a fresh host must not end with an empty store (DATA3-9) |
| Migration or cutover step error | **fail-closed**: boot halts, and the checkpoint stays for the offline restore; the next boot of the same build resumes from the step's cursor | G2 |
| A row a cutover step cannot decode, or an importer rejects | quarantined under `c/storage/quarantine/`, counted and reported in health and the step's report; boot continues (**open**); a reference to a quarantined ID is reported in health, not a `Verify` failure (DATA-2) | one bad row must not crash-loop the unit, and nothing is dropped in silence |
| A user, key or identity row M3 cannot convert; an account merge `applying` or `undoing` | **fail-closed** at the cutover preflight of a store whose cutover has not started, before the checkpoint and any write (on a resumed cutover, at M3's own check of the rows it has not yet imported, whose refusal names `make rollback-restore`, because `storage cutover-blockers` no longer opens a store whose cutover has started): boot refuses, the report names each row or merge; before the cutover starts `storage cutover-blockers` fixes or deletes the row, the pre-freeze release finishes the merge (D13) | identity is never quarantined-and-continued, and a half-done merge cannot be imported |
| `.encryption_key` missing while HEAD's encrypted rows are non-empty; a row the key does not decrypt | the first **fails closed** before M2 writes anything, naming the file and the override; the second is quarantined, its secret "re-enter" in health, and boot continues (**open**) | a missing key is usually a wrong mount; one bad row must not stop the server (05-config §1.12.2) |
| Checkpoint without disk headroom | **fail-closed** before any write, with the numbers | a full pool is an outage |
| A retired env name is set | for a `ProtectAdmin`+ or secret-class key **fail-closed**: refuse to start, naming the canonical variable; for a database-path name with no store at the resolved path, storage refuses to create one; for any other name one WARN, and `config check` lists it (**open**) | an ignored `DATABASE_PATH` opens or creates the wrong store (14 K15); an ignored secret disables a feature; env carries unrelated names (`HOST`, `PORT`) |
| A value that does not parse under the canonical grammar | **fail-closed**: refuse to start, naming the variable | strict decoding; `ENABLE_AUTH=yes` included |
| An auth or Basic-gate key whose operator and stored values differ | the more restrictive wins; health reports both (**closed**) | a stale operator value must not weaken a security control (§9.2.5) |
| Core Basic gate enabled with an unset or empty password | **closed**: 401 on every non-exempt route, health `basic_gate_no_password` | early fix 5 (R5-1(c)) |
| New API key with empty or wildcard scopes | **refused** (400) | a new key never has empty scopes |
| `make rollback` onto a migrated store | **refused** by HEAD's storage-format guard (the projection is raised) | rollback across R2 is a restore |
| `make rollback` or `make rollback-restore` while a swap is in flight | **refused** (`restore_inflight`), with no override: boot the current build once to finish the swap; a `failed` checkpoint restore is escaped by the previous binary's `storage restore-offline --checkpoint` (§9.2.1) | an older build must not open a half-moved store |
| `storage restore-offline --checkpoint` of a checkpoint that is not the latest attempt's, or after its 7-day deletion | **refused**, naming the latest checkpoint or the off-host archive | it would also discard a later upgrade's work |
| `make rollback-restore` whose `.prev` levels differ from the source's levels (the checkpoint's `from_vector`, or the archive's) | **refused** before anything is touched, naming the release to install: the checkpoint record's `from_release`, or on the archive branch the release that produced the archive (§9.2.1); `make rollback-restore RELEASE=<pre-freeze tag>` fetches the named release from the configured release source (HTTPS, plus the checksum check apply performs if the source publishes one) and renames it to `.prev` only once its levels equal the source's levels | a rollback must never re-migrate (DATA-9) |
| A swap intent (restore or reset) that expired or was staged by another build; a second staging while the slot is taken | **rejected** (closed), store untouched; `storage.ErrSwapPending` for the second | one rejection rule for every kind (R7-2, R8-1) |
| A reset: staging with a dangerous `RootDir`; a set-aside rename that fails; a post-reset hook that cannot delete an entry | refused (400), nothing staged; the rename moves back what it moved, journals `rejected` and boots the untouched store `degraded` (`factory_reset_failed`); the entry is logged, audited and reported (`factory_reset_cleanup_incomplete`), and the hook still records `hook_done` (**open**) | a reset never leaves a half-moved store, and a hook that never finishes would block every swap (COR8-3, DATA8-1, OPS8-5) |
| An archive without a manifest (`legacy-abo/0`) | restored (**open**); the next boot migrates it | old archives stay restorable |
| A ServeMux-conflicting set of gin routes whose members differ in requirement or owner | **fail** (`ErrPatternConflict`) at manifest generation or `Build` | a generalised pattern needs one requirement and one owner (A-1, R10-1) |

## 9.9 Concurrency contract

The cutover steps and every other migration run in the kernel's migration phase P4 (06 §6.5.1), before listeners, the
executor and the scheduler start (03 §3.7), and nothing writes the store before the checkpoint (P4a). Each step
converts in chunks, and each chunk is one batch that writes the importer's rows and deletes the HEAD rows it decoded.
A library-scale step is a `DataMigration` with its own `tasks.Parallelism` and a bounded worker pool, never a plain
sequential loop (CLAUDE.md): its workers own disjoint key ranges (by family prefix and shard), so no two workers ever
touch the same row, and shared state such as the quarantine counts and the ID map is merged per shard or held behind
its own lock. Native stores use storage's `Expect` preconditions and `Serialized` families; HEAD's mutexes, the
`apiKeyMu` of early fix 6 among them, are not carried over: core auth serializes its own writes (05-authn), and the
ported early-fix property tests prove that a last-use touch cannot undo a revocation. Derived state that R2 rebuilds
(indexes, caches) rebuilds after the boot as background tasks with bounded parallelism. Every step takes `ctx` and
honours cancellation between chunks. Pebble has one writer process; offline commands open through `app.OpenOffline`,
which refuses while another process holds the DB lock.

## 9.10 Config keys, metrics, tests

Config keys this file adds: none beyond the dials of §9.2.8, which their specs define; the reader level is compiled
into the build. Core series carry the app's metric namespace (§9.2.7; `audiobook_organizer` for ABO).

| Metric | Type | Labels |
|---|---|---|
| `<namespace>_storage_level` (05-storage §5S.8; the only level metric) | gauge | `owner`, `kind` (supported, store) |
| `<namespace>_storage_checkpoint_bytes` (05-storage §5S.3.12) | gauge | `state` (open, completed), as 05-storage §5S.8 defines them |
| `<namespace>_build_info` | gauge | `version` |
| `<namespace>_kernel_boot_phase_seconds` (06 §6.9) | gauge, per boot | `phase` and `step` exactly as 06 §6.9 defines them |

Each release's tests are in its table's Verification row (§9.4). This table names those that check the release,
rollback and cutover properties this file sets:

| Test | Release | Proves |
|---|---|---|
| `TestR0OpensR1Store` (05-storage) | R1, until R3 | R1's rollback to R0 loses nothing |
| `TestBootCrashMatrix` (core, over hello), `TestBootCrashMatrix_ABO` (books' full boot), on `coretest/crashmatrix`, extended by every boot writer | R1, then every R2 step | a kill or an error at any durable write, rename or fsync of a boot leaves a store the current build boots, and every cutover step resumes with no row lost or written twice |
| `scripts/tests/test_unit_lifecycle.py`, `test_deploy_wait_ready.py`, `test_make_rollback.py`, `test_ci_classifier.py`; `scripts/ci/check_commits_build.py` | R1 | the two unit cells; exit 78 reported by name; `make rollback` and `make rollback-restore` on the fake-ssh harness; the docs-glob classifier; every commit of a move PR builds |
| the cutover suite: the R0-written corpus; the per-family, ID, reference and byte-identity checks; the config, reach, schedule and search oracles; the restore drill | R2 | every family converts without loss and with the same meaning, and the checkpoint restores offline for the pre-freeze release |
| `TestCutover_NoRawTokenOnDisk` (05-authn), `TestConfigCutover_NoPlaintextSecretOnDisk` (05-config) | R2 | after M8 no file under the live DB directory holds a raw token or a plaintext secret; the checkpoint is the stated exception until its deletion |
| `TestCutoverRefusedByOlderBuilds` | R2 | HEAD, R0 and R1 refuse a migrated store and a store stopped in the middle of M3, at open and in `make rollback` |
| the deploy-files test | R2 (F3) | the shipped deploy files use canonical names only |
| the exit-78 boot test, `TestRestoreBelowUpgradeFloorRefusedAtStaging`, `TestFreshInstallPostFloorBootsTwice` | R3 | the floor at boot and at restore staging; fresh installs and factory resets pass it, M2–M6 recorded as satisfied |
| the cutover preflight's corpus (D13) | R2 | an unconvertible identity row or an unfinished merge refuses the boot before any write, and the report names it |

## 9.11 Risk table

| # | Risk | Detection | Mitigation |
|---|---|---|---|
| 1 | A cutover step loses or corrupts data, one-way | per-family checks; oracles; the quarantine report | a corpus the R0 binary wrote; the production-shaped store; the dry run; the crash matrix; the checkpoint and the off-host archive |
| 2 | A reference breaks: books' rows point at user, role, key, import-root, instance and run IDs; v1 rows carry kind IDs | the referential-integrity check | every ID kept; the kind-rename map applied to books' v1 rows |
| 3 | A shared family (`idx:`, `setting:`) is split wrongly | the byte-identity check outside the moved sub-prefixes | sub-prefix lists generated from `keyfamilies.go` and the census |
| 4 | An older binary opens a store that stopped between two cutover steps | `TestCutoverRefusedByOlderBuilds` on a store stopped in M3 | M7 before M2's first chunk; HEAD's `make rollback` guard reads the raised projection |
| 5 | Secrets are lost: `.encryption_key` missing, the keyring not exported, the gap-fill file refilling old keys | HEAD's key guard; "re-enter" in health; `secrets_keyring_not_exported` | a missing key file fails the boot before any write; an undecryptable row is quarantined, never dropped; the export is a deploy step; M2b strips the gap-fill file |
| 6 | The precedence change weakens security: a stale `ENABLE_AUTH=false` or compose's always-set Basic-gate variable locks its key | health reports the disagreement | the more restrictive value wins for auth and Basic-gate keys; compose passes them only when set |
| 7 | A renamed env name is ignored and an empty store opens at the default path (14 K15) | the boot refusal | refusal on a set retired protected or secret name; no store created while a retired DB-path name is set; `config check` |
| 8 | The first R2 boot is killed partway (start timeout, container health check) | the `unit-lifecycle` cell | `EXTEND_TIMEOUT` per step; resumable steps; the measured `DEPLOY_READY_TIMEOUT`; the Docker `start_period` note |
| 9 | The disk runs out (checkpoint, rewritten ranges and compaction together) | the headroom check | it fails closed before any write; P12 |
| 10 | A rollback by restore discards every write since the upgrade and revives every credential revoked since | — | decide within the smoke day; fix forward by default; the runbook says so and lists the revocations to repeat |
| 11 | The checkpoint and pre-R2 backups hold plaintext secrets and raw tokens | the canaries over every file of the live DB directory | the 7-day deletion of every checkpoint once it stops being needed, from R1; `storage prune-checkpoints`; mode 0700/0600; M8's rewrite into a fresh directory; the release notes |
| 12 | ABS phone apps break: logins resolve through migrated users; the secret's env name changes | the ABS suite on the migrated corpus | `abs_sess:` untouched; the secret's value kept; the boot refusal on its retired name; the smoke run's live check |
| 13 | Another external party breaks: identity providers, webhook receivers, the remote worker, the scrape key | the smoke run | the one changed item, the webhook format, is a deploy step (§9.6 row 5); the OIDC callback path, the worker API and the scrape key's reach do not change |
| 14 | Dashboards and alerts go silent on a renamed series | the dashboard-reference test; `promtool test rules` | the shipped files change in the renaming PR and are reloaded at the deploy; no rename for nothing |
| 15 | R2's blast radius; flips colliding with feature work | rebase failures; CI time | R1 carries the runtime half; serialized flips; move windows and the 35-minute budget (§9.2.10) |
| 16 | A half-flipped `main` or part of R1 reaches production, or a defect appears during a freeze | — | the R1 and R2 freezes (§9.2.1); unattended updates check-only; the checkpoint; a hotfix release from the pre-freeze release by default (never from the R1 tag, which may not open the production store), which the owner may override per defect |
| 17 | Schedules stampede at the first boot | the "no catch-up storm" test | the clocks migrate (M4); `TestScheduleParity_AllDefinitions` |
| 18 | The early fixes regress in core auth; no previous-build oracle remains | the ported property tests; the oracles | the six fixes' tests on core auth; oracles computed by the R0 binary |
| 19 | An R1 runtime change regresses: strict claims refuse a live writer, the perimeter changes CSP, compression or rate classes, a shutdown kills runner children | the full suite and e2e under strict claims; the perimeter tests; `unit-lifecycle` | the `shadow` dial; per-route data from HEAD's chain and per-owner CSP goldens; stop timeouts and `KillMode=mixed` |

## 9.12 Open items

| # | Item | Owner |
|---|---|---|
| O3 | `.encryption_key`, the gap-fill `config.yaml` and, from R2, `secrets.keyring` live outside the archived DB directory (`settings.go:44-52`, `backup.go:283-428`): an off-host copy must carry them, and a restore on a new host needs them | 05-config §2.4, 05-backup; the runbook |
| O4 | Env grammar and prefix (`ABO_`) | 05-config §1.3.6 |
| O8 | Pilot cost before any books route group is rewritten to native `httpapi` (SIMP-2 item 4): measure one group (for example playlists, 10 routes) | owner, when a group is first rewritten |
| O10 | How the required checks are enforced (R3-2) | resolved by D15: a no-bypass ruleset with the merge queue (14 Q-I) |
| O11 | The per-PR CI-time budget (35 min) and the target move-window length (two working days) of §9.2.10 | owner |
| O12 | The module name, which blocked R1's first core PR | resolved by D13: `github.com/falkcorp/mediacore` (14 Q-H) |

## Objections to 03

None open. The five items this file raised against 03 §3.7–§3.8 are settled, and 03 now states each: M7 runs right
after M1 and before any family row is deleted; the webhook format changes to core's native format with an owner step
at the deploy, while the OAuth/OIDC callback path stays HEAD's; every consumer changes in the PR that renames what it
uses, so R2's last PR carries only the runbook and the release note; the keyring is exported after R2's first boot,
and the pre-R2 archive carries `.encryption_key` and the gap-fill `config.yaml`; and F5 deletes HEAD's task routes
and moves the Activity page onto core's task routes, polling until F6.

## Out-of-scope defects found

1. Shipped deploy files set env names nothing reads: `AUDIOBOOK_ROOT_DIR` (`deploy/audiobook-organizer.service:97`,
   `deploy/com.audiobook-organizer.plist:38`; deliberately unread, `internal/config/config.go:2569-2575`), `AO_DIR` and
   `AO_DB` (`docker-compose.yml:18-19`; the Docker `CMD` supplies `--db`, `Dockerfile:104`), `OLLAMA_BASE_URL`
   (`deploy/local.conf.example:40`). They mislead operators; F3's deploy-file change removes them.
2. `--db-type` help still says "pebble (default) or sqlite" (`cmd/root.go:489`) although SQLite is rejected
   (`internal/database/store.go:1840-1843`); R3 deletes the flag.
3. The HEAD updater exits 0 to restart (`internal/updater/updater.go:331-334`), so under the committed unit's
   `Restart=on-failure` every auto-update leaves the service stopped, and it treats any version difference as an update
   (`:153`); R0 fixes both. It also cannot write next to the executable in the committed install layout and looks for a
   raw asset name the release may not publish (`updater.go:254-258,318-326`; `deploy/audiobook-organizer.service:41-43,88,122`).
4. A revoked API key can be re-activated: `PATCH /api/v1/auth/api-keys/:id {"status":"active"}`
   (`internal/server/handlers/apikeys.go:336-372`) reaches `SetAPIKeyStatus`, which sets `Status` without checking
   `RevokedAt` (`internal/database/pebble_store_auth.go:1226-1247`); filed in `todo.d/2026-11-04_platform_core_followups.md`
   and fixed in R0 (05-authn §11.1).
5. The `deploy` template's post-check calls `/api/v1/system/version` (`Makefile.local.example:103`), which no route
   serves (`internal/server/wire_system_routes.go:28-48`), so its "deployed version matches" step can never pass; R1
   changes it to run `ssh $(DEPLOY_HOST) $(DEPLOY_BIN) --version-json` and compare its `version` field, with no HTTP route
   and no credential (an unauthenticated route would put the version on a public surface, and the authenticated
   `GET /api/v1/system/status` would need an API key the post-check does not hold; 05-health §5H.3.3; OPS4-14).

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

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; 03 v1.12):

- A-1 / ADV-1 — S2 HTTP: the collision rule (same-owner ServeMux-conflict sets register as one generalised pattern only when `Perm`, `Guards`, `RateClass`, `OwnAuth`, `BasicExempt` and `rbac.LegacyAtom` agree; `…/v2/{id}` its own pattern; post-gin check accepts any member); `TestLegacyManifest_RegistersAsMuxPatterns` on the real engine, ABS on and off, from S2 r1; S5 `TestS5_CoreAliasesRegisterBesideLegacy`; §9.8, §9.10, §9.11.
- A-2 / ADV-3 / ADV-S1 — representation-neutral extraction (§9.0, §9.2.3 rewritten, §9.2.5, §9.2.6, S3, S4, S7): HEAD's `sess:` rows, one-time tokens, `abk_` keys, legacy-atom roles, plaintext blob secrets and auth-enable precedence; the extension family, fingerprints, suspension, the `auth` revoke duty, sign-in deletion, sentinels and re-import, `mck_`/`["none"]` for ABO, `operator-names.json`, the narrowing writes, `OperatorSecretOrigin`/`ErrOperatorSecretBound` for legacy-backed instances and R6-6's exemption removed; `/metrics` keeps `AnyAuthenticated` for ABO.
- A-2 — §9.2.1 re-derived as one nine-row list (old rows 1, 2, 4, 5, 10, 13, 14, 15, 16, 18, 19, 20 deleted; old 3 → 4 narrowed to the link forms, 6 → 5, 7 → 6, 8 → 7, 9 → 8, 11 → 9, 12 → 1, 17 → 2, 21 → 3 without `operator-names.json`); every overview, Rollback and cross-reference renumbered.
- A-2 — §9.13: the hardening cutovers H1 identity, H2 secrets and H3 auth precedence as named post-S10 stages with their levels and floors (the release itself); Q-D and Q-E deferred to H1 and H2.
- A-3 / ADV-S2 — no `CheckedWrites`, `Serialized` mode or blind-write record on legacy families; HEAD's keyed mutexes, `apiKeyMu` (early fix 6) and `opsMu` through `legacy.TasksWriteLock`; the enforcing releases S3 r2, S4 r3, S5 rE and S7 r2 with their soaks, rows and tests deleted (§9.2.3, §9.9, S2b, S3–S7, §9.4.1).
- A-4 / ADV-S3 — `check_stage_visibility.py`'s per-stage evaluation, `tools/stagecheck`, `origins.txt`, `archtest.DeferredMembersAbsent` and gate E as CI removed (§9.0, S0, S2, S5, S7, S8, §9.10); stage done-checks rely on Go's `internal/` rule; the plain `shipped_stage:` marker lives in `ci/stage.txt`, which this file owns with its ordered values.
- A-5 / ADV-S4 — no metric renames: §9.2.7 rewritten around `app.Options.MetricNamespace`; the S2b, S5, S7 and S8 renames, gather-time aliases, ledger rows, dashboard switches, old row 16, `GateP6`, P6 and `TestS5_FullAliasSetBoots`/`TestS8_FullAliasSetBoots` deleted; core series written `<namespace>_*`.
- A-6 / ADV-S5 — S8 moves the S0-hotfixed updater (exit 75, `.prev`, semver, level/floor checks, check-only unattended); signed manifests, the boot-attempt counter, automatic rollback (P−1, deferred-build per 06), the binary-layout owner action, `--updater-check`, R-test cell (d) and their risks are deferred-build (§9.2.2, §9.8, §9.11, O9).
- A-7 / ADV-2 — §9.2.5 legacy decoding parity and the fail-closed auth-bool rule with its release note; `TestLegacyEnvDecodeMatchesHEAD` in `extraction-gates` from S3 (S3, §9.2.1, §9.10).
- A-8 / ADV-4 — `TestBootCrashMatrix` required from S2b's flip PR (S2b row "Crash matrix"); every later boot writer extends it (§9.3).
- A-9 / ADV-5 — §9.2.1 "Soaks and owner checkpoints": `release:` in `ci/stage.txt`, the soak ledger `ci/soak.txt` written by the owner's `scripts/soak_record.py`, `check_soak.py` refusing a release or marker advance without a predecessor row ≥ 14 days old; S2b r1 and S4 r1 evidence; owner checkpoints listed.
- A-10 / ADV-6 — §9.3 staging marker (`<db>.staging`, `c/storage/staging`), `staging-scrub` refusal and `--confirm-destroy-credentials`; S0 done-check and `test_staging_scrub.py` cases (unmarked store refused, nothing written).
- A-11 / ADV-7 — new §9.2.10: announced move windows, the rebase-onto-move procedure and the 35-minute per-PR CI budget (O11).
- A-12 / ADV-S6 — §9.3 staging is a `--network none` container boot of the offline-restored, scrubbed copy; the ssh-reachable host, nftables rules, jump container, deploy-user rehearsal and `test_staging_egress.sh` deleted; `test_make_rollback.py` on S0's fake-ssh harness.
- A-13 / ADV-S7 — `compat-rehearsal` is a required check on each flip PR; the 36 h rule, the state-writing-PR criteria, `ci/state_writers.txt` and `check_rollback_rows.py` deleted.
- A-14 / ADV-S8 — W is dates, release notes and the owner's P8 counter readings; the usage records, `Present`, `recording_continuous_since`, the usage duty, `GET /api/core/v1/compat/usage`, `c/compat/`, the last-use gauge and old row 14 deleted (§9.2.1, §9.2.9, §9.5, §9.7 P8).
- A-15 — deferred-build contracts are design sketches (§9.0).
- A-16 / ADV-8 — no `config.yaml` sentinel clause; `TestCoreWritesOnlyHEADGapFillKeys` at S3. ADV-10 — S0 "User-visible changes" row with seven release-note lines. ADV-11 — §9.3 "Counts in done-checks are generated"; literals in S2, S4, S5, S7 and S8 marked as evidence-commit values.
- A-17 — Q-K decided: the legacy factory-reset alias keeps HEAD's `RootDir` wipe; the native route needs `delete_library_files: true` and names `RootDir` and the file count (§9.2.1 row 1, §9.2.6, S2b, S4).
- Cross-cluster (lead-approved) — R-C: health token `auto_update_paused`; R-E: auth enable is HEAD's exactly, `ABO_CORE_AUTH_MODE` an additional canonical name, row 3's release note; R-F: legacy-backed instances read `Local=true`, `ProtectAdmin` on destination and secret changes, 422 `ErrFieldNotStorable`; R-G: P−1 deferred-build (O9).
- Integration pass (advisor, lead) — §9.2.3: legacy writers may stage a voluntary `Expect` in a `Serialized` claim (A-3 ruling); §9.13 H3 also clears the other `BelowStored` flags (matches 05-config §1.12.5).

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; 03 v1.12):

- R9-1 / COR9-1 / DATA9-1 / DATA9-2 — HEAD's identity mutexes (`apiKeyMu`, `inviteMu`, `userWriteMu`, `sessionWriteMu`, `bootstrapMu`, `userMergeApplyMu`) and `opsMu` span core's commit through `legacy.WriteLock` and `storage.CommitLocked`; one instance per legacy store family; S4 r2 done-check `TestLegacyRevokeRacesTouch_NeverUnrevokes`, `TestInviteRedeemedOnce`, `TestBootstrapRedeemedOnce`, `TestLegacyStoresAreSingletons` (§9.2.1, §9.2.3, S4, S5, §9.9, §9.10, §9.11).
- R9-2 / COR9-2 — the T0+T1 PR moves the four `GET /api/v1/operations/{id}/{activity,changes,result,results}` routes to core with `…/operations/v2/{id}` as ledger-listed core aliases (`library.view`); `TestS5_CoreAliasesRegisterBesideLegacy` replaced by `TestS5_OperationsComponentMovesWhole`; cross-owner sets fail (S2 HTTP, S5, §9.8, §9.10, §9.11).
- R9-3 / MOD9-1 / MOD9-2 — one owner per metric family and series identity in the golden: the T0+T1 PR shrinks `opsmetrics` to `ops.abandoned_calls`, the v3 loop reports ticks through `legacy.TasksScheduleTicks`, `plugin` keeps HEAD's values; `TestABOMetricsGatherNoDuplicateOrMissing` from S5; core stage 2 is the only HTTP-metrics emitter and the legacy mount passes `c.FullPath()` as `http_route` (§9.2.7, S2, S5, §9.4.1, §9.6, §9.10, §9.11).
- R9-4 / COR9-3 / SEC9-6 — scoped `abk_` keys keep HEAD's authenticated-only reach (ambient scopes after the key intersection); creating a legacy-backed instance or setting `auth_ref` needs `ProtectAdmin` (§9.2.6, S4 R-test, S7).
- R9-5 / SEC9-1 / SEC9-2 / SEC9-7 — the Basic gate keys keep HEAD's auth-off write rule (`TestRollback_AuthOffBasicGateStaysOnPrev`); H3 moves only auth-enable precedence, in the enabling direction, with its release note (§9.2.1 row 3, §9.2.6, S3, S4, §9.10, §9.13).
- R9-6 / SEC9-4 / DATA9-4 — one reset request, `storage.ResetRequest`, with 422 `library_files_confirm_required`; the dangerous-root refusal only when the reset wipes `RootDir`; a reset journal mid-set-aside is never rejected without its move-back (§9.2.1 row 1, §9.2.6, S2b, S4).
- R9-7 / COR9-5 / DATA9-3 / DATA9-5 — `coretest/crashmatrix` with core's `TestBootCrashMatrix` and ABO's `TestBootCrashMatrix_ABO` over its full boot from S2b; range P0a to Run (3b); the no-hooks restore scenario and the override case; `inflight.json` before the first rename (§9.2.1 checks, §9.2.2, §9.3, S1, S2b, S4, §9.10).
- R9-8 / OPS9-1 / OPS9-2 / OPS9-3 — `Stage-Release: <stage> <n>` trailers checked by `check_soak.py`; soak rows record and verify `binary_id` and `commit`, with `<since>` and pass criteria; reverts of flip PRs always admitted (§9.2.1, S0, S2b, S5, §9.8, §9.10).
- R9-9 / OPS9-6 / OPS9-8 / COR9-6 — the `unit-lifecycle` job runs the four systemd-unit done-checks in a systemd-enabled `--network none` container; P7 branch B's live ABS client is an owner checkpoint; staging uses the archive writer's `binary_id` and refuses a mismatch; `app.Offline` carries the staging marker and core's offline commands (§9.2.1, §9.3, S0, S1, S2, S2b, §9.6 row 17, §9.7 P7, §9.8).
- R9-10 / MOD9-3 — required `vet-darwin` and `vet-integration` jobs over both modules; `TestCoreImportsOnlyCore` reads the tag union (§9.2.1, S0, S1).
- R9-11 / SEC9-3 / DATA9-8 / SEC9-8 — H1 and H2 delete the checkpoint and old Pebble files by forced compaction, state the archive residual and the rotate/re-issue release note (the re-issue clause later dropped by SEC10-7: no key re-issue is asked); owners `books` and `core` modelled by every guard-aware build; `TestHardeningCutoverRefusedByEveryOlderRelease`, `TestH1_NoRawTokenOnDisk`, `TestH2_NoPlaintextSecretOnDisk`, `TestH1_AmbientScopesPreserved`; H2 moves the four encrypted rows (05-config) (§9.13).
- OPS9-4 — §9.2.1 row 10 (S1 staged restore, restart-bound) with its release note; S1 overview and Rollback rows.
- OPS9-5 / DATA9-13 — §9.2.1 row 11 (from S4 r2 a restore leaves keys `inactive` and deletes sessions) with its release note; the "not a cost" sentence replaced; S4 overview, Rollback row and §9.4.1.
- OPS9-7 — S0 release note and recovery (`BASIC_AUTH_PASSWORD`, which an empty stored password no longer shadows); P14; the "no recovery command" sentence corrected.
- OPS9-9 / DATA9-14 / SEC9-12 — a pre-S0 archive cannot be staged (`staging-restore` refuses it; take an S0+ archive); S0 has no staging data point (§9.3, §9.8).
- OPS9-10 — `compat-rehearsal` also runs on `release:` changes; T7's conversion PR and the T8 PR change `release:` (§9.2.1, S0, S5).
- OPS9-11 — `TimeoutStopSec=60`, `stop_grace_period: 60s`, `ExitTimeOut` 60, `--stop-timeout 60` (S2).
- OPS9-12 — ops records move to the doc-only stage record `docs/plans/platform-core/stage-ledger/<stage>.md`; `TestCompatLedgerHoldsOnlyRows` (§9.2.9, §9.2.10, §9.3, S0, S1, P5, §9.11).
- OPS9-13 — `extraction-gates` as parallel jobs ≤ 30 min; the A-7/A-8/A-9 checks and prev-build tests never move to nightly; weekly p50 by `scripts/ci/ci_time_p50.py` (§9.2.10).
- OPS9-14 — one closed release-ID grammar (`S2br1`, `S5r<n>`, …) and the `ci/stage.txt` creation case (§9.2.1, S2b, S5, §9.4.1).
- OPS9-15 — the S4 overview lists row 3; the owner-checkpoint list names S0, S1 and S2 (§9.2.1, §9.3).
- OPS9-16 — `TestUpgradeFloor_RefusedBelowLegacyCap`; P13 reads the printed floor; `1..LegacyCap` in S2 and S2b.
- OPS9-17 — `TestABOUpdaterSourceMatchesHEAD` in S8; row 2 names `core.updater.mode`; `c/updater/` in §9.2.3 and the S8 overview.
- OPS9-18 — `ci/soak.txt` and `ci/prev_ref.txt` in `prerelease.yml`'s `paths-ignore` and `extraction-gates` only; S0's prerelease Docker build dropped.
- MOD9-5 — `scripts/ci/check_commits_build.py` in `extraction-gates` for PRs that rename a file into `core/` (S0).
- MOD9-6 — `TestCoreHoldsNoAppKeyNames` in S0's CI table and S1's done-check.
- MOD9-7 — header and S1: 04 rows are the informative planning list.
- MOD9-8 — S2 r3 adds no in-scope prefix; §9.2.7 golden "empty at S1, additions at S2 r3, S2b, S5, S7, S8"; §9.6 row 23 counts 74 families.
- COR9-9 — the merged-set field list is 05-http-api rule 4's (S2 HTTP, §9.8).
- COR9-14 — HEAD's AI families are books' AI recorder's through `ai.WithObserver`; core records only `<namespace>_ai_*` (§9.2.7, S7, §9.6 row 24).
- DATA9-10 — `c/config/` (S3) and `c/backup/` (S4 r2) in §9.2.3, the overview and S4; `TestLegacyToleratesNamespacedKeys` seeds them.
- DATA9-11 — S2b: `ResetArgs` for both kinds, then `restoreswap.Swapper.StageReset`; the not-undoable cite is §9.2.1 row 1.
- SEC9-9 — S4 Permissions lists every role-name site of 05-rbac §4.6 with its replacement; `TestRoleNameChecksUseCoreAdmin`.
- SEC9-10 — legacy-backed integration edits by API key are a recorded tightening with its release note (§9.2.6, S3).
- SEC9-11 — the auth mode's canonical name is unread below S4 r1 (row 3).
- Integration pass (round 9, lead) — §9.2.3 and S4: lock vocabulary `WriteLocks(names) []storage.Lock` / `CommitLocked(ctx, b, d, stage, locks...)` as 05-storage; row 17 lists `secrets export-keyring` and `secrets rotate-derive`.

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; 03 v1.14):

- R10-1 / COR10-1 — the five operations routes stay books' gin routes at every stage, their handlers on core's tasks client from T0+T1; core's run route `GET /api/core/v1/tasks/runs/{id}` is no alias; R9-2's move and `TestS5_OperationsComponentMovesWhole` removed, `TestS5_OperationsComponentStaysBooks` added; S5 alias count corrected (S2 HTTP, S5, §9.6 row 1, §9.8, §9.10, §9.11).
- R10-2 / SEC10-1 — the confinement condition for ambient scopes (the effective set, HEAD's rule) at every stage and in H1; `TestWorkerKey_NoAmbientScopes` (S4 r2) and `TestH1_ConfinedKeyStaysConfined` (§9.2.6, S4, §9.10, §9.13).
- R10-3 / COR10-3 / MOD10-1–MOD10-6 — `otelgin` stays on the legacy mount as at HEAD, the sole recorder of requests that reach gin; core's stage 2 records core and module routes and pre-mount refusals with no `http_route` (LEAD correction: HEAD's exporter drops scope info); `app.Options.MetricViews` carries HEAD's buckets; `otelgin is removed` and the route-report hook gone; the golden passes at S2 r2, S5 and S8 with `TestHistogramBucketsPinned` (§9.2.7, S1, S2, S5, S8, §9.11).
- R10-3 — §9.2.1 row 12: pre-mount refusals (401, 413, 429, CORS preflights, Basic-gate 401s) counted by core's HTTP metrics without an `http_route`, with its release note; "twelve rows" (§9.0, §9.3, §9.10 R-test).
- MOD10-7 (R10-3) — `ops_abandoned_calls` changes owner in exactly one PR, both books sources together (§9.2.7, S5).
- R10-7 / OPS10-1–OPS10-5 / SEC10-10 — `Stage-Release: <stage> <n|flip>` (single-release stages `1`), required on PRs that touch a not-yet-flipped stage's mapped packages and present on every non-doc commit (check (d), commit-msg hook, `/parallel-sweep` stamp); every flip PR changes `release:` and needs its predecessor's row (S6–S8 flips set `release:`); claims pass on no new violation since `<since>`; S4 r1 census against the acknowledged P1 list; health read at record time against `ci/soak_health_allow.txt` (05-health's tokens, `legacy_config_keys` included); `<since>` from the deploy timestamps `soak_record.py --deployed` records, run over ssh from the checkout, cross-checked against the S0 boot log line (`TestBootLogsBinaryID`), journal sudoers line and P15; rows keyed by landing commit, so a re-landing needs a fresh row; a revert is admitted only as the exact inverse diff of a trailered commit (§9.2.1, §9.2.2, S0, S2b, S4, S6–S8, §9.4.1, §9.7, §9.8, §9.10).
- R10-8 / OPS10-6 — S8 moves HEAD's raw-asset pipeline (plus the size cap and the `--version-json` self-test); archive, checksums, pre-update backup, container refusal and exec restart deferred-build; `TestApply_RawAssetNoBackup` in S8's done-check (S8, §9.4.1).
- R10-9 / MOD10-8 / OPS10-8 / MOD10-9 — `vet-darwin` runs without `embed_frontend` (no placeholder `web/dist`), the tag-union guards use `go list -e` and tolerate only an unbuilt listed embed output; 12's history assertion (`history_sample.txt`, `history_assert.py`) cited (§9.2.1, S0, §9.2.10, §9.10).
- R10-10 / SEC10-2 / SEC10-3 — auth-off installs keep editing integration rows as at HEAD (`HEADAuthOffWritable` extends to them); the `ABO_CORE_*` promise for integration fields removed; `Registry.Test` under `Put`'s `ProtectAdmin` rule (§9.2.1 row 3, §9.2.6).
- R10-11 / DATA10-5 / DATA10-6 — the staging marker carries `binary_id` and `staging-scrub` checks it; H1/H2 delete every pre-cutover checkpoint and old Pebble file, canaries planted before a fixture upgrade, set-asides stated (§9.3, S0, §9.8, §9.13).
- COR10-8 — `/metrics` is `Infra(…, "core:metrics.read")` on every app, held by non-confined ABO principals through `LegacyAmbientScopes` (§9.2.6, S4, S8).
- DATA10-7 — the first cutover that raises `core` lifts `{core, 0}` (§9.13).
- DATA10-8 — the S1 ABO-local swap follows 05-backup §5B.3.4 exactly; `TestS2bResumesS1Journal`; `TestInflightBeforeFirstRename` replaces S1's hook test (S1, S2b).
- SEC10-5 / SEC10-6 — the Basic gate keys keep HEAD's auth-off rule until 05-config §1.3.1's one end point, the three together (§9.2.1 row 3, §9.2.6, §9.13 H3).
- SEC10-7 — §9.13 owns one release-note text per cutover (H1 and H2 as 05-authn §11.2 quotes them); H1 deletes sessions and tokens, no key re-issue.
- OPS10-7 — `unit-lifecycle` on the Ubuntu runner VM with a `PrivateNetwork=yes` drop-in and user/directory preparation; triggered by the PRs that change the checked behaviour (§9.2.1, S0).
- OPS10-9 — the `deploy` recipe keeps the last 10 binaries by `binary_id`; archive and manifest copied together; `.manifest.json` pruned with `.sha256`; staging on linux/amd64 (§9.3, S0).
- OPS10-10 — row 10's S0-updater residue and release note; row 11 covers pre-S8 archives on S4 r2+; the S8 overview names it (§9.2.1, §9.3).
- Cross-cluster (lead-approved) — R-C: 05-updater "step 3" in S0 Endpoints; R-A, R-B, R-D, R-E requests as above.

Round 11:

- VER11-1 (R11-1) — §9.2.1 row 12 and §9.2.7: the wrapper clears `r.Pattern` before gin (`httpapi.MatchedPattern` carries it for SEC2-22), and stage 2 records into otelgin's instruments (`app.Options.HTTPMetricsScope`), so a label set both produce is one series; the "disjoint by `http_route`" argument deleted; the S2 summary row restated.
- VER11-2 (R11-2) — §9.2.1 check (d), §9.8 row 37, `test_check_soak.py` row: a bot's commit (Dependabot, Renovate, GitHub Actions; a fixed login list in `check_soak.py`) and a GitHub merge commit count as `Stage-Release: none` only in a PR touching just dependency manifests, lockfiles and `.github/`; `ratchet-lower.yml` writes `Stage-Release: none` itself; 03 §3.2 item 6 holds.
- VER11-3 (R11-3) — S5 Endpoints and UI rows: no `/api/v1/operations*` route is a core alias; all 38 stay books' gin routes, the run-acting ones on core's tasks client; the alias count under the prefix is 0.
- BLK11-1 (R11-4) — §9.2.1 "Soaks": release IDs and trailer stages add `UI0`/`UI-0`, `H1`, `H2`, `H3`, `H2H3`, with their order and predecessors; the pass criterion reads the levels the release declares (`levels:` in `ci/stage.txt`) against `--print-storage-levels` and the store's level sidecar; check (c) admits a `levels:` change only in an H flip PR; §9.3 order, UI-0 "Flip PR" row, §9.13 soak sentence and §9.8 rows 36 and 86 follow.
- R11-5 notes — check (c)'s backwards-value clause does not apply to an admitted revert (§9.2.1, §9.8 row 38); S2b: `seed` is not an offline-open user (05-storage §5S.4); §9.13 and the H1/H2 rows: a fresh verified archive before H1 and H2 (05-storage §5S.3.13 item 4, `TestH1H2_DeferredWithoutFreshVerifiedArchive`); `vet-darwin` row: the embed-only files' imports stated accurately; row 12: `http_server_response_body_size` counts uncompressed bytes from S2 r2, with its release-note clause.

Final verification:

- FIN12-1 — §9.2.1 "Bot commits", §9.8 row 37, `test_check_soak.py` row and S0 done-check: the admitted paths are a per-ecosystem table over HEAD's five Dependabot ecosystems (gomod, npm, docker, github-actions, gitsubmodule); the root-tidy commit carries `Stage-Release: none` and is pushed with the PAT; `1c2244869` and `.standards`-only cases added.
- FIN12-2 — §9.2.1 marker values gain `H1`, `H2`, `H3`, `H2H3` after `S10`; each H stage table names its flip PR (`<H> flip`); `levels:` leaves `ci/stage.txt` and the owner command writes the checked vector into the soak row's new `<levels>` field; check (c) refuses a `levels:` line; an H flip's revert is admitted only while no production tag contains it; §9.8 rows 36 and 38, §9.13 trailer sentence and tests follow.
- FIN12-3 — S5 Endpoints and §9.10: `GET …/timeline`, `GET …/v2/:id/logs/download` and `DELETE …/v2/:id/record` call `Client.Timeline`, `Log` and `Discard`; `TestS5_OperationsTimelineLogDiscardParity`.
- FIN12-4 — S5 Endpoints: the three `/api/v1/maintenance-window/*` routes are kept as books' gin routes on `Deps.Tasks`, no alias.
- FIN12-5 — §9.2.1 "Soaks", check (a), `<since>`, §9.3 and the UI-0 Flip PR row: `UI0` soaks in parallel with the next stage release and is never a predecessor; the `S4r1` flip needs a current `UI0` row.
- FIN12-6 — S2 HTTP row: the wrapper clears `r.Pattern` and carries `httpapi.MatchedPattern(ctx)`; stage 2 records into otelgin's instruments (`app.Options.HTTPMetricsScope`).
- FIN12-9 — §9.2.1 and §9.8 row 37: the merge-commit exemption is deleted (rebase-only merges); an untrailered merge commit is refused.
- FIN12-10 — §9.2.1 `<since>` and boot log, §9.13: a second boot line `boot levels=<vector>`; an H release's clock starts at the first boot that logged its raised levels, so a deferred H1/H2 boot does not start it.
- Final verification (lead) — §9.2.x: "production release tag" defined as a `v<major>.<minor>.<patch>` tag without a pre-release suffix (FIN12-2 residual).

Plan-phase consistency:

- PC-3 — §9.2.1 order, §9.3 order and S6 Flip PR row: `floor` (T8) precedes the S6 flip; S6's predecessor is always `floor`.
- PC-4 — §9.13 and the H1/H2 rows: no swap set-aside survives H1/H2 (deferred while one exists, 05-storage §5S.3.13 item 3); `TestH1H2_DeferredWhileSwapSetAsideExists`.
- PC-5 — history line R9-11: the re-issue clause marked as dropped by SEC10-7.
- PC-6 — §9.10 R-test: cell e is mandatory in S1, S4 r2 and S8; S1 and S4 Done-when name it.
- PC-7 — §9.13: H1–H3 move no package (no 04 row); their table rows are the planning-level contract until each gets its stage table.
- PC-10 — S0 Done-when: the separate throwaway `go work init` step dropped; `genproto-exclude-root` runs that probe outside the repository (03 §3.2 item 1; CLAUDE.md).
- PC-11 — S0 Done-when: the S0 PR captures `testdata/head_series_identity.golden`, commits `testdata/head_series.golden`, and `TestHEADSeriesStillPresent` passes (05-metrics §6).
- PC-12 — §9.2.1 trailer and S2 Flip PR row: a flip PR carries `<stage> flip` instead of `<stage> <n>`; S2's three flip PRs carry `S2 flip`.
- PC-13 — §9.2.7 and S2 Done-when: `TestHistogramBucketsPinned` at every stage and at each of S2 r1–r3.
- PC-14 — S1 Moves: `entity` and `coretest` land at S1 (04 §8.1 row S1).
- PC-15 — S4 Endpoints and Alias targets: `/api-keys/*` is 05-authn §4.13's; `all=true` → `GET /api/core/v1/api-keys` only for `core:users.manage`; `/api/v1/auth/api-keys/:id` → `/api/core/v1/api-keys/{id}`; `TestAlias_ListAllKeysNeedsUsersManage`.
- PC-18 — §9.2.3 core-rows row: `c/secrets/` lands at S3 with `c/config/`; each `c/` keyspace carries its stage.
- PC-20 — S4 Moves: the bridge is `internal/server/legacyauth` (03 §3.5.1).

- D10–D12 revision (2026-11-04): retitled "ABO on core: releases and the cutover" (file name kept); the S0–S10 stage sequence, UI-0 as its own release and H1–H3 replaced by the releases R0–R3 of 03 §3.8. Deleted outright: the compatibility window, the rollback cost table, soaks, the soak ledger, `ci/stage.txt`, the `Stage-Release:` trailer and the bot table (old §9.2.1); the served ledger, foreign-open record, open stamp, boot lineage, applied-steps sidecar, stepping-stone release and pre-swap floor checks (old §9.2.2); books' legacy stores and HEAD's locks across core's commit (old §9.2.3, §9.9); aliases, permanent env names, legacy env decoding, legacy atoms on input, `LegacyAmbientScopes`, `otelgin` co-recording and the HEAD metric goldens (old §9.2.4–§9.2.7); the compat ledger and `compat.Ledger` (old §9.2.9); the S0–S10 stage tables; surface contracts C3–C9 (§9.5); P8, P10, P11, P13 and P15 (§9.7); the staging scrub, staging marker, `binary_id` coupling, per-stage boot A/B and R-test (old §9.3, §9.10); the deferred per-family and hardening cutovers (§9.13). Rewritten: §9.0; §9.1 trimmed; §9.2 as the R2 cutover model (§9.2.1 release safety, keeping the named required checks, adding the release freeze of D12 F1 and rollback by restore; §9.2.2 the level stamps and the R3 floor; §9.2.3 the one-way migration with M1–M8, the checkpoint's 7-day deletion and quarantine; §9.2.4–§9.2.9 the per-surface rules and the release note; §9.2.10 kept, re-keyed); §9.3 the release overview and the reduced rehearsal; §9.4 the release tables R0–R3, with R1's CI classification and R2's flips F3–F9; §9.4.1 re-keyed; §9.6 "changed in R2" and the one kept contract (the ABS API, pinned by its conformance suite); §9.8–§9.12 cut to the native design, with a new risk table. The webhook format and the OIDC callback path are listed as changed, following 05-notify and 05-authn. M7 runs before M2 (Objections to 03, item 1, as 05-storage §5S.3.13 also requires); items 2–5 record the other differences from 03 §3.8, F5 deleting HEAD's task routes among them (with 05-tasks and 05-realtime). Restore commands, checkpoint retention and the gap-fill rule follow 05-storage §5S.3.12 and 05-config §1.12.2. Consistency pass: the five Objections to 03 are settled in 03 §3.8 (the section now records none open), so the OIDC callback path is kept (§9.6 row 4, no re-registration step), D12's answers are cited as D12 (a)–(d), the R1 reset is offline-only, the dirty set is replaced by the index rebuild, the AI series rename at F7, and the renamed tests and 05-config's rename-table section are cited. Final consistency sweep: F8 makes `/health` Basic-exempt (`TestBasicGate_ExemptSetGolden`), §9.2.7 keeps books' process gauges unchanged and has core's AI families replace HEAD's at F7, the undefined compose test is replaced by 05-config's `TestFailClosed_MoreRestrictiveWins`, the checkpoint's keyring is stated per L9, and the runbook escapes a `failed` checkpoint restore with the previous binary's `storage restore-offline --checkpoint`.

Round 13 (2026-11-04): R1 (IMPL-1, IMPL-2, IMPL-3, IMPL-4, IMPL-5, IMPL-12, XFILE-4: the F2 foundation PRs in §9.2.1, §9.2.3, §9.3, R2 content, the flip table and §9.4.1; the three late-owner kinds at F5, extended at F7 and F8; the executor creates no `ops` instrument before F5), R2 (CONFORM-1: the pre-freeze release in §9.2, §9.2.1, §9.3, R2, §9.11 row 16), R3 (IMPL-9: the R1 freeze, owner-overridable), R4 (CONFORM-2, CONFORM-6, DATA-8: checkpoint retention, the R1 sweeper, prune command and release-note line), R5 (XFILE-3), R6 (CONFORM-4, DATA-16, IMPL-7: `legacy-file-cleanup`, the host-loss recipe after R3, the fresh-install batch), R7 (DATA-1), R8 (DATA-2), R9 (DATA-3), R10 (DATA-5), R11 (DATA-7, DATA-9), R12 (IMPL-6), R13 (IMPL-8), R14 (XFILE-1, CONFORM-11), R15 (XFILE-2), R17 (IMPL-11, XFILE-6, XFILE-7, XFILE-11), R19 (SEC-2, and the deleted OAuth start path with its release-note line), R20 (SEC-3, SEC-4), R21 (SEC-5), SEC-1 (ABS logins through `auth.Compat`), SEC-9, SEC-10, SEC-11, SEC-13, IMPL-10, IMPL-13, IMPL-14, XFILE-8, XFILE-13, CONFORM-7, CONFORM-13, DATA-11, DATA-13, D13 (the read-only cutover preflight, `storage cutover-blockers` with `list [--out]`, `fix <key> --from` and `delete <key> --confirm`, permanent merges with 409 `merge_permanent`, no old-URL redirects, module name decided); lead follow-up: the R2 release-note list (§9.6 rows 4, 24 and 26) names the deleted OAuth start path, the permanent merges and the auth-off 401 on the credential-metadata and audit routes, with settings keeping their auth-off reach; the overlay lists 05-rbac's custom role rows.

Round 14 (2026-11-05): V1 (§9.2.3, §9.8: the cutover preflight runs only on a store whose cutover has not started; a resumed cutover skips it and M3's own check covers the rows not yet imported), V4 (§9.2.1: an archive's levels read from the archived store), V5 (§9.2.3 dry run and §9.3 `--into` copy: the scratch area `.mediacore-checkpoints/scratch/`, modes, deletion and the sweeper, throw-away keyring), V6 (`cutover-blockers list` prints keys and reasons; `list --out` writes the raw rows with a warning), V10 (runbook: `make rollback-restore RELEASE=<pre-freeze tag>` through `updater fetch-release`, the tag from the checkpoint's `from_release`, `--accept-loss` on both branches, in §9.2.1, the R2 rollback row and §9.8), V11 (§9.2.6: SEC-12's owner condition as 05-rbac §6.1), V12 (§9.2.3: the sweeper runs after readiness), V13 (F2 row: `core.storage` and `core.secrets` registered kernel-side), V14 (no `version` on `/health`: the R1 endpoints row and release note, the kept list and out-of-scope defect 5 use the authenticated `GET /api/v1/system/status`), V16 (`legacy-file-cleanup` is a start hook and skips only `.encryption_key` while `secrets_keyring_not_exported` is raised), V17 (invite allow-list rows dropped from the preflight), V19 (R1 release note: the reset button returns with <R2 release>), V22 (HEAD's OAuth start path kept at its exact path: F4 row, §9.6 row 4, the kept contract; the deletion text and its release-note line removed), V23 (round-13 ruling numbers replaced by the finding IDs they disposed throughout §9.2–§9.8).
Round 15 (2026-11-05): W4 (§9.2.3: the cutover has started when books' level has reached M7's target or any cutover step has a progress record in `c/storage/dm/`; the preflight runs if and only if it has not started; checkpoint reuse is not part of the condition), W7 (the R1 endpoints row and out-of-scope defect 5: the deploy post-check reads the version by running `ssh $(DEPLOY_HOST) $(DEPLOY_BIN) --version-json`, not over an HTTP route; `Makefile.local.example` matches), W8 (lead fix: `from_release` is the embedded pre-freeze tag; §9.2.1 and the §9.8 rollback row: `from_release` is the app version HEAD's store recorded for its last boot before the cutover, with the pre-freeze tag the cutover build embeds as the fallback; on the archive branch the refusal names the release that produced the archive, from its manifest, for `legacy-abo/0` the version string the archive carries), W9 (§9.2.1, §9.6 rollback row and §9.8: the fetched binary's levels are compared with the source's levels, the checkpoint's `from_vector` or the archive's), W10 (the fetch has apply's trust level: HTTPS from the configured release source plus the checksum check apply performs if the source publishes one; it is not a signature check). W3, W5, W6 checked: §9.2.3's dry-run text already names `.mediacore-checkpoints/scratch/`. Lead fix: `from_release` is the embedded pre-freeze tag (HEAD records no app version).

Round 16 (2026-11-05): plan-gap fixes G5 (§9.4 R0 content: `auto_update_paused` is shown in the authenticated `degraded` field R0 adds to `GET /api/v1/system/status` with that one token, never on `/health`; R1's endpoints row: R1 extends the field with the posture tokens), G9 (§9.2.2, §9.4 R3 verification and the failure table: `InstallFirst` holds the R2 release's tag, written when R2 is tagged; the refusal's last line is `install <R2 tag> first`, "install the R2 release (its tag) first").

Round 16 (2026-11-05): review fixes X1 (§9.4 F2 row and the §9.2 CI table: `archtest/tasklint` lands at F2, not F5, with its exemption list, which F5 requires empty).

Round 16 (2026-11-05): review fixes X4 (§9.4 R3 content: `UpgradeFloor{InstallFirst: <R2 tag>}`).

Owner answers (2026-11-05): D15 — §9.2.1: the required checks enforced by a no-bypass ruleset with the merge queue (Q-I); both freezes are the repository variable `vars.RELEASE_FREEZE`, toggled by the owner and logged in the tracking issue, the R1 freeze kept (advisor S3, S5); §9.2.3: M8 keeps one fixed ID (advisor S2); §9.4 R1 Owner and the R2 flip table; §9.12 O10 resolved.
