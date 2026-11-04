<!-- file: docs/plans/platform-core/reviews/spec-round-7-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8643863d-0c25-4020-a870-810c9b15845f -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7: ops, rollback and incremental delivery (lens OPS7)

This review walks S0–S10 of [09](../../../specs/platform-core/09-abo-on-core.md) v1.6.1, with S2b and UI-0
included. For each stage it asks five questions:

- Can the stage ship?
- Can its done-checks be measured? This covers the seeded series contract test, the staging host and the
  rollback rehearsal.
- Does a binary-swap rollback cost exactly what the §9.2.1 table says?
- Do the floor checks in the updater and the `deploy` recipe behave as written?
- Are the deploy files correct, and does the metric-alias boot at S8 succeed?

Code anchors are `path:line @748fd787a`; the code in this checkout is unchanged from that commit. Spec
anchors are `file §section (line)`, with line numbers taken from the current checkout.

R3-1, R3-2, R4-1…R4-6, R5-1…R5-7 and R6-1…R6-7 are binding. No finding below asks for removed machinery
back.

Counts: **0 BLOCKER, 5 MAJOR, 7 MINOR** (12 findings).

## A. Regression check (round-6 OPS BLOCKER and MAJOR findings)

| ID | Status in round 7 | Open finding |
|---|---|---|
| OPS6-1 floor release swapped in unchecked before S8 (MAJOR) | **Fixed.** 09 §9.2.2 (lines 279-287), S0 "Updater hotfix" and "Deploy", the S0 done-check, §9.8 row (line 1157), 05-updater §5U.5, §5U.12, the error row and `TestApply_NewBinaryFloorUnmetRefused`, 05-tasks (line 1667). Two residues: a missing sidecar refuses even an empty floor, and §9.6 row 33 still names the floor a "`min_upgrade_from`" | OPS7-10 |
| OPS6-2 S8 alias target refused by `ErrAliasNotCore` (MAJOR) | **Fixed** for the `ErrAliasNotCore` case: `metrics.RuntimeFamilies()`, the §8 row, `TestAlias_GoCollectorTargetAccepted` and the corrected S8 set (05-metrics §3, §6, §13). The S8 boot still fails, through the sibling error `ErrAliasConflict` | OPS7-5 |
| OPS6-3 live-scrape done-checks not reproducible (MAJOR) | **Half-fixed.** The text now names the seeded contract test as the normative check (09 §9.2.7, 05-metrics §6). The test it names cannot produce the stated diffs: it scrapes the default registry with `promhttp.Handler()`, it does not cover `mediacore_*` names, and it cannot seed core-internal emitters | OPS7-1 |
| OPS6-4 staging copy carries production credentials (MAJOR) | **Half-fixed.** R6-7's offline scrub is written down (09 §9.3, 05-backup, 05-config §2.2, 05-metadata `core.integrations.disabled`). As specified, though, it breaks the staging done-checks in two ways: no ssh, and no schedules or updater. It also has no executable and no secret list before S3, and the gap-fill file can refill the keys it deleted | OPS7-2, OPS7-3, OPS7-4, OPS7-7 |

The round-6 MINORs OPS6-5…OPS6-11 landed and are consistent across files. Exit 78/75, `ExecMainStatus` and the
launchd note are in 09 and 05-health. The health token and test names are unified. The `BelowStored` dating is
consistent with 05-config, and the flags appear as a recorded change. Every stage table has a "Flip PR" row.
Rows 16–18 are in the table. The `chown` command and README note are right. `TestS0LedgerKeysRoundTrip` is in
place, with the S0 fixture used at S2b. OPS6-8's flip choice for S6–S8 brought the same gate-placement defect
back (OPS7-6).

## B. Findings

### OPS7-1 — MAJOR — OPS6-3 half-fixed: the seeded contract test named as every stage's series check cannot show the aliases, cannot see `mediacore_*` families, and cannot seed core-internal emitters

- **Spec:**
  - 09 §9.2.7 (line 630): "its diff is the done-check of every stage".
  - The S1, S2, S2b, S5, S7 and S8 done-checks.
  - 05-metrics §6 "Normative: the seeded contract test" (line 267) and its table. At S2 r3 the diff is "only the
    kernel's own `mediacore_*` families". At S2b, S5 and S7 it is "each new `mediacore_*` family added, and its
    alias family present under the old name". At S8 the retired gauges are "produced by their aliases".
