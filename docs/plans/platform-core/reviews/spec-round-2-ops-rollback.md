<!-- file: docs/plans/platform-core/reviews/spec-round-2-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: 46dcc6a3-8844-4102-9c3e-739b429b1eaa -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2: ops, rollback and incremental delivery (lens OPS2)

Lens: after **every** stage of the revised [09](../../../specs/platform-core/09-abo-on-core.md) v1.1.1
(S0, S1, S2, S2b, UI-0, S3–S10, plus contracts C3–C9), can ABO be shipped, deployed, measured and rolled
back to the previous release, covering DB, config, env, the previous UI build and ABS clients? Is boot
time budgeted, and is no big-bang step left? Code anchors are `path:line @748fd787a`. Spec anchors are
`file §section`.

Counts: **0 BLOCKER, 17 MAJOR, 9 MINOR** (26 findings). The round-1 regression table comes first.

## A. Regression check: round-1 OPS BLOCKERs and MAJORs

| Round-1 | Status in round 2 | Where it is still open |
|---|---|---|
| OPS-1 Docker breaks at S0 | **Fixed in the Dockerfiles; the PR job is half-fixed** | OPS2-14 (trigger holes; job scope vs done-check) |
| OPS-2 skipped releases | **Fixed for S3–C8; reopened by S10** | OPS2-11 (S10 deletes adopt steps, yet "no stepping stone" is still claimed) |
| OPS-3 reader-capability levels | Fixed (09 §9.2.2 table, 05-config "Reader capability") | — |
| OPS-4 rollback widens a key | Fixed (exact-or-disabled mirror, `MirrorDisabled`+`Fresh`) | — |
| OPS-5 legacy env names | **Half-fixed** | OPS2-12 (canonical twins in deploy files override the legacy names), OPS2-21 (the scan test fails on HEAD's own unit) |
| OPS-6 updater auto-rollback vs guard | **Half-fixed** | OPS2-2, OPS2-3, OPS2-4, OPS2-5, OPS2-6 |
| OPS-7 two S5 designs | Fixed (05-tasks model) | OPS2-9 (`req-migrate` deletes legacy rows inside W), OPS2-22 (stale "raises the level" cell) |
| OPS-8 05-storage vs 09 | Fixed (one guard, 200 000 cap, no `SupportedStorageFormat` raise) | OPS2-10 (projection value collides with the storage-efficiency track) |
| OPS-9 S3 three models | Fixed (05-config §1.12.2 owns it) | OPS2-8 (blanking rule blind to pre-guard boots) |
| OPS-10 wall-clock alias sunset | Fixed (05-http-api §4.6 "No clock flips anything") | — |
| OPS-11 auth engine S2 vs S4 | Fixed (05-http-api §8.1 rows S2/S4 match 09) | — |
| OPS-12 sub-spec batches unstaged | Fixed (09 §9.4.1) | OPS2-15 (the map exposes three bundled stages) |
| OPS-13 shutdown premise | Fixed (05-health §5H.4, S2 ships timeouts) | OPS2-23 (budget hot-reload contradiction, KillMode, `docker run`, owner action) |
| OPS-14 backup format / stale restore | Fixed (05-backup §5B.3.4 step a, legacy names) | — |
| OPS-15 live restore | Fixed (staged restore in S1, C3/C4 precondition) | — |
| OPS-16 removal gate not measurable | **Half-fixed** (persisted record exists) | OPS2-7 (boot-only surfaces, pre-storage config uses, non-recording gaps) |
| OPS-17 dials | Fixed | — |
| OPS-18 flip PR | **Specified, not enforceable** | OPS2-1 (merges are not CI-gated; RCs and auto-promoted production minors are cut before CI runs) |
| OPS-19 lossy reconcile | Fixed (rebuild-from-legacy, per-principal fail-closed) | — |
| OPS-20 R-test misses clients | **Half-fixed** | OPS2-13 (`X-App-Build` has no owner after S2), OPS2-16 (cells missing per stage), OPS2-25 (`prev` tag unknowable, job not wired) |
| OPS-21 ABS only at S10 | **Half-fixed** | OPS2-16 (S6/S9 named in §9.3, missing from done-checks; P7 lists differ) |
| OPS-22 boot budget only S2 | **Half-fixed** | OPS2-17 (05-health forbids startup `EXTEND_TIMEOUT_USEC`), OPS2-16, OPS2-24 |
| OPS-23 auto-update users stranded at S8 | Fixed on the signing side | OPS2-4 (the updater cannot write the committed install layout at all) |

## B. Findings

### OPS2-1 — MAJOR — Release gating: RCs and production minors are cut from main before CI runs, so the flip-PR, contract and ratchet gates gate nothing

- **Spec:** 09 §9.1 row "Releases" ("production minors by manual dispatch, irregular cadence"); §9.2.1
  W (a) "two production minor releases" and "One flip PR per stage"; §9.5 "Every contract is a PR gated by
  `TestNoContractBeforeSunset` and P8"; §9.3 "A stage ships as one or more production releases; its flip
  PR is in exactly one".
- **Evidence:** `.github/workflows/auto-revert.yml:8-11`: "This repository does not gate merges on CI.
  Anything can land; the gate runs afterwards". `prerelease.yml:7-13` cuts an RC on every push to main, with
  no dependency on CI. `prerelease.yml:59-131` (`check-rc-ordinal`) **automatically dispatches
  `release-prod.yml`** when a base version reaches 10 RCs. That job builds the Docker image
  (`release-prod.yml:53`) and moves `releases/latest`, which the updater and `:latest` users follow. The
  §9.1 claim is therefore wrong. The consequences: (1) W (a) collapses to about 20 merges, so only the
  30-day floor is real. (2) `TestAdoptedPrefixWritesGoThroughMirror`, `TestNoContractBeforeSunset`,
  `TestCompatLedgerMatchesCode`, `TestLegacyRoleReachUnchanged` and the R-test all run after the merge. A
  dark PR that writes `u:` outside the mirror, or a contract merged early, is published as an RC at once
  and can become the tenth RC, which is auto-promoted to production before auto-revert acts. (3) The
  operator does not choose which production release carries a flip.
- **Fix:** In §9.1, correct the row and cite `prerelease.yml:59-131`. In §9.2.1, add "Release gating
  during the extraction":
  - (a) Every PR that touches `docs/compat-surfaces.md`, a `RaisesLevel` step, an adopt step or a `Mirror`
    declaration carries the label `compat-gate`. A required pre-merge status (the `make ci` subset plus
    `make compat-rehearsal`) blocks such PRs.
  - (b) `check-rc-ordinal` refuses to auto-promote when the RC commit's CI is not green, or when the ledger
    has a stage in state "dark PRs landed, flip not landed".
  - (c) Redefine W (a) as "two production minors **and** 30 days", and say that minors are auto-promoted.

### OPS2-2 — MAJOR — A contract can run on the first boot of an auto-applied release, which disables the updater's own rollback

- **Spec:** 05-storage §5S.3.5 "Skipped releases" (a deferred step is re-checked on every boot); 09 §9.2.2
  contract rule; 05-updater §5U.4 "Confirm / roll back" (`rollback_blocked` when `ReaderCheck(.prev)`
  refuses).
- **Evidence:** `RequiresServed` is per **level**, not per build: C3's level 2 has served since S3. On the
  first boot of the C3 build the preconditions already hold, so P4 raises the level before readiness. If
  that build then fails readiness (warmup crash, OOM), `Boot` finds `.prev` refused, records
  `rollback_blocked` and keeps the failing binary. Because deferral is open-ended, a contract can also fire on
  some unattended later restart (crash, updater restart, host reboot). The operator never sees the moment
  `make rollback` stops working.
- **Fix:** In 05-storage §5S.3.5, add a precondition kind `RequiresSelfServed{For}`. A step with
  `RaisesLevel` never runs on (a) a boot where `<state-dir>/updates/pending.json` exists, or (b) a boot of a
  build whose own served record is younger than `max(core.updater.confirm_after, 1h)` of continuous
  readiness. Then a contract always runs after the update is confirmed, and both `Boot` and `make rollback`
  can still undo a broken contract release. Health reports `storage_contract_pending` with the earliest run
  time from the first boot onward. Add R-test cell (d'): "contract release fails readiness on first boot →
  `Boot` rollback allowed".

### OPS2-3 — MAJOR — The updater's `Boot` has no place in the normative boot sequence, so config-stage failures never trigger an automatic rollback

- **Spec:** 05-updater §5U.3 ("Boot runs FIRST in app.New, before storage opens"); 06 §6.5.1 (P0a → P0 →
  … P8, with no updater phase); 09 §9.2.5 and §9.8 (a retired alias refuses boot); 09 C3 ("YAML legacy keys
  refused at boot").
- **Evidence:** 06's table starts with P0a (restore swap), then P0 (config `Store.Boot`). `Boot` needs
  `<state-dir>/updates` and the DB path for `ReaderCheck`, and both are config-derived. If `Boot` runs after
  P0, then every boot refusal in P0 leaves the unit crash-looping into `StartLimitBurst=5`
  (`deploy/audiobook-organizer.service:81-82`) with no rollback. Those refusals are `*RetiredAliasError`,
  C3's YAML refusal and a new config validation. If `Boot` runs after P0a, a staged restore makes
  `ReaderCheck` return `restore_pending` (05-storage §5S.3.7), and the interaction is undefined.
- **Fix:** In 06 §6.5.1, add phase **P−1 `updater.Boot`** before P0a. Like P0a, it resolves only the data
  and state directories and the DB path from flags and env, never from the config file or the DB. State
  that P0 refusals count as failed boots. Define the order with P0a: rollback is decided before any restore
  swap. If `pending.json` exists and the new binary is failing, `Boot` restores `.prev`, which then rejects
  the intent as foreign (05-backup step a). In 05-updater, make `ReaderCheck` ignore `restore_pending` when
  the intent was staged by the failing build.

### OPS2-4 — MAJOR — The updater (the S0 hotfix and core `updater`) needs to write to the executable's directory, but the committed install layout does not allow it

- **Spec:** 09 S0 "Updater hotfix" and its done-check ("a scripted auto-update on a staging unit ends
  `active (running)`"); 05-updater §5U.4 steps 4 and 8 ("`<exe>.new` in the executable's directory … rename
  `<exe>` → `<exe>.prev`").
- **Evidence:** The committed unit installs the binary with `sudo cp … /usr/local/bin/`
  (`deploy/audiobook-organizer.service:41-43`) and runs as `User=audiobook` with `NoNewPrivileges=yes`
  (`:88,122`). `make deploy`/`make rollback` use `sudo cp` (`Makefile:912-914`, `Makefile.local.example:96-98`).
  So the HEAD updater's `os.OpenFile(execPath+".new")` (`internal/updater/updater.go:254-258`) fails with
  EACCES, and so does the core updater's rename. The HEAD updater also looks for a raw asset
  `audiobook-organizer-<os>-<arch>` (`updater.go:318-326`), while goreleaser publishes tar.gz archives
  (05-updater §5U.2, which marks the asset contract UNKNOWN). The S0 done-check therefore cannot pass on the
  committed layout, and OPS-23's "users silently stop receiving updates" already holds today for every
  systemd install.
- **Fix:** In 05-updater §5U.4 step 1, add the precondition "executable directory writable by the process"
  (checked with `os.Root` create/remove of a probe), with `DisabledReason: "binary_not_writable"` and
  component Degraded while mode ≥ check. In §5U.5, list the supported layouts (a service-owned binary
  directory such as `/var/lib/audiobook-organizer/bin` referenced by `ExecStart`, or macOS
  user-writable). In 09 S0, replace the staging done-check with a unit/integration test: a fake release
  server serves the HEAD asset name to a binary in a temp directory, and the test asserts exit 75 and that
  `.prev` is kept. S0 also records, in the ledger, whether the reusable release publishes the raw asset.

### OPS2-5 — MAJOR — From S0 to S7 the hotfixed HEAD updater applies adoption releases unattended, with no guard, no backup and no automatic rollback

- **Spec:** 09 S0 "Updater hotfix" (exit 75 so `Restart=on-failure` restarts); §9.8 "Adoption or import
  error → fail-closed: boot halts"; S3/S4/S5 first boots run adoption.
- **Evidence:** Before S0, an auto-update stopped the service (exit 0, `updater.go:331-334`), so a broken
  release was noticed. After the hotfix, the HEAD updater installs `releases/latest`, which can be any
  auto-promoted minor (OPS2-1), and restarts into it. That updater does no verification and no pre-update
  backup (`updater.go:214-280`), has no boot counter and no `ReaderCheck`. An adoption that fails closed
  (P2 branch B: an unknown atom in a custom role) halts boot until `StartLimitBurst` leaves the unit
  `failed` overnight. The hotfix turns a visible stop into an unattended outage on exactly the releases that
  carry adoptions.
- **Fix:** In 09 S0, the hotfix also makes **scheduled** apply check-only until S8: it notifies, health is
  Degraded `auto_update_paused_until_core_updater`, and the Settings UI shows a banner. A manual apply stays
  available and is behind the credential guard. This keeps OPS-23's "never silently" goal. If the owner
  wants unattended applies before S8, S0 must instead ship a minimal boot-counter rollback (marker file +
  `.prev` restore + the `--print-storage-format` check) in the HEAD updater. Record the choice in §9.12.

### OPS2-6 — MAJOR — After an automatic rollback, the updater re-applies the same release on the next window, every night

- **Spec:** 05-updater §5U.4 (Check compares semver to the running binary; Apply is scheduled nightly);
  §5U.5 "Downgrades".
- **Evidence:** After `Boot` restores `.prev`, the running version is older than `Latest` again, so the
  next nightly `core.updater.apply` downloads, backs up, swaps and fails again. Each cycle costs up to
  4 boots × the start timeout, plus a pre-update backup. Nothing records "version X failed here". The same
  loop follows C3's YAML refusal or any P0 refusal on an install whose config is not yet migrated.
- **Fix:** In 05-updater §5U.4, `Boot`'s rollback appends `{version, reason, at}` to
  `updates/history.jsonl`. Scheduled apply skips any version ≤ the newest rolled-back version, and
  `Status.SkippedAfterRollback` names it. Only a manual apply or a newer release lifts the skip. Add the test
  `TestApply_SkipsVersionThatRolledBack` and a `rollbacks_total{outcome="done"}` alert snippet.

### OPS2-7 — MAJOR — The persisted usage gate can read "unused" while a surface is in use

- **Spec:** 09 §9.2.1 (c) and §9.2.9 `Handle.Use`; P8; C3 preconditions ("no use on the `config`/`env`
  window aliases"); 05-config §1.3.6 (`OnLegacyUse` is wired to `mediacore_compat_legacy_use_total`) and
  §1.8.
- **Evidence:**
  - (a) Config and env aliases are resolved in 06 P0, before storage (P2) and before the compat ledger
    exists. 05-config wires alias use only to the process counter, so no persisted record is ever written
    for those surfaces.
  - (b) Even if buffered, an env or YAML alias is "used" once per boot. A service that is not restarted for
    14 days shows `last_used_at` older than 14 days although the name is set in its unit. Example:
    `ABS_JWT_SECRET` is a `BindEnv` window alias (`internal/config/abs_config.go:45-47`). After C3 it is
    "ignored + one WARN" (§9.8), and absauth then refuses to boot without it
    (`internal/server/absauth/config.go:20-22`).
  - (c) Days on which the store ran a non-recording build (a rollback to a pre-S2b build) count as unused.
  - (d) 09 counts surface `env`; 05-config records only `config`.
- **Fix:**
  - In 09 §9.2.9, add `Handle.Present(bootID)` for boot-read surfaces. The persisted record stores
    `last_seen_set_boot` and `boots_seen`.
  - The gate for `config`/`env` becomes "not set on any boot within the last 14 days, and at least one
    recording boot in that period".
  - For every surface, the gate also requires that the served ledger shows a recording level continuously
    for those 14 days.
  - In 06, P0 alias uses are buffered and flushed after P2.
  - In 05-config, record env and file aliases on surface `env` and DB/blob aliases on `config`.
  - In §9.8, every window alias of a **secret-class** key is treated like `ProtectAdmin` after W, so it is
    refused with the canonical name rather than ignored.

### OPS2-8 — MAJOR — The plaintext-blanking rule cannot see a pre-guard build that booted in between

- **Spec:** 05-config §1.12.2 "Mirror" ("written **empty** when the build that booted this store
  immediately before the current one (09 served ledger) is at level T or later"); 09 S3 "Plaintext".
- **Evidence:** Only guard-aware builds write `c/storage/served/…` (05-storage §5S.3.7). A sequence such as
  deploy S0 → `make rollback` to HEAD → deploy S3 leaves the ledger showing S0 as the previous boot, while
  the HEAD build that really ran last, and is now `.prev` (`Makefile:912-914` keeps `.prev` = the binary
  rolled back to), cannot read legacy-format rows. Docker users can repin any older image tag the same way.
  The S3 build blanks the blob, and a rollback to `.prev` loses every secret outside the 4 legacy rows.
- **Fix:** In 05-config §1.12.2, blank only when **both** of these hold: (1) the ledger says the previous
  boot was level ≥ 1, and (2) `<exe>.prev` exists and `--print-storage-levels` reports `core ≥ 1`. Use the
  same probe as `ReaderCheck`, which the process can already run. In a container or with no `.prev`, keep
  plaintext until C3 and raise `secrets_plaintext_mirror` (documented). Add the R-test cell "pre-guard boot
  between two guard-aware boots".

### OPS2-9 — MAJOR — S5 deletes the legacy `Requires` stamps inside the window; a rollback to S4 re-runs prerequisites across the whole library

- **Spec:** 05-tasks §5T.6 row `op:completion:`/`op:deprev:` ("a resumable `DataMigration`
  `core.tasks.req-migrate` … moves the rest and then the fallback is removed"); §5T.8 T0+T1 includes
  `core.tasks.req-migrate`; 05-storage §5S.9.1 row 13 ("deleted by … `core.tasks.req-migrate`"); 09 S5
  rollback ("runs … no double execution").
- **Evidence:** The stamps are per subject × op type (`internal/database/pebble_store_ops_v2.go:1175-1192`),
  potentially one per book per kind. After T0+T1, new stamps go only to `c/tasks/req/`, and `req-migrate`
  removes the legacy ones while S4 is still a supported rollback target. S4 then finds no completion record
  for any subject and re-queues prerequisites (fingerprint, metadata, dedup) for the whole library: the
  multi-hour single-core incident class named in CLAUDE.md. This deletes legacy data before its contract,
  which §9.2.1 step 3 forbids.
- **Fix:** In 05-tasks §5T.6, during W `req-migrate` **copies** and never deletes. New stamps are mirrored to
  the legacy key in the same batch, with the same rule as the clock mirror. The legacy delete and the removal
  of the fallback move into C5 (`Adopts: [req-migrate]`). Fix 05-storage §5S.9.1 row 13 and the 09 S5
  rollback row, and add the R-test row "a stamp written on cur is honoured on prev".

### OPS2-10 — MAJOR — The legacy projection's hard-coded value 2 collides with the storage-efficiency program's real format 2

- **Spec:** 05-storage §5S.3.7 "Legacy projection (books)" ("value **1** while every owner's level is 0,
  **2** once any level was raised; every pre-guard build supports only 1"); 09 §9.0 (the storage-efficiency
  program "is a separate track").
- **Evidence:** `SupportedStorageFormat = 1` is planned to rise to 2 by the storage-efficiency release
  (`internal/database/storage_format.go:44-50`; `docs/design/2026-10-03-storage-efficiency-design.md`).
  Nothing orders that track against S0/S2b. If release B ships as an ABO release before the guard lands,
  pre-guard builds that support 2 exist. They accept a projection value of 2 and open a C3/C4-contracted
  store. They also refuse an "older" store (05-storage §5S.2 row "older or marked stores"), so the
  projection's 1 would refuse a real B-converted store.
- **Fix:** In 05-storage §5S.3.7, define the projection as `LegacyMaxFormat + 1` once any level is raised,
  where `LegacyMaxFormat` is the highest `SupportedStorageFormat` of any pre-guard release, recorded as a
  constant with a test that greps the release history. Map an existing B-converted store to books level ≥ 1
  at the first guard-aware boot. In 09 §9.0, add the ordering rule: release B ships only as a books
  `DataMigration` on core (S2b or later), or, if it ships earlier, the projection constant moves first.

### OPS2-11 — MAJOR — S10 deletes adopt steps, yet 09 and 05-updater still promise that no stepping-stone release is needed

- **Spec:** 09 S10 ("the mirror code of every contract that has run (a build without a stage's adopt step
  refuses an unadopted store)"); 09 §9.2.2 ("No stepping-stone release is required, so release manifests
  need no `min_upgrade_from`"); 05-storage §5S.3.11 "Adopt step … stays registered in every later build";
  05-updater §5U.13 item 2.
- **Evidence:** The three statements contradict each other. After S10, a HEAD-era install that jumps to
  `releases/latest` fails with `ErrAdoptMissing`. That covers the HEAD updater, `:latest` users and a restore
  of an old archive. HEAD's updater has already deleted `.old` (`updater.go:279-280`), so the install is down with
  no binary to return to.
- **Fix:** Pick one. **Preferred:** adopt steps stay registered forever (05-storage wins), and S10 deletes
  only mirror, rebuild, reconcile and alias code. Fix the S10 text accordingly. **Otherwise:** keep
  `min_upgrade_from` in the release manifest, make the updater refuse a release that needs a stepping stone,
  print a Docker entrypoint message naming the release to install, and remove the "no stepping stone"
  sentence.

### OPS2-12 — MAJOR — S3's "canonical names in addition to legacy names" in the deploy files can point ABO at an empty database

- **Spec:** 09 S3 "Deploy files"; 05-config §1.3.5 ("if both are set with different values the canonical
  wins and a warning plus health token `config_alias_conflict`"); 05-storage §5S.4 step 1 (refuses to create
  a store only for **retired or unread** DB-path names).
- **Evidence:** Operators override the base unit with drop-ins
  (`deploy/audiobook-organizer.service:50-60`; `deploy/local.conf.example`). Suppose the shipped unit sets
  both `ABO_CORE_STORAGE_PATH=<default>` and `DATABASE_PATH=<default>`, and an operator's drop-in sets
  `Environment=DATABASE_PATH=<real>`. Then the base unit's canonical value wins. `DATABASE_PATH` is a
  permanent, *read* alias, so the refuse-to-create rule does not fire, and ABO opens or creates an empty
  store at the default path. This is the exact hazard `internal/config/config.go:2519-2544` records.
- **Fix:** In 09 S3, shipped deploy files keep **only** the permanent legacy names for keys that have one
  (`DATABASE_PATH`, `BASIC_AUTH_*`, `FP_PARALLEL_WORKERS`, …), and canonical names appear only for keys
  without a permanent alias. In 05-config §1.3.5, an alias conflict on a key at `ProtectAdmin` or higher
  (DB path, sign-in, server paths) is **fail-closed** and names both variables. Add
  `TestAliasConflictOnProtectedKeyRefusesBoot`.

### OPS2-13 — MAJOR — The S0 reload check (`X-App-Build`) has no owner after S2 and conflicts with 07's build-ID design

- **Spec:** 09 S0 "Endpoints" and "UI"; §9.3 (the reload check protects tabs across S4–S9); 05-http-api §8.1
  (the perimeter list moved at S2); 07 §7.x shell-adoption table ("`ui_build_id` is `""` so the build-ID
  banner never shows"), §7.x build ID via the `<meta name="mediacore-build">` tag.
- **Evidence:** `X-App-Build` appears in no file but 09 (grep of 05-http-api, 06 and 07). S2 removes gin's
  global middleware "in the same PR" and lists every header it moves. `X-App-Build` is not among them, so
  after S2 the header the S0 check relies on can silently disappear. 07 defines a different mechanism
  (manifest `ui_build_id` + meta tag + `"dev"` exclusion) and says that no banner shows before shell
  adoption, which contradicts 09's reliance on the check for S4–S8 rollbacks. 09 also does not say what the
  check does when the header is absent (a pre-S0 `prev`).
- **Fix:** Give one mechanism one owner. In 05-http-api §8.1, the perimeter emits `X-App-Build` on every
  response (legacy mount, 404 and 410 included) from S2. 07 adopts the same build ID source (the meta tag
  and `MEDIACORE_BUILD_ID`, with `"dev"` skipped) for ABO's pre-shell bundle in S0. Its table row says the
  S0 check is active before shell adoption. A missing header on an API response counts as a mismatch, so the
  user is offered a reload. Add the header to S2's perimeter goldens.

### OPS2-14 — MAJOR — The Docker PR job still leaves the round-1 failure class open

- **Spec:** 09 S0 "Docker" row (path filter: `go.mod`, `core/go.mod`, `Dockerfile*`, `.dockerignore`,
  `web/package*.json`, `core/web/**`; "builds the image up to the `go-builder` stage") vs the S0 done-check
  ("`docker build .` … exit 0"); §9.1 row "only the manual production release builds the image".
- **Evidence:**
  - Both Dockerfiles now call `scripts/ci/build_web.sh` (09 S0 "Web build entry points"), yet the script is
    not in the filter.
  - Also missing from the filter: `go.sum`, `core/go.sum` (a Dependabot bump that changes only sums) and
    `web/**` sources such as `vite.config.ts`, which UI-0 edits. The frontend stage runs a full
    `npm run build` (`Dockerfile:15-21`).
  - The job stops at `go-builder`, while the done-check builds the full image. Nothing runs the image:
    runtime `alpine` + `ffmpeg`, the `HEALTHCHECK` and `CMD --db` (`Dockerfile:82-104`) go unchecked.
  - Prereleases still skip Docker (`prerelease.yml:48-49`), and production minors are auto-promoted
    (OPS2-1), so the first full image build is still a production release.
  - Which Dockerfile the reusable release actually builds is unverified.
- **Fix:** In 09 S0, add `go.sum`, `core/go.sum`, `scripts/ci/build_web.sh`, `web/**` and `core/**/*.go`
  under `//go:embed` users to the filter, or simply run the job on every PR touching non-doc files with
  layer caching. Build the **full** image, run it with a seeded volume, and wait for `"ready":true` on
  `/health` and for `--print-storage-levels` inside the container. Turn on Docker build without push in
  `prerelease.yml`. Record in the ledger which Dockerfile `reusable-release.yml` uses.

### OPS2-15 — MAJOR — Three stages remain big-bang releases

- **Spec:** 09 §9.4.1 rows for S2 (H0, logging stages 1–2, lifecycle and budget, all "S2"; no "Releases"
  row in S2), S3 ("05-config phase X + façade stage 1 (viper out of boot path)" as **one** batch), and S4
  (adoption, `LegacyAuthCore`, native routes with the replaced gin routes deleted in the same PR, and UI
  switch "in the **same** build"; one flip).
- **Evidence:**
  - S2 changes the process skeleton (listeners, TLS/H3, peer allow-list), the perimeter, the stop order and
    HTTP drain, the log file path and the config loader at once. A regression cannot be attributed, and
    rollback undoes all of it.
  - S3 couples a parser swap affecting ~251 keys plus 142 `BindEnv` calls (`internal/config`, 142 call
    sites at the evidence commit) with the config/secrets truth flip.
  - In S4, legacy stays authoritative during W (§9.2.3), so the adoption could ship **dark** before the
    auth engine swap at no rollback cost. Bundling them makes the riskiest stage also the largest.
- **Fix:** Add a "Releases" row to each of the three stages:
  - **S2:** r1 composition root, lifecycle and listeners (perimeter still in gin); r2 perimeter move (H0);
    r3 logging stages 1–2. Each release has its own done-check from the existing S2 list.
  - **S3:** r1 façade + core `Load` with aliases, no data change; done-check: the effective-config golden
    for production env, YAML and blob is identical to S2b's. r2 phase X (the flip).
  - **S4:** r1 adoption + mirror + rebuild, with no reader of the native copy and P1 census equality; r2
    flip (`LegacyAuthCore`, native routes, UI).
  - Mirror the splits in §9.4.1.

### OPS2-16 — MAJOR — Per-stage done-checks do not deliver what §9.3 promises (boot budget, R-test, UI and ABS cells)

- **Spec:** 09 §9.3 ("Each stage's 'Done when' includes" the boot budget and the R-test; "UI bundle and an
  external ABS client wherever the stage changes their request path (S2, S4, S6, S9)"); §9.10 ("Cells a–c
  are mandatory in … S2, S4, S6, S9"); P7 ("gate S2, S4 and S9"); §9.4.1 row "07 §7.19 step 7 … S5–S9, per
  native route".
- **Evidence:** Reading the Done-when rows:

  | Stage | Missing |
  |---|---|
  | S0 | R-test, and a live `make rollback` S0 → HEAD (S0 rewrites `storage_format_guard.py` and adds the sudoers line; a bug blocks every later rollback) |
  | S1 | recording the boot baseline as an artifact, and the R-test |
  | UI-0 | boot budget, R-test, cells a/b |
  | S5, S7, S8 | cells a/b, although UI calls move to native routes in those stages |
  | S6 | "R-test green" and ABS cell c (named in §9.3; absent from P7) |
  | S8 | "R-test green" (only cells d and e) |
  | S9 | "R-test green" and ABS cell c (named in §9.3 and P7) |

- **Fix:** Add one sentence to every stage table: "Done when also includes §9.3's boot budget and R-test."
  Make cells a/b mandatory for **every** release that moves a UI call (rule: any diff under
  `web/src/services` or the generated client). Use one list of ABS-path stages in §9.3, §9.10 and P7
  (S2, S4, S6, S9). Add to S0: "`make rollback` on staging S0 → HEAD and HEAD → S0 passes the new guard, and
  `deploy-match-unit-type.sh` runs". Add to S1: "write the S1 boot baseline (p50 of 3 boots on the restored
  copy) into the ledger".

### OPS2-17 — MAJOR — 05-health forbids the startup `EXTEND_TIMEOUT_USEC` that HEAD and 05-storage rely on

- **Spec:** 05-health §5H.3.4 ("`EXTEND_TIMEOUT_USEC` only through the tasks executor's bounded
  extension"); 05-storage §5S.3.5 "Systemd budget" ("Each step extends the start timeout … through
  `health`'s notifier"); 09 §9.3 (boot budget), `DEPLOY_READY_TIMEOUT` default 300.
- **Evidence:** HEAD extends the start timeout by 10 min per startup step: store open and each migration
  (`internal/database/startup_notify.go:15-27`, `internal/sdnotify/sdnotify.go:46-53`), and search index
  open (`internal/search/register.go:55`). The unit relies on this (`deploy/audiobook-organizer.service:28-31`).
  An implementer following 05-health removes it, so checkpoint, adoption, contract deletes in 10 000-key
  batches and over-cap paths are killed at `TimeoutStartSec=300` and restart-loop. Separately,
  `deploy-wait-ready.sh` waits a fixed 300 s (`scripts/deploy-wait-ready.sh:13`). A legitimately extended
  start then "fails" the deploy and invites a rollback that `systemctl restart` turns into a mid-migration
  kill.
- **Fix:** In 05-health §5H.3.4, list both uses: (1) the startup steps' per-step extension from 05-storage
  (store open, each migration step or chunk group, index open) with the HEAD budget, and (2) the
  executor's stop extension. In 09 S0, `deploy-wait-ready.sh` keeps polling while
  `systemctl show -p ActiveState` is `activating`, and `DEPLOY_READY_TIMEOUT` bounds only the time after
  `READY=1`. Add the done-check "a 12-minute fake migration under the unit is not killed".

### OPS2-18 — MINOR — The `Boot` attempt budget has no margin under `StartLimitBurst`

- **Spec:** 05-updater §5U.4 (`max_boot_attempts` 3, hot-reloadable; rollback then "exits with the restart
  code").
- **Evidence:** With `RestartSec=5` and `StartLimitBurst=5`/60 s (`deploy/audiobook-organizer.service:81-82,111-112`),
  a fast-crashing new binary goes: exit 75 → starts 1–3 (fail) → start 4 (`Boot` rolls back, exits 75) →
  start 5 (old binary). That is five starts in about 30 s, which is exactly the limit. Raising
  `max_boot_attempts` to 4, or one crash of the old binary, leaves the unit `failed` with `.prev` restored
  but not running.
- **Fix:** In 05-updater, the `Boot` rollback `exec`s the restored binary in place instead of exiting
  (Unix), which removes one supervisor start. Document the constraint
  `max_boot_attempts ≤ StartLimitBurst − 2`, cap the key at 3 when `NOTIFY_SOCKET` is set, and add a test.

### OPS2-19 — MINOR — The removal gate measures only the reference production store

- **Spec:** 09 §9.2.1 (c), P8 (run against `<abo-url>`), §9.5.
- **Evidence:** A contract PR removes a surface for every install, but P8 reads one store. External
  operators who still use an alias (scripts, compose env) learn of it only from a 410 or a refusal.
- **Fix:** In 09 §9.2.1, state that (c) is evaluated on the reference store. On every store, the
  contract build's first boot reads the persisted usage record. If a removed or `Gone` surface was used within
  14 days, it reports Degraded `legacy_surface_removed_in_use` with the surface and name, and shows a Settings
  banner. Release notes list each removed surface.

### OPS2-20 — MINOR — The S6 rollback leaves undelivered webhooks in `c/outbox/`, contradicting "nothing correctness-critical is carried only by the outbox"

- **Spec:** 09 S6 "Rollback"; 05-notify §(Publish/Dispatch/Deliver), where delivery is by outbox consumer
  and "undelivered work is in the outbox by construction".
- **Evidence:** At S6, webhook delivery runs through the outbox. S5 ignores `c/outbox/`, so entries pending
  at rollback are not delivered until a later roll-forward, possibly days later and stale.
- **Fix:** Correct the S6 rollback text. Add `core.notify.max_delivery_age` (default 24 h; older entries
  are dead-lettered and counted), plus the R-test row "webhook pending at rollback is delivered once on
  roll-forward or dead-lettered after the max age".

### OPS2-21 — MINOR — `TestShippedDeployEnvNamesArePermanent` fails on HEAD's own unit, and the permanent lists differ

- **Spec:** 05-config §1.12.4 (every name found is "either a permanent alias or on the explicit not-read
  list"); 09 §9.2.5, §9.6 row 16.
- **Evidence:** The unit sets `HOME`, `GOMEMLIMIT` and `GOGC` (`deploy/audiobook-organizer.service:96,101,104`),
  which appear in neither list, so the test fails as written. 09 row 16 lists `HOME`, `NOTIFY_SOCKET` and
  `OTEL_METRIC_EXPORT_INTERVAL` (read at `config.go:2748`), and 05-config does not. The `BindEnv` count is 141
  in 09 and 131 in 05-config; the code has 142 `BindEnv(` calls under `internal/config` and `cmd`. It is
  also undefined whether commented lines (`local.conf.example:50-61`) count.
- **Fix:** In 05-config, add a third class, "runtime/ecosystem names not read by config" (`HOME`,
  `GOMEMLIMIT`, `GOGC`, `TZ`, `NOTIFY_SOCKET`). Make 05-config the single list and have 09 row 16 point to
  it. Generate the counts from the alias table. State that commented `Environment=` lines are scanned.

### OPS2-22 — MINOR — 05-tasks' batch table still says T0+T1 "raises the level"

- **Spec:** 05-tasks §5T.8 T0+T1 row, last column ("raises the level (5T.6)") vs §5T.6 "Compat level and
  contract" (the adopt step is `Additive` and raises no level; only C5 does) and 09 §9.2.2.
- **Evidence:** The cell is 09's stated input to §9.4.1. If implemented as written, S5 would refuse
  `make rollback` to S4.
- **Fix:** Change the cell to "records the adopt-step applied record; no level raise (C5 raises `core` to
  4)".

### OPS2-23 — MINOR — Shutdown-budget details

- **Spec:** 05-health §5H.4, §5H.7; 06 §6.8; 09 S2 "Lifecycle" and done-check.
- **Evidence:**
  - (a) `core.shutdown.budget` hot reload is "yes" in 05-health and "no" in 06.
  - (b) The unit sets no `KillMode`, so systemd's control-group mode sends SIGTERM to the operation-runner
    children at t = 0, whatever their process group (`internal/operations/registry/subprocess.go:225,328`).
    The S2 check "stamps it interrupted" may then see subprocess runs fail instead.
  - (c) Plain `docker run` has a 10 s grace period, below the 25 s budget, and an image cannot carry a stop
    timeout.
  - (d) A unit or plist change reaches a host only when the owner copies it
    (`deploy/audiobook-organizer.service:8-10`), and S2 does not list that as an owner action.
- **Fix:**
  - (a) One value (no, restart-bound) in both files.
  - (b) Ship `KillMode=mixed` in the S2 unit, and add a subprocess-mode op to the stop check.
  - (c) When `sysinfo.Container` is set and the key is unset, the default budget is 8 s; compose sets
    `ABO_CORE_SHUTDOWN_BUDGET=25s` next to `stop_grace_period: 30s`.
  - (d) S2 "Deploy" row: owner action "install the updated unit/plist, `daemon-reload`".

### OPS2-24 — MINOR — Boot-phase and level metrics have two names each, and the phase set cannot attribute the steps S5 asks for

- **Spec:** 09 §9.10 (`mediacore_compat_level`; `mediacore_kernel_boot_phase_seconds` gauge with `phase`);
  05-storage §5S.8 (`mediacore_storage_level`); 06 §6.9 (`mediacore_kernel_boot_phase_seconds`
  **histogram**, `phase` `P1`…`P8`); 09 S5 done-check ("`core.tasks.adopt-active` timed by
  `mediacore_kernel_boot_phase_seconds`").
- **Evidence:** The merged guard (SIMP-18) has two gauge names. The boot metric differs in type, and its
  label set omits P0a, P0 and the updater phase, and has no per-step label. Adoption, rebuild, reconcile
  and `adopt-active` all fall inside P4 or later, so S5's check cannot be measured.
- **Fix:** Use one name, `mediacore_storage_level`, everywhere, and delete `mediacore_compat_level` from
  09. The boot metric is a gauge per boot with labels `phase` ∈ {P−1, P0a, P0…P8} and `step` (the bounded
  set of migration, mirror and adoption step names). 06 and 09 must match.

### OPS2-25 — MINOR — The R-test's `prev` cannot be determined from CI, and the job is wired nowhere

- **Spec:** 09 §9.10 (R-test "build `prev` from the **last production-deployed tag**"); §9.0 (`make
  compat-rehearsal` is "the CI-checkable proof of G5"); S0 CI classification table.
- **Evidence:** Deploys use an uncommitted recipe that builds the local checkout of main
  (`Makefile.local.example:88-110`), so the host's `.prev` is usually a main build, not a release tag.
  `compat-rehearsal` appears in no CI job, gate list (`ci/gates.txt`) or table row.
- **Fix:** In 09 §9.10, `PREV_REF` defaults to the latest production release tag, and before each flip
  deploy the operator overrides it with the commit printed by P10 (`<bin>.prev --version-json`). In the S0
  table, add a row: `compat-rehearsal` runs on PRs labelled `compat-gate` (OPS2-1) and nightly, is
  registered in `ci/gates.txt`, and blocks auto-promotion while it is red.

### OPS2-26 — MINOR — A deploy while a restore is staged silently discards the restore

- **Spec:** 09 S1 and S8 ("`make rollback` and the updater refuse while a staged restore is pending");
  05-backup §5B.3.4 step a (a foreign `staged_by_build` is rejected).
- **Evidence:** The operator's `deploy` recipe (`Makefile.local.example:88-110`) has no such check. A
  deploy between staging and the next boot makes the new build reject the intent as foreign. The data is
  safe, but the restore the operator asked for silently does not happen; only a Degraded token shows it.
- **Fix:** In 09 S1, add the same `restore/pending.json` refusal to the `deploy` recipe in
  `Makefile.local.example`, with `DEPLOY_DISCARD_RESTORE=1` to override, and mention it in the S1 "Deploy"
  row.

## Notes for the lead

- The data-side design (levels, adopt records, rebuild from legacy, exact-or-disabled keys) is now sound.
  The remaining risk is **operational plumbing that the spec assumes but the repo does not provide**:
  merges that are not CI-gated and auto-promoted production minors (OPS2-1); an updater that cannot write
  its install directory (OPS2-4); the boot phase the updater needs (OPS2-3); and the startup
  timeout extension (OPS2-17).
- Shortest path to "every stage reversible": apply OPS2-1, OPS2-2, OPS2-3, OPS2-5 and OPS2-9 first. They
  are the places where a rollback that the design otherwise supports is lost, either by timing or by a delete
  inside the window.
- Not attacked, by design: D3, cross-cutting resolutions 2, 5, 6 and 8, and "no state-shape flags".
