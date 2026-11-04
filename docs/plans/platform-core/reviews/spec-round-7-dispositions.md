<!-- file: docs/plans/platform-core/reviews/spec-round-7-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: d57981e4-bb8b-424e-b255-e196133bc579 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7 — dispositions

Inputs: `spec-round-7-{correctness,data-migration,frontend,modules-build,ops-rollback,security,simplicity}.md`.
Totals: **96 findings — 1 BLOCKER, 31 MAJOR, 64 MINOR.** All accepted; 0 rejected.
Distinct blockers per round: 20 → 10 → 9 → 6 → 5 → 2 → 1.

## Round-7 resolutions (binding)

- **R7-1 Operator tightening survives a rollback.** When the effective `core.auth.mode` is `required` and the legacy row
  stores `enable_auth: false`, the legacy config store writes `enable_auth: true` at P4d step 1, as an audited core write
  (`core.config.legacy_narrowed`). This is the one exception to "stored values of locked keys are kept", and it only ever
  narrows an older build. The same rule applies to `core.http.basic_gate.enabled`, which is written `true` only when a
  stored password exists. A release note and a §9.2.1 row cover it (removing `ENABLE_AUTH=true` afterwards does not turn
  auth off), as does `TestRollback_OperatorEnabledAuthStaysOn`. A stored value equal to the default still beats a
  `BelowStored` legacy value, and "staged as an unset" writes the default explicitly (SEC7-1, SEC7-2).
- **R7-2 Factory reset only stages; nothing destructive runs before the restart.**
  - From S2b until S4 r2, books' gin handler, behind HEAD's chain unchanged, calls
    `legacy.Maintenance.StageFactoryReset`. This is a narrow handle handed to the module that declares legacy claims as
    `module.StorageHandles.Maintenance`.
  - There is no core route before S4 r2. At S4 r2 the core route lands in a new `internal/storagehttp` (L6, imported only
    by `kernel`), and the legacy path becomes its `Alias`.
  - HEAD's side effects (gap-fill config file reset, Open Library data, and the library `RootDir` wipe) run in a books
    post-reset hook. The hook runs only after the reset journal reads `applied`, and it is stated as not undoable.
  - Whether to keep the `RootDir` wipe is owner question **Q-K** in 14. The recommendation is to keep HEAD parity with a
    release-note line.
  - The live blob is never written with defaults.
  - `reset_pending` joins `CheckReader`'s tokens, so `make rollback`, `deploy` and the updater refuse
    (`ROLLBACK_IGNORE_RESET=1` overrides).
  - The intent records the live Pebble open stamp, and is rejected when that stamp moved.
  - A staged restore and a staged reset are mutually exclusive: staging either refuses while the other is pending, and P0a
    states the precedence.
  - An empty store is exempt from the upgrade-floor check. Its fresh-install batch records every books legacy step
    1..`LegacyCap` as applied, and books' versioned migrations are numbered from `LegacyCap+1`.
  - (COR7-1, COR7-9, SEC7-4, SEC7-5, DATA7-6, DATA7-7, DATA7-17, OPS7-11.)
- **R7-3 Run-store write checking is per guard row.**
  - `CheckedWrites` applies only to the guard rows: `opv3:run:`, `opv2:op:`, and `opv3:meta:counts`.
  - Every other run family is `Serialized` only, and its writes are legal only in a batch that carries the run's guard
    `Expect`. Taskstore's archtest enforces this.
  - `DeleteRange` is refused in a `CheckedWrites` claim, and allowed in a `Serialized`-only one.
  - `Transition` and `Insert` on a conflict re-read the run. They recommit under `storage.RetryOnConflict` while
    `want` (state, attempt, fence) still matches, and return `ErrRunConflict` only when it does not.
  - Native counts derive from the state index (no `c/tasks/count/`).
  - The drop predicate is HEAD's closed-ledger rule: non-terminal under the same `Attempt`/`FenceEpoch`. Only a bare
    progress tick is dropped on any state change.
  - `legacy.Extension.Delete` takes `ctx` and `seen` and stages a conditional delete.
  - (DATA7-1, DATA7-2, DATA7-3, DATA7-8.)