- **Evidence:**
  - **The aliases are not in the scrape.** The test scrapes "through `promhttp.Handler()` on the default
    registry" (`internal/telemetry/contract/series_contract_test.go:451-456`). 05-metrics §6 says gather-time
    aliases exist only in `Provider.Handler()` (row S2 r3–S7: the gin route's handler "becomes
    `Provider.Handler()` (gathering that registry plus the gather-time aliases …)"). So the S2b Pebble rename makes
    the old `audiobook_organizer_pebble_*` lines disappear from this scrape, and the done-check cannot show "alias
    family present".
  - **At S8 the books families leave the default registry.** The legacy client_golang families move to the
    App's private registry (05-metrics §6 row "S8 on"). A test that still scrapes the default registry then loses
    every books family.
  - **`mediacore_*` names are filtered out.** `inScopePrefixes` is `audiobook_organizer_`, `ai_dispatch_`, `ai_`
    and `http_server_` (`series_contract_test.go:69-71`). Every `mediacore_*` family is dropped before the
    comparison. That includes the S2 r3 kernel series, `mediacore_storage_pebble_*`, `mediacore_tasks_*`,
    `mediacore_ai_*` and `mediacore_build_info`.
  - **The core-internal emitters cannot be seeded from ABO.** HEAD seeds by importing `internal/database`,
    `internal/opsmetrics`, `internal/ai` and `internal/aidispatch` (`:50-55`). After their moves, the Pebble
    collector is `mediacore/internal/pebble` (09 S2b) and the ops instruments live in the core executor (09 S5).
    The ABO module cannot import a `mediacore/internal/...` package. The test file's own header says the Pebble
    collector "without a source" exports nothing (`:15-18`).
- **Fix:** state the following in 05-metrics §6 and point 09 §9.2.7 at it.
  1. From S2 r3, `internal/telemetrypolicy` builds the same `metrics.Provider` the kernel builds. It uses a
     public constructor, for example `coretest.MetricsProvider(opts)`, plus ABO's metric-alias rows taken from
     the same table ABO's `main` passes, and it scrapes `Provider.Handler()`, never `promhttp.Handler()`.
     `TestContractScrapesServedHandler` asserts that the handler under test is the one the `/metrics` mount
     serves at that stage.
  2. The S2 r3 PR adds `mediacore_` to `inScopePrefixes`.
  3. Seeding rows for core-owned emitters go through public SDK paths:
     - Pebble: a store opened through `coretest`/`storage`;
     - ops: one `hello.count` run;
     - AI: a fake endpoint.

     Alternatively, core ships its own seeded golden (`core/metrics/contracttest`) for `mediacore_*` families, and
     ABO's golden asserts only the books names plus the alias names.
  4. In the S8 row, say that the test's registry becomes the private one in the same PR.

### OPS7-2 — MAJOR — The staging host boots with `--network none`, which cannot publish the API port and blocks ssh, so the `make rollback`, `deploy` and wait-script done-checks that must run there cannot be measured

- **Spec:**
  - 09 §9.3 "Staging" step (4) (line 794): "boots the copy with egress denied (container network `none`, the API
    port forwarded)". The same paragraph adds: "The `make rollback` and updater done-checks run against it as the
    deploy user".
  - Done-checks that depend on it:
    - S0: "`make rollback` on staging (§9.3), run as the deploy user …; then the `deploy` recipe HEAD → S0";
    - S8: "a dry-run `make rollback` as the deploy user";
    - the boot budget (§9.3), which records restart → ready on `/health`.
- **Evidence:**
  - With `--network none` a container has only loopback. Docker cannot publish a port for it, so "the API port
    forwarded" is not possible as written.
  - `make rollback` reaches the host only over `ssh $(DEPLOY_HOST)` (`Makefile:892-917`).
  - `deploy-wait-ready.sh` polls `ssh $SSH_HOST systemctl show …` and the URL from the operator's machine
    (09 S0 "Deploy"; `scripts/deploy-wait-ready.sh:8-13`).
  - The deploy recipe copies over ssh (`Makefile.local.example:88-110`).
  - None of these can reach a staging container that has no network. The S0 and S8 rollback done-checks are
    therefore unmeasurable on the host the spec prescribes.
