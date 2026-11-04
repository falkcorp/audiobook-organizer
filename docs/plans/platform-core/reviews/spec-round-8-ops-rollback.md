<!-- file: docs/plans/platform-core/reviews/spec-round-8-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: fbc6c4c6-e9d6-4208-a13d-8e1ca639743e -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8: ops, rollback and incremental delivery (lens OPS8)

This review walks S0–S10 of [09](../../../specs/platform-core/09-abo-on-core.md) v1.7.0, with S2b and UI-0
included. For each stage it asks three questions:

- Is the stage shippable?
- Are its done-checks measurable?
- Are its rollback costs stated?

It also checks four mechanisms:

- the §9.3 staging procedure: firewall egress, running schedules, the fake release server, `staging-scrub` and
  `secret_fields.txt`;
- the restore and reset tokens in `make rollback`, `deploy` and the updater;
- the S8 metric-alias boot;
- the seeded metrics contract test.

Anchor formats:

- code: `path:line @748fd787a` (the code in this checkout is unchanged from that commit);
- spec: `file §section (line)`, with line numbers from the current checkout.

R3-1…R7-8 are binding. No finding below asks for removed machinery back.

Counts: **0 BLOCKER, 6 MAJOR, 8 MINOR** (14 findings).

## A. Regression check (round-7 OPS BLOCKER and MAJOR findings)

| ID | Status in round 8 | Open finding |
|---|---|---|
| OPS7-1 contract test cannot see aliases or `mediacore_*` families (MAJOR) | **Fixed.** 05-metrics §6 "What it scrapes" (lines 271-291): `coretest.MetricsProvider` (06 line 1271-1277), the served `Provider.Handler()`, `mediacore_` added at S2 r3, public seeding paths, the private registry at S8, `TestContractScrapesServedHandler`. 09 §9.2.7 (line 679-686) points there. One residue remains: the kernel's own families cannot be seeded by a Provider "built without booting" | OPS8-10 |
| OPS7-2 `--network none` staging host (MAJOR) | **Fixed.** 09 §9.3 step 2 (line 848-854) installs nftables egress drop with ssh and the API port open, before the first boot; `test_staging_egress.sh`; 05-backup (line 596-600) cites 09 only | — |
| OPS7-3 scrub disables what S5 and S8 measure (MAJOR) | **Half-fixed.** Schedules now run (§9.3 step 3(d)) and 05-backup no longer contradicts 09. Two problems remain. The S5 check "every one of the 38 schedule definitions fires" still cannot pass, because some definitions never fire on any install. The S8 fake release server cannot drive the shipped S8 binary | OPS8-4, OPS8-3 |
| OPS7-4 scrub has no executable or list; a directory copy refills keys (MAJOR) | **Half-fixed.** `staging-scrub`, `secret_fields.txt` committed at S0, archive-only provisioning and the `config.yaml` refusal all landed. The order "archive restore, then offline scrub before the first boot" cannot run from S1, and from S2b `app.OpenOffline` refuses it | OPS8-2 |
| OPS7-5 S8 alias set fails with `ErrAliasConflict` (MAJOR) | **Fixed for S8.** 09 S8 "Metrics" (line 1102), 05-metrics §13 item 3 (line 471), `TestS8_FullAliasSetBoots`. The same root cause is still live at **S5**, where the `operations_*` client_golang families keep their registration while the S5 PR adds aliases with the same names | OPS8-1 |

The round-7 MINORs landed:

- **OPS7-6:** "S3–S5 dark, S2b and S6–S8 live", with the four mechanical criteria. The criteria miss two of the PRs
  the text names (OPS8-9).
- **OPS7-7:** URLs are cleared at every stage, `u:` hashes and `apikey:` rows are neutralised, and the switch is
  keyed on the booting binary.
- **OPS7-8:** the ordered marker values are in 04 §2.1 (line 138-141).
- **OPS7-9:** `ci/prev_ref.txt` and the two-row matrix.
- **OPS7-10:** the empty-floor pass and row 33.
- **OPS7-11:** one `find` over `restore/` and `reset/`, with the tokens in 05-storage, 05-updater and 09. The
  override scope (OPS8-7) and the pre-S8 updater's probe (OPS8-8) need fixing.
