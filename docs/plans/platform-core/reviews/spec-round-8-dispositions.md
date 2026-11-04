<!-- file: docs/plans/platform-core/reviews/spec-round-8-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 113c9d53-fabe-4b4a-a685-422b0da96105 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8 — dispositions

Inputs: `spec-round-8-{correctness,data-migration,frontend,modules-build,ops-rollback,security,simplicity}.md`.
Totals: **80 findings — 0 BLOCKER, 31 MAJOR, 49 MINOR.** All accepted; 0 rejected.
Distinct blockers per round: 20 → 10 → 9 → 6 → 5 → 2 → 1 → **0**. Round 8 meets the loop's exit rule (zero open BLOCKERs,
every MAJOR dispositioned). The resolutions below are applied before Phase 4, whose fresh reviewer checks them.

## Round-8 resolutions (binding)

- **R8-1 A factory reset is a kind of the one swap.**
  - **One swap mechanism.** A reset is `KindReset` of `restoreswap` (SIMP8-1):
    - one intent slot, `restore/pending.json` with `kind`, created atomically;
    - one sentinel, `storage.ErrSwapPending`;
    - one rejection rule for every kind (`expires_at`, `staged_by_build`, a moved open stamp);
    - one journal;
    - one token pair, `restore_pending`/`restore_inflight`, with one override.

    `reset_pending`, `reset_inflight`, `reset/<id>/` and `ROLLBACK_IGNORE_RESET` are deleted.
  - **Two reset kinds.** `ResetStore` serves HEAD's `POST /system/reset`: store and settings only, no file effects.
    `ResetFactory` adds the post-reset hook. From S2b, both legacy routes stage through `legacy.Maintenance` behind HEAD's
    chain. From S4 r2, both are `Alias`es of the `internal/storagehttp` route (COR8-1).
  - **The layer-legal hook-argument capture of COR8-2.** It is followed by the `restoreswap` → `storage` direction of
    COR8-5.
  - **Set-aside and failure rule.**
    - Every target is pre-validated at staging. A dangerous `RootDir` answers 400 and nothing is staged.
    - Each target is set aside beside itself, on its own filesystem.
    - A `Recreatable` auxiliary DB on another filesystem is handed to the hook for deletion.
    - A failed set-aside moves back what it moved, journals `rejected` and boots the untouched store `degraded`
      (COR8-3, DATA8-1).
  - **The hook never fails boot.** Per-entry errors are logged, audited and reported as `factory_reset_cleanup_incomplete`.
    The hook always records `hook_done`, and only a crash reruns it. Its runtime is bounded and exempt from the 60 s
    start limit. Kept paths are skipped. When no app hook exists, the kernel records `hook_done` itself (COR8-4, OPS8-5).
  - **The reset boot stages HEAD's factory defaults.** It writes the `factoryDefaultConfig` blob (`root_dir` empty,
    `setup_complete` false) in the fresh-install batch, so a stale `ENABLE_AUTH=false` loses as it does after HEAD's
    reset (SEC8-5).
  - **The reset's rollback cost and §9.2.1 rows are dated S2b** (OPS8-6).
- **R8-2 An offline open never launders a foreign open.**
  - `OpenOffline` computes the boot verdicts. When it finds a foreign open, a missing or unreadable record, or a behind
    lineage, its close batch stages the owed entries a serve boot would stage before it promotes.
  - DATA8-4's user list is corrected.
  - An open checkpoint's offline finish follows DATA8-5.
  - (DATA8-3, SEC8-4, DATA8-4, DATA8-5.)
- **R8-3 A staging copy is never staged-restored.**
  - `provision.sh` runs `staging-restore --archive <file> --db <empty path>`, an ABO subcommand from S0. It reads and
    verifies the archive offline, refuses a non-empty target and never writes `config.yaml`.
  - The subcommand runs with the binary of the release that wrote the archive, and so does `staging-scrub`; that binary
    has no pending step on the copy.
  - `test_staging_scrub.py` asserts that the first serve boot finds everything already scrubbed.
  - R7-6 is amended: the scrub clears every **legacy** integration URL. Native instances are covered by the operator
    switch and the firewall, because only S7+ binaries read them and every such staging boot carries the switch.
  - (OPS8-2, SIMP8-2.)
