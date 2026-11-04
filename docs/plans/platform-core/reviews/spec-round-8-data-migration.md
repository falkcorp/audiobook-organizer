<!-- file: docs/plans/platform-core/reviews/spec-round-8-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 954d86ce-ad50-49ad-ab04-d86bac5bc2ef -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8: DATA, STORAGE AND MIGRATION (`DATA8-*`)

Antagonistic review of `docs/specs/platform-core/` after the round-7 revision and integration pass
(03 v1.10.1, 05-storage v1.8.1, 05-backup v1.7.1, 05-tasks v1.8.1, 05-authn v1.8.1, 05-config v1.8.1,
06 v1.8.1, 09 v1.7.0, 14 v1.8.1). R3-1, R4-1/R4-2, R5-1 … R5-7, R6-1 … R6-7 and R7-1 … R7-8 are binding.
A finding attacks text only where it does not carry a resolution through, where two files disagree, or
where it breaks against the code at `748fd787a`. Spec anchors are `file §section (l.N)`; code anchors are
`path:line` at `748fd787a`.

Several defects below were also found under other lenses this round. Each is noted ("also COR8-3"), so
the lead can merge them; the data-specific evidence and fix are kept here.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 7 | DATA8-1 … DATA8-7 |
| MINOR | 6 | DATA8-8 … DATA8-13 |

| ID | Sev | Title |
|---|---|---|
| DATA8-1 | MAJOR | DATA7-6 residue: a staged factory reset has no failure path. The set-aside renames across filesystems unchecked, and a post-reset hook that cannot finish keeps `reset_inflight` for ever |
| DATA8-2 | MAJOR | DATA7-1 half-fixed: the `Serialized`-only run families still take one commit stripe per written key, so `ReplaceOpRowsPrefix` still stalls every `Serialized` commit, against 05-tasks' claim. `DeleteRange`'s stripe rule is undefined |
| DATA8-3 | MAJOR | DATA7-11 half-fixed: `OpenOffline` detects a foreign open, does not act on it, then promotes its own stamp. One offline subcommand after a pre-S0 rollback erases the only evidence of that rollback |
| DATA8-4 | MAJOR | DATA7-11 residue: the `OpenOffline` user list breaks HEAD's break-glass `diagnostics reserved-prefs` and the fresh-DB `seed`, and omits two direct opens |
| DATA8-5 | MAJOR | DATA7-12 half-fixed (1): an open checkpoint's journal is finished offline. The step-e identities are then never handed to the next boot, so that boot reads a foreign open |
| DATA8-6 | MAJOR | DATA7-12 half-fixed (2): rule (3) leaves a `KindCheckpoint` journal at `failed`. Neither `CheckReader`'s exception nor P0a's resume range names that state, and the exception admits a pre-S2b candidate that cannot finish the journal |
| DATA8-7 | MAJOR | DATA7-5 residue: a swap-back restores `backup.location.json` but not the journal. The aborted restore's hook revocations are re-applied to the original store |
| DATA8-8 | MINOR | R7-3 residues: `op:batch:` and `opv3:sched:` are misclassified as run-guarded, and `Prune` rewrites the census row with no retry rule |
| DATA8-9 | MINOR | `c/tasks/idem/` is not `Serialized`, so two concurrent starts with the same `Idempotency-Key` both insert |
| DATA8-10 | MINOR | R7-5 residue: when the first core boot of a pre-S0 store fails after `BootWrites` and is retried, the retry deletes every legacy session and open invite |
| DATA8-11 | MINOR | R7-2 residue: boot exempts an empty store from the floor, but the pre-apply checks refuse a host with no store |
| DATA8-12 | MINOR | Reset journal residues: P0a "resumes" a reset journal until `hook_done`, and the manual undo leaves the journal and the keyring header on the reset install |
| DATA8-13 | MINOR | Drift: (3b) and the restore lineage, the fresh-install rule in 09, `storage.boot.json` deleted vs moved, `<db dir>/config.yaml`, a wrong section cite, stale `DeleteLegacySignIns` text, "P4b's lineage verdict" |

## Regression check (round-7 BLOCKER/MAJOR, this lens)

There was no round-7 BLOCKER in this lens.