- **OPS7-12:** staging-only effective-config comparison.

## B. Findings

### OPS8-1 — MAJOR — OPS7-5's root cause recurs at S5: the five `operations_*` client_golang families stay registered while the T0+T1 PR adds aliases with their names, so `AddAliases` fails closed and the S5 flip build does not boot

- **Spec:**
  - 05-metrics §13 item 3, rename table row (line 468):
    `audiobook_organizer_operations_{started,completed,failed,canceled}_total` and `_operation_duration_seconds`
    are "superseded by the tasks family | S5 | yes: one alias per family".
  - 05-metrics §13 item 2 (line 453): client_golang families "stay on the default registry … and move to
    `Provider.Registerer()` with their current names in the S8 PR …, except the two process gauges". The five
    `operations_*` families get no exception.
  - 05-metrics §8 (line 342): "Alias collides with a live family | fail-closed at `AddAliases`".
  - 09 S5 "Moves and batches" (line 1053) says only "the ops series are renamed … with their gather-time aliases".
- **Evidence:**
  - All five are client_golang vectors in `internal/metrics`: `operationStarted`, `operationCompleted`,
    `operationFailed`, `operationCanceled` and `operationDuration` (`internal/metrics/metrics.go:20-47`).
  - They are registered in the same `MustRegister` call as the two process gauges (`:300-310`), on the default
    registry. From S2 r3 to S8 the App's Provider gathers that same registry (05-metrics §6 table, line 256).
  - Their emitter is the v2 worker (`internal/operations/registry/worker.go:611,1204,1293-1297`), which stays at its
    path as opsbridge until T8 (09 S5 line 1053).
  - Nothing deletes them at S5. When the T0+T1 PR's alias rows reach `AddAliases`, the names are live, so boot
    refuses with `ErrAliasConflict`.
  - If books happens to call `metrics.Register()` (`internal/server/server.go:734`) after the aliases are installed,
    the check passes instead. Each scrape then carries two families with one name, which promhttp reports as a
    gather error.
  - `TestS8_FullAliasSetBoots` covers S8 only.
  - Separately, 05-metrics' alias for `_operation_duration_seconds` filters a counter (`mediacore_tasks_runs_total`).
    A histogram cannot be derived from a counter.
- **Fix:**
  - 05-metrics §13 item 2 adds a second exception. The **T0+T1 PR** deletes:
    - the five `operations_*` vectors;
    - `IncOperationStarted`, `IncOperationCompleted`, `IncOperationFailed`, `IncOperationCanceled` and
      `ObserveOperationDuration` (`metrics.go:351-358`);
    - their calls in `registry/worker.go`.

    The same PR replaces their seeding rows with alias rows. `_operation_duration_seconds` becomes an alias of
    `mediacore_tasks_run_duration_seconds`, not of the runs counter.
  - 09 S5 "Moves and batches" states this.
  - Generalise the S8 test into **`TestStageAliasSetBoots`**, run by the seeded contract test at every renaming
    stage (S2b, S5, S7 and S8). The test takes ABO's whole metric-alias table for that stage plus every legacy
    collector `metrics.Register()` still registers, and asserts that `AddAliases` returns nil.
  - Add `TestStageAliasSetBoots` to the S2b, S5, S7 and S8 done-checks.

### OPS8-2 — MAJOR — OPS7-4 half-fixed: "archive restore, then offline scrub before the first boot" cannot be executed from S1 on, so the first boot of a staging copy runs unscrubbed production data

