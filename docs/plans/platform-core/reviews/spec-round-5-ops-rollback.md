<!-- file: docs/plans/platform-core/reviews/spec-round-5-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1aad4702-a9f3-4932-ba7e-2d7a7a1c28b2 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 5: ops, rollback and incremental delivery (lens OPS5)

Lens: walk S0–S10 of [09](../../../specs/platform-core/09-abo-on-core.md) v1.4.1 (S2b and UI-0 included).
For each stage the review checks five things: the stage can ship; its done-check can be measured; a binary-swap
rollback works with the costs listed in the §9.2.1 table; the floor and stepping-stone releases are kept apart;
and `make rollback`'s probes work. It also checks the state-directory fallback, the Docker image jobs, who serves
`/metrics` from S2 to S8, release gating as a named list of required checks, and the shipped deploy files. Code
anchors are `path:line @748fd787a`. Spec anchors are `file §section` against the current checkout (09 v1.4.1,
05-config, 05-metrics, 05-storage, 05-updater, 05-http-api, 06, 14).

R3-1 (one stored format, binary-swap rollback), R3-2 (one gating requirement), R3-4 (state-directory fallback),
R4-1 (books supplies the legacy stores) and R4-2 (sentinels for moved secrets) are binding. The findings below
attack only places where the revised text does not work as written.

Counts: **2 BLOCKER, 5 MAJOR, 9 MINOR** (16 findings).

## A. Regression check (round-4 OPS BLOCKER and MAJOR findings)

| ID | Status in round 5 | Open finding |
|---|---|---|
| OPS4-1 empty Basic password after rollback (BLOCKER) | Fixed by R4-2: the old build now fails closed instead of open. But no supported way exists to re-enter the password on the older build, so the fail-closed state cannot be cleared there | OPS5-2 |
| OPS4-2 state-dir `ENV` in the Dockerfile | Fixed: no shipped file sets the variable (09 S2 "Deploy", 05-config §1.7, §9.2.5). The image job's second run checks the key location only from S3, but the resolver changes at S2 | OPS5-7 |
| OPS4-3 restore probe | Fixed: one `find` probe, `cannot_determine` on a non-zero exit, the sudoers line is an S0 owner action, script cases listed (09 §9.2.2, 05-storage §5S.3.7) | — (ordering residue OPS5-16) |
| OPS4-4 binary directory inside the 0700 state dir | Fixed (09 S8 preconditions; 05-updater §5U.5 layout (a); "no root for the copy" deleted) | — |
| OPS4-5 two meanings of "minimum-upgrade-from" | Fixed: 09 §9.2.2, 05-tasks §5T.6, 05-storage §5S.3.5, 05-updater, 03 §3.7, 02 N14 and 04 all use "floor release" and "stepping-stone release"; `TestStepSteppingStoneValid` is specified | — |
| OPS4-6 rehearsal gates nothing | Fixed in substance: the prev-build tests are in `extraction-gates`, and a red nightly run fails the next flip PR. No check can tell which PR is a flip PR | OPS5-8 |
| OPS4-7 `make ci` cannot be required | Fixed: named required checks, job-level `if:` skips, an up-to-date branch or a merge queue. Two defects came with the fix: the `docker-image` trigger list is narrower than before, and the docs classifier skips new Go-test inputs | OPS5-3, OPS5-4 |

## B. Findings

### OPS5-1 — BLOCKER — The shipped compose file always sets `BASIC_AUTH_ENABLED` (default `false`). Under core config's operator lock (C3), that env value overrides a Basic gate turned on in the UI, so S3 silently turns the gate off on compose installs