| Round 7 | Status in round 8 | Residual |
|---|---|---|
| DATA7-1 (M) per-key `CheckedWrites` vs per-run guard; `DeleteRange` | **Half-fixed.** `CheckedWrites` is now on the three guard rows only, the same in 05-storage D-S2, §5S.3.4 and row 11/13, 05-tasks §5T.6.2 and 14. `DeleteRange` is refused in a checked claim. But the `Serialized`-only families still take a stripe per written key, so the stall DATA7-1 named is still there, and 05-tasks says it is gone | DATA8-2 |
| DATA7-2 (M) census and progress-row conflicts | **Fixed.** `Transition`/`Insert` recommit under `RetryOnConflict` while `want` holds (05-storage l.358-361, 05-tasks §5T.7). Native `c/tasks/count/` is deleted. Residue: `Prune` also rewrites `opv3:meta:counts` but has no retry rule | DATA8-8 |
| DATA7-3 (M) drop predicate | **Fixed.** HEAD's closed-ledger rule is used everywhere (05-storage l.353-357, 05-tasks §5T.7, error rows); no "or stopping" text is left | — |
| DATA7-4 (M) lineage rewritten only at (3b) | **Fixed.** The `marker_written` batch writes `c/storage/boot` and `storage.boot.json` before `applied`. Simulated below: a failure after `applied` reads as the same store | DATA8-7 (c), DATA8-13 |
| DATA7-5 (M) anchor rewritten before hooks | **Fixed for the anchor** (05-backup row "Head in the store", frozen; step c saves the file, step x restores it). The journal itself is not restored, and a failed restore's hook lines are re-applied | DATA8-7 |
| DATA7-6 (M) factory-reset side effects | **Fixed in shape** (the request only stages; there is a post-reset hook and `reset_pending`/`reset_inflight`). No failure path after staging | DATA8-1, DATA8-12 |
| DATA7-7 (M) floor before the fresh-install decision | **Fixed at boot** (§5S.4 step 3; `UpgradeFloor` comment; `Migration.Version` from `LegacyCap+1`; 06 P4a; 09 §9.2.2). The pre-apply checks were not given the same exemption | DATA8-11 |
| DATA7-8 (M) `Extension.Delete` blind | **Fixed** (`Delete(ctx, b, legacyKey, entryID, seen)`, l.1538; per-writer precondition table l.1618-1629) | — |
| DATA7-9 (M) first r2 boot signs everyone out | **Fixed** (05-authn §4.3 l.816-822, `TestFirstR2BootKeepsLegacySessions`). The zero-`Since` clause misfires on a retried first boot | DATA8-10 |
| DATA7-10 (M) "missing cut-off" signal | **Fixed** (deleted from 05-authn §3.4/§4.14/§6; 05-backup (2) is the one classifier). Stale `DeleteLegacySignIns` text in 05-authn §4.3 and 14 K22 | DATA8-13 |
| DATA7-11 (M) no guard-aware offline open | **Half-fixed.** `kernel.OpenOffline` exists (05-storage §5S.4, 06 §6.4.6). It promotes over a detected foreign open, and its user list breaks two subcommands | DATA8-3, DATA8-4 |
| DATA7-12 (M) checkpoint restore vs `CheckReader` | **Half-fixed.** Rules (1)–(3) are in 05-storage §5S.3.12, 05-backup and `CheckReader`. Rule (1) loses R6-4's identity hand-over, and rule (3)'s state is unadmitted and unresumed | DATA8-5, DATA8-6 |

