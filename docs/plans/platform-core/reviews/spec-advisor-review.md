<!-- file: docs/plans/platform-core/reviews/spec-advisor-review.md -->
<!-- version: 1.0.0 -->
<!-- guid: f44c9caa-7823-4f87-9fb5-782e521ebf38 -->
<!-- last-edited: 2026-11-04 -->

# Spec advisor review — platform core (`mediacore`)

Reviewer: the independent advisor (phase 4). Scope: the whole spec under `docs/specs/platform-core/`, judged as one
design rather than through one lens. Evidence commit `748fd787a`; every code anchor below is `path:line @748fd787a`
(the checkout's code is identical to that commit; only docs differ).

**Method.** I read in full the README, 01, 02, 03 §3.1–3.3, 09 §9.0–9.4 (every stage table), 14 and the round-8
dispositions. I read the relevant parts of 04 §2.1, 05-storage (D-S table, §5S.3.4, §5S.11), 05-config (§1.3.6,
§1.10–1.12, §2.2), 05-http-api (§4.8–4.9, §8.2–8.3, §12), 05-authn §4.4, 05-tasks (route table, T0+T1 row),
05-updater (§5U.1–5U.5) and 06 §6.5.1, and skimmed the rest. I checked the load-bearing claims against code and ran
one experiment: I registered the HEAD route dump (`raw/09-http-api.md` §3.1–3.2, 572 routes) in a go1.27.0
`net/http.ServeMux` using 05-http-api §4.8's own translation rule (appendix A). I did not re-litigate D1–D9.

## Verdict

**READY WITH CHANGES.**

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | ADV-1 |
| MAJOR | 6 | ADV-2 … ADV-7 |
| MINOR | 5 | ADV-8 … ADV-12 |

The core strategy is sound and I would keep it: one stored format per family during S0–S10, store interfaces whose
legacy implementations are books code, a permanent legacy mount, a deferred one-way cutover, and an honest stepping
stone. Under that strategy every stage I examined is rollback-safe by binary swap, with the costs listed in 09 §9.2.1.

The one BLOCKER is an internally consistent claim that is wrong about the code. HEAD's gin routes cannot all be
registered as ServeMux patterns, so S2 r1 cannot boot as specified. The fix is small and local.

The larger problem is proportionality (ADV-3). Most of the mechanism that rounds 4–8 added, and most of what round 8
still found, comes from one choice: the extraction also hardens how credentials, scopes and secrets are stored. That
is a choice, not a requirement of D1–D9. If the owner keeps it, the spec is ready once the BLOCKER and MAJOR fixes are
in. If the owner adopts ADV-S1, revise 09 S3/S4, 05-authn, 05-rbac and 05-config §1.12.2 first, then write the plan.

## Q1 — Does the stage sequence get ABO onto core, shippable and rollback-safe?

Yes, with two stages that fail as written and three that are conditional.

| Stage | Shippable as written | Rollback by binary swap | Note |
|---|---|---|---|
| S0 | yes | yes | It changes user-visible behaviour (ADV-10). |
| S1 | yes | yes | Restore becomes restart-bound. |
| S2 | **no** (r1 does not boot) | yes | ADV-1. Fine once the collision rule lands. |
| S2b | yes | yes | The riskiest concentration of mechanism (ADV-4). The shadow → strict soak is not enforced (ADV-5). |
| UI-0 | yes | yes | — |
| S3 | **conditional** | yes, with the row-1 outage of moved secrets | Legacy env/YAML decoding parity is unspecified (ADV-2). |
| S4 | yes | yes, with rows 2–5, 13, 18–20 | The r1 → r2 → r3 soaks are not enforced (ADV-5). |
| S5 | **no** at T0+T1 | yes | ADV-1 recurs as a cross-owner conflict. The fresh executor is the highest-risk code in the plan (guidance §G1). |
| S6, S7 | yes | yes | — |
| S8 | conditional | yes | The updater done-check depends on a release-asset contract that 05-updater §5U.2 marks UNKNOWN (ADV-S5). |
| S9, S10 | yes | yes | — |

What makes the sequence work: the code moves while the stored formats stay the same, and the books module keeps
writing every family. I checked the parts most likely to break that and found them sound:

- The Pebble format pin: `internal/database/storage_format.go:60-78` and `pebble_store.go:614-697`, with
  05-storage `TestAuxiliaryDBUsesPinnedFormat`.
- Backups that carry `c/` and `m/` keys, because HEAD backs up through a Pebble checkpoint (`internal/backup/backup.go:428-529`).
- The worker confinement "nothing else" test (`internal/server/middleware/auth.go:268-297`).
- The `["none"]` key scope, which never reaches HEAD's intersect, because HEAD accepts only `abk_`
  (05-authn §4.4; `middleware/auth.go:76-99`).
- The swap's one rejection rule (`staged_by_build`), which makes an older build reject a reset intent it did not stage.

## Q2 — Findings

### ADV-1 — BLOCKER — HEAD's routes cannot all become ServeMux patterns (S2 r1 cannot boot; S5 collides again)

- **Spec.** The mount rule and its checks:
  - 05-http-api §4.8 translates gin `:name` → `{name}` and turns a registration panic into `ErrPatternConflict`.
  - §8.2 registers "each manifest route … as its own `ServeMux` pattern" (lines 1112–1116, and the `Routes` field
    comment, line 1058).
  - The §8.1 S2 (H0) row (line 1032) and 09 S2 "HTTP" (line 1051) say the same.
  - The error table (line 890) makes a pattern conflict a boot error, closed.
  - `TestLegacyManifest_RegistersAsMuxPatterns` asserts "every gin route is a mux pattern, no conflict" (line 1310).
  - At S5 the core alias `GET /runs/{id}` replaces v1 `operations/v2/:id` (05-tasks route table; 09 S5 "Endpoints").
- **Code.** All of the default-build routes below are `library.view`:
  - `internal/server/wire_operations_routes.go:27` `GET /api/v1/operations/v2/:id` conflicts with
    `internal/server/wire_library_routes.go:48` (`…/operations/:id/activity`),
    `internal/server/wire_operations_routes.go:103` (`…/:id/result`), `:110` (`…/:id/changes`) and
    `internal/server/server_lifecycle.go:1952` (`…/:id/results`).
  - In the ABS build, `internal/server/handlers/abs/handler.go:871`
    (`GET /api/me/item/:id/remove-from-continue-listening`) conflicts with `:885`
    (`GET /api/me/item/listening-sessions/:id`). The comment at `:882-884` records that gin resolves this pair by
    routing the static sibling first, a precedence ServeMux does not have.
- **Experiment (appendix A).** Of the 572 routes, 5 pairs conflict (4 in the default build, 1 in the ABS build). Go
  reports, for example, "both match some paths, like `/api/v1/operations/v2/activity`. But neither is more specific
  than the other." Every other route registers.
- **Consequence.**
  - S2 r1's `Build` fails at boot by the spec's own rule.
  - An implementer would have to improvise: drop a route (breaking the AudioBooth ABS client the comment protects),
    rename one, or fall back to a catch-all pattern. A catch-all voids SEC2-22's guarantee that the route stages 8,
    10 and 13 judged is the route gin runs.
  - At S5 the v2 alias becomes core's while the four `/{id}/…` routes stay books'. That is a cross-owner *conflict*,
    which SEC3-8's alias exemption does not cover: the exemption covers strict specificity, not conflicts.
- **Fix (small).** Add a collision rule to §4.8 and §8.2:
  1. The manifest generator computes the conflict graph.
  2. Each connected set of same-owner conflicting routes is registered as one generalised pattern
     (`GET /api/v1/operations/{p4}/{p5}`, `GET /api/me/item/{p4}/{p5}`), but only when all members carry identical
     `Perm`, `Guards`, `RateClass`, `OwnAuth`, `BasicExempt` and `rbac.LegacyAtom`. Both sets meet this today; any
     other set fails generation.
  3. The post-gin check accepts any member's `c.FullPath()`.
  4. `…/v2/{id}` stays its own pattern, because it is strictly more specific than the generalised one. At S5 it is
     then a ledger-listed core alias over a less specific books pattern, which SEC3-8 already exempts. Moving the four
     routes to core in the T0+T1 PR is the alternative.
  5. Run the test against the real engine (`(*gin.Engine).Routes()` with ABS on and off) from S2 r1, not a fixture.
  6. Add `TestS5_CoreAliasesRegisterBesideLegacy`.

### ADV-2 — MAJOR — S3 drops viper without specifying how legacy env and file values decode

- **Spec.** 05-config §1.10.1 replaces viper with "one decoder (`encoding/json`), exact-case paths" and "~150 LOC of
  file/env parsing". §1.3.6 says object and array keys take JSON. 09 S3 "Releases" says "viper leaves the boot path".
  The S3 done-check compares effective config with HEAD only for the production env and the compose env.
- **Code.**
  - HEAD reads typed values through viper getters: `internal/config/config.go:3346,3355,3358` (`GetBool` for
    `enable_auth`, `enable_rate_limit`, `basic_auth_enabled`) and `:3226,3229,3276,3298` (`GetStringSlice`).
  - Env lookup is prefix-less and case-insensitive (`cmd/root.go:625`).
  - Viper converts through `spf13/cast` v1.10.0. `basic.go:51-52` is `strconv.ParseBool`, and `zz_generated.go:8-10`
    turns a parse error into `false`. `slice.go:94-95` splits a string on whitespace (`strings.Fields`).
- **Hazard.** The new decoder's behaviour on HEAD's spellings is undefined:
  - `ENABLE_AUTH=yes` reads `false` at HEAD (auth off).
  - `BASIC_AUTH_ENABLED=1` or `True` reads `true`.
  - `SUPPORTED_EXTENSIONS=".m4b .mp3"` is a two-element list.
  - Under a JSON or strict decoder each could become a boot error, an unset value or a different value. R6-6 ("a
    value that enables auth") cannot even be evaluated without a decode rule.
  - Compose and systemd users set these names (05-config §1.12.4), so ABO can boot differently on the same host after
    S3, including the auth switches.
- **Fix.**
  - Add a normative rule to §1.3.6: a legacy name decodes with HEAD's per-type semantics (ParseBool spellings,
    whitespace-split slices, Go durations, cast integers), and canonical names keep the strict decoder.
  - A legacy bool that HEAD would silently read as `false` becomes a boot error on `ProtectAdmin` and auth keys
    (fail-closed, with a release note) and `false` elsewhere.
  - Add `TestLegacyEnvDecodeMatchesHEAD`: a generated corpus of every alias × representative spellings, decoded by
    HEAD's `InitConfig` (built from `PREV_REF`) and by core `Load`, diffing effective values. It joins
    `extraction-gates` from S3.

### ADV-3 — MAJOR (systemic) — the extraction also changes how credentials, scopes and secrets are stored, and that drives most of the residual risk

- **Spec.** 11 of the 21 rows of 09 §9.2.1 (rows 1–5, 10, 13, 15, 18–20) exist only because S3, S4 and S7 change
  stored representation or operator-auth precedence while the code moves:
  - hashed native sessions and one-time tokens;
  - `mck_` keys with canonical scopes;
  - namespaced role scopes in extension records;
  - moved plaintext secrets, which leave sentinels;
  - the `Local` grant;
  - R6-6's operator auth-enable exemption, and the R7-1 and R8-4 narrowing writes that keep it across a rollback.
- **Machinery.** Supporting these needs the extension family, `legacy_ref.fp` fingerprints, suspension and
  re-confirmation (`legacy.ExtConfirmer`), the `auth` duty's revoke-after-rollback, the sentinel re-import rule,
  `operator-names.json`, `OperatorSecretOrigin` and the row rule's frozen-snapshot dominance.
- **Footprint.** In the spec files "extension" appears on 499 lines, "sentinel" on 202 and "suspend" on 169.
- **Trend.** The share of finding *titles* in the disposition tables that touch this machinery (keywords: sentinel,
  extension, suspend, secret, session, scope, role, reset, swap, restore, foreign, offline, staging, scrub, operator,
  narrow, rollback, journal) rose from 17 % in round 1 (32/192) to 30 % in round 4 (35/117) and 52 % in round 8
  (42/80).
- **Why it matters.** The loop converged on BLOCKERs by adding mechanisms (R7-1, R8-2, R8-4, R8-5). Each one adds an
  interaction, and the interactions are where rounds 6–8 kept finding defects (ADV-4). None of this hardening is
  needed to put ABO on core, and N15 already designs the one-way, checkpointed, level-guarded step where
  representation changes belong.
- **Fix.** Make the extraction representation-neutral, and move the hardening into post-S10 "hardening cutovers"
  built on N15. The six early fixes stay. ADV-S1 lists what this deletes and trades. If the owner declines, keep the
  spec as it is, but apply ADV-4 with extra rigour.

### ADV-4 — MAJOR — the boot path is now a multi-mechanism state machine with no whole-boot crash model

- **Spec.** 06 §6.5.1 is one boot table that every subsystem extends:
  - phases P−1, P0, P0a, P1–P8;
  - P4d steps 1, 1c, 1(v), 4 and 5;
  - Run steps 2, 3a, 3b and 5.

  It interacts with:
  - the served ledger, owed duties, the foreign-open record, sidecar `pending` identity and the Pebble open stamp
    (05-storage §5S.3.7);
  - the one swap journal with its reset kinds (§5S.3.15; 05-backup §5B.3.4, steps a–g);
  - offline opens that stage owed entries (R8-2);
  - the narrowing writes (R7-1, R8-4).

  The tests are per scenario (`TestRestoreCrashAtEveryJournalStep`, `TestCrashAfterStep9RevokesSessions`, …).
- **Evidence.** The late-round defects are interaction defects at exactly these joints: DATA6-2, DATA7-11, DATA7-12,
  DATA8-3, DATA8-4, DATA8-5, DATA8-6, DATA8-7, SEC8-4, COR8-3, COR8-4 and OPS8-5.
- **Fix.**
  1. Draw one explicit state diagram for each durable state machine (swap journal, served ledger and duties, sidecars,
     open stamp), with the legal transitions, in 05-storage.
  2. Make a **crash-matrix harness** a required S2b test. It uses Pebble `vfs` error injection plus a shim for
     state-directory files, kills the boot at every durable write, rename and fsync from P−1 to Run (3b), then asserts
     the cross-mechanism invariants: no duty lost, the journal resumes or rejects, sidecars agree with the store, and
     both `cur` and `prev` boot.
  3. Every later stage that adds a boot writer extends the matrix in the same PR.

### ADV-5 — MAJOR — production soaks and release boundaries are not enforced, and production releases are cut automatically

- **Spec.** Several stage gates depend on time served in production:
  - "One flip PR per stage release" and "Enforcing releases (OPS8-13)": ships "only after ≥ 14 days of production
    with zero persisted blind-write violations" (09 §9.2.1, lines 183–199; §9.4.1 lines 1225–1236).
  - S2b release 2 (strict) follows "14 days" of shadow mode (line 1066).
  - S4 r1's decode census must be "0 on production" (line 1116).
  - Condition (c) of W is evaluated on the reference production store (line 130).
- **Code.** `.github/workflows/prerelease.yml:59-131` mints an RC on every push and dispatches a production minor at 10
  RCs. Release boundaries therefore follow the merge count. No gate reads production state, and `/parallel-sweep`
  merges autonomously.
- **Hazard.** An agent, or a busy week, merges the next flip or enforcing PR before the previous release has served
  its soak. The strict, enforced or r2 build then reaches installs that never ran the observation release, and nothing
  reports it.
- **Fix.**
  - Keep a committed soak ledger (`ci/soak.txt`, or ledger rows). An owner-run command on the reference host writes
    each entry: release tag, deploy time, and the hash of the zero-violation evidence.
  - `check_stage_visibility.py` (or `extraction-gates`) refuses a PR that advances `shipped_stage:`, enforces a
    `CheckedWrites` flag or ships an rN+1, unless the predecessor's row exists and is at least 14 days old.
  - The plan marks these as owner checkpoints that no agent may satisfy.

### ADV-6 — MAJOR — `staging-scrub` ships in every production binary with no target interlock

- **Spec.** 09 §9.3 step 3 (lines 914–932) says `staging-scrub` deletes sessions, one-time tokens and the encrypted
  secret rows, replaces every password hash, revokes every API key, blanks every plaintext secret field and clears
  every legacy integration URL. It ships in `cmd` from S0 and runs with the production release's binary. From S2b it
  opens through `app.OpenOffline`, which admits that binary on its own store. Only `staging-restore` refuses a
  non-empty target.
- **Hazard.** With the service stopped, `staging-scrub --db <production path>` irreversibly destroys every credential
  and integration setting. The only way back is a restore.
- **Fix.**
  - `staging-restore` writes a staging marker: a sidecar `<db>.staging` plus a `c/storage/staging` key holding the
    archive hash and a nonce.
  - `staging-scrub` refuses any store without a matching marker and also requires `--confirm-destroy-credentials`.
  - Test: scrubbing an unmarked fixture store is refused and writes nothing.

### ADV-7 — MAJOR — no policy for ABO feature work running alongside the extraction

- **Evidence.**
  - ABO merges fast: a production minor every 10 RCs, plus the parallel sessions in CLAUDE.md.
  - S2b re-homes `internal/database`: 103,432 non-test LOC (04, row `internal/database`) and about 850 direct engine
    call sites (`.db.Get|Set|Delete|NewIter|NewBatch|NewIndexedBatch|NewSnapshot|DeleteRange|Apply(` outside tests).
  - S4 and S5 move handler groups.
  - On every PR that touches a root `*.go` file, the stage-visibility gate "fails on any user that §8.2/§8.3 do not
    declare" and each stage PR regenerates those tables (04 §2.1, lines 137–147). Every non-doc PR also runs the
    prev-build tests and `docker-image`.
  - 14 has no throughput or conflict risk.
- **Fix.**
  - Add a K-row and a 09 rule. Each stage gets announced short move windows that freeze the moved packages, and a
    documented rebase-onto-move procedure.
  - Feature PRs do not hand-edit 04's generated tables: a bot PR regenerates them. Alternatively, the gate runs only on
    PRs that touch mapped packages.
  - State the CI-time budget per PR.

### ADV-8 — MINOR — moved secrets have no `config.yaml` copy, yet four files say one is sentinelled

- **Spec.** 02 decision 13, 03 §3.3 `secrets`, 09 §9.2.1 row 1 and 14 Q-E say the first core write replaces "the legacy
  row and the second copy in `config.yaml`". 05-config §2.2's own table lists no file copy for any moved secret.
- **Code.** HEAD's gap-fill file holds only the three *kept* secrets (`internal/config/persistence.go:168-178`, read
  back at `:73-87`). The same file may be the operator's `--config` (comment at `:110-112`), so a literal
  implementation could plant sentinel values in an operator file.