- **Spec:**
  - 05-config C3 (line 26: "a key supplied by file, env or flag cannot be overridden from the DB layer") and
    §1.3.5 (lines 541-549: "if any operator layer (file, env, flag) supplies a key, that key is locked: its stored
    value is ignored at load").
  - 05-config §1.12.4 permanent rows (lines 1309-1311): `BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME`,
    `BASIC_AUTH_PASSWORD` are permanent aliases.
  - 09 §9.2.5, and 09 S3 "Deploy files" (the compose file keeps `BASIC_AUTH_*`).
  - 05-http-api S3 row (line 972: from S3 stage 8 reads `core.http.basic_gate.*`).
  - 09 S3 done-check: the effective-config golden covers only "the production env, YAML and blob".
- **Evidence:**
  - `docker-compose.yml:20-22` passes `BASIC_AUTH_ENABLED=${BASIC_AUTH_ENABLED:-false}`,
    `BASIC_AUTH_USERNAME=${BASIC_AUTH_USERNAME:-admin}` and `BASIC_AUTH_PASSWORD=${BASIC_AUTH_PASSWORD:-}`.
    All three are set in every compose container, whether or not the user set them.
  - At HEAD these values are only a **default**:
    - viper reads them through prefix-less `AutomaticEnv` (`cmd/root.go:625`) into the base config;
    - `LoadConfigFromDatabase` then lays the blob over that base (`internal/config/persistence.go:1131-1170`) and
      applies the encrypted secret rows (`:1186-1199`);
    - the Basic keys are neither in `envLockedSettings` (`internal/config/config.go:2241-2253`) nor re-applied by
      `applyEnvAuthoritativeConfig` (`:2435` on) or `SyncConfigFromEnv` (`persistence.go:2023-2057`).
    - Result at HEAD: a Basic gate turned on and given a password in the Settings UI is effective on a compose
      install.
    - viper also treats an empty env value as unset: HEAD never calls `AllowEmptyEnv`.
  - From S3, the façade projects `AppConfig` from core config, and stage 8 reads `core.http.basic_gate.*`. Under C3:
    - the always-present `BASIC_AUTH_ENABLED=false` locks the switch off;
    - the blob's `basic_auth_enabled: true` is "ignored at load";
    - the gate stops guarding anything, with no warning beyond a UI lock note.
  - Where the user did set `BASIC_AUTH_ENABLED=true` but saved the password in the UI, compose still passes
    `BASIC_AUTH_PASSWORD=` (empty). No file defines whether core counts an empty env value as "set". If it does, the
    password locks to empty. HEAD's comparison (`internal/server/middleware/basicauth.go:81-87`) then admits
    `<username>:`, which is exactly the hole R4-2 closed.
  - Neither `TestPerimeterHonoursBlobOnlyValues` nor the production-env golden runs with the compose environment, so
    no done-check sees this.
  - The same precedence flip affects every other key that HEAD lets the blob override, for example
    `FP_PARALLEL_WORKERS`, which the unit sets.
- **Fix:**
  1. 05-config: give `Alias` a field `EnvIsDefault bool` (or `Layer: LayerDefault`). It applies to every alias whose
     key HEAD lets the blob override, which is every `AutomaticEnv`/`BindEnv` name except `envLockedSettings`,
     `applyEnvAuthoritativeConfig`, `absEnvOnlySettings` and `SyncConfigFromEnv` keys. Such a name feeds the
     **default** layer, below the DB overrides, and locks nothing. Canonical `ABO_*` names keep C3. State that an
     env value set to `""` is unset (HEAD parity with viper's default).
  2. Mark `BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME` and `BASIC_AUTH_PASSWORD` `EnvIsDefault` in the §1.12.4
     table. Generate the flag from HEAD's lists, so the table cannot drift.
  3. Add `TestComposeEnvKeepsUIBasicGate`: compose's three variables at their defaults, with the blob holding
     `basic_auth_enabled: true` and a stored password. On S3: stage 8 is on, `<username>:` gets 401 and the stored
     password gets 200.
  4. Add the compose environment as a second fixture of the S3 effective-config golden (09 S3 "Done when").
  5. Optionally, in S3 "Deploy files", change the compose lines to `- BASIC_AUTH_ENABLED` (pass through only when
     set). Existing users keep their copied compose files, so (1) is still required.

### OPS5-2 — BLOCKER — After a rollback from S3+, a Basic-gated install cannot re-enter its password on the older build: the gate blocks the UI, env does not override the stored row, and no CLI writes it. The S3 R-test row cannot pass

- **Spec:**
  - 09 §9.2.1 row 1 ("until you do, the older release refuses them"; release note "re-enter every secret … Basic
    password").
  - 09 S3 "Done when" (`TestRollback_BasicGateNeverOpenOnPrev`: "re-enter it on prev, roll forward, the re-entered
    value is effective").
  - 05-config C16 and §1.12.2 ("until the operator re-enters the password there").
  - 14 K13 ("rollback runbooks state … the secret re-entry").
- **Evidence:** the rollback target after S3 is S2b, S2, S0 or HEAD. All of them load config with HEAD's code in
  `serve` (`cmd/root.go:336-383`).
  - The UI and `PUT /api/v1/config` sit behind the Basic gate. Its only exemptions are health, static assets and
    ABS-token routes (`internal/server/middleware/basicauth.go:33-72`). With the sentinel stored, every credential
    gets 401, so the Settings page cannot be reached.
  - Env cannot override the stored row. `BASIC_AUTH_PASSWORD` enters only viper's base config. The encrypted
    `basic_auth_password` row, which now holds the sentinel, is applied over it (`persistence.go:1186-1199`). Neither
    `SyncConfigFromEnv` (`:2023-2057`) nor `applyEnvAuthoritativeConfig` re-applies a Basic key. `basic_auth_enabled`
    comes from the blob the same way, so `BASIC_AUTH_ENABLED=false` cannot turn the gate off either.
  - No CLI writes it. HEAD's commands are `serve scan playlist tag organize inspect-metadata seed diagnostics
    {cleanup-invalid,reserved-prefs,query} fp-worker dedup-bench` (`cmd/*.go` `Use:`). None writes a setting.
  - Result: after the rollback, the older build is up but unusable for every request that is not exempt. The only
    ways out are to roll forward (often the build being escaped) or to edit the store by hand. The R-test step "re-enter
    it on prev" has no path a harness could drive.
  - Other moved secrets do not have this problem. The UI stays reachable, and `openai_api_key`/`acoustid_api_key`
    are re-applied from env (`persistence.go:2030-2054`).
- **Fix:**
  1. Add an S0 ABO-local change (S0 "Hotfixes", 09 §9.4.1). Every rollback target from S3 on (S0 through S2b) then
     carries a recovery path that stays off unless the operator asks for it. Recommended form: an offline command
     `audiobook-organizer config set-secret basic_auth_password --db <path>`. It refuses while the store is open,
     reads the new value from stdin, and writes the legacy encrypted row through HEAD's `SetSetting(..., true)`.
     Also accept `--disable-basic-gate`, which writes `basic_auth_enabled=false` into the blob.
  2. Alternatively, make S0's `SyncConfigFromEnv` re-apply a non-empty `BASIC_AUTH_PASSWORD` when the stored value
     is a sentinel. The S0 build cannot recognise a sentinel by value, so (1) is preferred.
  3. Row 1's release-note text and K13's runbook sentence name that command for the Basic gate. The rollback target
     must be ≥ the release that ships (1). K21 names it as the oldest safe target for Basic-gated installs.
  4. Rewrite the R-test row: "save the Basic password on cur, roll back: `<username>:` and the old password both get
     401 on prev; run the S0 recovery command, restart prev: the new password gets 200; roll forward: the re-entered
     value is effective".

### OPS5-3 — MAJOR — The `docker-image` required check no longer runs on PRs that change only root Go code, so the image-only done-checks (auth-off boot, keyring on the volume, build-ID header) are not gated by the PRs that can break them

- **Spec:**
  - 09 §9.2.1 gating table, row `docker-image` (trigger: "every PR that changes `core/**`, `go.mod`/`go.sum`, a
    Dockerfile, `.dockerignore`, `web/**` or any `//go:embed` input").
  - 14 K20 ("each job's trigger list covers every input it builds").
  - In contrast: 09 §9.11 row S0 ("Detection: the image job on every non-doc PR"); 09 review history OPS2-14
    ("Docker job on every non-doc PR with a full image run").
- **Evidence:**
  - The image builds the whole root module with `-tags "embed_frontend fts5 native_taglib"` and CGO static linking
    (`Dockerfile:74-78`). Every root `.go` file is an input, and `ci.yml` builds neither those tags nor the static
    link.
  - Assertions that live only in the image job:
    - `TestS4_AuthOffDockerBoots` (09 S4);
    - the keyring/`.encryption_key` location and decryption after a container recreate (09 S0, S3);
    - `TestBuildID_HeaderEqualsEmbeddedMeta`;
    - `--print-storage-levels` inside the container.
  - The code those assertions guard lives under `cmd/` (the S2 state-dir switch, `cmd/root.go:336`) and `internal/`
    (books' legacy config store, the auth-off listener check). A PR that changes only those paths matches none of
    the listed triggers. It merges green and can break Docker installs, and nothing catches it before the release.
  - The narrowing came in with the OPS4-7 fix. The risk table and the round-2 disposition still say "every non-doc
    PR".
- **Fix:**
  - Set the `docker-image` trigger to "every PR that touches a non-doc file", the same `changes` classification as
    `extraction-gates`, with layer caching. Fix 14 K20 and Q-I to match.
  - If CI minutes matter, split it in two. A cheap `docker-build` (build only, cached) runs on every non-doc PR. The
    full run (seeded volume, recreate, auth-off boot) runs on the same set, or at least on every PR that touches
    `cmd/**`, `internal/config/**`, `internal/books/**` or `internal/server/**`.

### OPS5-4 — MAJOR — CI's `changes` classifier treats every `*.md` and `docs/*` path as docs-only, but the revision makes such files Go-test and embed inputs; a PR that changes only them skips `extraction-gates`, the Go jobs and the image job

- **Spec:**
  - 09 §9.2.9 (the embedded copy `internal/books/compat-surfaces.md`; the source of truth
    `docs/compat-surfaces.md`; `TestCompatLedgerCopyFresh`, `TestCompatLedgerMatchesCode` and
    `TestNoContractBeforeSunset` read the ledger).
  - 03 §3.4 and 09 S0 table (`archtest.DeferredMembersAbsent` is generated from every "Deferred-build members"
    heading under `docs/specs/platform-core/`).
  - 09 §9.2.1 (`extraction-gates` runs on "every PR that touches a non-doc file").
- **Evidence:**
  - `.github/workflows/ci.yml:92` classifies `web/*|docs/*|*.md|changelog.d/*|…` as not Go. In a bash `case`
    pattern `*.md` also matches `internal/books/compat-surfaces.md`.
  - The workflow's own invariant (`ci.yml:46-48`: "No Go test reads a file under a docs glob as an input … so
    docs-only really cannot change a Go verdict") is false once these tests exist.
  - Concrete failures:
    - A PR that edits only `docs/compat-surfaces.md`, for example pulling a row's earliest-removal date earlier,
      skips `TestNoContractBeforeSunset` and the freshness check. It merges, and the next unrelated Go PR fails
      `extraction-gates` on a stale copy it did not touch.
    - A PR that edits only the embedded copy changes what ships, unchecked.
    - A spec PR that deletes a "Deferred-build members" heading skips `DeferredMembersAbsent`.
  - R4-4 chose a path below the root because `.dockerignore`'s `*.md` is root-level only (`.dockerignore:30`). CI's
    classifier has no such limit.
- **Fix:**
  - 09 S0 "Gates": the `changes` classifier gains explicit `go=true` cases **before** the docs glob for
    `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md`, `core/compat/*.md` and
    `docs/specs/platform-core/*.md`, the generator input of `DeferredMembersAbsent`.
  - Update the `ci.yml:46-48` comment.
  - Add `scripts/tests/test_ci_classifier.py`: it lists every file that a Go test or `//go:embed` reads under a docs
    glob and asserts that each one sets `go=true`.
  - Better still, generate `DeferredMembersAbsent`'s list into a committed non-Markdown file with its own freshness
    check.

### OPS5-5 — MAJOR — Who enforces `core:metrics.read` on `/metrics` from S4 to S7 is contradicted: 09 says from S4, while 05-metrics says gin's legacy route keeps its "any credential" check until S8

- **Spec:**
  - 09 §9.2.6 "`/metrics` for scoped keys" ("From S4 `/metrics` needs `core:metrics.read` … a scoped legacy scrape
    key must be replaced", release note at S4); 09 S4 "Permissions" ("`/metrics` per §9.2.6").
  - 05-metrics §6 per-stage table, row S2–S7: ABO's **legacy gin route**, "unchanged in path, credential check and
    compression exclusion". The kernel's `Router.Infra(…, "core:metrics.read", …)` arrives only at S8.
  - 05-rbac §4.8 ("the change is recorded in 09 §9.2.6").
- **Evidence:**
  - At HEAD `/metrics` is `buildTopLevelAuthChain(cfMW, RequireAuth, promhttp)`, which accepts any credential
    (`internal/server/server_lifecycle.go:1742-1747`).
  - Under `MountLegacy` it is one of the permission-less routes annotated `AnyAuthenticated` (09 S2 "HTTP"). No file
    re-annotates it at S4. Stage 13 therefore keeps admitting any credential until the S8 PR removes the gin route.
  - Effects:
    - Scoped keys keep scraping from S4 to S7, against 09.
    - The behaviour change and its release note land at S8, not at S4.
    - The S4 R-test and `TestInfra_MetricsRequiresScope` assert a rule that is not live at S4.
- **Fix:** choose one, and state it in 09 §9.2.6, 05-metrics §6 and 05-rbac §4.8:
  - (a, preferred) The S4 r2 PR changes the legacy manifest entry for `/metrics` from `AnyAuthenticated` to
    `core:metrics.read`, so stage 13 enforces it from S4. 05-metrics §6 row S2–S7 reads "credential check unchanged
    until S4 r2, then `core:metrics.read` through stage 13". Add `TestLegacyMetricsRoute_RequiresScopeFromS4`.
  - (b) 09 §9.2.6 says "from S8", and the scoped-key release note moves to S8.

### OPS5-6 — MAJOR — 09 schedules the core-subsystem metric renames at S8 and checks "series-name set equals S7's" there, while 05-metrics and 06 put the aliases at S2b and each rename in the stage that moves its emitter; the S8 check cannot pass, and S2b, S5 and S7 have no rename check

- **Spec:**
  - 09 S8 "Metrics" ("Core-subsystem renames with gather-time aliases (§9.2.7) … `mediacore_build_info{version}`").
  - 09 S8 "Done when" ("every aliased core family answers under both names; the sorted series-name set equals S7's").
  - 09 §9.4.1 S8 row ("05-metrics renames and kernel `/metrics`").
  - In contrast:
    - 05-metrics §6 ("gather-time aliases of core-renamed series, which therefore work from the stage that renames
      them", from S2);
    - 06 landing table, S2b (`Options.MetricAliases`);
    - 09 §9.2.7 (a series is renamed when its emitter moves);
    - 09 S2b "Moves" (the Pebble collector moves to `internal/pebble`);
    - 05-metrics §13 item 1 (`opsmetrics` becomes core `tasks` instrumentation, S5);
    - 05-ai (`mediacore_ai_*`, S7).
- **Evidence:**
  - If the Pebble, ops and AI families are renamed when their emitters move (S2b, S5, S7), the old names vanish there
    unless the alias and the `deploy/` dashboard switch ship in the same PR. 09 S2b, S5 and S7 mention neither, and
    none of their done-checks compares series names.
  - If the renames really wait for S8, the moved emitters must keep the old names until then, and no file says so.
  - S8 adds `mediacore_build_info`. A series-name set that equals S7's is then impossible.
  - §9.6 row 24 ("W after S8") does not fit an alias that ships at S2b.
- **Fix:**
  - Give each rename in 09 the stage that moves its emitter: Pebble at S2b, ops at S5 (T0+T1), AI at S7, the two
    process gauges at S8.
  - For each of those stages: the alias row in the ledger, the `deploy/` dashboards and rules switched in the same
    PR (05-metrics §13 item 4), and a done-check "the series-name set ⊇ the previous stage's, every renamed family
    answers under both names".
  - The S8 check becomes "set(S8) = set(S7) ∪ {`mediacore_build_info`, …listed additions}". §9.6 row 24 counts W
    from each alias's own release.

### OPS5-7 — MAJOR — The image job's second run (`--db` on another volume) checks the key location only "from S3", but the state-directory resolver changes at S2; and as written the run cannot write to its own `/config` volume

- **Spec:**
  - 09 S0 "Docker" ("A **second run** … and from S3 it seeds, boots, recreates the container and asserts that the
    keyring is under `/config`").
  - 09 S2 "Deploy" ("The S0 image job's second run (`--db /config/x.pebble`) checks it").
  - 09 S2 "Moves" and 05-config §1.7 ("One resolver from S2": `serve` calls `app.ResolveStateDir` instead of
    `EnsureSecureStateDir`).
- **Evidence:**
  - S2 is the release in which ABO stops resolving through HEAD's `EnsureSecureStateDir`
    (`internal/config/state_dir.go:133-155`, `cmd/root.go:336`) and moves to the kernel's rule, which reads only flags
    and env.
  - The risk R3-4/OPS3-1 named, `.encryption_key` landing off the volume, is therefore introduced at S2. The
    `.encryption_key` exists from HEAD on, so it can be asserted from S0. Yet the assertion is switched on only at
    S3, and 09 S2 claims a check that is not running.
  - Separately: the image creates and chowns only `/data` (`Dockerfile:93-94`) and runs as uid 1000 (`:96`). A fresh
    named volume mounted at a path that does not exist in the image is root-owned. `serve --db /config/x.pebble`
    then cannot create the store, and the second run fails before it asserts anything.
- **Fix:**
  - S0 "Docker": the second run asserts from S0 on that `.encryption_key` sits beside the DB on `/config` and that a
    secret set through `PUT /api/v1/config` decrypts after the container is recreated. From S3 it also asserts
    `secrets.keyring`.
  - The job prepares the volume as a user must (`docker run --rm -v cfg:/config <image-base> chown 1000:1000
    /config`, or `--user` plus a pre-created directory). The README's compose notes say the same.
  - Add `TestStateDirFallbackMatchesHEAD` and the one-resolver test (OPS5-13) to 09 S2 "Done when", run inside the
    image as well as unit tests.

### OPS5-8 — MINOR — No job can tell that a PR is "a stage's flip PR", so `extraction-gates`' red-nightly rule cannot be implemented

- **Spec:** 09 §9.2.1 gating table (`extraction-gates` "fails a stage's flip PR while the latest nightly
  `compat-rehearsal` on `main` is red"); S0 CI table row `compat-rehearsal`.
- **Evidence:** a flip PR is defined by intent ("exactly one flip PR makes it truth and is recorded in the ledger"),
  and no file gives a machine-readable marker. The nearest is the stage-visibility gate's `shipped_stage:` marker,
  "set by each flip PR" (09 S0 table). It is also unstated which run counts when no nightly has run since the last
  merge.
- **Fix:** define a flip PR as "a PR whose diff changes the `shipped_stage:` marker". `extraction-gates` reads the
  conclusion of the most recent `compat-rehearsal` run on `main` (via `gh run list --workflow … --branch main
  --limit 1`) and fails when it is not `success` or is older than 36 h. Add a test of the rule to
  `scripts/tests/test_run_gates.py`.

### OPS5-9 — MINOR — The `/metrics` series-name done-checks curl without a credential, which gets 401 on any install with auth on

- **Spec:** 09 S1 "Done when" ("the sorted series-name set of `curl -s <abo-url>/metrics` equals the S0 build's");
  09 S8 "Done when" ("Every books series name in `curl -s <abo-url>/metrics` …").
- **Evidence:**
  - With `enable_auth` on, `/metrics` requires a credential (`internal/server/server_lifecycle.go:1742-1747`).
  - The comparison as written compares two 401 bodies and passes trivially.
- **Fix:** use `curl -sS -H "Authorization: Bearer <redacted-token>" <abo-url>/metrics | grep -v '^#' | sed -E
  's/[{ ].*//' | sort -u`, the same command at S1, S2 r3 and S8, and state that an empty result fails.

### OPS5-10 — MINOR — The S0 done-check "`make rollback` … HEAD → S0" cannot be run: the recipe copies `.prev` over the current binary and leaves `.prev` unchanged

- **Spec:** 09 S0 "Done when" ("`make rollback` on staging (§9.3), run as the deploy user, S0 → HEAD and HEAD → S0
  passes the guard").
- **Evidence:**
  - `make rollback` runs `cp $(DEPLOY_BIN) $(DEPLOY_BIN).rolled-back && cp $(DEPLOY_BIN).prev $(DEPLOY_BIN)`
    (`Makefile:912-914`).
  - After S0 → HEAD, `.prev` is still HEAD. A second run copies HEAD over HEAD, and the guard compares HEAD with
    itself.
  - The way back to S0 is the operator's `deploy` recipe (`Makefile.local.example:88-110`), which has its own
    restore probe (09 S1) but no reader check.
- **Fix:** "`make rollback` S0 → HEAD passes the guard; then the `deploy` recipe HEAD → S0 passes its restore probe
  and the S0 build boots on the store HEAD wrote". The same wording applies wherever "both directions" is meant.

### OPS5-11 — MINOR — Staging: no stage delivers the provisioning script, and a restored production copy without the production key refuses to boot

- **Spec:** 09 §9.3 "Staging" (`scripts/staging/provision.sh`, "a store seeded from the production-size restored
  copy"); 09 §9.3 boot budget ("on one production-size restored copy"); S0 and S8 done-checks that run on staging.
- **Evidence:**
  - No stage's "Moves" or "Deploy" row lists `scripts/staging/provision.sh`, yet S0's done-check needs it.
  - A restored copy holds encrypted settings rows. On a host without that install's `.encryption_key`,
    `guardAgainstKeyRegeneration` refuses to start unless `ABK_ALLOW_ENCRYPTION_KEY_REGEN` is set
    (`cmd/root.go:760-791`).
  - Copying the production key to a disposable host spreads credentials. The spec chooses neither option.
- **Fix:**
  - S0 "Deploy" delivers `scripts/staging/provision.sh`.
  - The script boots the copy once with `ABK_ALLOW_ENCRYPTION_KEY_REGEN=1`, which drops the production secrets on
    staging (desirable), and seeds test secrets through the API.
  - `scripts/boot_budget.py` documents the same preparation.
  - State that no production key or keyring export is ever copied to staging.

### OPS5-12 — MINOR — The "one complete list" of rollback costs and consequences (§9.2.1) still misses three consequences that the revised stages introduce

- **Spec:** 09 §9.2.1 table ("the R-test asserts exactly these rows"); 09 §9.2.3 rows `log.JournalStore` ("new
  journal rows" in books' extension family) and integration instances ("instances with no legacy form live in core's
  `c/integrations/instances/`"); §9.2.1 W rule ("days run by a non-recording build … do not count").
- **Evidence:** consequences that are not in the table:
  - **S5:** system-log records written on S5+ live in books' extension family, which an older build never reads, so
    the older build's Activity system view shows none of them.
  - **S7:** an integration instance created natively on S7+ (a second webhook, mail target or download client) does
    not exist on the older build. Events that happen while rolled back are never queued for it; row 7 covers only
    deliveries already pending in `c/outbox/`.
  - **S2b+:** every day spent on a pre-S2b build delays every surface contract's W clock.
- **Fix:** add three rows to the table, each with its stage and release-note text: "System-log entries written on
  this release are not shown after a rollback; they reappear after the upgrade" (S5); "Integrations created on this
  release do nothing after a rollback; events during the rollback are not delivered to them" (S7); "Time spent on a
  rollback to a release older than S2b does not count toward removing legacy names" (S2b). Extend the R-test
  accordingly.

### OPS5-13 — MINOR — "One resolver from S2" turns a database path given only in YAML into a boot failure where HEAD fell back; it is not recorded as a behaviour change, and the test has two names

- **Spec:** 05-config §1.7 "One resolver from S2" ("A database path given only in the YAML file with an uncreatable
  default therefore gets the boot error"); 05-config tests `TestStateDir_OneResolverYAMLDBPath`; 06 tests
  `TestStateDirOneResolver`; 09 S2 "Done when" (lists only `TestStateDirFallbackMatchesHEAD`); C15 ("keeps HEAD's
  fallback").
- **Evidence:**
  - HEAD's fallback uses `AppConfig.DatabasePath`, which includes the `--config` YAML file
    (`internal/config/state_dir.go:143-153`).
  - An install that gives `database_path` only in YAML, with an uncreatable default (any non-root, non-systemd
    host), boots at HEAD and refuses at S2.
  - The file path itself is a pre-boot input (`--config`, `<APP>_CONFIG`). Reading one key from that file in step 0
    does not conflict with "a config file that sets `core.paths.state_dir` fails boot".
- **Fix:**
  - In `ResolveStateDir` step 3, take `dbPath` from flags, env **and** the operator YAML file, so behaviour matches
    HEAD.
  - Otherwise, list the case under 09 §9.2.6-style recorded changes and in S2's release note.
  - In both cases: one test name in 05-config and 06, and add it to 09 S2 "Done when".

### OPS5-14 — MINOR — S2 r1 and r2 build the App metrics provider while ABO's façade exporter is still installed, and neither release has a `/metrics` check

- **Spec:** 09 S2 "Releases" (r1 composition root; r3 logging and the install move); 09 §9.4.1 (telemetry install
  code deleted at **r3**); 05-metrics §7 step 1 (`app.New` runs `metrics.New` with `Options.PromRegisterer`); 06
  ("ABO passes `prometheus.DefaultRegisterer` from S2"); 09 S2 "Done when" (series check only "at S2 r3 … equals S2
  r2's").
- **Evidence:**
  - At r1 the kernel already creates instruments (`mediacore_kernel_boot_phase_seconds`, "emitted from S2", 09
    §9.3).
  - If ABO passes the default registerer at r1 while the S1 façade's exporter is still registered there, two OTel
    exporters export to one registry. Their `target_info`/scope series can collide at gather time, and promhttp's
    default error handling then fails the whole scrape.
  - If ABO passes nil, the kernel's series are invisible until r3.
  - The text does not choose, and r1/r2 compare nothing.
- **Fix:**
  - Say that `app.Options.PromRegisterer` is set in the **same release** that deletes the façade's exporter (r3),
    and is nil (private, unserved) in r1 and r2. Or move both into r1.
  - Add to r1 and r2 "Done when": the S1 series-name set ⊆ the scrape, and the scrape answers 200 (OPS5-9 command).

### OPS5-15 — MINOR — The floor release's distinct exit code 78 is not used by any supervisor setting, so systemd restarts the refusal until `StartLimitBurst` and Docker restarts it forever

- **Spec:** 09 §9.2.2 ("The boot refusal exits with a distinct code (78)"); 09 §9.8 row "Store below the release's
  `min_upgrade_from`".
- **Evidence:**
  - The unit has `Restart=on-failure` and `StartLimitBurst=5`/60 s (`deploy/audiobook-organizer.service:81-112`).
  - Compose has `restart: unless-stopped` (`docker-compose.yml:23`), and the plist has `KeepAlive` true
    (`deploy/com.audiobook-organizer.plist:51-52`).
  - Each restart re-runs the guard and writes the same refusal. The unit ends `failed` for a start-limit reason that
    hides the real one.
- **Fix:**
  - The S2 unit change (or the floor release) adds `RestartPreventExitStatus=78` to the committed unit.
  - The plist uses `KeepAlive` → `SuccessfulExit=false` plus the release note.
  - The floor release's note says that Docker users see the line in `docker logs` and must stop the container.
  - Add a `deploy-wait-ready.sh` case: exit status 78 is reported as "install the stepping-stone release first",
    not as a crash loop.

### OPS5-16 — MINOR — S0's own rollback depends on owner sudoers actions that nothing verifies before S0 is deployed

- **Spec:** 09 §9.2.2 "Owner actions, all in S0" ("without the first, every rollback refuses"); 09 S0 "Deploy"
  (`deploy-preflight.sh` warns only about `DEPLOY_STATE_DIR`).
- **Evidence:**
  - The deploy recipe overwrites `.prev` before anything checks the host
    (`Makefile.local.example:96-98`).
  - If S0 is deployed before the three sudoers lines exist, the first `make rollback` refuses with
    `cannot_determine`. That is fail-closed, but it is found at the moment a rollback is needed.
  - While no level is raised, `.storage-format` = 1 already proves an all-zero level vector, and its sudoers line
    exists since HEAD (`docs/system/deploy-and-gpu-ops.md:208-218`).
- **Fix:**
  - `deploy-preflight.sh` runs `ssh $(DEPLOY_HOST) sudo -n -l` and refuses an S0+ deploy unless the
    `.storage-levels`, `.storage-applied` and restore-probe lines are listed.
  - `scripts/storage_format_guard.py` treats an unreadable `.storage-levels` together with `.storage-format` = 1 as
    "all owners at 0" (logged). It still refuses on the restore probe.
  - Add both cases to `scripts/test_storage_format_guard.py`.