- **R7-4 No false "replaced store" verdicts.**
  - A restore boot writes `c/storage/boot` and `storage.boot.json` in its `marker_written` batch, before journal
    `applied`.
  - The journal component never rewrites the committed-head anchor while a restore journal is between `swapped` and
    `applied`, and step x restores the saved `backup.location.json`.
  - The "user rows but no cut-off" signal is deleted; 05-backup (2)(a) already covers its case. 05-backup (2) is the
    one classifier.
  - A new `kernel.OpenOffline` gives guard-aware offline read-write opens. It promotes its own open stamp and sidecar
    identity at a clean close. Core's offline commands and every ABO subcommand that writes from S2b use it;
    `diagnostics query` opens read-only.
  - Checkpoint restore follows DATA7-12's three rules:
    - an open checkpoint is finished to `applied` without hooks;
    - `CheckReader` passes a `KindCheckpoint` journal for an admitting candidate;
    - a failing checkpoint restore boot does not swap back.
  - The restore boot's second auth/role construction, and the record type that `restoreswap` reads, follow COR7-8 and
    COR7-10.
  - (DATA7-4, DATA7-5, DATA7-10, DATA7-11, DATA7-12, COR7-8, COR7-10.)
- **R7-5 The S4 r2 upgrade signs no one out.** `DeleteLegacySignIns` runs only when the session cut-off record exists
  and predates `Owed.Since`. The boot that writes the cut-off deletes nothing. `TestFirstR2BootKeepsLegacySessions` covers
  it (DATA7-9).
- **R7-6 One staging procedure, measurable and inert.**
  - 09 §9.3 is the only procedure; 05-backup cites it.
  - Egress is denied by firewall, with ingress (ssh, API port) allowed, installed before the first boot.
  - Schedules run, while integrations are disabled and every legacy and native integration URL (webhooks included) is
    cleared, at every stage.
  - Every boot of a staging copy is egress-limited. S8's updater check uses a local fake signed-release server.
  - The scrub is the ABO subcommand `staging-scrub`, with `scripts/staging/secret_fields.txt` committed at S0 and
    checked against the generated list from S3.
  - Provisioning accepts only an archive restore and refuses a `config.yaml` beside the copy.
  - The integration switch is keyed on the boot, not on the copy's age.
  - (OPS7-2, OPS7-3, OPS7-4, OPS7-7, SEC7-6, SEC7-10.)
- **R7-7 Mirror tests live in the owner's external test package.**
  - The owner's `export_test.go` exports test-only aliases.
  - `legacy_mirror_test.go` in `package <owner>_test` asserts both directions; 03 §3.3 states this once.
  - An owner that receives a legacy-only value with no SDK interface types the field `any`
    (`secrets.Options.LegacyStore`, `library.Options.LegacySource`). It asserts its mirror at construction and fails the
    boot with `ErrLegacyStoreType` on a mismatch, so R6-3's freedom holds.
  - (SIMP7-1, COR7-2, SIMP7-4.)