- **Fix.** Delete the clause in all four files, or reword it as "no moved secret has a file copy at HEAD". Add a test
  that no core build writes a key into `<db parent>/config.yaml` that HEAD's `SaveConfigToFile` does not manage.

### ADV-9 — MINOR — 02 N12, 14 K9 and Q-A read D9 as "OSI open source only"; D9 is "nothing paid"

PolyForm Noncommercial 1.0.0 (`LICENSE:1`) costs nothing, so the licence does not conflict with D9. Restate K9 and Q-A
as an optional reuse decision (see the Q-A answer), and drop the "licence mismatch" framing from N12.

### ADV-10 — MINOR — S0 says "no user-visible behaviour change", but S0's own rows contain several

09 S0 "Goal" says nothing changes for users. Yet the updater hotfix makes both unattended apply paths check-only, adds
health `auto_update_paused_until_core_updater` and a Settings banner, and restarts with exit 75. S0 also adds the SPA
`StaleBuildBanner`, `/health.version`, `--version-json` and the `/api/<ns>/v<n>` redirect exclusion. List these and
give S0 release-note lines.

### ADV-11 — MINOR — literal counts from `748fd787a` are used as test oracles for a months-long program

Examples: "572 routes × 4 seed roles" (S4 done-check), "233 defs" and "38 definitions" (S5), and the 36 permission-less
routes. Main will drift during the extraction. Every count in a done-check should be generated at the stage's base
commit, as the alias counts already are; mark the literals informative.