- **R8-4 Operator values across a rollback.**
  - `core.http.basic_gate.enabled` narrows whenever it is `true`. Without a password the older build can read, a hotfixed
    older build refuses every request (fail-closed), and the release note tells the operator to set
    `BASIC_AUTH_PASSWORD` before rolling back below S3 (SEC8-1).
  - A §9.2.1 row covers canonical-only operator names.
  - Every boot from S3 writes `<state_dir>/operator-names.json`, which lists names only. `CheckReader` reports it as the
    advisory token `canonical_only_operator_values`.
  - `core.http.rate_limit.enabled` narrows (SEC8-6).
- **R8-5 Operator secrets bind to origin plus provenance.**
  - A secret is served when every destination field's effective origin equals the operator or adapter-default origin, or
    equals the instance's `OperatorSecretOrigin`.
  - `OperatorSecretOrigin` is set at the first S7 boot from the stored destination, and on any `Put` that passes
    ProtectAdmin or the confirm route (SEC8-2).
  - Until the integrations cutover, a `Put` that changes a destination origin of a legacy-backed instance with an
    operator-sourced secret requires ProtectAdmin. Without it, the `Put` gets 403 `ErrOperatorSecretBound` (SEC8-3).
- **R8-6 What counts as a move.**
  - A file is a move when every ABO import changes by import path alone or by one of 04 §2.2's enumerated mechanical
    rewrites (logger, store interface). 03 R1(c) and R7-8 are reworded to that, so all 21 §8.5 rows keep their
    `origins.txt` lines (MOD8-1).
  - `childop`'s and `opsmetrics`' ABO files stay, as opsbridge, until their last ABO user is gone. `opsmetrics` goes to
    `internal/executor` (MOD8-2).
  - *Split* cells list the tag-union file set, and build-tag twins travel with their partner (`reflink_darwin.go` with
    `reflink.go`; `static_embed.go` named in its cell) (MOD8-3).

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR8-1 | MAJOR | HEAD's second whole-store wipe, `POST /system/reset`, has no path from S2b, but the S2b PR deletes `PebbleStore.Reset` | Accepted (resolution R8-1) | See resolution. |
| COR8-2 | MAJOR | The staging contract that captures the hook arguments has no layer-legal implementation, and the S4 r2 route bypasses it | Accepted (resolution R8-1) | See resolution. |
| COR8-3 | MAJOR | The P0a set-aside renames every target into `<db parent>/.mediacore-reset/<id>/previous/`, but two kinds of target can sit on another filesystem | Accepted (resolution R8-1) | See resolution. |
| COR8-4 | MAJOR | `hook_done` has no owner when an app has no post-reset hook, and the hook's failure rule contradicts the start-hook contract | Accepted (resolution R8-1) | See resolution. |
| COR8-5 | MINOR | COR7-10 half-fixed: `internal/restoreswap` imports `storage`, while `storage.Admin` delegates to it | Accepted (resolution R8-1) | See resolution. |
| COR8-6 | MINOR | The reset/restore refusal sentinels live in L4 `backup` (S8) but are returned by S2b and L1 code, and the S2b–S7 mutual exclusion has no shared lock | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| COR8-7 | MINOR | Who retries `Insert`/`Transition`: 05-storage and the `RunStore` contract say the methods do, 5T.7 says the callers do | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| COR8-8 | MINOR | Three statements about the reset contradict the definitions they cite | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| COR8-9 | MINOR | The reset text has a stale landing row, a signature mismatch and an ambiguous path | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-1 | MAJOR | DATA7-6 residue: a staged factory reset has no failure path | Accepted (resolution R8-1) | See resolution. |
| DATA8-2 | MAJOR | DATA7-1 half-fixed: `Serialized`-only writes still take a stripe per key | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-3 | MAJOR | DATA7-11 half-fixed: `OpenOffline` launders a foreign open | Accepted (resolution R8-2) | See resolution. |
| DATA8-4 | MAJOR | DATA7-11 residue: the `OpenOffline` user list breaks two subcommands and misses two opens | Accepted (resolution R8-2) | See resolution. |
| DATA8-5 | MAJOR | DATA7-12 half-fixed (1): an open checkpoint's offline finish re-creates DATA6-2's false foreign open | Accepted (resolution R8-2) | See resolution. |
| DATA8-6 | MAJOR | DATA7-12 half-fixed (2): rule (3)'s `failed` journal is neither admitted nor resumed | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-7 | MAJOR | DATA7-5 residue: a swap-back restores the location file but not the journal | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-8 | MINOR | R7-3 residues in books' run store | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-9 | MINOR | `c/tasks/idem/` is written blind | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-10 | MINOR | R7-5 residue: a retried first core boot of a pre-S0 store deletes every legacy sign-in | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-11 | MINOR | R7-2 residue: the pre-apply floor checks refuse a host with no store | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-12 | MINOR | Reset journal residues | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| DATA8-13 | MINOR | Drift | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SEC8-1 | MAJOR | The Basic-gate narrowing leaves the gate off in the cases 09 recommends | Accepted (resolution R8-4) | See resolution. |
| SEC8-2 | MAJOR | Withholding by source breaks working HEAD configurations at S7 | Accepted (resolution R8-5) | See resolution. |
| SEC8-3 | MAJOR | The SEC7-3 protection does not survive a rollback below S7 | Accepted (resolution R8-5) | See resolution. |
| SEC8-4 | MAJOR | An offline open erases the evidence of a foreign open | Accepted (resolution R8-2) | See resolution. |
| SEC8-5 | MAJOR | After a core factory reset, a stale `ENABLE_AUTH=false` turns auth off | Accepted (resolution R8-1) | See resolution. |
| SEC8-6 | MAJOR | Canonical-only operator values are lost on a rollback | Accepted (resolution R8-4) | See resolution. |
| SEC8-7 | MAJOR | User merge keeps a role-name check that ignores key scopes | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SEC8-8 | MINOR | How S4 r1 reads `enable_auth`: two texts | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SEC8-9 | MINOR | A failed narrowing commit is silent | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SEC8-10 | MINOR | The instance-scoped test is unspecified for stored secrets | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SEC8-11 | MINOR | The staging scrub cannot run on a copy that `cur` still has to migrate | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SEC8-12 | MINOR | NarrowStored over an absent legacy field | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-1 | MAJOR | OPS7-5's root cause recurs at S5: the five `operations_*` client_golang families stay registered while the T0+T1 PR adds aliases with their names, so `AddAliases` fails closed and the S5 flip build does not boot | Accepted | Same rule as OPS7-5: the T0+T1 PR deletes the five `operations_*` client_golang families in the PR that adds their aliases; `TestS5_FullAliasSetBoots`. |
| OPS8-2 | MAJOR | OPS7-4 half-fixed: "archive restore, then offline scrub before the first boot" cannot be executed from S1 on, so the first boot of a staging copy runs unscrubbed production data | Accepted (resolution R8-3) | See resolution. |
| OPS8-3 | MAJOR | OPS7-3 half-fixed: the S8 updater done-check cannot exercise the shipped S8 binary | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-4 | MAJOR | OPS7-3 residue: the S5 done-check "every one of the 38 schedule definitions fires on the staging store within one interval" cannot pass at HEAD parity | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-5 | MAJOR | A post-reset hook that cannot finish keeps `reset_inflight` forever, so `deploy`, `make rollback` and the updater refuse indefinitely, and the hook retries a library wipe on every boot | Accepted (resolution R8-1) | See resolution. |
| OPS8-6 | MAJOR | The factory-reset rollback cost is dated S3 although it ships at S2b, and the stage tables omit rows 12, 19 and 20, so the S2b release note and the S2b R-test miss it | Accepted (resolution R8-1) | See resolution. |
| OPS8-7 | MINOR | `ROLLBACK_IGNORE_RESET=1` also overrides `reset_inflight`, contradicting "a rollback never lands an older build on a half-moved directory" | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-8 | MINOR | The S0–S7 updater "uses the same probe" as the scripts, but `sudo` cannot run under the unit's `NoNewPrivileges=yes`, and the S2b rewiring of `internal/updater` onto core's `CheckReader` is unstated | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-9 | MINOR | The red-rehearsal rule's mechanical criteria miss two state-writing PRs that §9.2.1 names | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-10 | MINOR | OPS7-1 residue: the contract test's Provider is built "without booting", so the kernel-emitted `mediacore_*` families of the S2 r3 and S2b diffs are never seeded | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-11 | MINOR | The S0 done-check requires `test_staging_scrub.py` cases that need S2b, S6 and S7 builds | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-12 | MINOR | The "graceful restart" that a staged restore or a staged factory reset requests has no exit code, and the committed unit restarts only on failure | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-13 | MINOR | The `CheckedWrites` enforcement releases are unplaced | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| OPS8-14 | MINOR | The R-test does not assert §9.2.1 row 20, and the row's scope disagrees with its "From" | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-1 | MAJOR | R7-8's normative rule and the gate that implements it classify ~20 files differently. | Accepted (resolution R8-6) | See resolution. |
| MOD8-2 | MAJOR | Re-implementing `childop` and `opsmetrics` at S5 deletes two helpers that ABO code keeps using until T2–T7/T8. The spec also gives `opsmetrics` two destinations | Accepted (resolution R8-6) | See resolution. |
| MOD8-3 | MAJOR | *Split* cells list default-tag (linux) files only, but the gate's file set is the tag union, so build-tag twins are left behind in ABO. | Accepted (resolution R8-6) | See resolution. |
| MOD8-4 | MINOR | (h) as specified cannot produce its own `maintenance.go` row, and the generator fails a stale entry. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-5 | MINOR | The *Split* cell sends the whole of `service.go` to `health`, but §8.6 and 05-health keep part of it in books. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-6 | MINOR | MOD7-4's reasoning applies to this edge too, so the cut is required, not optional, and the injected func is incomplete. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-7 | MINOR | Stale figures and one stale sentence after R7-8. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-8 | MINOR | A permanent books subscriber is placed in a path that T8 deletes. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-9 | MINOR | The date codec names no zone for `Get`, and the obvious reading re-runs the window. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-10 | MINOR | Gate E's test half needs exactly the `ForTest` variants that the shared parse drops. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-11 | MINOR | The tolerance compares a pattern that is package-relative and prefixed with a module-relative list entry. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| MOD8-12 | MINOR | Two comment copies of the pin are in neither list. | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-1 | MAJOR | The preset cannot build the core packages it claims to build | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-2 | MAJOR | Case 2 cannot be evaluated by the stated `checkBuild` signature | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-3 | MINOR | `onStaleBuild` loses early detections, and the pre-shell banner has no owner | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-4 | MINOR | FE7-1 residue: only `core/web` is covered, and SC4's check cannot run | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-5 | MINOR | FE7-12 half-fixed: the ui-kit set still reaches ABO code and routes | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-6 | MINOR | `MaintenanceTab` and `ThrottlePanel` are classified twice, oppositely | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-7 | MINOR | FE7-8 residue: the app-ID check has no missing-meta rule or test hook | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-8 | MINOR | 09 S9 omits FE7-8, and neither file orders `UIBundle()` after the preset | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-9 | MINOR | Realtime control vocabulary differs between 07 and 05-realtime | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| FE8-10 | MINOR | Name residue against `web/` and against FE7-5 | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-1 | MAJOR | The factory reset duplicates the restore swap, and the copy has already drifted | Accepted (resolution R8-1) | See resolution. |
| SIMP8-2 | MAJOR | `staging-scrub` cannot run in the order §9.3 gives, and its native-URL clause is not implementable | Accepted (resolution R8-3) | See resolution. |
| SIMP8-3 | MINOR | The kernel's capability assertions are listed three ways | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-4 | MINOR | `legacy.Maintenance` is wider and lives longer than its callers | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-5 | MINOR | `ReadShares` has no S0–S10 consumer, and SIMP7-2's fix guards it with new machinery | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-6 | MINOR | `NarrowStored` is missing from the one "stays in SDK" list, and its predicate is declared twice | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-7 | MINOR | The post-reset hook's carrier: a redundant field, a name off the rule, and the wrong lifetime | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-8 | MINOR | Two public spellings of the swap journal's record types | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-9 | MINOR | `OpenOffline`: one wrong phase range, and a handle that should be nil offline | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
| SIMP8-10 | MINOR | The S8 updater check has two fake-server designs, one of them a staging-only build | Accepted | Reviewer's concrete fix applied as written, unless a round-8 resolution supersedes it. |
