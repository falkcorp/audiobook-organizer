<!-- file: docs/plans/platform-core/reviews/spec-round-6-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1fe6e73-6705-4c21-8444-2c91b43574bf -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6: ops, rollback and incremental delivery (lens OPS6)

Lens: walk S0–S10 of [09](../../../specs/platform-core/09-abo-on-core.md) v1.5.1 (S2b and UI-0 included). For each
stage the review asks:

- Can the stage ship?
- Can its done-checks be measured? This covers the compose fixture at S3 and the metric series checks per stage.
- Does a binary-swap rollback cost exactly what the §9.2.1 table says?
- Do the updater exit codes, the floor release and the stepping-stone release behave as written?
- Are the Docker image jobs and the shipped deploy files correct?

Code anchors are `path:line @748fd787a`. Spec anchors are `file §section` (line numbers are in the current
checkout).

R3-1, R3-2, R3-4, R4-1, R4-2 and R5-1…R5-7 are binding. Nothing below asks for removed machinery back.

Counts: **0 BLOCKER, 4 MAJOR, 7 MINOR** (11 findings).

## A. Regression check (round-5 OPS BLOCKER and MAJOR findings)

| ID | Status in round 6 | Open finding |
|---|---|---|
| OPS5-1 compose `BASIC_AUTH_ENABLED=false` overrides a UI-enabled gate (BLOCKER) | Fixed in substance. R5-1(a): `Alias.BelowStored` (05-config C3, §1.3.5, §1.12.4), the compose fixture of the S3 golden, `TestComposeEnvKeepsUIBasicGate`, compose pass-through, empty-is-unset. Residue: 09 §9.2.5 dates the rule "from the S3 façade", but 05-config applies it at S2 already. 09 also puts legacy flags under the rule, which 05-config denies | OPS6-7 |
| OPS5-2 Basic-gated install locked out after rollback (BLOCKER) | Fixed. R5-1(b): the four HEAD-encrypted rows are never moved (09 §9.2.1 row 1, §9.2.3, S3; 05-config secrets read order (2)/(6); 05-http-api §10). An env-only password still resolves through `EphemeralDefault`, read-order step (6) | — |
| OPS5-3 `docker-image` trigger narrowed | Fixed (09 §9.2.1 table; 14 K20, Q-I) | — |
| OPS5-4 docs classifier skips Go-test inputs | Fixed (09 §9.2.1, S0 "Gates", `test_ci_classifier.py`) | — |
| OPS5-5 `/metrics` scope S4–S7 contradiction | Fixed and consistent (09 §9.2.6, 05-metrics §6, 05-rbac §4.8, 05-http-api `TestLegacyMetricsRoute_RequiresScopeFromS4`) | — |
| OPS5-6 metric renames scheduled at S8 | Fixed in timing (Pebble at S2b, ops at S5, AI at S7, gauges at S8). Two defects came with the fix: the S8 alias target is refused by core's own alias rule, and the per-stage live-scrape checks cannot be measured as specified | OPS6-2, OPS6-3 |
| OPS5-7 image second run from S2/S3 and volume ownership | Fixed in timing. The preparation command as written does not run against the ABO image | OPS6-10 |

The round-5 MINORs OPS5-8…OPS5-16 landed. Three of them came back defective: OPS5-11 (staging, OPS6-4), OPS5-15
(exit 78, OPS6-5) and OPS5-8 (flip PR, OPS6-8).

## B. Findings

### OPS6-1 — MAJOR — The floor release ships at S5, but only the S8 updater checks the stepping stone; the S0–S7 updater and the `deploy` recipe swap it in unchecked, although three files say "an S0+ updater refuses"

- **Spec:**
  - 09 §9.2.2 rules: "an updater **from S0 on** compares the store's `<db>.storage-applied` with the signed
    requirement … and refuses with `stepping_stone_required`".
  - 05-tasks (lines 1529-1530): "an S0+ updater refuses to apply the floor release to it".
  - 05-updater §5U.5 "Stepping stones": "This protection exists only where the running updater is this one or
    the 09 S0 hotfix".
  - 09 §9.8 row "Updater: store does not meet the signed `min_upgrade_from`".
  - 09 §9.11 row S5 (detection `stepping_stone_required`).