- **Fix:** in 09 §9.3, and in 05-backup's paragraph (line 589), replace "container network `none`, the API port
  forwarded" with **egress denied by firewall, ingress allowed**.
  - The container or VM gets an `nftables` OUTPUT policy `drop`, except loopback and `established,related`, with
    inbound ssh and the API port open.
  - If Docker is used, the equivalent is an `internal: true` network reached through a jump container that runs
    sshd.
  - `scripts/staging/provision.sh` installs the rules **before** the first boot.
  - `test_staging_scrub.py` (or a new `test_staging_egress.sh`) asserts that an outbound TCP connect from the
    service user fails and that ssh to the deploy user succeeds.

### OPS7-3 — MAJOR — The scrub turns off what the S5 and S8 done-checks measure: S5 needs all 38 schedules to fire on staging, and S8 needs auto-update with egress to a release source. 09 and 05-backup also disagree on what the scrub disables

- **Spec:**
  - 09 §9.3 step (3): "it disables outbound integrations and schedules". The steps it lists then set only
    `core.integrations.disabled` (or clear URLs) and `auto_update.enabled=false`.
  - 05-backup (line 589): "the updater **and every schedule** disabled, and the first boot run with egress denied".
  - S5 done-check (line 965): "every one of the 38 schedule definitions fires on the staging store within one
    interval of the T0+T1 release (DATA5-8)".
  - S8 done-check (line 1010): "a staging install (§9.3) with `auto_update.enabled=true` … receives the next
    signed release".
  - The S8 risk row (§9.11) uses that check as its detection.
- **Evidence:**
  - **S5 cannot pass.** If schedules are disabled as 05-backup says, no schedule fires. If they are not, 09's
    sentence is false and the two files contradict each other.
  - **S8 cannot pass either.** The scrub forces `auto_update.enabled=false`, and egress is denied, so the S8
    updater can reach no release source.
  - **The S8 check depends on a later release.** "Receives the next signed release" cannot be measured until a
    release after S8 exists.
- **Fix:**
  - Rewrite step (3) in both files as: "**nothing outbound** (integrations disabled and URLs cleared, OPS7-7;
    egress denied); schedules **run**". A schedule that fires against a disabled integration fails closed (05-ai,
    05-download, 05-indexer rows for `core.integrations.disabled`), which is what DATA5-8 needs: the run is
    enqueued and its clock advances.
  - For S8, `provision.sh --updater-check` starts a local fake signed-release server on the staging host. It is
    the S0 updater test's fake server, signed with a test key compiled into a staging build, or with the real key
    over a locally re-served copy of the newest signed release. The script then allows egress to that one
    loopback or bridge address and sets `auto_update.enabled=true` after the scrub.
  - Change the S8 check to "applies the fake release and `Boot` confirms it".

### OPS7-4 — MAJOR — The offline scrub has no executable and no secret list before S3, and a DB-directory copy brings back the deleted keys through `<db dir>/config.yaml`

- **Spec:**
  - 09 S0 "Deploy": "S0 delivers `scripts/staging/provision.sh` with its offline scrub".
  - 09 §9.3 step (1): "from the secret-class key list that 05-config generates".
  - 05-config §2.2 "One generated secret-class list": `abo_keys.golden`, checked by
    `TestSecretClassListMatchesLegacyStore` against `SecretsStore.Has`/`Kept`.
  - 09 §9.3: "a store seeded from the production-size restored copy".
