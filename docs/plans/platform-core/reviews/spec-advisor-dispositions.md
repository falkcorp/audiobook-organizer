<!-- file: docs/plans/platform-core/reviews/spec-advisor-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: d1629c3d-8ce8-449d-b026-0e4d72bc6633 -->
<!-- last-edited: 2026-11-04 -->

# Spec advisor review — dispositions

Input: `spec-advisor-review.md` (phase 4; verdict READY WITH CHANGES; 1 BLOCKER, 6 MAJOR, 5 MINOR, 9 simplification
candidates). **Every finding is accepted, and every simplification candidate is adopted** (ADV-S6 partly: see A-12).
0 rejected.

**Why the lead adopts the simplifications without waiting for the owner.**
- The prompt pre-authorises design decisions in this operation, and this decision touches only documents.
- ADV-3 is supported by the review record itself: findings about storage-representation machinery grew from 17 % of
  round 1 to 52 % of round 8.
- ADV-S1 removes that machinery without regressing anything HEAD protects today. HEAD's stored formats stay exactly as
  they are, and the six early fixes still ship.
- The hardening is not lost: it moves to one-way, floored cutovers (N15), where representation changes belong.
- The owner can still ask for the hardening back during the extraction. That reverts A-2 to A-4 and restores the
  round-8 design, which is preserved in git history at the commit before this file.

## Binding resolutions

- **A-1 Route collision rule (ADV-1, BLOCKER).**
  - In 05-http-api §4.8/§8.2, the manifest generator computes the ServeMux conflict graph. Each connected set of
    same-owner conflicting routes registers as one generalised pattern, but only when every member carries the same
    `Perm`, `Guards`, `RateClass`, `OwnAuth`, `BasicExempt` and `rbac.LegacyAtom`. Any other set fails generation.
  - The post-gin check accepts the `c.FullPath()` of any member.
  - `…/operations/v2/{id}` stays its own, strictly more specific pattern.
  - From S2 r1, the test runs against the real engine (`(*gin.Engine).Routes()`, with ABS on and off).
  - Add `TestS5_CoreAliasesRegisterBesideLegacy`.
- **A-2 Representation-neutral extraction (ADV-3, ADV-S1).**
  - **What stays as at HEAD, through S10.** Books' legacy stores keep HEAD's stored representation:
    - raw `sess:` rows and HEAD's one-time tokens;
    - `abk_` keys holding legacy atoms;
    - roles holding legacy atoms only;
    - plaintext blob secrets (the four already-encrypted ones stay as they are);
    - HEAD's auth-enable precedence (stored value wins, as at HEAD).
  - **Deleted for ABO:**
    - the books extension family `m/books/ext/`, `legacy_ref.fp`, Widening fields, suspension and re-confirmation
      (`legacy.ExtConfirmer`);
    - the `auth` revoke-after-rollback duty, and `DeleteLegacySignIns` with its cut-off record;
    - moved-secret sentinels and the re-import rule;
    - `["none"]`, and `mck_` keys for ABO;
    - `operator-names.json` and its advisory tokens;
    - the narrowing writes (`NarrowStored`, R7-1/R8-4);
    - `OperatorSecretOrigin` and `ErrOperatorSecretBound` for legacy-backed instances;
    - R6-6's operator auth-enable exemption.
  - **What is kept:**
    - The legacy stores implement the core store interfaces.
    - Core-namespaced scopes are an API-level mapping over legacy atoms (`rbac.LegacyAtom`), so D7 holds.
    - The routes stay as at HEAD.
    - Native stores (hello, TV, Movies) keep the full native design: hashed sessions, `mck_` keys, native scopes and
      encrypted secrets.
    - The six early fixes ship unchanged.
    - SEC7-3's protection reduces to HEAD's behaviour on legacy-backed instances, which is no regression. Native
      instances keep origin binding.
  - **Rollback rows.** In 09 §9.2.1, rows that existed only for the representation changes become zero-cost or are
    deleted. 09 re-derives the complete list.
  - **Hardening cutovers.** The hardening becomes named post-S10 cutovers on N15's one-way, floored machinery:
    identity (hash sessions and tokens, `mck_`, native scopes), secrets (encrypt plaintext), and auth precedence. 12
    and 14 list them. 14's Q-D and Q-E become "deferred to the identity and secrets cutovers".