Round-7 MINORs:
- Fixed: DATA7-13 (the one-shots are `Seed` steps; HEAD's markers are `setting:` rows, `internal/config/persistence.go:1102-1129`, so a seed writes them), DATA7-14 (one writer), DATA7-15 (09 S4/S5/S7 done-checks), DATA7-16 (§9.2.1 row 20, B7, K22 (d)) and DATA7-17.
- DATA7-18 is fixed except for 06 P4d step 2's "P4b's lineage verdict" (DATA8-13).

## Simulations (crash or failure noted at each step)

**(1) Factory reset staging → reset boot → post-reset hook (floor exemption included).**
- *Stage.* S2b–S4 r1: gin → `legacy.Maintenance`. From S4 r2: `internal/storagehttp`. Staging takes the mutex, refuses a pending restore and calls `ResetArgs`.
  - A crash before the `pending.json` rename leaves nothing staged.
  - No target rename is pre-validated (DATA8-1).
- *P−1.* `updater.Boot` may restore `.prev`, which then rejects the intent (`staged_by_build`). Correct.
- *P0a.*
  - The intent is rejected on expiry, on another build, or on a moved `OPTIONS-<n>`. Correct.
  - A crash during the renames resumes. Correct.
  - A deterministic `EXDEV` has no failure path: a state directory or an Open Library `LegacyPath` on another filesystem (DATA8-1).
  - After `set_aside`, the "resume" semantics are undefined (DATA8-12).
- *P2.* The store path is empty, and the lineage verdict is none (`storage.boot.json` was moved). The missing foreign-open records count as a foreign open. That is harmless on an empty store, where every duty is owed anyway with `since` zero.
- *Step 3.* The empty store is exempt from the floor. Correct.
- *Step 6.* One batch writes the store, books 1..`LegacyCap` included.
  - A crash before the batch leaves an empty store, and the next boot starts over.
  - A crash after the batch and before P4d step 1: the next boot finds a non-empty store at books `LegacyCap`, so the floor passes. The remaining seeds run under a checkpoint. `ResetBoot` is set again, because the journal still reads `set_aside`, and the rewrite is idempotent. Correct.
- *P4b.* The seeds, including HEAD's three markers, run. Correct.
- *P4d.*
  - Step 1 sets `ResetUnfinished`, rewrites the keyring header and marks the journal `applied`.
  - Step 2 (S4 r2+) sets the old journal aside on the install mismatch.
  - Step 4 writes the cut-off and deletes nothing. Correct.
- *Run step 2 (the hook).*
  - A crash before `Done` reruns the hook. Correct.
  - A per-entry failure (read-only library, `EACCES`) or a dangerous root never reaches `Done`, so `reset_inflight` stays for ever. If the error fails the start hook, the install also crash-loops (DATA8-1).
- *(3b) and the second boot.* The floor passes (`TestFreshInstallPostFloorBootsTwice`). Correct.
- *Manual undo* (DATA8-12).

**(2) A restore boot failing after its hooks.**
- *Failure after `applied`* (P4d step 4, P5–P8, Run before (3b)). The next boot reads:
  - lineage same (written in `marker_written`);
  - marker and anchor same;
  - Pebble open stamp and sidecar equal to the `pending` the restore boot staged at step 9;
  - generation equal to the file.

  So it is the same store and runs no hook. On the archive's own `last`, `gap` is already covered and `auth` revokes nothing new. **DATA7-4 holds.**
- *Failure between `hooks_done` and `applied`, or a hook that fails after an earlier hook committed.* Step x swaps back. The journal keeps the aborted restore's revocation lines, and the original store re-applies them (DATA8-7 (a)). If the journal was moved, the move stays (DATA8-7 (b)). If `storage.boot.json` was already rewritten, it stays one ahead (DATA8-7 (c)).
- *A crash anywhere between the swap and `applied`.* The next boot resumes the restore boot. Correct.
- *A `KindCheckpoint` boot whose DAG fails.* Rule (3) leaves the journal at `failed` (DATA8-6).

**(3) A CLI subcommand open followed by a serve boot.**
- *Normal case.* Serve commits `num=N1`. The CLI opens (`N2`) and promotes at a clean close. The next serve boot lists `N2 = num`, so there is no foreign open. Correct.
- *CLI crash.* The next serve boot reads a foreign open (fail-closed, as stated).
- *Pending steps.* The open is refused. Correct.
- *After a rollback to a pre-S0 build (HEAD writes no `last`).* The CLI computes the foreign verdict, ignores it and promotes, so the serve boot owes nothing (DATA8-3).
- *Three subcommands.* `seed` on a fresh DB is refused; `diagnostics reserved-prefs` cannot open a refused store; `dedup-bench` is unlisted (DATA8-4).
- *Staging.* `staging-scrub` before the first boot is refused while the restore is staged (`restore_pending`). Also SIMP8-2, OPS8-2 and SEC8-11; the data-side requirement is in DATA8-5.

**(4) The first S4 r2 boot keeps legacy sessions.**
- *The boot.* `auth` is owed (`since` = r1's `last.at`). There is no cut-off, so nothing is deleted, and the cut-off is written in the `BootWrites` batch. Correct.
- *A crash between P4d steps 2 and 4.* There is no fifth signal, so the store is not read as replaced. Correct.
- *A failure after step 4 and before (3b), on an S0+ store.* On the retry `since` is non-zero and older than the cut-off, so nothing is deleted. Correct.
- *The same on a pre-S0 store, skipped straight to S4 r2+.* `since` is zero on the retry, so everything is deleted (DATA8-10).
- *r2 → r1 → r2.* The cut-off predates `since`, so legacy sign-ins are deleted. That is intended.

**(5) Forward, rollback and roll-forward per stage.**
- *S0.* No change from round 7.
- *S2b.*
  - Fresh install → rollback to S2: the markers are present, so `embed_cover_art=false` is kept. HEAD treats an absent stamp on a store with data as format 1 (`internal/database/storage_format.go:56-58`). Correct.
  - S2's S0 guard reads the `{num, pending}` shape that `OpenOffline` wrote. Correct.
  - Factory reset (DATA8-1) and staging (DATA8-5).
- *S3.* Not this lens.
- *S4 r2.* The cut-off works (DATA8-10 aside). Restores in S4 r2–S7 are foreign with nil coverage, so a failed restore disables every key of the original store (DATA8-7).
- *S5.*
  - Forward: count-only, then strict, on the guard rows. Correct.
  - Rollback to S4: HEAD's `opsMu` code reads every row. Correct.
  - Roll-forward: `Recover` runs. Correct.
  - Stalls (DATA8-2), `Prune` vs the census (DATA8-8) and idempotency (DATA8-9).
- *S8.* `Maintenance` is deleted, and `backup` stages restores. Correct.
- *Floor release.*
  - A fresh install passes. Correct.
  - A new host is refused before apply (DATA8-11).
  - A failed migration → `restore-checkpoint` → `make rollback`: DATA8-5 and DATA8-6.

---

## Findings

### DATA8-1 — MAJOR — DATA7-6 residue: a staged factory reset has no failure path

Also COR8-3 (`EXDEV`), COR8-4 and OPS8-5 (the hook). The data-side points are the missing
pre-validation and the per-entry failure rule.

- **Where:**
  - 05-storage §5S.3.15, row "Stage" (l.1737). The targets include `<state_dir>/config.boot.json`, `<state_dir>/storage.boot.json` and "a module's `LegacyPath`, such as books' Open Library store". Nothing is pre-validated.
  - Row "Set aside" (l.1738): every target is renamed into `<db parent>/.mediacore-reset/<id>/previous/`.
  - Row "Side effects" (l.1742): "A crash or a failure before `Done` reruns the hook at the next boot". Row "Rollback tooling" (l.1743).
  - 06 P0a (l.1675); 06 §6.5.2 (l.1770): "If any hook's `Start` fails … `Run` returns the joined error".
  - 05-updater error row (l.608): "no updater override".
- **Evidence:**
  - The state directory "may be another filesystem when the DB path is set by flag or env" (05-storage §5S.3.12 "Where", l.1643).
  - Books' Open Library store is `<GetOLDumpDir()>/oldb`, with a default of `<RootDir>/openlibrary-dumps` (`internal/metafetch/openlibrary.go:55-61,73`), so by default it sits on the library volume.
  - `rename(2)` across filesystems fails with `EXDEV`. The restore swap pre-validates every rename for exactly this reason (05-backup step 6, l.645-648, `ErrRenameRefused`). The reset does not, and neither §5S.3.15 nor 05-backup says what P0a does when a set-aside rename fails. "Resumed the same way" covers only a crash.
  - HEAD's wipe is warn-and-continue per entry. It refuses a dangerous root only after it has already wiped the store (`internal/server/handlers/system/handler.go:563-580`, the refusal at `:565-569`). A library on a read-only mount fails every `RemoveAll`; the shipped compose file mounts it `:ro` (`docker-compose.yml:16`).
  - As written, any such failure never reaches `Done`. So `reset/<id>/inflight.json` stays, and the updater (with no override), `deploy` and `make rollback` refuse for ever. If the hook's error fails the start hook, the install also crash-loops (06 l.1770).
- **Fix:**
  1. Have `StageFactoryReset` pre-validate every target as 05-backup step 6 does. Set each target aside beside itself (state-directory files to `<state_dir>/reset/<id>/previous/`, as COR8-3 proposes). Hand a `Recreatable` auxiliary DB on another filesystem to the post-reset hook for deletion instead of moving it. Refuse a dangerous `RootDir` at staging (400, nothing written), instead of in the hook.
  2. Give P0a a failure rule for a reset: a set-aside rename that fails moves the targets already moved back, journals `rejected`, deletes `inflight.json`, and boots the untouched store `degraded` (`factory_reset_failed`).
  3. The hook never fails boot and always calls `Done` once each action has been attempted. Per-entry errors are logged and audited, and health reports `factory_reset_cleanup_incomplete` with the paths, as HEAD warns and continues. Only a crash reruns the hook.
  4. Add `TestFactoryResetCrossDeviceTargets`, `TestFactoryResetReadOnlyRootDirCompletes` and `TestFactoryResetSetAsideFailureKeepsStore`.

### DATA8-2 — MAJOR — DATA7-1 half-fixed: `Serialized`-only writes still take a stripe per key

- **Where:**
  - 05-tasks §5T.6.2 (l.1536-1540): "So `ReplaceOpRowsPrefix` … stages one guard `Expect`, not one per written key, and takes no other writer's stripes for its fsync."
  - 05-storage row 11 and row 13: every non-guard run family is "`Serialized` only".
  - §5S.3.2 commit step 2 (l.331-335): "If the batch writes **or** expects any key in a `Serialized` range, Commit acquires the commit stripes of those keys". §5S.6 (l.2037) says the same.
- **Evidence:**
  - The rows `ReplaceOpRowsPrefix` writes (`opv2:row:`, `opv2:rowix:`) are `Serialized` keys, so each one takes its stripe. Tens of MB of rows hash onto practically all 4 096 stripes, and the commit holds them through apply and fsync. Every other `Serialized` commit stalls for that time: identity, config, the extension family, and every run's ticks.
  - HEAD commits this write without `opsMu` precisely so as not to stall other writers (`internal/database/pebble_store_op_rows.go:110-114`).
  - So 05-tasks' sentence contradicts storage's commit procedure, and the stall DATA7-1 named is still there.
  - `DeleteRange` has no stripe rule. Stripes are "indexed by a hash of the absolute key", and a range has no key set. §5S.3.4 allows a `DeleteRange` in a `Serialized`-only claim "with no `Expect`", but does not say which stripes it takes. In a `Serialized` claim that still has `Expect` writers, an unstriped range delete can land between such a writer's check and its apply, and the writer then resurrects the row.
- **Fix:**
  - 05-storage §5S.3.4: add `Claim.Guarded bool`, which requires `Serialized` and excludes `CheckedWrites`. An `Expect` on its keys is refused (`ErrNotSerialized`). Its writes and its `DeleteRange` take no stripe, because their serialization is the guard row's stripe, which every legal writer holds (the owner's archtest, 05-tasks §5T.6.2).
  - §5S.3.2 step 2: take stripes for every expected key, and for written keys of non-`Guarded` `Serialized` claims and sub-prefixes. A `DeleteRange` inside a non-`Guarded` `Serialized` claim takes the whole stripe table (a barrier), or is refused; state which.
  - Mark row 11/13's non-guard families `Guarded` (a flag of the same build, so D-S3 is unaffected). The native store already declares those families non-`Serialized` (5T.6.1).
  - Keep 05-tasks' sentence, which is then true.
  - Add `TestGuardedWritesTakeNoStripes`: a 100 000-key `ReplaceOpRowsPrefix` commit does not delay a concurrent `apikey:` commit.

### DATA8-3 — MAJOR — DATA7-11 half-fixed: `OpenOffline` launders a foreign open

Also SEC8-4.

- **Where:**
  - 06 §6.4.6 "Offline open": "It runs the checks of P−1 to P3 of §6.5.1 **without acting on them**".
  - 05-storage §5S.4 "Offline open", step 1 (l.1955) and step 4 (l.1962-1967): at a clean close it promotes its own highest `OPTIONS-<n>` to `num` and its sidecar identity, keeps `last` and the lineage, and records only an unjournaled-revocation `journal` duty.
- **Evidence:**
  - Owed duties are recorded only at step 9 (§5S.4), which an offline open does not run.
  - A pre-S0 build (HEAD) writes no served ledger. After a rollback to it, the next core boot's only evidence of the interval is the sidecar identity and the Pebble open stamp (§5S.3.7 rows; SEC5-5, SEC6-4).
  - An offline subcommand run before the serve boot (`scan`, `diagnostics cleanup-invalid`, `users reset-password`) computes that verdict, ignores it and promotes its own number. The serve boot then reads no foreign open.
  - Because nothing is owed, four things are skipped:
    - the `auth` revoke-after-rollback (SEC4-9);
    - the `gap` line (later restores across the interval keep coverage they should not have);
    - the `gen` rebuild;
    - W's restart (09 §9.2.1 row 14).
- **Fix:**
  - 05-storage §5S.4 "Offline open" step 1: compute the verdicts of step 2 as a boot does.
  - Step 4: when step 1 found a foreign open (or a missing or unreadable record) or a **behind** lineage, the close batch first stages the owed entries step 9 would stage. `since` is the core `last.at` (zero when there is none), `detected` is now, and entries already present keep theirs. Only then does it promote. Alternatively, refuse a read-write offline open in that case (`ErrOfflineAfterForeignOpen`, "start `serve` once first"); pick one.
  - Mirror the rule in 06 §6.4.6.
  - Extend `TestOfflineOpenIsNotForeign`: a pre-guard open, then an offline write, then serve: `auth`, `journal`, `gen` and `usage` are owed.

### DATA8-4 — MAJOR — DATA7-11 residue: the `OpenOffline` user list breaks two subcommands and misses two opens

- **Where:** 05-storage §5S.4 "Offline open" "Users" (l.1972-1977); 06 §6.4.6 (users list); 09 S2b "Data/keys" (offline opens).
- **Evidence:**
  - **`diagnostics reserved-prefs`** is HEAD's break-glass tool. It is "Recovery for a store that will not open because a reserved preference row holds a bad value. It opens Pebble directly, bypassing the storage-format guard … so it works on a store the guard refuses" (`cmd/diagnostics.go:41-53`; `internal/database/storage_format.go:39-41`). `OpenOffline` runs the level guard (a legacy stamp above the build's projection is too new, §5S.3.7), the floor and claim validation, and refuses pending steps, so the tool cannot open the stores it exists for. After S2b, no tool repairs a corrupt core level stamp either, or an unreadable `c/storage/ks/<owner>/version` (§5S.3.5 "Unreadable version" fails closed, l.777).
  - **`seed`** "populates a fresh database … after `make build`" (`cmd/seed.go:5-8`). On an empty store, step 8 would run every `Seed` step, and the fresh-install batch is step 6, which an offline open does not run. So `ErrOfflinePendingSteps` refuses it: the documented dev workflow breaks from S2b.
  - **Unlisted direct opens.** `dedup-bench` (build tag `bench`) opens the store read-write through `initializeStore` (`cmd/dedup_bench.go:106-109`). So does the `Isolate` child mode (`cmd/child_mode.go:71`). Neither is in the `OpenOffline` list or the documented-foreign list. `initializeStore`'s store constructor is deleted at S2b (§5S.9.3).