- **Evidence:**
  - **The S0 hotfix does no stepping-stone check.** 09 S0 "Updater hotfix" (line 761) lists exit 75, `.prev`,
    semver with local-build detection, a reader check of `<exe>.new --print-storage-levels`, and both unattended
    paths check-only. It does not mention a stepping-stone check. 05-updater §5U.12 lists the same items, and the
    S0 done-check's updater test (line 764) asserts no `stepping_stone_required` case.
  - **The signed manifest comes too late.** The "signed requirement" is `min_upgrade_from` in a signed
    `release-manifest.json` (05-updater §5U.5). 05-updater §5U.12 adds that manifest to the pipeline only "before
    the S8 release", and 09 S8 "Preconditions" makes the first signed release an S8 precondition. The floor
    release is 05-tasks T8, which ships in a later release of **S5** (09 S5 "Moves and batches", §9.4.1). So when
    it ships there is no signed manifest to compare against, and no S0–S7 updater code that would compare.
  - **A manual apply below the floor leaves the service down.** A manual apply on S0–S7 of the floor release to a
    store below `LegacyCap` swaps the binary. The floor release then exits 78. From S2 on,
    `RestartPreventExitStatus=78` (09 S2 "Lifecycle") keeps the unit stopped. No `updater.Boot` exists before S8,
    so nothing rolls back on its own; the service stays down until someone copies `.prev` back by hand.
  - **The `deploy` recipe has no forward guard either.** It copies the new binary in with no check
    (`Makefile.local.example:96-99`). Only `make rollback` runs a guard. P13 is a manual command.
  - Exposure is narrow while `LegacyCap` equals the evidence commit's 66: every S0+ store already ran 66. But
    §9.2.2 explicitly allows HEAD-style steps 67, 68, … until S2b, and then a store last booted on an early S0/S1
    build is below the floor.
- **Fix:**
  1. Extend the S0 updater hotfix (09 S0 row, 05-updater §5U.12) so that, before the swap, it runs
     `<exe>.new --print-upgrade-floor`, which ships at S0 (§9.2.2 "Rollback tooling"), and compares the result
     with `<db>.storage-applied`. It refuses with `stepping_stone_required`, naming the release the new binary
     prints. This uses the floor the binary carries, so it needs no signed manifest. State that the S8 updater
     additionally checks the signed copy.
  2. Add a case to the S0 done-check's updater test: "a fake new binary whose `--print-upgrade-floor` the store's
     sidecar does not meet → refused, nothing swapped".
  3. Make the `deploy` recipe template (and `scripts/deploy-preflight.sh`) run the same pair of commands against
     the uploaded binary before `cp … .prev`. Use one helper shared with `storage_format_guard.py`.
  4. Reword 09 §9.2.2, 05-tasks and 05-updater §5U.5 to "the S0 hotfix checks the binary's own floor; the S8
     updater also checks the signed manifest".
  5. Alternatively, keep T8 out of every release before S8 and say so in §9.4.1.

### OPS6-2 — MAJOR — The S8 process-gauge aliases target `go_goroutines`/`go_memstats_alloc_bytes`, which `metrics.AddAliases` refuses with `ErrAliasNotCore`, so S8 boot fails as written; the S8 set equation also lists those two names as additions although they exist at HEAD

- **Spec:**
  - 09 §9.2.7 and S8 "Metrics" (line 930): the two process gauges move to the Go collector with gather-time
    aliases.
  - 05-metrics §13 item 3 table, row `audiobook_organizer_process_memory_alloc_bytes`, `_process_goroutines` →
    `go_memstats_alloc_bytes`, `go_goroutines`, "Alias? yes".
  - 05-metrics §3 (`ErrAliasNotCore`, "alias source is not a core-subsystem family") and §8 row "Alias whose
    `New` is not a `mediacore_*` family | fail-closed (`ErrAliasNotCore`)".
  - 05-metrics §7 step 3: the kernel installs the aliases before listeners open, so the error fails boot.