### ADV-12 — MINOR — normative text is interleaved with review provenance

Sentences routinely carry three to eight finding IDs, and some table cells exceed 3,000 characters (03 §3.3 `config`
row; 09 S0 "Done when"). This is accurate, but an agent cannot tell rule from history. Before the plan is written,
produce a provenance-free **normative digest per stage** of at most 300 lines, generated or hand-cut, that cites the
full files for detail (guidance §G4).

## Q3 — Proportionality: simplification candidates

The spec is about 38k lines for a goal that amounts to "move platform code behind interfaces without changing stored
formats". Its own rules (one format, store interfaces, the permanent mount, the deferred cutover) are cheap. The cost
sits in mechanisms that protect optional changes. Each candidate below is compatible with D1–D9 and loses no safety
property the extraction needs.

**What to keep:** the one-format rule, the store interfaces and the `legacy` package boundary, the six early fixes,
staged restore, the per-owner guard and sidecars, the stepping stone, the permanent mount, the shadow → strict claims,
and the prev-build one-format tests on every PR.

| ID | Delete or defer | Risk | Trade |
|---|---|---|---|
| **ADV-S1** | **Representation-neutral extraction; hardening moves to cutovers.** Books' legacy stores keep HEAD's representation through S10: raw `sess:` rows and HEAD's one-time tokens, `abk_` keys with legacy atoms, roles holding legacy atoms only, plaintext blob secrets (the four already-encrypted ones stay as they are), and HEAD's auth-enable precedence. Delete for ABO: the extension family, `legacy_ref.fp`, suspension and re-confirm, the `auth` revoke-after-rollback duty, the sentinel and re-import rule, `["none"]`, `mck_`, `operator-names.json`, the narrowing writes and `OperatorSecretOrigin`. Rollback rows 1–5, 10, 13, 15 and 18–21 become zero-cost or vanish. Native stores (hello, TV, Movies) keep the full design. | Hardening arrives later. Raw session tokens and plaintext blob secrets stay at rest exactly as at HEAD (no regression, no improvement) until the identity and secrets cutovers. ABO roles cannot hold core-only scopes until then (keep `/metrics` `AnyAuthenticated` for ABO). | S3 and S4 shrink to façades. Rollback across them is free. Most of 05-authn §4, 05-rbac §6.3 and 05-config §1.12.2 shrink. The hardening ships later as one-way cutovers with a floor, through N15's existing machinery. D7 still holds: scopes are namespaced in the API and native for new apps. |
| **ADV-S2** | **Do not convert ABO's legacy writers to storage-enforced `CheckedWrites` during the extraction.** Keep HEAD's lock discipline (keyed mutexes, `apiKeyMu` with early fix 6) and the existing single-writer AST ratchet. Apply `Expect` and `Serialized` checks to native keyspaces and to rows that core newly writes. This deletes the four enforcing releases (S3 r2, S4 r3, S5 rE, S7 r2), their 14-day soaks, the `op=blind` violation record and the per-guard-row rules for legacy families (R6-1, R7-3, DATA8-2). | A lost update on a legacy row that HEAD's locks miss is not detected at runtime. That is HEAD's behaviour today, and Pebble has one writer process. | About eight weeks of calendar and a large rule and test surface, against parity on legacy rows. |
| **ADV-S3** | **Drop the predictive stage-visibility machinery and the move/re-implementation taxonomy.** Delete the `tools/stagecheck` module, the per-stage-point evaluation in `check_stage_visibility.py`, the regeneration of 04 §8.2/§8.3 on every PR, `origins.txt`, R1(c)'s move rule (R7-8, R8-6; MOD8-1 to MOD8-3) and `archtest.DeferredMembersAbsent`. Keep Go's `internal/` rule, `TestCoreImportsOnlyCore`, the layering guard and the `SDKOnly`/kernel guard, plus a plain `shipped_stage:` marker if ADV-S7 is not taken. | A later stage's import problem surfaces when that stage's PR builds, not earlier. Split history comes from git rename detection rather than a curated list. | Removes a whole tool chain, the review hot spot of rounds 6–8 for modules and builds, and the tax on feature PRs (ADV-7). |
| ADV-S4 | **Keep ABO's metric names.** Core already takes the namespace as a constructor option (09 S1). Delete the renames at S2b, S5, S7 and S8, the gather-time aliases, the metric ledger rows, the dashboard switches, rollback row 16, and the alias-collision boot failures (OPS7-5, OPS8-1). | Cross-app dashboards need per-app queries. | Uniform `mediacore_*` naming is cosmetic, and nothing in this repo scrapes `/metrics` (09 §9.1). |
| ADV-S5 | **S8 updater: move it, do not rebuild it.** Move the S0-hotfixed updater into `core/updater`, parameterised by release source: exit 75, the `.prev` slot, semver comparison, the level and floor checks, and check-only unattended mode. Defer signed manifests, the boot-attempt counter and automatic rollback, the binary-layout migration of the committed unit, and the real-RC `--updater-check`. | No unattended self-update on bare-metal installs. It already cannot work on the committed unit, which installs into a root-owned directory with `NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:41-43,88,122`). The updater refuses containers, and the HEAD asset contract is UNKNOWN (05-updater §5U.2). | The heaviest S8 done-check and the layout change go, against a feature no shipped layout can use. |
| ADV-S6 | **Shrink staging.** Keep the offline `staging-restore` and `staging-scrub` (with ADV-6's interlock) and a container run with `--network none` for the boot-budget A/B and the R-test. Drop the ssh-reachable, nftables-filtered host, the jump containers and the deploy-user `make rollback` rehearsal on staging. Test that rehearsal with S0's fake-ssh harness instead. | No end-to-end rehearsal of the operator's ssh and sudo recipe on a production-size store. | Owner infrastructure that agents cannot provision goes away, and so does a class of OPS findings. |
| ADV-S7 | **Simplify the red-rehearsal rule.** `compat-rehearsal` becomes a required job that runs *on* each flip PR. Drop the "latest nightly < 36 h" rule, `ci/state_writers.txt` and `check_rollback_rows.py`. The per-PR prev-build tests stay. | A state-writing non-flip PR merges without a full rehearsal. | Fewer moving parts in the merge gate. |
| ADV-S8 | **Replace W's persisted usage gate** with dates, release notes and the owner checking the process-local counter on the reference host. Delete the `c/compat/` usage records, presence-per-boot, `recording_continuous_since`, the `usage` duty, P8 evaluation, `GET /api/core/v1/compat/usage` and row 14. Keep the ledger, `TestNoContractBeforeSunset` and the 410-for-one-minor step. | An alias still used by an unmeasured client is removed. Only the reference store was ever measured anyway. | One consumer of the served-ledger duty machinery goes (it helps ADV-4). |
| ADV-S9 | **Deferred-build contracts become non-normative sketches.** About 3k lines in 10, 05-indexer, 08 and the deferred parts of 05-download, 05-ai and 05-notify become design notes, not SDK contracts. | Less up-front proof that core fits TV and Movies. | Avoids freezing guesses, and D5's "proof depth" still holds. |

Top three by value: **ADV-S1**, **ADV-S2**, **ADV-S3**.

## Q4 — Implementation-plan guidance

### G1 — Risks the plan must handle

1. **ADV-1 lands first.** The collision rule and its real-engine test are a precondition of S2 r1, and the S5
   variant is a precondition of T0+T1.
2. **Owner checkpoints are tasks no agent can complete.** They include the 14-day soaks, the production census at
   S4 r1, the P4/P8 reads, reference-host deploys, provisioning the staging host, the signed RCs for S8, and reading
   `reusable-release.yml` (MOD6-5) before UI-0 and S8 are briefed. Model them as blocking checkpoint tasks (ADV-5).
3. **Auto-promotion.** S3–S5 must land dark behind their flip, and no partial dark set may become live. Each
   PR-by-PR PR in S2b and S6–S8 must be shippable on its own.
4. **Every commit builds** (rebase merges). Keep import-path-only move PRs separate from behaviour PRs.
5. **Drift.** Re-verify anchors and counts at each stage's base commit (ADV-11), and follow the concurrent-work policy
   (ADV-7).
6. **Use HEAD as the test oracle.** The differential tests are the strongest signal the spec offers: effective config
   (with ADV-2's corpus), `TestLegacyRoleReachUnchanged`, the wire goldens, `TestScheduleParity_AllDefinitions`, and
   the R-test against the real previous binary. Brief them before the code they guard.
7. **The S5 executor is fresh code over 233 definitions.** Port HEAD's resume, cancel and shutdown suites to run
   against both engines. Keep 05-tasks' 1:1 concurrency mapping (`Fixed(n)`, or `Sequential` for zero). Any
   parallelisation is a separate PR under CLAUDE.md's concurrency rules, never part of T0.
8. **Crash matrix at S2b** (ADV-4). It is the integration test for every later boot writer.
9. **Destructive paths** (scrub, factory reset, restore) are tested only on fixtures, never against a store an agent
   did not create.
10. **Budget CI time.** `docker-image` plus the prev-build tests run on every non-doc PR.

### G2 — Suggested wave boundaries

Do not overlap waves that touch `internal/database` or `internal/server` (CLAUDE.md).

| Wave | Content | Tier |
|---|---|---|
| W0 | The six early fixes (05-authn §11.1), the toolchain bump | Sonnet; Opus review for fix 6's race |
| W1 | S0-CI: required checks, `changes` classifier, Docker and web build paths, nested module skeleton, layering guard, embed check | Sonnet; Haiku for path filters |
| W2 | S0-state: guard, sidecars, served ledger, foreign-open record, updater hotfix, deploy scripts, staging tools with ADV-6 | Opus for guard and ledger; Sonnet for scripts |
| W3 | S1: leaf moves; staged restore | Haiku for import-path-only moves; Opus for restore |
| W4 | S2 r1 composition root and mount (with ADV-1) → r2 perimeter → r3 logging and telemetry | Opus → Sonnet with Opus review → Sonnet |
| W5a–f | S2b, split: (a) core engine and the `LegacyRange` adapter, about 850 mechanical call-site edits; (b) the claim table and shadow mode; (c) kernel phases P0–P4 with the crash matrix; (d) the swap, reset staging and `OpenOffline`; (e) `compat`; (f) metric renames, unless ADV-S4 is taken | Sonnet with an Opus-written adapter for (a); Opus for (b)–(d); Sonnet for (e) |
| W6 | UI-0 (react-router 8, generated client), in parallel with W5 and W7 | Sonnet |
| W7 | S3: the legacy config store and façade; generated alias tables; ADV-2's corpus test | Opus; Sonnet for tables |
| W8 | S4 r1 dark → r2 flip → r3 | Opus for the bridge, the row rule and the stores; Sonnet for routes and UI |
| W9 | S5 T0+T1 executor and bridge → T2–T7 batches (`/parallel-sweep`) → T8 floor | Opus → Sonnet or Haiku per batch → Opus |
| W10–W14 | S6, S7, S8, S9 (frontend-heavy), S10 | Sonnet with Opus for outbox, routed stores and backup |

### G3 — Areas too ambiguous or too coupled to brief as they stand

| Area | Why | What to do before briefing |
|---|---|---|
| 06 §6.5.1 boot table | Every stage edits it, and its correctness is cross-mechanism. | One owner per stage, the state diagrams and the crash matrix (ADV-4). |
| 05-config §1.12.2, the legacy config store | It layers `BelowStored`, `SafeLegacyValue`, `NarrowStored`, sentinels, `operator-names.json` and three files. | Write the precedence matrix as a test first, and brief it as one Opus task. ADV-S1 removes most of it. |
| 05-storage §5S.3.11, legacy capabilities | They are detected through unexported mirrors (R7-7) across owners. | Land a skeleton of `legacy` and the mirror-test template at S2, then brief owners against it. |
| 05-rbac §6.3 row rule | The frozen-snapshot dominance is algorithmic and has no worked example. | Add a worked table: atom → reach → admitted atoms. |
| S2b as one stage | Too many mechanisms ship live PR by PR. | Split it as in W5. |
| 09 §9.3 staging | Owner infrastructure. | Owner task, or ADV-S6. |
| Prose density | Provenance is mixed into rules (ADV-12). | A per-stage normative digest. |

### G4 — Brief-writing convention

Each brief cites the digest section, the owning 05 file, the code anchors re-verified at its base commit, the
done-check tests it must make green, and the tests it must not break. A rule's provenance (finding IDs) belongs in the
digest's footnotes, not in the brief.

## Q5 — Owner questions

| Q | Recommendation in 14 | Advisor |
|---|---|---|
| Q-A | Relicense core as Apache-2.0 | **Agree as an option, not with the premise.** D9 is "nothing paid", and PolyForm Noncommercial costs nothing (ADV-9). Apache-2.0 is right if third-party reuse of core is wanted. The local clone's history is shallow, so confirm authorship over the full history before relicensing. |
| Q-B | Public core repository | **Agree.** Public Actions minutes, the proxy and npm publishing are all free. |
| Q-C | Delete the 15 never-run v2 cron strings | **Agree.** Enabling them is an untested behaviour change. |
| Q-F | Deploy-file names that are not sole sources: keep until 1.0 | **Agree.** It is cheap, and operators copy deploy files. |
| Q-G | SC1 and SC5 thresholds as report-only | **Agree.** Calibrate SC5 after S2b, when core holds real code. |
| Q-H | `github.com/falkcorp/mediacore` | **Agree.** |
| Q-I | (a) required checks, no bypass | **Agree, with the D4 reconciliation written down.** Require status checks on `main`, require no reviews (single maintainer), set `enforce_admins: true`, and allow only rebase merges (merge queue or `gh pr merge --rebase --auto`). D4's "admin rebase merge" remains the merge act, never a bypass. `/parallel-sweep` and the Quick Fix flow wait for checks. State the per-PR CI time. |
| Q-K | (a) HEAD parity: the factory reset wipes `RootDir` and the Open Library data | **Agree for the legacy alias, with one change on the native route.** The legacy `POST /system/factory-reset` alias keeps HEAD's wipe. The native route deletes library files only with an explicit `delete_library_files: true` (the alias passes it), and its confirmation names `RootDir` and the file count. The wipe runs after a restart, is irreversible and sits in a hook that never fails boot, so it should not be a default side effect of a new API. |
| Q-D (lead) | No legacy-readable session copy; hash sessions now | **Disagree under ADV-S1:** keep HEAD's `sess:` rows in place through S10 and hash them at the identity cutover. If ADV-S1 is declined: agree. |
| Q-E (lead) | Move plaintext secrets on first write | **Disagree under ADV-S1:** defer to a secrets cutover. Nothing regresses, and it removes rollback row 1's outage. If ADV-S1 is declined: agree, and apply ADV-8. |
| Q-J (lead) | A factory reset keeps the keyring | **Agree.** |

## Appendix A — ServeMux experiment (reproducible)

1. Take every `| <METHOD> | \`<path>\` |` row of `raw/09-http-api.md` §3.1 (497 rows) and §3.2 (75 rows).
2. Translate each gin path by 05-http-api §4.8: `:name` → `{name}`, `*name` → `{name...}`, and `/` → `/{$}`.
3. For each pair of patterns, register both in a fresh `http.NewServeMux()` under go1.27.0 and record whether the
   second registration panics.

Result: 5 conflicting pairs, listed below. No other pair conflicts.

| Pattern A | Pattern B | Build |
|---|---|---|
| `GET /api/v1/operations/{id}/activity` | `GET /api/v1/operations/v2/{id}` | default |
| `GET /api/v1/operations/{id}/changes` | `GET /api/v1/operations/v2/{id}` | default |
| `GET /api/v1/operations/{id}/result` | `GET /api/v1/operations/v2/{id}` | default |
| `GET /api/v1/operations/{id}/results` | `GET /api/v1/operations/v2/{id}` | default |
| `GET /api/me/item/{id}/remove-from-continue-listening` | `GET /api/me/item/listening-sessions/{id}` | ABS on |

The two sides of each default-build pair carry the same requirement (`library.view`), and both ABS routes run
`ABSRequireAuth` with `loginCeiling` and no route-level permission (`raw/09-http-api.md` rows for these paths). That is
why ADV-1's same-requirement merge rule is enough.