- **Fix:**
  - Keep `reserved-prefs` a direct open, documented as a break-glass foreign open beside `pebble-inject-skip`. From S2b, extend it (or add `<app> storage repair`) to list and rewrite core's reserved records (`c/storage/level/<owner>`, `c/storage/ks/<owner>/version` and its applied records) and the sidecars, with HEAD's confirm-and-log rules.
  - `seed`: either an offline open of an **empty** store runs the fresh-install batch and the `Seed` steps (step 6 and the seed part of step 8; no checkpoint, as on a fresh install), or `seed` refuses with "start `serve` once first", and its help text and `docs/` say so.
  - `dedup-bench` opens read-only through `OpenOffline`. The `Isolate` child mode is listed, or deleted with its caller.
  - `TestCLISubcommandsUseOfflineOpen` covers the `bench` tag and names `reserved-prefs` on its allow-list.

### DATA8-5 — MAJOR — DATA7-12 half-fixed (1): an open checkpoint's offline finish re-creates DATA6-2's false foreign open

- **Where:**
  - 05-storage §5S.3.12 "Restore" rule (1) (l.1648); 05-backup "Checkpoint restore" (l.711-716): the tool "journals `applied_offline`, deletes `inflight.json`".
  - §5S.4 step 2 (l.1821-1823): the step-e identities "stand in" **on a restore boot**.
  - 06 P0a: a restore boot is decided from a journal "between `previous_moved` and `applied`".