- **Evidence:**
  - The alias's `New` is a Go-collector name, not `mediacore_*`. As specified, `AddAliases` rejects it at boot.
    The S8 PR cannot pass its own done-check ("the two retired process gauges answer only through their
    aliases"). The required checks stop the PR before release, so no production exposure follows, but the spec
    must change.
  - Separately, the S8 done-check's set equation (09 line 934; 05-metrics §6) is "set(S7) ∪
    {`mediacore_build_info`, `go_memstats_alloc_bytes`, `go_goroutines`, …}". At HEAD–S7 `/metrics` is
    `promhttp.Handler()` over the default gatherer (`internal/server/server_lifecycle.go:1746-1747`), whose
    default registry already carries the Go collector. Those two names are therefore already in set(S7), and
    listing them as additions hides nothing but misleads the reader.
- **Fix:**
  - 05-metrics §3/§8: allow an alias `New` of `mediacore_*` **or** a name in an explicit core-owned list of
    runtime-collector families (`go_goroutines`, `go_memstats_alloc_bytes`). Add the test
    `TestAlias_GoCollectorTargetAccepted`.
  - Alternatively, ship ABO's two legacy names as a books-side collector until C8. ABO's own scope would then
    emit them, with no alias.
  - In both files, drop the two `go_*` names from the S8 "additions" set and state that they are present since
    HEAD.

### OPS6-3 — MAJOR — Every per-stage series-name done-check is a live scrape, which lists only families that have recorded a sample since boot. HEAD already has a seeded series contract test for exactly this reason. As written the checks are not reproducible, and S7's cannot pass on staging

- **Spec:**
  - 09 §9.2.7 (one credentialed scrape command, "an empty result fails").
  - The done-checks:
    - S1: "the series-name set … equals the S0 build's (diff empty)";
    - S2 r1/r2/r3;
    - S2b, S5 and S7: "⊇ the previous stage's and every renamed family answers under both names";
    - S8: set equation.
  - 05-metrics §6 "Series-name checks".
- **Evidence:**
  - A client_golang `Vec` with no observed label set, an OTel instrument with no data point and the Pebble
    collector without a source export nothing. HEAD's own contract test says so and fixes it with a seeding table:
    `internal/telemetry/contract/series_contract_test.go:15-18,162,431`.
  - Two separate boots of two builds therefore produce different name sets, depending on which code paths ran.
    S1's "diff empty" is flaky in both directions.
  - Families that depend on configured integrations never appear on staging. The staging procedure drops the
    encrypted provider keys (09 §9.3 "Staging"), so AI dispatch never runs, `mediacore_ai_*` and its `ai_*`
    aliases never get a sample, and S7's "every renamed AI family answers under both names" cannot be met there.
    The same holds for filtered aliases such as `audiobook_organizer_operations_completed_total` (a `Filter` on
    `outcome=completed`) before any run completes, and for the S5 ops families `_ops_zombies` and
    `_schedule_missed_total`.
  - 09 S1 keeps the contract test in ABO as `internal/telemetrypolicy` but no done-check uses it.
- **Fix:**
  - Make the normative series check the **seeded contract test**: `internal/telemetrypolicy`'s golden, with each
    renamed family's alias rows added to the seeding table in the renaming PR. Its golden diff is the done-check:
    - at S1, empty;
    - at S2b, S5 and S7, only the renamed families and their aliases;
    - at S8, as OPS6-2 corrects it.
  - Keep the live credentialed scrape as a smoke check only: status 200, non-empty, and a superset of a short fixed
    list of always-on families (`process_resident_memory_bytes`, `go_goroutines`, `mediacore_build_info` from
    S8).
  - Say this once, in 05-metrics §6, and point every stage of 09 at it.

### OPS6-4 — MAJOR — The staging procedure claims no production credential reaches staging, but key regeneration drops only the four encrypted rows. The restored copy keeps every plaintext secret, every raw session token, and live integrations that act on production systems

- **Spec:** 09 §9.3 "Staging" (lines 727-735). The script boots the copy once with
  `ABK_ALLOW_ENCRYPTION_KEY_REGEN=1`, "which drops the production secrets on staging"; "never … copied to staging".
- **Evidence:**
  - `guardAgainstKeyRegeneration` only lets boot proceed (`cmd/root.go:760-791`). The rows that then fail to
    decrypt are the four encrypted ones. The load first tries to recover them from the gap-fill config file and
    re-encrypts them if found (`internal/config/persistence.go:1229-1262`). Only rows that cannot be recovered are
    deleted.
  - Everything 09 §9.1 calls "every other secret" sits in `setting:config_blob` **in plaintext**
    (`persistence.go:884,1996-2008`) and survives untouched: webhook secrets, Deluge and SMTP passwords, and the
    other provider keys.
  - `sess:` rows hold raw bearer session tokens. 09 §9.1 notes that the session list returns them as `id`. These
    tokens are valid on production for as long as the sessions live.
  - The booted copy also runs its schedulers. With production Deluge credentials, SMTP and webhook URLs it can:
    - move or remove torrents in the production Deluge;
    - send mail;
    - deliver webhooks;
    - spend provider budgets.
  - S0, S2b, S5 and S8 done-checks, the boot budget and `make rollback` all run on this host.
- **Fix:** replace the sentence with an explicit scrub, run **offline on the restored copy before its first boot**
  by `scripts/staging/provision.sh`:
  1. Blank every plaintext secret field of the blob. The list is generated from 05-config's secret-class keys, so
     it cannot drift.
  2. Delete `sess:`, `idx:sess:user:`, `invite:` and the encrypted secret rows.
  3. Clear integration URLs (Deluge, SMTP, webhooks, AI endpoints) and set `auto_update.enabled=false`.
  4. Boot with egress denied (container network `none`, plus the API-only port forwarded), then seed test
     secrets through the API as now.

  Add `scripts/tests/test_staging_scrub.py`, which asserts on a fixture store that no secret-class field, session
  or integration URL survives. Also state that the boot-budget copy is the scrubbed one.

### OPS6-5 — MINOR — Exit 78: the plist change does not stop launchd restarting it, `deploy-wait-ready.sh` cannot see an exit status, and 09 omits 05-updater's exit-75-under-marker exception

- **Spec:** 09 §9.2.2 (lines 278-285), S2 "Lifecycle" (line 816), §9.8 row "Store below the release's
  `min_upgrade_from`"; 05-updater §5U.4 (lines 454-460), `TestBoot_FloorRefusalUnderPendingRestarts`.
- **Evidence:**
  - **launchd.** `KeepAlive` → `SuccessfulExit=false` tells launchd to relaunch whenever the job exits
    *unsuccessfully*. Exit 78 is unsuccessful, so it is still relaunched forever, exactly as with today's
    `KeepAlive` true (`deploy/com.audiobook-organizer.plist:51-52`). The plist has no per-exit-code setting.
  - **Wait script.** `deploy-wait-ready.sh` polls only `ActiveState,SubState,NRestarts` (09 S0 "Deploy"; 05-health
    line 279). With `RestartPreventExitStatus=78` the unit simply turns `failed`, so the script cannot tell 78
    from any other failure.
  - **Missing exception.** 09 says the refusal "exits with a distinct code (78)". 05-updater makes an
    updater-applied release exit 75 instead while the marker names it.
- **Fix:**
  - 09 §9.2.2 and S2: drop the plist claim. State plainly that launchd relaunches a refusal (throttled), and that
    the release note tells macOS users to `launchctl bootout` the job. Optionally the binary sleeps 60 s before
    exiting 78 when it detects launchd (`XPC_SERVICE_NAME` set), to bound log spam.
  - The wait script adds `ExecMainStatus` to its `systemctl show -p` list (09 S0, 05-health) and reports 78 by
    name. `test_deploy_wait_ready.py` covers it.
  - 09 §9.2.2 and §9.8 add "(75 while an S8+ updater's pending marker names the running binary, so `Boot` can
    roll back; 05-updater §5U.4)".