- **A-3 Legacy rows keep HEAD's lock discipline (ADV-S2).**
  - No storage-enforced `CheckedWrites` on ABO legacy families during the extraction. HEAD's keyed mutexes,
    `apiKeyMu` (with early fix 6) and the single-writer AST ratchet stay.
  - `Expect`/`Serialized` checks apply to native keyspaces and to rows that core newly writes.
  - Deleted: the enforcing releases (S3 r2, S4 r3, S5 rE, S7 r2), their 14-day soaks, the `op=blind` violation record,
    and the per-guard-row rules for legacy families (R6-1, R7-3, DATA8-2 as they apply to legacy rows).
  - The shadow → strict claim rollout for **claims** (key-ownership) stays.
- **A-4 No predictive stage-visibility machinery (ADV-S3).**
  - Delete: the `tools/stagecheck` module, the per-stage-point evaluation in `check_stage_visibility.py`,
    regenerating 04 §8.2/§8.3 on every PR, `origins.txt`, the move/re-implementation taxonomy as a CI rule (R7-8, R8-6
    and MOD8-1 to MOD8-3 as gates), and `archtest.DeferredMembersAbsent`.
  - Keep: Go's `internal/` rule, `TestCoreImportsOnlyCore`, `archtest.TestNoAppRequire`, the layering guard, the
    `SDKOnly`/kernel guard, R1(c)'s key-name check (`TestCoreHoldsNoAppKeyNames`), and a plain `shipped_stage:` marker.
  - 04 stays the informative per-package map: rows, destinations, stages and LOC. Its §8.6 re-implementation rows stay
    as planning information.
  - 12's history split uses git rename detection.
- **A-5 ABO keeps its metric names (ADV-S4).** Core takes the metric namespace as an `app.Options` value, and ABO
  passes its own.
  - Delete the renames at S2b, S5, S7 and S8, gather-time aliases, metric ledger rows, dashboard switches, rollback
    row 16, and the alias-collision boot rules (OPS7-5 and OPS8-1 become moot).
- **A-6 S8 moves the updater, it does not rebuild it (ADV-S5).**
  - Move the S0-hotfixed updater into `core/updater`, parameterised by release source. It keeps exit 75, the `.prev`
    slot, semver comparison, level/floor checks and check-only unattended mode.
  - Deferred-build: signed manifests, the boot-attempt counter, automatic rollback, the unit's binary-layout migration,
    and the real-RC `--updater-check` done-check.
- **A-7 Legacy env and file decoding parity (ADV-2).**
  - In 05-config §1.3.6, legacy names decode with HEAD's per-type semantics: `strconv.ParseBool` spellings,
    whitespace-split slices, Go durations, `cast` integers. Canonical names stay strict.
  - A legacy bool on an auth or `ProtectAdmin` key that HEAD would silently read as `false` is a boot error
    (fail-closed, with a release note). Elsewhere it is `false`.
  - `TestLegacyEnvDecodeMatchesHEAD` is a generated alias × spelling corpus, compared against HEAD's `InitConfig` built
    from `PREV_REF`, and it joins `extraction-gates` from S3.
- **A-8 Whole-boot crash model (ADV-4).**
  - 05-storage gets one state diagram per durable state machine: the swap journal, the served ledger and duties, the
    sidecars and the open stamp.
  - The crash-matrix harness is a required S2b test. It uses Pebble `vfs` error injection plus a state-dir shim, kills
    at every durable write, rename and fsync from P−1 to Run (3b), and asserts the cross-mechanism invariants.
  - Every later boot writer extends the matrix in its own PR.
- **A-9 Soaks are enforced as owner checkpoints (ADV-5).**
  - A committed soak ledger, `ci/soak.txt`, is written by an owner-run command on the reference host. Each entry holds
    the tag, the deploy time and an evidence hash.
  - The gate refuses a PR that advances a stage marker or ships the next release of a multi-release stage until the
    predecessor's row exists and is at least 14 days old.
  - No agent may satisfy one.
- **A-10 Staging scrub interlock (ADV-6).**
  - `staging-restore` writes a staging marker: a sidecar `<db>.staging` plus `c/storage/staging`, holding the archive
    hash and a nonce.
  - `staging-scrub` refuses any store without a matching marker and requires `--confirm-destroy-credentials`.
  - Test: an unmarked store is refused, and nothing is written.
- **A-11 Concurrent feature work (ADV-7).**
  - A K-row in 14 and a 09 rule cover announced short move windows that freeze the moved packages.
  - 09 documents a rebase-onto-move procedure and a per-PR CI-time budget.
  - With A-4, feature PRs never regenerate 04.