- **Evidence:**
  - After rule (1), the next boot is not a restore boot, so restoreswap's step-e record is not handed to storage.
  - `pebble.Checkpoint` copies the live OPTIONS file under its own number (`cockroachdb/pebble/v2@v2.1.7` `checkpoint.go:198,266-269`). So the checkpoint's highest `OPTIONS-<n>` is the failed boot's own open, which that boot never recorded: it failed at P4b, before step 9 staged `pending`.
  - Step e rewrote `.storage-format`.
  - The next boot (`.prev` after `make rollback`) therefore reads a foreign open on both signals and owes every duty:
    - a `gap` line, so every later restore across it gets nil coverage and every key needs confirmation;
    - every native session revoked;
    - W restarted.

    That is the recovery path for a failed upgrade.
  - `TestRestoreCheckpointOffline` asserts only that the previous binary boots.
  - Any offline restore apply introduced to fix the staging order (SIMP8-2, OPS8-2) inherits the same defect.
- **Fix:**
  - 05-storage §5S.3.12 rule (1) and 05-backup: before journaling `applied_offline`, the tool commits `OpenOffline`'s close batch over the restored store, with the step-e identities standing in for the store's records as on a restore boot. It promotes the directory's highest `OPTIONS-<n>` and the `.storage-format` identity it wrote, and keeps `last` and the lineage.
  - Alternatively, the kernel hands the identities of the newest `applied_offline` journal to P2 once, consumed at (3b).
  - The same rule binds every offline swap finished without a boot.
  - Add `TestOpenCheckpointRestoreThenRollbackIsNotForeign`.