### OPS6-6 — MINOR — The Basic gate's health token and test have two names each

- **Spec and evidence:**
  - **Health token.** 09 S3 "Basic gate" (line 853) and §9.8 (line 1087) say `basic_auth_no_password`. 05-authn
    (line 1574), 05-health §5H.3.1 and 05-http-api stage 8 (line 629) say `basic_gate_no_password`.
  - **Test name.** 09 (lines 853, 859, 1143) says `TestCoreBasicGate_UnsetPasswordRefuses`. 05-http-api (lines
    994, 1198) and 05-authn (line 1581) say `TestBasicGate_EmptyPasswordRefuses`.
  - Health tokens are what operators alert on and what the Settings banner keys on.
- **Fix:** use `basic_gate_no_password` and `TestBasicGate_EmptyPasswordRefuses` in 09 (S3 rows, §9.8, §9.10).

### OPS6-7 — MINOR — OPS5-1 residue: 09 dates `BelowStored` "from the S3 façade" and extends it to flags. 05-config applies it from S2 and keeps C3 for flags, and says that change is "recorded in 09 §9.2.6", where it is not

- **Spec:**
  - 09 §9.2.5 (lines 451-457): "every legacy env name **and flag** … from the S3 façade on it feeds the default
    layer".
  - 05-config §1.3.5 (lines 594-603): "ABO's legacy flags keep C3 … from the S3 façade a typed flag wins; this is
    recorded in 09 §9.2.6".
  - 05-config §1.12.2 (line 1241): "a `BelowStored` legacy name sits below it", applied when the store attaches,
    which is P3 at S2 and P4d from S2b.