- **A-12 Smaller staging (ADV-S6, partly).**
  - Keep the offline `staging-restore`/`staging-scrub` (with A-10), and a container boot with `--network none` for the
    boot-budget A/B and the R-test.
  - Drop the ssh-reachable, nftables-filtered host, the jump container and the deploy-user rehearsal on staging; S0's
    fake-ssh harness tests the `make rollback` recipe.
  - Kept from R7-6/R8-3: integrations disabled, schedules run.
- **A-13 Simpler rehearsal rule (ADV-S7).** `compat-rehearsal` is a required job that runs on each flip PR.
  - Drop the "nightly < 36 h" rule, `ci/state_writers.txt` and `check_rollback_rows.py`.
  - The per-PR prev-build tests stay.
- **A-14 W's usage gate becomes dates plus owner check (ADV-S8).**
  - Delete the `c/compat/` usage records, presence-per-boot, `recording_continuous_since`, the `usage` duty, P8
    evaluation, `GET /api/core/v1/compat/usage` and row 14.
  - Keep the ledger, `TestNoContractBeforeSunset` and the 410-for-one-minor step.
- **A-15 Deferred-build contracts are non-normative sketches (ADV-S9).** 10, 05-indexer, 08 and the deferred parts of
  05-download, 05-ai and 05-notify are labelled "design sketch (non-normative)", and the SDK freezes none of it. D5's
  proof depth holds: 10 still shows that the frozen contracts fit TV and Movies.
- **A-16 Minors.**
  - ADV-8: delete the "second copy in `config.yaml`" clause, and add a test that core writes only keys HEAD's
    `SaveConfigToFile` manages.
  - ADV-9: restate K9, Q-A and N12. D9 is "nothing paid", so relicensing is an optional reuse decision.
  - ADV-10: list S0's user-visible changes, with release-note lines.
  - ADV-11: literal counts in done-checks are generated at each stage's base commit; mark the literals informative.
  - ADV-12: provenance-free per-stage normative digests are produced as the first artefact of Phase 6, under
    `docs/plans/platform-core/digests/`.
- **A-17 Q-K (advisor amendment).** The legacy `POST /system/factory-reset` alias keeps HEAD's wipe. The native route
  deletes library files only with `delete_library_files: true` (the alias passes it), and its confirmation names
  `RootDir` and the file count.

## Findings

| ID | Sev | Finding | Disposition |
|---|---|---|---|
| ADV-1 | BLOCKER | HEAD's routes cannot all become ServeMux patterns | Accepted (A-1) |
| ADV-2 | MAJOR | S3 drops viper without legacy decode semantics | Accepted (A-7) |
| ADV-3 | MAJOR | Representation changes drive most residual risk | Accepted (A-2) |
| ADV-4 | MAJOR | No whole-boot crash model | Accepted (A-8) |
| ADV-5 | MAJOR | Soaks and release boundaries not enforced | Accepted (A-9) |
| ADV-6 | MAJOR | `staging-scrub` has no target interlock | Accepted (A-10) |
| ADV-7 | MAJOR | No concurrent-feature-work policy | Accepted (A-11) |
| ADV-8 | MINOR | Moved secrets have no `config.yaml` copy | Accepted (A-16) |
| ADV-9 | MINOR | D9 misread as OSI-only | Accepted (A-16) |
| ADV-10 | MINOR | S0 is not behaviour-neutral | Accepted (A-16) |
| ADV-11 | MINOR | Literal counts as oracles | Accepted (A-16) |
| ADV-12 | MINOR | Rules interleaved with provenance | Accepted (A-16, Phase 6) |
| ADV-S1 | — | Representation-neutral extraction | Adopted (A-2) |
| ADV-S2 | — | No CheckedWrites on legacy rows | Adopted (A-3) |
| ADV-S3 | — | Drop predictive stage machinery | Adopted (A-4) |
| ADV-S4 | — | Keep ABO metric names | Adopted (A-5) |
| ADV-S5 | — | Move the updater | Adopted (A-6) |
| ADV-S6 | — | Shrink staging | Adopted in part (A-12) |
| ADV-S7 | — | Simpler rehearsal rule | Adopted (A-13) |
| ADV-S8 | — | Dates instead of persisted usage gate | Adopted (A-14) |
| ADV-S9 | — | Deferred contracts as sketches | Adopted (A-15) |