### DATA8-6 — MAJOR — DATA7-12 half-fixed (2): rule (3)'s `failed` journal is neither admitted nor resumed

- **Where:**
  - 05-backup step x "Exception" (l.701): "journals `failed`, keeps `inflight-checkpoint.json` … the operator rolls back to the previous binary (which `CheckReader` admits, step 9's exception)".
  - Step 9 (l.672), 05-storage §5S.3.7 (l.1110) and 05-updater `TestApply_CheckpointJournalAdmitsCandidate`: the exception is "a `KindCheckpoint` journal at **`swapped`**".
  - The script applies it "from the path alone" (l.1124).
  - P0a resumes journals "between `previous_moved` and `applied`" (05-backup l.683; 06 P0a). `failed` is a journal step (05-backup l.509).
- **Evidence:**
  - After rule (3) the journal reads `failed`.
    - The Go `CheckReader` refuses it, so the updater's `Boot` records `rollback_blocked` and the failing build keeps restarting.
    - The script passes it, so `TestReaderCheckMatchesRollbackScript` cannot agree on the case.
    - Whether `.prev`'s P0a resumes a `failed` journal as a restore boot is unstated. If it does not, `inflight-checkpoint.json` stays for ever and the `--accept-loss` restore never runs its hooks.
  - The exception also admits any candidate "whose levels and floors admit", including a pre-S2b build. That build has no journaled swap (05-backup "Stages") and ignores the journal, so a later core build resumes a stale journal long after the fact: DATA7-12's override case, now without an override.
- **Fix:**
  - In 05-storage §5S.3.7, 05-backup steps 9 and x, 05-updater and the script, name the exception as: a `KindCheckpoint` journal at `swapped` **or** `failed` admits a candidate whose levels and floors admit the restored store **and** which implements the journaled swap. Detect that with a fast-exit flag that ships at S2b (`--print-restore-journal`); a candidate without it is `restore_inflight`.
  - That candidate's P0a resumes the journal as a restore boot: its own DAG, the hooks, then `applied`.
  - A `failed` journal records `failed_by_build`, and that build does not resume it.
  - Add `failed` and the pre-S2b candidate to `TestReaderCheckMatchesRollbackScript` and `TestCheckpointRestoreBootFailingStepKeepsCheckpoint`.

### DATA8-7 — MAJOR — DATA7-5 residue: a swap-back restores the location file but not the journal

- **Where:**
  - 05-backup step x (l.701): `restore_aborted` closes "the line" only. Steps c and x restore `backup.location.json` only.
  - §5B.3.6 "Boot re-apply" (3) (l.812): "every verified line after `head_before` is re-applied … On the same store this is all".
  - Error row (l.959) and `TestRestoreFailureSwapsBack` (l.1042): "no key needs confirmation".
  - §5B.3.6 "Where" (l.805).