- **Evidence:**
  - From S2 the core loader already builds the perimeter keys from env plus the blob (09 S2 "Config"). These are
    the rate limiter, CORS origins, peer allow-list, log level, and `ALLOWED_PEER_CIDRS`/`LOGIN_SATURATED_*` from
    `deploy/local.conf.example`. 09's date leaves S2–S2b undefined.
  - 09 §9.2.6 has no entry for `--log-level`/`--playlists` ("recorded changes" list, lines 512-568).
- **Fix:**
  - 09 §9.2.5: "every legacy env name and legacy file key (not flags) … feeds the default layer **wherever core
    config attaches books' legacy config store (S2 on)**".
  - Add to 09 §9.2.6 a recorded change: "`--log-level` and `--playlists` beat a stored value from S3; at HEAD the
    stored value won (`cmd/root.go:648-651`)". Include its release-note text.

### OPS6-8 — MINOR — "One flip PR per stage" is defined for only five stages; S2 ships three behaviour-changing releases; S2b's rehearsal gate lands on the release that is least risky for rollback

- **Spec:** 09 §9.2.1 "One flip PR per stage" (lines 172-175: "the PR whose diff changes `shipped_stage:`"), the
  `extraction-gates` rule, and 04 §2.1 ("which each stage's flip PR sets").
- **Evidence:**
  - Only five stages name their flip PR: S2b (strict, line 830), S3 (line 851), S4 r2 (line 866), S5 T0+T1, and
    S9 by implication.
  - S0, S1, S2, UI-0, S6, S7, S8 and S10 never say which PR changes `shipped_stage:`, so the red-rehearsal rule's
    trigger is undefined for them.
  - S2 has three releases, each listed "yes" for behaviour change (§9.4.1 rows S2 r1/r2/r3), against "exactly one
    flip PR … never runs half a stage".
  - At S2b the state core writes first (`c/storage/`, `c/compat/`, `c/storage/boot` and its state-dir mirror)
    starts at **release 1** (shadow). The flip (strict) is the one change a dial can undo without a rollback (S2b
    "Rollback"). So the rehearsal gate sits on the wrong release.