- **R7-8 A file whose ABO imports have no import-path-only replacement is a re-implementation, not a move.**
  - The registry engine, `childop`, `opsmetrics`, `opsv3` and the state-using `opstest` files get §8.6 rows and generated
    counts. Core's executor is written fresh, and the ABO files stay as opsbridge until T8 (no `origins.txt` line).
  - ABO legacy formats stay ABO:
    - v1 checkpoint helpers go to `internal/books/contract`;
    - `BulkMetadataFetchParams` goes to `internal/server`;
    - `legacy_op_status.go`/`legacy_backfill.go` stay in opsbridge;
    - the activity mirror becomes a books subscriber.
  - `maintenance_window_last_run` is taskstore's `ClockStore` for the maintenance-window kind, with HEAD's date codec.
  - The generator fails on any core-bound file with such an import and no §8.6 row.
  - (MOD7-1, MOD7-2, MOD7-3, MOD7-11.)

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR7-1 | MAJOR | Factory reset has no working path from S2b to S4 r2, and its core route has no owner or stage | Accepted (resolution R7-2) | See resolution. |
| COR7-2 | MINOR | The prescribed in-package `export_test.go` assertion against `legacy` is an import cycle | Accepted (resolution R7-7) | See resolution. |
| COR7-3 | MINOR | `legacy.ConfigOpener` and `legacy.ConfigSecrets` cannot follow the mirror rule they are stated under | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-4 | MINOR | P2 cannot decide a routed store's `legacy` vs `legacy+native` backend: `Covering` is detected only at P4c | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-5 | MINOR | 12 deletes a family's `legacy` identifiers in the cutover release; 06 and 05-storage keep them through it | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-6 | MINOR | 03's one list of store interfaces omits the fourth `tasks.Stores` member (stand-down markers) | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-7 | MINOR | The extension sub-keyspace segment `<store>` is a stored format with no normative mapping | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-8 | MINOR | A restore boot calls the auth and role constructors a second time, over a store for which no handle type exists | Accepted (resolution R7-4) | See resolution. |
| COR7-9 | MINOR | P0a has no precedence between a staged restore and a staged factory reset, and nothing refuses while a reset journal is in flight | Accepted (resolution R7-2) | See resolution. |
| COR7-10 | MINOR | `core/internal/restoreswap` and storage's P2 read a record type that lives in L4 `backup/journal` | Accepted (resolution R7-4) | See resolution. |
| COR7-11 | MINOR | 04's move table dates `seed.go` "S4" with no release, while the stage plan moves it in the r2 PR | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-12 | MINOR | 04's S3 row omits `legacy`'s S3 members | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| COR7-13 | MINOR | COR6-4 residue: the seed test's row still describes the round-5 test | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| DATA7-1 | MAJOR | DATA6-1 half-fixed: `CheckedWrites` is per key, the run store's guard is per run, and `DeleteRange` is undefined | Accepted (resolution R7-3) | See resolution. |
| DATA7-2 | MAJOR | DATA6-1 half-fixed: the global census row and the progress-carrying op row make unrelated writes conflict, and `Transition`'s conflict rule is missing | Accepted (resolution R7-3) | See resolution. |
| DATA7-3 | MAJOR | The drop predicate refuses ledger commits and row marks while a run is `stopping`, which HEAD accepts | Accepted (resolution R7-3) | See resolution. |
| DATA7-4 | MAJOR | DATA6-2 half-fixed: the restore exemption ends at `applied`, the lineage is rewritten at Run (3b) | Accepted (resolution R7-4) | See resolution. |
| DATA7-5 | MAJOR | DATA6-3 half-fixed: the committed-head anchor is rewritten before the hooks of a restore boot | Accepted (resolution R7-4) | See resolution. |
| DATA7-6 | MAJOR | Factory reset: HEAD's side effects are unplaced, some run before the restart, and the undo claim is false | Accepted (resolution R7-2) | See resolution. |
| DATA7-7 | MAJOR | The upgrade-floor check precedes the fresh-install decision; §5S.3.15 relies on a level where the floor checks a version | Accepted (resolution R7-2) | See resolution. |
| DATA7-8 | MAJOR | R6-1's keyspace rule makes every `legacy.Extension.Delete` a refused blind write | Accepted (resolution R7-3) | See resolution. |
| DATA7-9 | MAJOR | The first S4 r2 boot always owes `auth`, so the new `DeleteLegacySignIns` wipes every legacy session and open invite at the upgrade | Accepted (resolution R7-5) | See resolution. |
| DATA7-10 | MAJOR | The "missing cut-off" replaced-store signal has a crash window, and the one classifier does not contain it | Accepted (resolution R7-4) | See resolution. |
| DATA7-11 | MAJOR | Every read-write open outside a serve boot is a foreign open; no guard-aware offline open exists | Accepted (resolution R7-4) | See resolution. |
| DATA7-12 | MAJOR | The checkpoint-restore recovery path contradicts `CheckReader` and is undone by the failing build | Accepted (resolution R7-4) | See resolution. |
| DATA7-13 | MINOR | Fresh-install one-shots are recorded applied without HEAD's markers | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| DATA7-14 | MINOR | `backup.location.json` has three writers and no single-writer rule | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| DATA7-15 | MINOR | The blind-write ≥ 14-day gate is missing from 09's S4 r2, S5 and S7 done-checks | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| DATA7-16 | MINOR | DATA6-4 residue: the pre-S8 archive is still ABO's, so its restore is foreign with nil coverage, and P′ starts only at S8 | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| DATA7-17 | MINOR | Factory-reset bookkeeping: owners disagree, the lineage verdict is misstated, the keyring rewrite has no trigger | Accepted (resolution R7-2) | See resolution. |
| DATA7-18 | MINOR | Name and phase drift | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SEC7-1 | BLOCKER | A rollback turns operator-enabled auth back off | Accepted (resolution R7-1) | See resolution. |
| SEC7-2 | MAJOR | Equal-to-default stored values lose to `BelowStored` legacy names | Accepted (resolution R7-1) | See resolution. |
| SEC7-3 | MAJOR | An env-supplied AI key follows a URL that a non-admin stored | Accepted | Reviewer fix 1 (withhold operator-sourced secrets when the URL is not operator-sourced; `degraded` `operator_secret_withheld`). Fix 2 is not applied. |
| SEC7-4 | MAJOR | The core factory-reset route exists before core auth does | Accepted (resolution R7-2) | See resolution. |
| SEC7-5 | MAJOR | Factory reset's in-request side effects run before a reset that may be rejected | Accepted (resolution R7-2) | See resolution. |
| SEC7-6 | MAJOR | R6-7 half-fixed: the staging copy is inert only for `cur`'s first boot | Accepted (resolution R7-6) | See resolution. |
| SEC7-7 | MINOR | Stale `scopes_source: legacy` records have no status rule | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SEC7-8 | MINOR | Pre-swap restrictions carry only revocations of keys | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SEC7-9 | MINOR | 09 P3 keeps the deleted predicate | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SEC7-10 | MINOR | Two staging scrubs, and three gaps in the procedure | Accepted (resolution R7-6) | See resolution. |
| SEC7-11 | MINOR | Early fix 5 is not tracked where 05-authn says it is | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SEC7-12 | MINOR | Two S4 r2 health tokens have no carrier until S8 | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SEC7-13 | MINOR | Wording | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| OPS7-1 | MAJOR | OPS6-3 half-fixed: the seeded contract test named as every stage's series check cannot show the aliases, cannot see `mediacore_*` families, and cannot seed core-internal emitters | Accepted | Fix 1 (the contract test scrapes the served `Provider.Handler()` through `coretest.MetricsProvider`, with `mediacore_` in scope from S2 r3). |
| OPS7-2 | MAJOR | The staging host boots with `--network none`, which cannot publish the API port and blocks ssh, so the `make rollback`, `deploy` and wait-script done-checks that must run there cannot be measured | Accepted (resolution R7-6) | See resolution. |
| OPS7-3 | MAJOR | The scrub turns off what the S5 and S8 done-checks measure: S5 needs all 38 schedules to fire on staging, and S8 needs auto-update with egress to a release source. 09 and 05-backup also disagree on what the scrub disables | Accepted (resolution R7-6) | See resolution. |
| OPS7-4 | MAJOR | The offline scrub has no executable and no secret list before S3, and a DB-directory copy brings back the deleted keys through `<db dir>/config.yaml` | Accepted (resolution R7-6) | See resolution. |
| OPS7-5 | MAJOR | S8 metric-alias boot: the two books process gauges still exist when the S8 PR adds their aliases, so `AddAliases` fails with `ErrAliasConflict` and the S8 build does not boot | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| OPS7-6 | MINOR | The rehearsal gate sits on S6–S8 deletion PRs, while the PRs that write each stage's new state merge earlier and can reach production through the 10-RC auto-promotion. "Never runs half a stage" is false for S2b and S6–S8 | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| OPS7-7 | MINOR | The scrub's integration switch depends on the copy's age, not on the binaries that boot it, and production credential verifiers stay on staging | Accepted (resolution R7-6) | See resolution. |
| OPS7-8 | MINOR | The `shipped_stage:` values `S2r1`–`S2r3` are not stage points of 04 §2.1, and the marker's initial value and its values at S2b, S4 and S5 are unstated | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| OPS7-9 | MINOR | The flip-PR gate reads a rehearsal run against the latest production tag, while the rollback target on the reference host is a main build. The override happens at deploy time, after the gate | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| OPS7-10 | MINOR | Floor-check residue of OPS6-1: a missing `.storage-applied` refuses even an empty floor, which blocks the S0 done-check's HEAD → S0 deploy; §9.6 row 33 is stale | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| OPS7-11 | MINOR | From S2b a staged factory reset is pending state like a staged restore, but `make rollback`, the `deploy` recipe and the S0 sudoers probe look only under `restore/` | Accepted (resolution R7-2) | See resolution. |
| OPS7-12 | MINOR | The S3 done-check compares an effective-config golden built from the production environment, YAML and blob, but the spec does not say where that fixture lives or how it is redacted | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-1 | MAJOR | MOD6-1 residue: the S5 engine "move" is a rewrite, and the spec classifies it three different ways. | Accepted (resolution R7-8) | See resolution. |
| MOD7-2 | MAJOR | More ABO legacy formats travel into core inside moved files. No store carries them, and (h) cannot see them. | Accepted (resolution R7-8) | See resolution. |
| MOD7-3 | MAJOR | A persisted ABO setting key in a re-implemented scheduler file is neither listed nor given a store (MOD6-4 class). | Accepted (resolution R7-8) | See resolution. |
| MOD7-4 | MAJOR | The S4 consolidation creates a wrong-way edge that the ABO guard rejects. | Accepted | Fix 1 (the MOD4-18 rule): `internal/books` injects the owner check into `internal/plugins/maintenance` as a func. 04 §2.4 states `internal/cfaccess`'s `layerOf` (5). |
| MOD7-5 | MINOR | As specified, the cross-check fails on HEAD. | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-6 | MINOR | Hello's dist is on two lists, and the root one cannot pass. | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-7 | MINOR | MOD6-7 is half-fixed: the two texts still differ. | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-8 | MINOR | (a) `pebble_store_ops_v2_timeline_race_test.go` (`//go:build race`) moves, according to the `!race` twin's row and the name table, but has n | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-9 | MINOR | Note 3 contradicts the table that gate E checks | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-10 | MINOR | Two naming rules in one gate | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-11 | MINOR | Stale after R6-2: `internal/operations/state/state.go` stays in ABO | Accepted (resolution R7-8) | See resolution. |
| MOD7-12 | MINOR | The text is garbled and stale | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-13 | MINOR | Two internal core paths named only in 05-tasks | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-14 | MINOR | Both texts call themselves "the one rule", but they differ | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| MOD7-15 | MINOR | The type-checking half of two required gates (gate E and (h)) is a module that no target builds, vets or tests | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-1 | MAJOR | Untracked `core/web/node_modules` makes every build tree dirty from UI-0 | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-2 | MINOR | `PREV_REF` from a `+dirty` commit is not a git ref | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-3 | MINOR | 09 S0 still puts the UI build ID into a `define` | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-4 | MINOR | `MEDIACORE_CORE_VERSION` is undefined wherever shell sources are compiled by the app | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-5 | MINOR | `checkBuild`'s output and its other callers are unspecified | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-6 | MINOR | `BUILD_ID=dev` is accepted | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-7 | MINOR | The build meta no longer marks a shell bundle | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-8 | MINOR | Preset `appId` and theme-key hygiene | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-9 | MINOR | 09 S9 is stale against FE6-2/FE6-6 | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-10 | MINOR | FE6-5 half-fixed: the fragment-cell test still has two names | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-11 | MINOR | The icons peer: 07 says none, 13 says ui-kit, and the code imports icons in the moved files | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-12 | MINOR | ui-kit-bound components reach the ABO API and the shell; the build orders differ | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-13 | MINOR | `ui_build_id` names two different values | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| FE7-14 | MINOR | 09 S0 omits the `buildid` leaf from Moves and `TestVersionJSONFields` from Done-when | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-1 | MAJOR | The mirror assertion cannot live in the owner's `export_test.go` | Accepted (resolution R7-7) | See resolution. |
| SIMP7-2 | MAJOR | Root suspension and confirmation are built at S7 with no consumer | Accepted | Root suspension and confirmation move to 05-library's deferred-build table; a legacy-served root refuses `ReadShares`. |
| SIMP7-3 | MINOR | Three versions of the "stays in the SDK" list, none complete | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-4 | MAJOR | One SDK field exposes a mirror, and one input is unspecified | Accepted (resolution R7-7) | See resolution. |
| SIMP7-5 | MINOR | Capabilities are detected twice, and the kernel's record has no consumer | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-6 | MINOR | `storage_extension_suspended` duplicates the owners' tokens and has no producer | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-7 | MINOR | `storage.LegacyMigration` and `Spec.Legacy`: a contradictory reason, then dead SDK | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-8 | MINOR | The naming rule is applied inconsistently | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-9 | MINOR | Wrong section cited for the config basis deferral | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-10 | MINOR | 03's one list omits `tasks.StandDownStore` | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
| SIMP7-11 | MINOR | Generated and stale text residues | Accepted | Reviewer's concrete fix applied as written, unless a round-7 resolution supersedes it. |