- **Evidence:**
  - **No program can do the scrub.** Steps (1)–(2) blank fields inside `setting:config_blob` and delete `sess:`,
    `idx:sess:user:`, `invite:` and the secret rows of a Pebble store. A POSIX `sh` script cannot do that, and no
    Go subcommand or tool is named for it.
  - **The list does not exist until S3.** It is checked against books' legacy `SecretsStore`, which lands at S3
    (09 S3 "Moves", 05-config §1.12.2). S0's own done-check needs a scrubbed staging copy (OPS7-2).
  - **The gap-fill file refills the deleted keys.** `ConfigFilePath()` is `<db dir>/config.yaml`
    (`internal/config/persistence.go:28-37`). `LoadConfigFromFile` fills an empty OpenAI, Google Books or
    Hardcover key from it (`:42-95`), and the corrupt-secret repair re-encrypts it into the store (`:1229-1262`).
    `SaveConfigToFile` keeps those keys in that file in plaintext (`:163-172`). So when the copy is taken as a
    directory (rsync of the DB's parent), not as an archive restore, step (2)'s deletion of the encrypted rows is
    undone on the first boot. Only archives are safe: they never contain the file (05-backup lines 107 and 124).
- **Fix:**
  1. Name the executable: `audiobook-organizer staging-scrub --db <copy> --fields <file>`. It is a cobra
     subcommand in `cmd` and stays ABO. It uses books' store code offline and writes only the listed keys.
     Because its open is a foreign open to the guard, the boot budget already discards the first boot (state
     this).
  2. S0 commits `scripts/staging/secret_fields.txt`, written by hand from 05-config §2.2's table. From S3,
     `TestSecretClassListMatchesLegacyStore` also asserts that this file equals the generated list.
  3. `provision.sh` accepts only an archive restore (05-backup's staged restore, or HEAD's extract before S1).
     It refuses to continue while `<db parent>/config.yaml` exists, or deletes it. `test_staging_scrub.py` gets a
     case where a `config.yaml` beside the copy holds an OpenAI key, and asserts that no key survives the first
     boot.

### OPS7-5 — MAJOR — S8 metric-alias boot: the two books process gauges still exist when the S8 PR adds their aliases, so `AddAliases` fails with `ErrAliasConflict` and the S8 build does not boot

- **Spec:**
  - 09 S8 "Moves" (line 1002): "the legacy client_golang families move to `Provider.Registerer()` in the same
    PR".
  - 09 S8 "Metrics" and 05-metrics §13 item 3: the two process gauges become aliases of `go_goroutines` and
    `go_memstats_alloc_bytes`.
  - 05-metrics §8 row "Alias collides with a live family | fail-closed at `AddAliases`".
  - 05-metrics §7: aliases are installed before listeners open.
- **Evidence:**
  - Both gauges are ordinary members of the legacy set: `memoryAllocGauge` and `goroutinesGauge`
    (`internal/metrics/metrics.go:188-197`), registered in the same `MustRegister` call as every other books
    family (`:302-303`).
  - They are set by `SetMemoryAlloc`/`SetGoroutines` (`:422-423`), which HEAD's 5-second status ticker calls
    (`internal/server/server_lifecycle.go:432-433`). S6 moves that ticker into a books worker with the same payload
    (09 S6 "Moves").
  - Nothing in 09 or 05-metrics deletes the two gauges. Moving "the legacy families" as specified therefore puts
    `audiobook_organizer_process_goroutines` live on the private registry, and the alias with the same name is
    refused at boot.
  - `TestAlias_GoCollectorTargetAccepted` runs on a bare private registry, so it cannot see this.
- **Fix:** 09 S8 "Metrics" and 05-metrics §13 item 3 state that **the same PR**:
  - deletes `memoryAllocGauge`, `goroutinesGauge`, `SetMemoryAlloc` and `SetGoroutines`, and the two calls in the
    books status worker. The `books.system.status-ticked.v1` payload keeps its `memory_alloc` and `goroutines`
    fields, read from `runtime`.
  - replaces the two gauges' seeding rows with the alias rows.

  Also add `TestS8_FullAliasSetBoots`: ABO's whole metric-alias table plus ABO's legacy collectors on the S8
  Provider → `AddAliases` returns nil.

### OPS7-6 — MINOR — The rehearsal gate sits on S6–S8 deletion PRs, while the PRs that write each stage's new state merge earlier and can reach production through the 10-RC auto-promotion. "Never runs half a stage" is false for S2b and S6–S8

- **Spec:**
  - 09 §9.2.1 (line 178): "native code lands dark across many PRs; exactly one flip PR … so an RC or production
    build cut from main between PRs never runs half a stage".
  - The S6, S7 and S8 "Flip PR" rows (lines 971, 985, 999) name deletion and mount PRs.
  - S2b's flip is "Release 1's first core-storage PR" (line 899).
- **Evidence:**
  - The state-writing changes of S6–S8 are separate PRs that do not change `shipped_stage:`:
    - S6 `c/outbox/` rows;
    - S7 the `Local` `Migration`, `c/integrations/instances/` and the AI rename;
    - S8 the backup catalogue and `updater.Boot`.
  - They therefore pass `extraction-gates` without the red-rehearsal rule.
  - Releases are auto-promoted every 10 RCs (§9.1; `prerelease.yml:59-131`), so a production minor can ship such
    a change before the flip.
  - Likewise at S2b the flip is the **first** PR, so the compat ledger, the Pebble rename and the factory-reset
    change ship live PR by PR.
  - This is the defect OPS6-8 fixed for S2b, now recurring for S6–S8.
- **Fix:**
  - Reword §9.2.1: "S3–S5 land dark behind their flip; S2b and S6–S8 land live PR by PR".
  - Extend the `extraction-gates` rule to every PR whose diff:
    - changes the generated claim list (`TestBooksClaimsGenerated` input);
    - registers a `Migration`/`DataMigration`;
    - adds a `c/` or `m/` key family to 05-storage's table;
    - or changes `docs/compat-surfaces.md`.

    All four are mechanical from the diff. Test the rule in `test_run_gates.py`.

### OPS7-7 — MINOR — The scrub's integration switch depends on the copy's age, not on the binaries that boot it, and production credential verifiers stay on staging

- **Spec:** 09 §9.3 step (3): "from S7 on it sets `core.integrations.disabled=true` …, and on a copy older than
  S7 it clears the legacy integration URLs instead". Step (2) deletes only `sess:`, `idx:sess:user:`, `invite:`
  and the encrypted rows. 05-backup B1 calls a main archive "credential-equivalent: it holds every password hash".
- **Evidence:**
  - At S7 and S8 the boot-budget A/B and the staging `make rollback` boot the **previous** binary (S6, then S7)
    on the same copy. S6 ignores `core.integrations.disabled`, so it runs with the production Deluge, SMTP and
    webhook URLs. Only egress denial protects those systems.
  - The `u:` password hashes and `apikey:` hashes survive. Offline guessing of them is the residual that
    05-backup names.
- **Fix:**
  - Step (3): "always clear the legacy integration URLs, and from S7 also set `core.integrations.disabled=true`".
  - Step (2): also replace every `u:` password hash with a never-matching value, and set every `apikey:` row
    `revoked`. Staging access then goes through the bootstrap-token flow.
  - `test_staging_scrub.py` asserts all three.

### OPS7-8 — MINOR — The `shipped_stage:` values `S2r1`–`S2r3` are not stage points of 04 §2.1, and the marker's initial value and its values at S2b, S4 and S5 are unstated

- **Spec:**
  - 09 S2 "Flip PR" (line 881): "each sets `shipped_stage:` to `S2r1`, `S2r2`, `S2r3`".
  - S0, S1 and UI-0 "leave `shipped_stage:` unchanged".
  - 04 §2.1 (lines 132, 142): the generator evaluates "every remaining stage point" of 09, meaning stages, UI-0
    positions and T8 positions.
- **Evidence:** 04 defines no `S2r*` stage point and no order among them. S0 commits the mapping input but no
  initial marker value. No stage table says what value the S2b, S4 r2 or S5 T0+T1 flip writes. The rule
  "`check_stage_visibility.py` fails a PR that changes `shipped_stage:` to a stage whose table names no flip"
  therefore has no vocabulary to check against.
- **Fix:** 04 §2.1 lists the ordered marker values:
  `pre-S0` (committed by S0) `< S2r1 < S2r2 < S2r3 < S2b < S3 < S4 < S5 < S6 < S7 < S8 < S9 < S10`.
  - UI-0, T8 and S4 r1 are never marker values.
  - Rows that 04 §8 keys "S2" stay remaining until `S2r3`.
  - The generator rejects an unknown value.

  09's flip rows each name the value they write.

### OPS7-9 — MINOR — The flip-PR gate reads a rehearsal run against the latest production tag, while the rollback target on the reference host is a main build. The override happens at deploy time, after the gate

- **Spec:**
  - §9.10 R-test (line 1233): `PREV_REF` is "by default the latest production release tag; before each flip
    deploy the operator overrides it with the commit P10 prints".
  - §9.2.1 `extraction-gates`: the flip PR fails unless "the most recent `compat-rehearsal` run on `main`" is green
    and less than 36 h old.
  - §9.2.1: "`.prev` … may be any main build".
- **Evidence:** the nightly run uses the default `PREV_REF`. The gate therefore never sees a rehearsal against the
  `.prev` that `make rollback` will actually swap in on the reference host, and the override comes after the flip
  PR has merged, with no gate on it.
- **Fix:**
  - Commit `ci/prev_ref.txt`, updated after each reference-host deploy from P10's output.
  - `compat-rehearsal` runs a two-row matrix: the latest production tag and `prev_ref.txt`.
  - The flip-PR rule requires both rows green and less than 36 h old.
  - `test_run_gates.py` covers a red second row.

### OPS7-10 — MINOR — Floor-check residue of OPS6-1: a missing `.storage-applied` refuses even an empty floor, which blocks the S0 done-check's HEAD → S0 deploy; §9.6 row 33 is stale

- **Spec:**
  - §9.8 row (line 1157): "store does not meet it, **or `<db>.storage-applied` cannot be read** → closed: apply
    refused".
  - 05-updater error row: "or `StoreState` fails".
  - S0 done-check: "the `deploy` recipe HEAD → S0 passes its restore probe and the S0 build boots on the store
    HEAD wrote".
  - §9.6 row 33 (line 1124): "Release `min_upgrade_from` | the floor release".
- **Evidence:** a HEAD store has no applied-steps sidecar; only S0+ boots write one (§9.2.2 records table). As
  written, the S0 recipe's floor check refuses the very first S0 deploy. The S0 binary's floor is empty, so
  nothing needed checking. Row 33 calls a signed field the floor, although that field exists only from S8; at
  S5 the floor is carried by the binary.
- **Fix:**
  - §9.8, 05-updater and the shared helper: "an empty `--print-upgrade-floor` passes without reading the sidecar;
    a non-empty floor with a missing sidecar refuses with 'boot any release ≥ S0 once'". Add both cases to
    `scripts/test_storage_format_guard.py`.
  - Row 33: "upgrade floor (binary-carried from the floor release; also the signed `min_upgrade_from` from S8)".

### OPS7-11 — MINOR — From S2b a staged factory reset is pending state like a staged restore, but `make rollback`, the `deploy` recipe and the S0 sudoers probe look only under `restore/`

- **Spec:**
  - 09 §9.2.2 "Rollback tooling" (line 320): the one restore probe `find`s `restore/pending.json` and
    `restore/*/inflight.json`.
  - S2b "Data/keys" and §9.2.1 row 12: the staged reset.
  - 05-storage §5S.3.15: `<state_dir>/reset/pending.json` and the journal `reset/<id>/journal.json`.
  - 06 P0a.
- **Evidence:**
  - **An in-flight reset can be half-applied.** Rolling back to a pre-S2b build with a reset journal in flight
    leaves the set-aside half done: the DB directory may already be moved while sidecars remain. The older build
    then opens an empty path. HEAD creates the store if it is missing (`cmd/root.go:640-645`).
  - **A pending reset can be dropped silently.** A deploy between staging and restart turns the admin's audited
    reset into a rejected intent (`staged_by_build` differs).
  - The S0 sudoers line is a literal `find` under `restore/` (§9.2.2 "Owner actions"), so the tooling cannot see
    either case.
- **Fix:**
  - `storage_format_guard.py`, the `deploy` recipe and the updater refuse while `reset/pending.json` or
    `reset/*/journal.json` exists (override `ROLLBACK_IGNORE_RESET=1`).
  - The probe becomes one `find` over both `restore/` and `reset/`, delivered as an S0 owner action so it is in
    place before S2b.
  - Add cases to `test_storage_format_guard.py`.
  - Add a §9.8 row.

### OPS7-12 — MINOR — The S3 done-check compares an effective-config golden built from the production environment, YAML and blob, but the spec does not say where that fixture lives or how it is redacted

- **Spec:** 09 S3 "Done when" (line 930): "The effective-config golden equals **HEAD's** for two fixtures: the
  production env, YAML and blob, and the compose environment". §9.7: "paste counts only, never values".
- **Evidence:** the production blob holds every plaintext secret, and the env and YAML hold hostnames and
  integration URLs (09 §9.1 row "Config"). A golden committed or attached to CI from these leaks them. Nothing
  says the comparison runs offline or on the scrubbed copy.
- **Fix:**
  - The production half runs on the staging host, against the scrubbed copy and the production unit's env names
    (values from P4, with secret-class values replaced by placeholders). It reports the diff of key names and
    value hashes only.
  - Nothing derived from production is committed. Only the compose fixture is a committed golden.