- **Fix:** add a "Flip PR" row to every stage table:
  - S2: one flip per release (r1, r2, r3), with §9.2.1 reworded to "one flip PR per stage release".
  - S2b: release 1's first core-storage PR changes the marker. Strict is a dial step under the persisted-violation
    check.
  - S6–S8: the PR that deletes the legacy implementation (hub A/v3 broker; `internal/plugin`; gin `/metrics` and
    `internal/backup` staging).
  - S0, S1 and UI-0: no flip (the marker is unchanged).
  - `check_stage_visibility.py` fails a PR that changes `shipped_stage:` to a stage whose table names no flip.

### OPS6-9 — MINOR — The §9.2.1 "one complete list" still misses three rollback consequences that the revision introduced or kept

- **Spec:** 09 §9.2.1 table ("the R-test asserts exactly these rows"); §9.2.7 per-stage renames; 09 S3 "Rollback"
  ("keys with no legacy name do not exist there"); §9.2.6 `/metrics` scoped keys.
- **Evidence:** none of the following has a row.
  - **Metric renames.** A rollback below S2b, S5, S7 or S8 brings back only the old names of the families that
    stage renamed. The `deploy/` dashboards and rules switched in that same PR (§9.2.7) go blank during the
    rollback, and so does any operator dashboard moved to the new names.
  - **Keys with no legacy name.** Such keys, saved on S3+ (for example `core.auth.disabled_allow_remote`,
    `core.integrations.egress.lan_allow`, `core.updater.*`, `core.tasks.kinds`), are ignored by an older build,
    which runs its own defaults.
  - **The scrape key.** From S4 r2 a scoped legacy scrape key must be replaced (§9.2.6). The replacement is an
    `mck_` key, which every pre-S4 build refuses (row 4). After a rollback below S4, Prometheus therefore gets 401
    for the whole incident.
- **Fix:** add three rows, each with release-note text, and extend the R-test.
  - S2b/S5/S7/S8: "After a rollback, renamed series answer only under their old names; dashboards switched to the
    new names show no data until the upgrade."
  - S3: "Settings that exist only on this release are not applied after a rollback; they return after the
    upgrade."
  - S4: "Keep the old scrape key configured as a second scrape job until you no longer need to roll back below
    this release; the new key does not work there."

### OPS6-10 — MINOR — The image job's volume preparation command does not run against the ABO image

- **Spec:** 09 S0 "Docker" (line 753):
  `docker run --rm -v cfg:/config <image-base> chown 1000:1000 /config`, "as the README's compose notes say too".
- **Evidence:** the ABO image's `ENTRYPOINT` is the binary and it runs as `USER audiobook` (uid 1000)
  (`Dockerfile:96,103`).
  - If `<image-base>` means the ABO image, `chown …` is passed to the binary as arguments.
  - Even with `--entrypoint chown`, uid 1000 cannot chown a root-owned fresh volume.
  - If it means the alpine base, the text should say so. No stage delivers the README change.
- **Fix:**
  - Write the command as `docker run --rm --user 0 --entrypoint chown -v cfg:/config <abo-image> 1000:1000
    /config`, which needs nothing beyond the image under test.
  - Deliver the README compose note in S0 "Deploy".
  - Optionally have the Dockerfile pre-create and chown `/config` too. A named volume first mounted on an
    existing image path inherits its ownership, which removes the step for users.

### OPS6-11 — MINOR — The S0 done-check requires `TestS2bOpensStoreWrittenByS0`, which needs core storage from S2b

- **Spec:** 09 S0 "Done when" (line 764: "`TestLegacyToleratesNamespacedKeys` … and `TestS2bOpensStoreWrittenByS0`
  pass"); §9.10 "(S0, S2b)"; S2b "Done when" (line 833).
- **Evidence:** at S0, core `storage` does not exist; it lands at S2b (09 S2b "Moves"). An S0 PR cannot run a test
  that opens the store with S2b code. What S0 can prove is the key encoding and the write order.
- **Fix:**
  - At S0, run `TestS0LedgerKeysRoundTrip`: the install marker precedes every `c/` key, and every
    S0 key round-trips through `storage/keys`.
  - Keep `TestS2bOpensStoreWrittenByS0` in S2b's done-check only, with an S0-written fixture store committed at S0
    as its input.