- **Evidence:**
  - **(a) The hook lines.** Hook restrictions are journaled. 05-authn §4.12 (l.1198-1205) says "Every restriction is first recorded with `Deps.Revocations.Append`", with kinds `apikey.disabled` ("`needs_confirmation` included"), `credential_epoch.bumped` and `session.revoked`, "by the post-restore hook" too.
    - The hooks commit in several batches (auth, rbac, audit; "the last hook batch records `restore_hooked`").
    - If rbac's or audit's hook fails after auth's committed, step x swaps back. The journal then holds auth's lines between the `restore` line and `restore_aborted`.
    - The original store's next boot reads `head_before` J0, below the last revocation line, with the same marker and anchor: the same store, so it re-applies those lines. Every user's epoch is bumped. Under nil coverage, which is every restore in S4 r2–S7 (09 §9.2.1 row 20), every active API key of the **original** store is set `needs_confirmation`. That is DATA6-3's outcome, reached through the tail.
  - **(b) A journal move.** "When the effective directory … differs from the recorded one — … or a restore brought back another override — the kernel first moves the journal" at P4d step 2 of the restore boot. Step x restores the old file but not the journal's location.
  - **(c) The lineage file.** Since R7-4, step h also rewrites `storage.boot.json`, which step c does not save. A failure (not a crash) after that rename and before `applied` that swaps back leaves the file one ahead of the original store, so the next boot reads **behind**, which (2)(c) classifies as replaced.
- **Fix:**
  1. (3) "re-apply" and step x: lines between a `restore` line and its `restore_aborted` line, or after a `restore` line whose restore journal reads `swapped_back`, are verified but never re-applied. To make the span exact, the hooks' `Append` carries the restore line's seq in `Detail`.
  2. On a restore boot the journal is not moved before `applied`: the `restore` line and the hook lines go to the recorded location, and the first boot after `applied` moves the journal.
  3. Either a failure from `marker_written` on is resumed forward, never swapped back, or step c also saves `storage.boot.json` and step x restores it.
  4. Extend `TestRestoreFailureSwapsBack`: rbac's hook fails after auth's committed; no key needs confirmation, no epoch is bumped, and the journal is where it was.

### DATA8-8 — MINOR — R7-3 residues in books' run store

- **(a) Two families are misclassified.**
  - 05-storage row 11 classes `opv3:sched:` with "the other families here are `Serialized` only, their writes legal only in a batch that carries the run's guard `Expect`". 05-tasks §5T.6.2 (l.1526-1529) classes `op:batch:` the same way.
  - `op:batch:<opType>:<subjectType>:<subjectID>` is the debounce bucket (`internal/database/pebble_store_ops_v2.go:1554-1567`). `opv3:sched:` is HEAD's v3 schedule state (`keyfamilies.go:303`), served as `ClockStore`.
  - Neither has a run. `Debounce` and `Advance` stage their own `GetForUpdate` (5T.3.13; 5T.6.2 l.1509). They cannot go through the per-run staging handle that `TestOpsFamiliesWrittenOnlyByTaskstore` enforces.
- **(b) `Prune` has no retry rule.** `Prune`'s delete also rewrites `opv3:meta:counts` (`del` → `count(prev,-1)` → `flush`, `pebble_store_ops_v3.go:309-325,421-439`). That row is `CheckedWrites`, yet only `Insert`/`Transition` retry on a census conflict (5T.7). Under N busy workers a chunk of deletes conflicts almost every time, so retention stalls. `Recover` (`pebble_store_ops_v3_reconcile.go:75`) has the same gap.
- **Fix:**
  - List `op:batch:` and `opv3:sched:` as per-key guards (`Serialized`, with an `Expect` per key, like native `c/tasks/debounce/` and `c/tasks/sched/`), outside the per-run handle.
  - `Prune` and `Recover` commit each chunk under `RetryOnConflict`, re-reading each row's guard. A row that is no longer terminal is skipped.
  - Add `TestPruneUnderConcurrentCompletions`.

### DATA8-9 — MINOR — `c/tasks/idem/` is written blind

- **Where:** 05-tasks §5T.6 (l.1451): `c/tasks/idem/<kind>/<key>` is "staged in the batch of the run's `Insert`". 5T.6.1: "the families marked `Serialized` are exactly the guard keys"; `idem/` is not one of them. 5T.3.10 (l.985): a duplicate "returns `200` with the existing `RunView`".
- **Evidence:** A non-`Serialized` key cannot carry an `Expect` (§5S.3.2). Two concurrent starts with one key, such as a client retry, each read "absent", insert distinct runs and overwrite each other's record. So the `Idempotency-Key` promise fails exactly in the case it exists for. Both stores are affected, because `idem/` is core's whichever implementation serves runs.
- **Fix:** Declare `idem/` `Serialized`. `Insert` stages `Expect(idem, nil)`. On a conflict the retry of 5T.7 re-reads the record and returns the existing run (replay). Expiry deletes with an `Expect`. Add `TestStart_ConcurrentSameIdempotencyKeyOneRun`.

### DATA8-10 — MINOR — R7-5 residue: a retried first core boot of a pre-S0 store deletes every legacy sign-in

- **Where:** 05-authn §4.3 (l.816-822): "a zero `Since` (no previous core record …) with a cut-off present counts as predating, so the rule fails closed". 05-storage served-ledger row: `since` is "zero … when there is no previous record".
- **Evidence:**
  - A HEAD store skipped straight to an S4 r2+ release has no `last`.
  - Its first core boot owes `auth` with `since` zero. It writes the cut-off and clears `auth` in the `BootWrites` batch, then fails before (3b): a `Register` error, a derived-index load failure or a power loss.
  - The retry still has no `last`, so it owes `auth` again with `since` zero. It finds a cut-off, which "counts as predating", and runs `DeleteLegacySignIns`. Every HEAD session and open invite is gone, against R7-5's "the upgrade signs no one out".
  - `TestFirstR2BootKeepsLegacySessions` crashes only between steps 2 and 4, before the cut-off exists.