- **Spec:**
  - 09 §9.3 step 1 (line 843): the copy comes from an archive restore only ("05-backup's staged restore; HEAD's
    extract before S1").
  - Step 3 (line 855-859): the copy is scrubbed "offline, before its first boot … from S2b it opens the store
    through `app.OpenOffline`".
  - §9.3 (line 837): "A staging copy is inert at every boot and every stage."
  - 06 `TestOfflineOpenIsNotForeign` (line 1918): "an open with a pending migration step or a pending restore or
    reset is refused".
  - 05-backup §5B.3.4 (line 614): the offline `<app> restore --archive` runs "the same steps", so it **stages** too.
- **Evidence:**
  - **From S1 the restore is staged** (09 S1 "ABO-local fixes", line 965). The archive's data reaches the DB path
    only at the next boot's P0a swap.
  - **Before S2b,** `staging-scrub` therefore scrubs whatever store sits at the live path. That is a fresh store or
    nothing, not the archive. The first boot then swaps in the unscrubbed archive and serves it. That boot runs
    with:
    - raw `sess:` tokens;
    - password hashes;
    - production integration URLs;
    - schedules firing.
  - **From S2b,** `app.OpenOffline` refuses while `restore/pending.json` exists, so the scrub cannot run at all.
  - **Pending steps block it too.** An archive from the production release, restored to test a newer `cur`, has
    pending steps for `cur`:
    - S2b's `Seed` migrations;
    - S5 T0+T1;
    - S7's `Local` migration.

    `OpenOffline` refuses those as well.
  - **The restore itself needs a booted install before S8.** HEAD has no restore subcommand (`cmd/` holds none),
    so the restore needs a booted install and admin credentials. A foreign restore through the API also requires
    interactive owner proof, which a staging host does not have (05-backup line 613-614).
  - Egress denial limits the damage, but the procedure's claim that the copy is inert, and the R6-7 invariant, are
    false at S1–S10.
- **Fix:** rewrite 09 §9.3 steps 1 and 3, and 05-backup's paragraph (line 591-600):
  1. **S0 ships an ABO subcommand `staging-restore --archive <file> --db <empty path>`.** It reads the archive
     offline (HEAD's `extractArchive` logic, and later the `legacy-abo/0` and 05-backup readers), refuses a
     non-empty target, never writes `config.yaml` and boots nothing. `provision.sh` uses it at every stage,
     **never the staged restore**.
  2. **`staging-scrub` runs with the binary of the release that wrote the archive** (the production release on the
     reference host; every release from S0 carries the subcommand). That binary has no pending steps on its own
     archive, so `OpenOffline` admits it.

     Alternatively, `app.OpenOffline` gains a `Scrub` mode that tolerates pending steps but still refuses a pending
     restore or reset. It writes only legacy-format rows, which every pending step tolerates under R3-1.
  3. **`test_staging_scrub.py` asserts that the first serve boot finds every secret, session and URL already
     scrubbed.** It plants a production-shaped archive, runs `staging-restore`, then `staging-scrub` with the
     archive's binary, then boots `cur`.

### OPS8-3 — MAJOR — OPS7-3 half-fixed: the S8 updater done-check cannot exercise the shipped S8 binary

- **Spec:**
  - 09 §9.3 step 5 (line 883-886) and 05-updater §5U.12 (line 730-738): a local fake signed-release server, "behind
    `Options.Source` (the GitHub source's base URL is a parameter)", serves a manifest signed "with a test key
    compiled into a staging build or with the real key over a locally re-served copy of the newest signed
    release".
  - 09 S8 done-check (line 1106): the install "applies the fake signed release and `Boot` confirms it".
- **Evidence:**
  - **The base URL is a constructor argument, not a setting.** `Options.Source` is an `app.Options` field set in
    the app's `main` (05-updater line 57; `github.New(owner, repo, opts…)`, line 331-333). No config key or env name
    overrides it at runtime, so a production S8 binary always dials the GitHub host. Egress is dropped, and the
    fake server cannot be reached without impersonating that host's DNS and TLS.
  - **Option (a) tests another binary.** A staging build with a compiled-in test key is not the artefact that
    ships.
  - **Option (b) is refused.** The S8 updater refuses a target that is not newer (05-updater line 364; 09 S0
    "Updater hotfix"). "The newest signed release" re-served to the S8 build under test is that build or an older
    one.
  - So neither path measures what the S8 risk row relies on: the shipped binary's trusted key, source wiring,
    apply and `Boot`.
- **Fix:**
  - Run the check between two **real signed prereleases**: install S8 RC *n* on staging with
    `core.updater.channel=prerelease`, and have it apply RC *n+1*. This needs `prerelease.yml` to publish the signed
    `release-manifest.json` too, stated in 05-updater's release-pipeline row.
  - Reach the release source in one of two ways:
    - (i) allow egress to the release host names only for this check: `provision.sh --updater-check` adds an
      nftables set resolved from them, and the scrubbed copy has nothing else to call;
    - (ii) add an operator-file-only key `core.updater.source.base_url` (`ProtectOwner`, never settable through the
      API, rejected unless the scheme is `https` or the host is loopback) that the fake server sits behind.
  - Delete the "test key compiled into a staging build" option.
  - 09 S8 and 05-updater name the chosen path.

### OPS8-4 — MAJOR — OPS7-3 residue: the S5 done-check "every one of the 38 schedule definitions fires on the staging store within one interval" cannot pass at HEAD parity

- **Spec:** 09 S5 "Done when" (line 1061). §9.3 step 3(d) (line 876-878) leaves schedules running "so S5's
  done-check can see all 38 fire".
- **Evidence:**
  - **Two definitions are manual-only by construction:** `IsEnabled: func() bool { return false }` and
    `GetInterval` 0, for transcode and series-normalize (`internal/scheduler/tasks.go:553-554,708-709`). They never
    fire on any install, under HEAD or core.
  - **Config-disabled definitions do not fire either.** The ticker runs only `IsEnabled()` definitions
    (`internal/scheduler/scheduler.go:335,361,397`). Several are switched by config, for example
    `Scheduled.LibraryScanFull.Enabled` (`tasks.go:465-466`) and `DedupRefresh.Enabled` (`:579`). A production copy
    with one of them off cannot fire it.
  - **Some guards depend on integrations.** `HasDedupPlugin` needs the embedding store (`scheduler.go:56-61`,
    `tasks.go:653`). The scrub clears exactly what those guards need.
  - **The window can be long.** "Within one interval" of a daily or weekly definition makes the stage's
    done-check a week-long wait on a disposable host.
- **Fix:** replace the clause, in 09 S5 and 05-tasks' G5 text, with two checks:
  - **Normative: `TestScheduleParity_AllDefinitions`.** It runs on a fake clock in root CI. For each of the 38
    definitions it asserts that `cur` (T0+T1, core scheduler plus books ticker) and HEAD produce the same
    sequence: enqueue or no enqueue, start-run, maintenance-window reachability and clock advance. Inputs are the
    definition's `IsEnabled`, interval and `RunOnStart`, for both enabled and disabled config. Manual-only
    definitions are asserted never to self-start.
  - **Smoke: the staging boot.** It logs the scheduler's table (name, enabled, interval, next due). Every enabled
    definition with an interval of 24 h or less fires within one interval, and the rest are listed with their next
    due time.

### OPS8-5 — MAJOR — A post-reset hook that cannot finish keeps `reset_inflight` forever, so `deploy`, `make rollback` and the updater refuse indefinitely, and the hook retries a library wipe on every boot

- **Spec:**
  - 05-storage §5S.3.15 "Side effects" (line 1742): the hook runs "on every boot whose reset journal reads `applied`
    without `hook_done` … A crash or a failure before `Done` reruns the hook at the next boot". It "wipes the
    entries of the recorded `RootDir` with HEAD's `IsDangerousRoot` refusal".
  - "Rollback tooling" (line 1743): `reset_inflight` holds "until `hook_done`".
  - 05-updater error row (line 608): "no updater override (`ROLLBACK_IGNORE_RESET=1` is the scripts' only)".
  - 09 §9.2.2 (line 348), §9.8 (line 1279).
- **Evidence:**
  - **HEAD treats wipe errors as best-effort.** Each `os.RemoveAll` failure is a `Warn` and the reset completes
    (`internal/server/handlers/system/handler.go:571-576`).
  - **HEAD's dangerous-root refusal is a 400 after the store was already wiped** (`:565-568`). Moved into a hook
    that reruns on any failure, that refusal is deterministic, and so is a read-only or partly root-owned library
    mount.
  - **The hook then never reaches `Done`.** `inflight.json` stays, and every later boot attempts the destructive
    wipe again.
  - **Every swap path is blocked.** `CheckReader` returns `reset_inflight` for good. Auto-updating installs have no
    override, so they are stuck on that release, and `deploy` and `make rollback` need an override that the text
    says is unsafe ("open a half-moved store").
  - The stage is not shippable for any install whose reset hits this, and nothing reports why.
- **Fix:**
  - **05-storage §5S.3.15:**
    - (a) The `IsDangerousRoot` check moves to **staging**: `StageFactoryReset` refuses with
      `ErrDangerousRoot` before writing `pending.json`, through `legacy.ConfigResetArgs`, which already returns the
      recorded `RootDir`.
    - (b) Inside the hook, per-entry removal errors are HEAD's warnings: logged, counted in the journal record
      (`hook_done{failed: n, paths: [...]}`) and reported as health Degraded `factory_reset_incomplete` naming the
      count. The hook always reaches `Done` once it has attempted every entry.
    - (c) Only a crash before `Done` reruns it.
  - **Tests:** `TestPostResetHook_RemoveErrorsStillDone` and `TestStageFactoryReset_DangerousRootRefused`, added to
    the S2b done-check.
  - **Release note (§9.2.1 row 12):** files the hook could not delete are listed in the log and in health.

### OPS8-6 — MAJOR — The factory-reset rollback cost is dated S3 although it ships at S2b, and the stage tables omit rows 12, 19 and 20, so the S2b release note and the S2b R-test miss it

- **Spec:**
  - 09 §9.2.1 row 12 (line 160), column "From": **S3**. Its text says "A core build's factory reset (S2b on) only
    stages", and its release note says "A factory reset on this release takes effect at the next restart; undoing
    it restores the database but not the library files".
  - §9.2.1 (line 145): "Each row's text goes into the release note of the release that introduces it". The R-test
    "asserts exactly these rows" (§9.10, line 1330).
  - 09 S2b: Data/keys (line 996) ships the staged reset, the `RootDir` wipe after restart and the `reset_pending`
    refusal. Yet the S2b Rollback row (line 997) and the §9.3 overview row (line 800) list only rows 14 and 16.
  - The S3 overview row (line 802) lists rows 1, 12, 15 and 17, without row 19 (From "S3 (Basic gate)"). The S4
    overview row (line 803) lists rows 2–5, 13 and 18, without row 19 (auth, from S4 r1) or row 20 (From S4).
- **Evidence:**
  - The S2b release ships the behaviour change with no release-note line. That release is a production minor that
    can auto-promote (§9.1).
  - Users of S2b learn neither that a factory reset now waits for a restart nor that "undo" does not bring the
    library back, and no S2b note warns that `make rollback` refuses while a reset is pending.
  - The S2b rehearsal, keyed by the rows of its stage, does not exercise the reset.
- **Fix:**
  - Row 12's "From" becomes **"S2b (factory reset), S3 (settings reset)"**. Split the release-note text so the
    factory-reset sentence ships with S2b.
  - The S2b Rollback and overview rows add row 12. S3 adds row 19. S4 adds rows 19 and 20.
  - Add a generated check, `scripts/ci/check_rollback_rows.py` in `extraction-gates`: every §9.2.1 row's "From"
    stage lists that row in its stage's overview and Rollback rows, and no stage lists a row whose "From" is later.

### OPS8-7 — MINOR — `ROLLBACK_IGNORE_RESET=1` also overrides `reset_inflight`, contradicting "a rollback never lands an older build on a half-moved directory"

- **Spec:**
  - 05-storage §5S.3.15 "Rollback tooling" (line 1743): "`ROLLBACK_IGNORE_RESET=1` overrides … A rollback therefore
    never lands an older build on a half-moved directory".
  - §5S.3.7 (line 1120ff.): "overrides only the reset part".
  - 09 §9.2.2 (line 350), §9.8 (line 1279).
- **Evidence:**
  - At `set_aside` the DB path is empty. A pre-S2b build creates a store there (`cmd/root.go:640-645`).
  - The next core boot resumes the reset journal (P0a "first resumes any … reset journal in flight", 05-storage line
    1739) over that store, then runs the hook. The result is a library wipe after the operator rolled back to get
    away from it.
  - Overriding `reset_pending` is safe: the intent is rejected later because the open stamp moved, 05-storage line
    1738. Overriding `reset_inflight` is not.
- **Fix:** `ROLLBACK_IGNORE_RESET=1` overrides `reset_pending` only. `reset_inflight` has no override: "boot the
  current build once to finish the reset". Apply the same split to `ROLLBACK_IGNORE_RESTORE=1`: pending only, never
  `restore_inflight`. Add both cases to `scripts/test_storage_format_guard.py`.

### OPS8-8 — MINOR — The S0–S7 updater "uses the same probe" as the scripts, but `sudo` cannot run under the unit's `NoNewPrivileges=yes`, and the S2b rewiring of `internal/updater` onto core's `CheckReader` is unstated

- **Spec:** 09 §9.2.2 (line 350-351): "The `deploy` recipe and the S0 updater use the same probe", the `sudo -n
  find` line. 09 S2b (line 996): "`make rollback`, `deploy` and the updater refuse".
- **Evidence:**
  - The committed unit sets `NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:122`), so `sudo` inside the
    service fails. Its stderr starts `sudo:`, which reads as `cannot_determine`, which refuses every manual apply.
    From S0 to S7 that is the only apply path, because the unattended paths are check-only.
  - From S2b the guard is core `storage`'s (09 §9.2.2, line 234). Nothing says the ABO `internal/updater`, which
    lives until S8, starts calling it and so learns `reset_pending` and `reset_inflight`.
- **Fix:**
  - §9.2.2: "the S0–S7 updater runs the same **decision** in process. As the service user it stats `restore/` and
    `reset/` directly under the state directory, never through `sudo`. From S2b it calls core `storage`'s
    `CheckReader` through the kernel binding".
  - Add `TestHotfixUpdater_RefusesWhileResetPending` to the S2b done-check.

### OPS8-9 — MINOR — The red-rehearsal rule's mechanical criteria miss two state-writing PRs that §9.2.1 names

- **Spec:** §9.2.1 (line 185-193) names the factory-reset change (S2b) and `updater.Boot` (S8) as state-writing PRs
  under the red-rehearsal rule. The rule's four criteria are: claim-list change, `Migration`/`DataMigration`, a new
  `c/` or `m/` family, and `docs/compat-surfaces.md`.
- **Evidence:** both PRs write state-directory files, not key families:
  - the reset PR writes `reset/pending.json`, `reset/<id>/journal.json` and `.mediacore-reset/`;
  - `updater.Boot` writes the pending marker.

  Neither registers a migration or changes the claim list or the ledger, so neither trips the rule that the text
  says applies to it.
- **Fix:** add a fifth criterion, "changes a file listed in `ci/state_writers.txt`", and commit that list at S0:
  - the restore and reset swap packages (`core/internal/restoreswap`, `storage/restorejournal`);
  - the updater marker code;
  - `internal/storagehttp`;
  - ABO's `internal/backup` staging.

  Extend the list in the PR that creates each package. Add the case to `scripts/tests/test_run_gates.py`.

### OPS8-10 — MINOR — OPS7-1 residue: the contract test's Provider is built "without booting", so the kernel-emitted `mediacore_*` families of the S2 r3 and S2b diffs are never seeded

- **Spec:**
  - 06 `coretest.MetricsProvider` (line 1271): "builds, without booting, the same metrics.Provider".
  - 05-metrics §6 (line 285-287): seeding paths are given for Pebble, tasks and AI only.
  - The golden diff at S2 r3 is "only the kernel's own `mediacore_*` families, now gathered" (line 298).
- **Evidence:** the kernel's own families are written by kernel code during a boot, not by the Provider:
  - `mediacore_kernel_boot_phase_seconds` (06 §6.9);
  - `mediacore_compat_*` (S2b);
  - `mediacore_storage_level`;
  - `mediacore_storage_checkpoint_bytes`.

  05-metrics §6 itself says a family with no recorded sample exports nothing, so the S2 r3 done-check cannot show
  the families it promises.
- **Fix:** add a seeding row to 05-metrics §6: "kernel families: one `coretest` zero-module boot (G7's harness)
  with the stage's `app.Options`, whose Provider is the one scraped". Alternatively, drop the kernel families from
  ABO's golden and assert them in core's own seeded test (`core/metrics/contracttest`).

### OPS8-11 — MINOR — The S0 done-check requires `test_staging_scrub.py` cases that need S2b, S6 and S7 builds

- **Spec:**
  - 09 S0 "Done when" (line 925): "`scripts/tests/test_staging_scrub.py` and `test_staging_egress.sh` pass".
  - §9.3 (line 888-892): that test "boots the scrubbed fixture with the `prev` of S7's rollout (S6)", clears
    `c/integrations/instances/` URLs and, from S2b, opens through `app.OpenOffline`.
- **Evidence:** none of these exist at S0.
- **Fix:** date the cases in §9.3:
  - S0: blob fields, sessions, hashes, keys, legacy URLs, the `config.yaml` refusal and egress;
  - S2b: the `OpenOffline` path;
  - S7: native instance URLs, `core.integrations.disabled` and the S6-`prev` boot.

  Each stage's done-check names the cases it adds.

### OPS8-12 — MINOR — The "graceful restart" that a staged restore or a staged factory reset requests has no exit code, and the committed unit restarts only on failure

- **Spec:**
  - 05-backup step 9 (line 663): "request a graceful restart through the lifecycle supervisor".
  - 05-storage §5S.3.15 "Stage" (line 1737): "request a graceful restart".
  - 09 §9.1 row "Updater" (line 75) already records that `os.Exit(0)` under `Restart=on-failure` is not restarted.
- **Evidence:**
  - `deploy/audiobook-organizer.service:111` sets `Restart=on-failure`.
  - The only defined restart exit code is the updater's 75 (05-updater line 443). HEAD has no self-restart after a
    restore.
  - If the supervisor exits 0, a factory reset or restore never takes effect on systemd until someone starts the
    unit, and `reset_pending` blocks `deploy` meanwhile.
- **Fix:**
  - 06 §6.5.2 defines one `Lifecycle.RequestRestart(reason)`. It drains, then exits with
    `core.updater.restart_exit_code`, which is renamed or aliased `core.lifecycle.restart_exit_code` (default 75).
    The restore and reset steps and the updater all call it.
  - Add `TestRestartRequest_ExitsRestartCode`, plus an S1 and S2b done-check: under the committed unit, staging a
    restore or a reset brings the service back without operator action.

### OPS8-13 — MINOR — The `CheckedWrites` enforcement releases are unplaced

- **Spec:**
  - S3 "Release" (line 1018): "One release", yet its flags are "enforced from a later release after ≥ 14 days".
  - The same deferred enforcement applies at S4 r2 (`apikey:`, line 1036), S5 (run guard rows, line 1061) and S7
    (`import_path:`, `aijob*`, lines 1084 and 1089).
  - §9.4.1 has no row for any enforcing release.
- **Evidence:**
  - The enforcing change flips a claim mode, so it is a state-writing PR under the rehearsal rule, and a rollback
    of it is a binary swap. It has no stage, no "Flip PR" row and no release-note text. "Done when" depends on it,
    so no stage table says when S3, S4, S5 or S7 is finished.
  - It can also land inside the next stage's dark window, which breaks "S3–S5 land dark behind their flip".
- **Fix:**
  - Name each enforcing release in its stage's Releases row: S3 r2, S4 r3, S5 "enforcement release" and S7 r2. None
    is a marker value. Give each a §9.4.1 row ("refused blind writes; rollback: binary swap or the claims dial").
  - State that the stage is done when its enforcing release has served 14 days with zero refusals.

### OPS8-14 — MINOR — The R-test does not assert §9.2.1 row 20, and the row's scope disagrees with its "From"

- **Spec:**
  - Row 20 (line 168): "Restoring an archive taken during S2b–S7 needs every API key re-confirmed", with "From" S4.
  - The R-test (§9.10, line 1330) asserts "exactly the rows of §9.2.1's table, rows 16–18 included". Cell (e) lists
    restores across builds, but asserts no `needs_confirmation` outcome.
- **Evidence:**
  - Row 20 is the only §9.2.1 cost with no R-test assertion.
  - "S2b–S7" does not match "From S4". Before S4 r2 there is no revocation journal and no `needs_confirmation`
    stamp. Archives taken before S2b, HEAD's included, are equally foreign on S4+.
- **Fix:**
  - Reword row 20 as "Restoring an archive made by ABO's backup (any release before S8) on S4 r2–S7".
  - Add the R-test row "an archive written on `prev` (pre-S8), restored on `cur` (S4 r2+): every API key reads
    `needs_confirmation`; legacy sessions deleted".
  - List row 20 in cell (e).