- **Fix:** A zero `Since` from an **absent** record never deletes. Only an **unreadable** record (fail-closed) does, or a cut-off older than a non-zero `Since`. Add the case "fail after P4d step 4 of the first boot of a pre-S0 store, retry: no legacy sign-in deleted".

### DATA8-11 — MINOR — R7-2 residue: the pre-apply floor checks refuse a host with no store

- **Where:**
  - 09 §9.2.2 (l.302): "a non-empty floor with a missing sidecar refuses with 'boot any release ≥ S0 once'".
  - 05-storage `CheckReader` (`.storage-applied` missing → `sidecar_unreadable`); 05-updater `StoreState`.
  - Against this, boot exempts an empty store (§5S.4 step 3, `UpgradeFloor` comment).
- **Evidence:** From the floor release, `deploy`, `scripts/deploy-preflight.sh` and the S0 updater hotfix refuse to install the floor binary on a host with no DB directory: a new host, or a host prepared for a restore. Yet that binary's first boot would be exempt.
- **Fix:** In `CheckReader`, the script and the updater, no DB directory at the resolved path (or an empty one) and no sidecars passes `upgrade_floor`. That is the same exemption as at boot. Add the case to `TestReaderCheckMatchesRollbackScript` and `scripts/test_storage_format_guard.py`.

### DATA8-12 — MINOR — Reset journal residues

- **(a) P0a's resume.**
  - `inflight.json` is now kept until `hook_done` (§5S.3.15 "Rollback tooling"), and P0a "first resumes any restore or reset journal in flight" (row "Precedence").
  - Re-running the set-aside renames after `set_aside` would move fresh-store artifacts that have no counterpart in `previous/` (renames run only when the destination is absent). Examples: the sidecars of a pre-S0 old store, or a derived directory the old install never had.
  - Fix: state that P0a re-runs renames only while the journal's last step precedes `set_aside`. From `set_aside` on, `inflight.json` only gates `CheckReader` and `ResetUnfinished`.
- **(b) The manual undo** (row "Undo", l.1745).
  - The reset boot renamed the old install's revocation journal aside (`<name>.other-install-<ts>`, 05-backup (0)), rewrote the keyring header's `install_id`, and rewrote `backup.location.json` for the new install.
  - Moving `previous/` back restores none of these. The undone store's next boot therefore sets its journal aside again (install mismatch) and starts a new one, so every old archive restores with nil coverage.
  - Fix: list these items in the "Undo" row (rename `<name>.other-install-<ts>` back, restore the header field through `secrets export-keyring`/import or a documented `secrets set-install-id`), or keep the old journal and the header value in `previous/` so that moving it back is complete.

### DATA8-13 — MINOR — Drift

- **(3b) on a restore boot.** 06 §6.5.2 (3b) (l.1758) says a restore boot's lineage is written in `marker_written` "and (3b) does not write them again". 05-storage §5S.4 step 14b (l.1935) says it "writes it again here". 06 (3b) also omits the Pebble open stamp promotion that step 14b lists. Pick one statement (either is correct), and list the stamp in 06.
- **09 §9.2.2.** In "What the store records", the boot-lineage row (l.253) names only the (3b) writer; add the restore boot's `marker_written` batch. "Fresh installs" (l.257-259) says "every registered migration step's applied record — seed steps excepted". Two sentences later it, and 05-storage §5S.3.5, say "below that owner's lowest `Seed` step" (COR6-4). Use the latter.
- **`storage.boot.json`.** 06's write rule (l.1625) says an accepted factory reset **deletes** `storage.boot.json`, while 06 P0a and §5S.3.15 **move** it into `previous/`. Say "moves".
- **`<db dir>/config.yaml`.** 05-storage l.1742, 05-config §1.12.2 and 09 §9.2.1 rows 1 and 12 name the gap-fill file `<db dir>/config.yaml`, and the same texts say it "sits beside the DB directory". HEAD writes `filepath.Dir(DatabasePath)/config.yaml` (`internal/config/persistence.go:29-33`). Write `<db parent>/config.yaml`.
- **A wrong cite.** 09 §9.2.1 row 12 cites "§9.2.6" (permission atoms) for the undo statement; cite 05-storage §5S.3.15.
- **`DeleteLegacySignIns`.** 05-authn §4.3 (l.813-815) still opens with "and deletes every legacy session and legacy invite the store holds (`DeleteLegacySignIns`, DATA6-4: so an in-place restore that detection cannot see never honours pre-cut-off legacy sessions either)" before R7-5's condition. 14 K22 says that on a replaced store "the owed-`auth` consumer deletes every legacy sign-in". On a replaced store it is `PostRestore` step 2 that deletes them, and the consumer deletes them only under R7-5's condition. Reword both.
- **P2, not P4b.** 06 P4d step 2 (l.1680) still says "P4b's lineage verdict" (DATA7-18). The verdict is computed at P2 (§5S.4 step 2).
