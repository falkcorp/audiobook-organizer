<!-- file: docs/plans/platform-core/reviews/spec-round-7-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: e8f58570-0c37-40e4-8cf1-a988447bd8d6 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7: DATA, STORAGE AND MIGRATION (`DATA7-*`)

This is an antagonistic review of `docs/specs/platform-core/` after the round-6 revision and integration pass:
03 v1.9.1, 05-storage v1.7.1, 05-backup v1.6.0, 05-authn v1.7.1, 05-tasks v1.7.0, 05-config v1.7.1, 06 v1.7.1,
09 v1.6.1, 14 v1.7.1. R3-1, R4-1/R4-2, R5-1 … R5-7 and R6-1 … R6-7 are binding and are not attacked as designs.
A finding attacks the text only when it does not carry a resolution through, when two files disagree, or when
it breaks against the code at `748fd787a`.

Spec anchors take the form `file §section (l.N)`. Code anchors are `path:line` at `748fd787a`.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 12 | DATA7-1 … DATA7-12 |
| MINOR | 6 | DATA7-13 … DATA7-18 |

| ID | Sev | Title |
|---|---|---|
| DATA7-1 | MAJOR | DATA6-1 half-fixed (1): `CheckedWrites` checks the written key, but the run store guards each write with an `Expect` on the run's record. `DeleteRange` is undefined under the flag. 05-tasks and 05-storage disagree on whether row 13 has the flag |
| DATA7-2 | MAJOR | DATA6-1 half-fixed (2): two things now raise false `ErrRunConflict` against a `Transition`. One is the global census row, which every state change of every run rewrites. The other is a progress tick, which rewrites the `opv2:op:` row. Neither the retry rule nor the census design handles this |
| DATA7-3 | MAJOR | The new drop rule refuses ledger commits and row marks on a `stopping` run. HEAD refuses them only on a terminal run. Every quiesce or shutdown then re-applies the in-flight Fixer rows on resume |
| DATA7-4 | MAJOR | DATA6-2 half-fixed: the restore exemption ends at `applied` (P4d step 3), but the lineage is rewritten only at Run (3b). A restore boot that fails in between leaves the next boot reading "behind", so the store is classified replaced |
| DATA7-5 | MAJOR | DATA6-3 half-fixed: the new committed-head anchor is rewritten during the restore boot, at P4d step 2, on the `restore` line's `Append` and at shutdown. A hook failure then swaps back, and the original store's head sits below the anchor, so it reads replaced |
| DATA7-6 | MAJOR | Factory reset: HEAD's handler also deletes the library folder and the Open Library data and resets every setting. The spec does not say where those side effects go. Some run before the restart, and a pending reset intent can be rejected. "Moving `previous/` back undoes the reset" is false |
| DATA7-7 | MAJOR | The upgrade-floor check runs at step 3, before step 5 decides "fresh install". An empty store has books version 0, so from the floor release every fresh install, and every factory reset, is refused. §5S.3.15 confuses level with version |
| DATA7-8 | MAJOR | R6-1's keyspace rule refuses every blind write in `m/books/ext/`, but `legacy.Extension.Delete` takes neither `ctx` nor `seen`, so it can only write blind. Every extension-record delete fails with `ErrBlindWrite` |
| DATA7-9 | MAJOR | The owed-`auth` consumer now calls `DeleteLegacySignIns`. On the first S4 r2 boot `auth` is always owed, so that boot deletes every legacy session and every open legacy invite. Upgrade-on-use never runs. This cost is missing from 09 §9.2.1 |
| DATA7-10 | MAJOR | The "user rows but no cut-off record" signal has a crash window on the first S4 r2 boot, between the `served_boot` pair at P4d step 2 and the cut-off write. 05-backup's one classifier does not contain the signal, and the content marker already covers what it detects |
| DATA7-11 | MAJOR | Every read-write open outside a serve boot reads as a foreign open under SEC6-4's open stamp, which costs a full round of fallbacks. Such opens include core's offline `users reset-password`, ABO's `scan`/`organize`/`tag`/`playlist`/`seed`/`diagnostics` subcommands, `pid-census` and `pebble-inject-skip`. No guard-aware offline open is specified |
| DATA7-12 | MAJOR | `storage restore-checkpoint` is said to be "usable by `make rollback`", but it leaves an in-flight restore journal that `CheckReader` refuses. The failing build's restore boot then re-runs the failing step and swaps the checkpoint back out |
| DATA7-13 | MINOR | The three settings one-shots are "recorded applied on a fresh install" without writing HEAD's markers. A rollback below S2b then re-runs them and drops a stored `embed_cover_art=false` |
| DATA7-14 | MINOR | `backup.location.json` has three concurrent writers and no single-writer rule. A lost update can hide Create's P′ |
| DATA7-15 | MINOR | D-S2 says a `CheckedWrites` flag refuses only after "zero persisted blind-write violations ≥ 14 days". 09 states that gate only for S3: S4 r2, S5 (T0+T1) and S7 list nothing |
| DATA7-16 | MINOR | DATA6-4 residue: archives are `legacy-abo/0` until S8, which means foreign, nil coverage and no P′. The 09 S4 R-test cell and `TestOlderBuildInPlaceRestore_SameDirDetected` claim "content marker older" for archives that ABO made in S4 r2–S7 |
| DATA7-17 | MINOR | Factory-reset bookkeeping disagrees between 06 P0a and 05-storage/05-backup. The reset boot's lineage verdict is "behind", not "none". The keyring-header rewrite has no trigger, and a pending restore and a pending reset are not exclusive |
| DATA7-18 | MINOR | Drift: the open stamp says "above" where it means "differs"; 06 says "P4b" where it means P2 and omits `OPTIONS-<n>`; K22 says "restores `backup.location.json`"; `TouchAPIKey` has three skip predicates |

## Regression check (round-6 BLOCKER/MAJOR, this lens)

| Round 6 | Status in round 7 | Residual |
|---|---|---|
| DATA6-1 (B) run writes other than `Transition` unchecked | **Half-fixed.** The rule is now general: 05-storage §5S.3.2 (l.346-362), 05-tasks §5T.3.13 (l.1155-1170) and §5T.7 (l.1694-1709). Rows 11/13 get `CheckedWrites` from T0+T1. But the flag's per-key semantics do not match the per-run guard, `DeleteRange` is undefined, the global census row and the progress-carrying `opv2:op:` row make unrelated writes conflict, and the drop predicate departs from HEAD's closed-ledger rule | DATA7-1, DATA7-2, DATA7-3 |
| DATA6-2 (M) staged restore read as foreign open | **Half-fixed** (05-storage l.1004, l.1009-1010; 05-backup step e l.675). The exemption is keyed to "journal between `swapped` and `applied`". The lineage it protects is rewritten later, at Run (3b) | DATA7-4 |
| DATA6-3 (M) failed-hook swap-back poisons the original store | **Half-fixed.** The marker and file are now written after the hooks (05-backup step h l.678, step x l.679). The new committed-head anchor (l.774), added for DATA6-4, is still rewritten before the hooks | DATA7-5 |
| DATA6-4 (M) per-boot content marker misses last night's archive | **Fixed from S8** (Create's P′, 05-backup §5B.3.5 step 4; head anchor; (2)(b)). Archives taken during S4 r2–S7 are still ABO-made. The `DeleteLegacySignIns` addition has a side effect at the first S4 r2 boot | DATA7-16, DATA7-9 |
| DATA6-5 (M) 06 and 05-authn keep the old classifier | **Fixed**: 06 P4d step 2, 05-authn §4.14 and 05-storage step 11(b) all point to 05-backup (2). Residue: 05-authn adds a "missing cut-off" signal that 05-backup (2) does not list | DATA7-10 |
| DATA6-6 (M) factory reset unspecified | **Fixed in shape** (05-storage §5S.3.15, 05-backup l.681, 05-config l.1454-1469). HEAD's side effects, the floor ordering and the bookkeeping owners are wrong or missing | DATA7-6, DATA7-7, DATA7-17 |
| DATA6-7 (M) W restarts at `since` | **Fixed** (`Detected`; `c/compat/recording_since/<owner>`; 05-storage l.1008, 06 P4d step 5, 09 l.124-127) | — |
| DATA6-8 (M) coalesced last-use writers resurrect records | **Fixed** (`TouchSession`/`TouchAPIKey`, 05-authn §3.4 l.560-574). The same R6-1 rule that this fix relies on breaks `legacy.Extension.Delete` | DATA7-8 |

Round-6 MINORs:
- Fixed: DATA6-9 (`c/storage/owed/gen/<owner>/<index>`, cleared at completion), DATA6-10, DATA6-11, DATA6-13 and DATA6-14.
- DATA6-12 is fixed in 05-storage but not carried into 09's done-checks (DATA7-15).

## Simulations

**(a) Forward S0 → S10.**
- S0–S2 are clean.
- S2b: the first boot owes `gen` and `usage` (intended).
- S4 r2: the first boot owes `auth`, so every legacy session and invite is deleted (DATA7-9). A crash between steps 2 and 4 makes the next boot "replaced" (DATA7-10).
- S5 T0+T1: concurrent completions conflict on `opv3:meta:counts` (DATA7-2), and quiesce re-applies Fixer rows (DATA7-3). The strict release after the count-only one refuses every progress tick and every terminal `DeleteRange` (DATA7-1).
- Floor release and every later release: a fresh install is refused at step 3 (DATA7-7).

**(b) Rollback and roll-forward, at each stage.**
- A rollback below S2b on a store that was a fresh install at S2b+ re-runs HEAD's one-shots (DATA7-13).
- An offline CLI run in between is a foreign open (DATA7-11).
- Otherwise the costs match §9.2.1.

**(c) Stepping stone.**
- The floor is checked on `.storage-applied` before apply, and at boot.
- The empty-store case is broken (DATA7-7).

**(d) Restores.**
- Staged restore:
  - a hook failure swaps back, then the next boot reads replaced through the anchor (DATA7-5);
  - a failure after `applied` and before (3b) makes the next boot read "behind", so replaced (DATA7-4).
- `restore-checkpoint` followed by rollback is refused, and the next boot swaps back (DATA7-12).
- HEAD's in-place restore of an S4 r2–S7 archive taken in the last journaling boot is caught only by the anchor (DATA7-16).

**(e) Crash at each boot phase.**
- P−1 to P3 write nothing.
- P4a and P4b are as in round 6.
- First S4 r2 boot: a crash between P4d steps 2 and 4 (DATA7-10).
- Restore boot: a crash or failure between step h `applied` and Run (3b) (DATA7-4).
- Reset boot: the lineage reads "behind" (DATA7-17).

**(f) Concurrent writers on `Serialized`/`CheckedWrites` families.**
- Progress tick vs cancel:
  - the tick never reverts the cancel (fixed);
  - but the cancel conflicts with the tick (DATA7-2).
- Two runs completing at once conflict on the census (DATA7-2).
- Extension delete: `ErrBlindWrite` (DATA7-8).
- `backup.location.json`: lost update (DATA7-14).

**(g) Factory reset with the keyring kept.**
- The keyring is kept, but the header rewrite has no trigger (DATA7-17).
- Library and settings side effects (DATA7-6).
- Floor (DATA7-7).

---

## Findings

### DATA7-1 — MAJOR — DATA6-1 half-fixed: `CheckedWrites` is per key, the run store's guard is per run, and `DeleteRange` is undefined

- **Where:**
  - 05-storage §5S.3.4 `Claim.CheckedWrites` comment (l.496-506) and D-S2 (l.56): "refuses a Set or Delete of a key in this claim unless the same batch carries an Expect **on that key**".
  - §5S.9.1 row 11 (l.1892): "every write books' run store stages … carries `GetForUpdate`/`Expect` on the run's `opv3:run:` record and its paired `opv2:op:` row".
  - Row 13 (l.1895): "every `op:`, `opv2:` and `opv3:` family … like row 11, `CheckedWrites` from T0+T1".
  - 05-tasks §5T.3.13 (l.1155-1161) and §5T.6.2 (l.1488-1497): "**Row 11 is also `CheckedWrites`**"; only "the queue and index sub-prefixes of row 13" are Serialized.
  - `TestT0T1RunRowsCheckedWrites` (05-storage l.2029).
- **Evidence:**
  - A progress tick writes `opv3:prog:<id>` (row 11) and the `opv2:op:` row (`internal/database/pebble_store_ops_v2.go:717-751`; `pebble_store_ops_v3.go:383`). The specified guard is an `Expect` on `opv3:run:` and `opv2:op:` only. In the release after the count-only one, the `opv3:prog:` write therefore fails with `ErrBlindWrite`. So do `SaveCheckpoint` (`opv3:ckpt:`), the ledger (`opv3:ledger:`), every page append, every index diff and every census write.
  - HEAD stages `DeleteRange` on row-13 families on every terminal transition (`pebble_store_ops_v3.go:285-300`, snapshots, marks, row marks), on every discard (`pebble_store_ops_v2.go:938-942,1056-1060`), in step 62's hollow sweep (`pebble_store_op_rows.go:76-90`) and in `ReplaceOpRowsPrefix` (`:125`). §5S.3.2/§5S.3.4 define the flag only for `Set`/`Delete`.
  - `Expect` is evaluated against committed state, so `DeleteRange` + `Set(k)` + `Expect(k, nil)` in one batch always conflicts when `k` already existed.
  - `ReplaceOpRowsPrefix` writes tens of MB in one batch without `opsMu`, precisely so as not to stall other writers (`pebble_store_op_rows.go:110-114`). With per-key `Expect` its commit takes most of the 4 096 stripes and stalls every `Serialized` commit (identity, config, runs) for the whole fsync.
  - The two files disagree on whether row 13 is `CheckedWrites` at all.
- **Fix:**
  - In 05-storage row 11, row 13 and 05-tasks §5T.6.2, state one rule: `CheckedWrites` applies only to the guard rows (`opv3:run:`, `opv2:op:`, and `opv3:meta:counts`, DATA7-2). Every other row-11/13 family is `Serialized` only, and its writes are legal only in a batch that carries the run's guard `Expect`. The 05-tasks archtest `TestOpsFamiliesWrittenOnlyByTaskstore` enforces that, not storage.
  - Add to §5S.3.4: "`DeleteRange` inside a `CheckedWrites` claim is refused (`ErrBlindWrite`); inside a `Serialized`-only claim it is allowed and needs no `Expect`."
  - Add `TestCheckedWritesDeleteRangeRule`. Extend `TestT0T1RunRowsCheckedWrites` with a terminal transition, a discard and `ReplaceOpRowsPrefix` in the strict release.

### DATA7-2 — MAJOR — DATA6-1 half-fixed: the global census row and the progress-carrying op row make unrelated writes conflict, and `Transition`'s conflict rule is missing

- **Where:**
  - 05-tasks §5T.3.13 (l.1155-1166: "a write whose run changed since it was read fails … with `ErrRunConflict`"; "What the caller does with ErrRunConflict is 5T.7's rule").
  - §5T.7 (l.1694-1709: "a lost precondition means another actor changed the run". The list covers ticks, ledger, checkpoint, rows, marks, pages and debounce, and names no rule for `Transition`/`Insert`).
  - §5T.6.1 `c/tasks/count/<state>` "census counters (Serialized)" (l.1448).
- **Evidence:**
  - Books' store keeps HEAD's single global counter row `opv3:meta:counts` (`pebble_store_ops_v3.go:100`). `opsV3Stage.flush` reads it and writes it back in every `Insert`/`Transition` batch (`:243-248,423-439,461-473`), safe at HEAD only under `opsMu`.
  - Staged into the caller's batch, the write is either blind, which is a lost update and makes the census drift, or carries `GetForUpdate` under R6-1/row 13. In the second case any two state changes of **different** runs overlap and one fails.
  - Native has the same shape: `c/tasks/count/<state>` is Serialized, and every transition touches two counters.
  - Separately, both `Transition` and `Progress` stage `GetForUpdate` on the `opv2:op:` row, which every tick rewrites (progress columns, `LastProgressAt`). An HTTP cancel, a decide, a watchdog stop or a shutdown quiesce of a chatty run therefore conflicts on a progress-only change.
  - 5T.7's premise does not hold, and no text says whether such a `Transition` retries or surfaces 409. The executor's own completion transition under N workers is unspecified too.
- **Fix:**
  - 05-tasks §5T.3.13/§5T.7: "`Transition` and `Insert` on `ErrConflict` re-read the run. If `want` (state, attempt, fence) still matches, they rebuild the whole batch and recommit under `storage.RetryOnConflict` (bounded, jittered). They return `ErrRunConflict` only when `want` no longer matches."
  - Native: delete `c/tasks/count/`. `Counts` derives from `c/tasks/ix/state/` or from per-run deltas folded by `Prune`.
  - Books: `opv3:meta:counts` is the one non-run key in the batch, carries `GetForUpdate`, and is retried as above.
  - State that a run's own writes are funnelled through one per-run writer, which the storage rule allows ("an in-process mutex for their own fairness", §5S.3.2 l.341), so a run's page appends never race its own ticks.
  - Add `TestConcurrentTransitionsOfDistinctRunsAllCommit` (N runs complete at once; all commit; counts exact) and `TestCancelCommitsUnderProgressTicks` (`synctest`, both stores) to `taskstest.StoresSuite`.

### DATA7-3 — MAJOR — The drop predicate refuses ledger commits and row marks while a run is `stopping`, which HEAD accepts

- **Where:**
  - 05-storage §5S.3.2 (l.353): "a progress tick or a ledger commit against a run that is now terminal **or stopping** is dropped". Error table (l.1749).
  - 05-tasks §5T.7 (l.1702-1705): "a chunk-ledger commit, checkpoint, Fixer row or mark is re-staged … only if that read still shows the run `running` …, and is otherwise refused, so its chunk … is redone on resume".
- **Evidence:**
  - HEAD refuses a ledger commit only on a **terminal** run (`pebble_store_ops_v3.go:1664-1666`: "its ledger is closed"), and a row mark only on a missing or terminal run (`pebble_store_ops_v3_rowmarks.go:54-56`).
  - Marks are "resume evidence … written as each row's Apply returns" (`keyfamilies.go` `opv3:rowmark:`; rowmarks.go:22-23): the Fixer's apply commits **before** its mark.
  - Quiesce (scan stand-down) and shutdown both move running runs to `stopping`. Under the new predicate every in-flight row's mark is refused, and that row's apply is re-run on resume.
  - At HEAD that happens only in a crash window. Here it happens on every quiesce and every graceful restart, and a Fixer whose apply is not idempotent double-applies. Marks exist because applies are not assumed idempotent.
  - "A tick can never revert a cancel" does not need this predicate: the commit re-reads, and writes the fresh record.
- **Fix:**
  - In both places, replace "terminal or stopping" / "still shows the run `running`" with: "the fresh read shows the run non-terminal under the same `Attempt` and `FenceEpoch` (HEAD's closed-ledger rule); a progress tick alone is dropped on any state change".
  - Add `TestFixerMarksLandDuringQuiesce` (`synctest`: a row applies while a stand-down quiesces the run; on resume that row is skipped) to `taskstest.StoresSuite`.

### DATA7-4 — MAJOR — DATA6-2 half-fixed: the restore exemption ends at `applied`, the lineage is rewritten at Run (3b)

- **Where:**
  - 05-storage §5S.3.7 row "Boot lineage" (l.1004): **restored** applies only while "a restore journal between `swapped` and `applied`".
  - §5S.4 step 14b (l.1701-1706).
  - 05-backup §5B.3.4 step h (l.678): "then journal `applied`, and `inflight.json` is deleted" at P4d step 3.
  - 06 §6.5.2 (3b), after module hooks, workers and (3a).
- **Evidence:**
  - On a restore boot the journal reaches `applied` at P4d step 3. Afterwards the boot still runs P4d step 4 (`BootWrites`), P5–P8, the module start hooks, workers and (3a), and only then (3b) writes `c/storage/boot` and `storage.boot.json`.
  - Suppose the boot fails or crashes in between, for example a module `Register` error, a derived-index validation, a Fatal settings check or a power loss. The next boot is a normal boot. The restored store's `c/storage/boot` is the archive's pair, older than the file's, so the verdict is **behind**.
  - That verdict is a foreign open, so every duty is owed: a `gap` line, sessions revoked, every derived index rebuilt, W restarted. 05-backup (2)(c) also reads it as replaced, so `KindExternal` hooks run with nil coverage: every API key `needs_confirmation`, every epoch bumped.
  - The pending mechanism protects the sidecar identity and the open stamp, but the lineage has none.
- **Fix:**
  - 05-storage step 14b and 05-backup step h: on a restore boot, write `c/storage/boot` (`n = max + 1`, new nonce) in the `marker_written` batch and rewrite `storage.boot.json` before journal `applied`.
  - Alternatively, keep the journal at `hooks_done` (and `inflight.json`) until that boot's (3b) commits, and treat a journal at `hooks_done` or later as "restored" at step 2.
  - Add `TestRestoreBootFailingAfterHooksIsNotReplaced` (fail at P6 after `applied`; the next boot reads same, no hook, no key confirmation).

### DATA7-5 — MAJOR — DATA6-3 half-fixed: the committed-head anchor is rewritten before the hooks of a restore boot

- **Where:**
  - 05-backup §5B.3.6 row "Head in the store" (l.774): the journal component rewrites `backup.location.json`'s `head` "at its next `Append`, on an in-process one-minute tick … and at shutdown", and "P4d step 2 rewrites it after the re-apply". None of this has a restore-boot exception.
  - Step h (l.678) says the file is written on a restore boot "only then", after the hooks.
  - (2)(b) (l.779): "`head_before` is below the file's committed-head anchor ⇒ replaced".
- **Evidence:**
  - On a restore boot, P4d step 2 re-applies the tail into the **restored** store and appends the `restore` line with its head update. Both the "after the re-apply" rewrite and the `restore` line's `Append` then move the anchor to the restored store's head, which is J0+1 (J0 = the journal end).
  - A hook fails; step x closes storage, which triggers the shutdown rewrite, and renames `previous/` back.
  - The original store's head is J0, which is below the anchor. The next boot reads replaced through (b): `KindExternal` hooks, every key `needs_confirmation`, every epoch bumped. That is the DATA6-3 outcome again.
  - The claim "it may lag … never ahead of it" fails on every swap-back.
- **Fix:**
  - In the "Head in the store" row add: "On a restore boot, and whenever a restore journal exists between `swapped` and `applied`, the component does not rewrite the anchor. Step h writes it with the marker, after the hooks. Step x never rewrites it."
  - Alternatively, step c saves `backup.location.json` into `restore/<id>/` and step x restores it, as 14 K22 already says.
  - Extend `TestRestoreFailureSwapsBack`: the anchor equals the original store's head after the swap-back.

### DATA7-6 — MAJOR — Factory reset: HEAD's side effects are unplaced, some run before the restart, and the undo claim is false

- **Where:**
  - 05-storage §5S.3.15 (l.1559-1579), rows "Stage", "Set aside", "Undo".
  - 05-config "A core build's factory reset" (l.1454-1469: books' handler runs `config.ResetToDefaults` and `SaveConfigToDatabase` "after `StageFactoryReset` succeeded and before the restart").
  - 05-storage §5S.3.7 `CheckReader` reason tokens (l.1089-1091).
- **Evidence:**
  - HEAD's `FactoryReset` does four things:
    - wipes the store (`internal/server/handlers/system/handler.go:551`);
    - deletes the Open Library data (`:560`, `deleteOLDataLocked`, `:507-536`);
    - **deletes every entry of the library `RootDir`**, the organized audiobooks (`:563-580`);
    - resets and saves every setting (`:584-589`).

    The spec places only the first and the config-file part. The library wipe and the OL wipe are neither kept, dropped, nor moved after the reset boot.
  - The parts that run before the restart (the settings save, and any library wipe kept in the handler) are not undone when P0a **rejects** the intent: `expires_at` passed, or the updater or `make rollback` swapped the binary first.
  - Nothing stops that swap. `reset/pending.json` is not one of `CheckReader`'s tokens, and the updater and `deploy` refuse only `restore_*`.
  - The result is a store with every book row and default settings (empty `RootDir`, `SetupComplete=false`), and possibly no media files.
  - "Moving `previous/` back undoes the reset" (05-config l.1462; §5S.3.15 "Keyring") is false whenever the media or OL files were deleted.
- **Fix:**
  - §5S.3.15 gains a "Side effects" row. Nothing destructive runs before the restart. Books' handler only calls `StageFactoryReset`.
  - The settings reset is implicit: the fresh store has no blob. The gap-fill file reset moves to a books first-boot hook that runs only when the reset journal reads `applied`.
  - The library and OL wipes either run in that same hook (stated as "not undoable") or are dropped with a release-note line. The owner decides; record it in 14.
  - Add `reset_pending` to `CheckReader`, the updater and `deploy` (refuse; override `ROLLBACK_IGNORE_RESET=1`).
  - Reword "Undo" as "restores the store, its secrets and its settings; files deleted by the side-effect hook are not restored".
  - Add `TestFactoryResetRejectedLeavesStoreAndSettings` and `TestCheckReaderRefusesPendingReset`.

### DATA7-7 — MAJOR — The upgrade-floor check precedes the fresh-install decision; §5S.3.15 relies on a level where the floor checks a version

- **Where:**
  - 05-storage §5S.4 step 3 (l.1613-1614): "the store short of a declared UpgradeFloor version → ErrUpgradeFloor", before step 5's "empty store → FRESH INSTALL" (l.1621).
  - §5S.3.5 `UpgradeFloor` (l.695-705) and rule "Upgrade floor" (l.761: "refused at lifecycle step 3, before the checkpoint").
  - §5S.3.15 "Next boot" (l.1575): "every owner at the build's **level** (books included, so no floor refuses it)".
  - `Migration.Version` "1..n, contiguous per owner" (l.678).
- **Evidence:**
  - From the floor release, books declares `UpgradeFloor{Version: LegacyCap}`. On an empty store, books' version (`preference:db_version` through the version-source adapter) is absent, which is 0. Step 3 runs first, and no empty-store exemption is written, so every fresh install, every factory reset and every staging rebuild is refused with `ErrUpgradeFloor` and exit 78.
  - §5S.3.15's justification mixes up the reader **level**, which stays 0, with the applied **version**, which is what the floor checks.
  - Whether a fresh install passes on its second boot depends on an unspecified detail: whether books' `Migration`s are numbered `LegacyCap+1…` in the version-source rows, or "1..n" as l.678 says. If they start at 1, the fresh-install batch sets books' version below `LegacyCap`, and every later boot is refused.
  - `TestFactoryResetPostFloorBoots` (S2b done-check) cannot pass against the text as written.
- **Fix:**
  - Step 3 (and the `UpgradeFloor` comment): "An empty store (step 5's fresh-install case) is exempt from the floor check; its fresh-install batch records, through the version-source adapter, every books legacy step 1..`LegacyCap` and every registered books step below its lowest Seed step as applied."
  - State that books' versioned `Migration`s are numbered `LegacyCap+1` onwards, contiguous with the legacy track.
  - Reword §5S.3.15 "level" as "applied version".
  - Add `TestFreshInstallPostFloorBootsTwice` (a floor build creates a store, then boots it a second time).

### DATA7-8 — MAJOR — R6-1's keyspace rule makes every `legacy.Extension.Delete` a refused blind write

- **Where:**
  - 05-storage §5S.3.2 (l.356-359): "in a core or module keyspace a `Serialized` sub-prefix is checked like a `CheckedWrites` claim … refuses a `Set` or `Delete` … unless the batch carries an `Expect` on that key". This applies from the first release, with no count-only release (D-S2, l.56).
  - §5S.3.11 "Native-only data" (l.1328): books declares `ext` Serialized.
  - `legacy.Extension.Delete(b *storage.Batch, legacyKey []byte, entryID string) error` (l.1446-1448).
- **Evidence:**
  - `Delete` has no `ctx`, so it cannot call `GetForUpdate`, which takes `ctx`. It has no `seen`, so it cannot stage `Expect` on the bytes the caller read. The only write it can stage is a blind `Delete` in `m/books/ext/`, and that is `ErrBlindWrite`.
  - The callers are not marginal:
    - a key or user delete with its record;
    - the user merge (§5S.3.2 l.367-375);
    - the orphan sweep ("deleted by the next write of that key or by the owner's sweep", l.1329);
    - tasks' `Prune` ("deleted … by `Prune`", 05-tasks l.1585).
  - `TestExtensionStaleAfterPrevWrite` ("a deleted row reads `ExtOrphan` and is swept") cannot pass.
- **Fix:**
  - Change the signature to `Delete(ctx context.Context, b *storage.Batch, legacyKey []byte, entryID string, seen ExtRecord[T]) error`. It stages `Expect(record, seen.raw)` plus `Delete`, and `seen` zero means "no record expected".
  - Name `legacytest.ExtensionSuite`'s case `TestExtensionDeleteIsConditional`.
  - Audit the other module-keyspace writers that §5S.3.2's rule now covers (standalone session and token records, the cut-off record, task extension records) and state per method which `Expect` each stages.

### DATA7-9 — MAJOR — The first S4 r2 boot always owes `auth`, so the new `DeleteLegacySignIns` wipes every legacy session and open invite at the upgrade

- **Where:**
  - 05-authn §4.3 (l.795-805): when `auth` is owed, `BootWrites` "deletes every legacy session and legacy invite the store holds (`DeleteLegacySignIns`, DATA6-4)".
  - 05-storage §5S.3.7 "Served ledger" (l.1008): `auth` is "listed from 09 S4 r2, never by the dark S4 r1", and owed when "the previous `core` record lacks the duty".
  - 05-authn §11 (l.1665): legacy cookies created before the cut-off are "upgraded on use".
  - 09 §9.2.1 rows 2–3 (l.150-151).
- **Evidence:**
  - Every build before S4 r2 omits `auth` from its served record, including the S0 guard and S2b–S4 r1. So the first S4 r2 boot always records `owed.auth`, and its `BootWrites` deletes every legacy `sess:` row and every open legacy `invite:` row before any listener opens.
  - `TakeLegacySession` and `TakeLegacyInvite` (§3.4) therefore never see a production session or invite. Every browser is signed out by the upgrade itself, and every HEAD-issued invite still open is void.
  - The fallback (code removed "after S4 + 7 days", l.1665) is dead on arrival, and `TestSession_LegacyRawULIDCookieUpgradesOnUse` only tests a fixture.
  - Neither cost is in §9.2.1's "one complete list". Row 3 voids only "invites that exist only in the new build".
- **Fix:**
  - 05-authn §4.3: "`DeleteLegacySignIns` runs only when the store's session cut-off record exists and predates `Owed.Since`, that is, after an interval that followed the first r2 boot. On the boot that writes the cut-off it deletes nothing; the cut-off itself refuses later legacy sessions."
  - Alternatively, if signing everyone out at S4 r2 is accepted, add a §9.2.1 row ("the S4 r2 upgrade signs every browser out once and voids open invites") and delete the upgrade-on-use path and its tests.
  - Add `TestFirstR2BootKeepsLegacySessions`.

### DATA7-10 — MAJOR — The "missing cut-off" replaced-store signal has a crash window, and the one classifier does not contain it

- **Where:**
  - 05-authn §3.4 `legacy.AuthSignIns` comment (l.625-633), §4.14 (l.1300-1301), §6 row (l.1404: "content marker, out-of-store journal head, lineage, **missing cut-off**"), `TestMissingCutOffAfterServedIsReplacedStore` (l.1532). 14 K22.
  - 05-backup §5B.3.6 (2) (l.779) lists only (a)–(d).
  - 05-backup row "Content marker" (l.775): the pair is written "at the end of P4d step 2".
- **Evidence:**
  - The cut-off is written "once by the first r2 boot" (05-authn l.1665), at a step not stated. Its natural home is `BootWrites`/`Reconcile`, at P4d step 4.
  - The same boot writes `served_boot` into the store and into `backup.location.json` at the end of step 2.
  - Suppose step 3 or step 4 fails (a Fatal identity decode, which is exactly the risk S4 r1's census exists for) or the boot crashes. The next boot finds user rows, no cut-off, and a pair in the file, so it is "replaced": `KindExternal`, every key `needs_confirmation`, every epoch bumped.
  - The signal adds no detection: a store restored in place from a pre-r2 archive has **no pair while the file has one**, which (2)(a) already classifies as replaced.
  - DATA6-5 required one classifier, but 05-authn keeps a fifth input that 05-backup (2) does not have.
- **Fix:**
  - Delete the "user rows but no cut-off record" signal from 05-authn §3.4, §4.14, the §6 row and its test, and from 14 K22. Cite (2)(a) for the pre-r2-archive case.
  - If it is kept, move it into 05-backup (2) and require that the first r2 boot stage the cut-off in the same batch as its first `served_boot` pair (step 2's marker batch, through the identity store).

### DATA7-11 — MAJOR — Every read-write open outside a serve boot is a foreign open; no guard-aware offline open exists

- **Where:**
  - 05-storage §5S.3.7 row "Pebble open stamp" (l.1010): every read-write `pebble.Open` writes a higher `OPTIONS-<n>`, and one not recorded by a boot "means another binary opened the store … a **foreign open**". Error table (l.1735).
  - 05-authn and 05-rbac: the offline `<app> users reset-password` (05-rbac l.1216-1222).
  - 04 `cmd` row (l.374: no file of `cmd` moves); 04 l.379 (`pebble-inject-skip` opens Pebble directly).
- **Evidence:**
  - Verified against `cockroachdb/pebble/v2@v2.1.7` `open.go:572-600`: every read-write open renames a new `OPTIONS-<n>` into place.
  - Read-write opens outside `serve` exist and stay:
    - core's offline `users reset-password`, which must write the user row;
    - ABO's `scan`, `playlist`, `tag`, `organize` (`cmd/root.go:120,152,177,213`) and `seed` (`cmd/seed.go:95`);
    - `diagnostics query`/`cleanup-invalid`/`reserved-prefs` (`cmd/diagnostics.go`; `query` opens without `ReadOnly`, §5S.13);
    - `cmd/pid-census/main.go:53`;
    - `cmd/pebble-inject-skip/main.go:35`;
    - the `Isolate` child mode (`cmd/child_mode.go:71`, unused at HEAD).
  - Each such run makes the next serve boot pay the full round:
    - a `gap` line, so every later restore across it gets nil coverage and every key `needs_confirmation`;
    - all native sessions revoked, and with DATA6-4's change all legacy sessions too;
    - every derived index rejected and rebuilt (hnsw is library-scale; a `Blocking` index holds readiness);
    - the W window restarted.
  - The spec does not say how the ABO subcommands open the store after S2b, when `NewPebbleStore(path)`/`InitializeStore` are deleted (§5S.9.3 l.1986), nor whether they honour claims.
- **Fix:**
  - Add a §5S.4 "Offline open" procedure, `kernel.OpenOffline` (P−1–P3 read-only checks, guard, floor, claims, P4 migrations refused). At a clean close it commits one batch that promotes its own `OPTIONS-<n>` and sidecar identity as the confirmed records, keeps `last` and the lineage unchanged, and records an owed `journal` duty only if it committed a revoking write without journaling it.
  - Core's offline commands and every ABO subcommand that writes from S2b use it.
  - `diagnostics query` opens `ReadOnly` (fold the §5S.13 defect in).
  - `pebble-inject-skip` and `pid-census` are documented as foreign opens, costing one round of fallbacks.
  - Add `TestOfflineOpenIsNotForeign` and `TestCLISubcommandsUseOfflineOpen` (ABO).

### DATA7-12 — MAJOR — The checkpoint-restore recovery path contradicts `CheckReader` and is undone by the failing build

- **Where:**
  - 05-storage §5S.3.12 "Restore" (l.1488): "It performs the journaled directory swap … directly with kind `KindCheckpoint` … its result is then **usable by `make rollback`** to the previous binary."
  - §5S.3.7 `CheckReader` (l.1084-1091): refuses while "a restore journal `restore/<id>/` holds an `inflight.json` … A swap that has started always refuses".
  - 05-backup step g (l.677: the DAG "bring[s] an older archive forward … every versioned step the archive's store lacks runs again") and step x (l.679: "a failing migration … swap back").
- **Evidence:**
  - The documented recovery for a failed migration is: stop the service, run `restore-checkpoint`, then `make rollback`.
  - The offline swap leaves the journal at `swapped` (the hooks need a boot), so `make rollback`, `deploy` and the S8 updater's `Boot` refuse with `restore_inflight`.
  - If the failing binary boots next (systemd restarts it every 5 s, `deploy/audiobook-organizer.service:81-82`), its restore boot re-runs the same failing step on the checkpoint and swaps it back, reinstating the half-migrated store.
  - If the operator overrides and starts a pre-S2b build, that build ignores the journal. A later core build then resumes a stale `swapped` journal and runs restore hooks long after the fact, and every updater apply in between refuses.
- **Fix:**
  - 05-storage §5S.3.12 and 05-backup:
    - **(1)** for an **open** checkpoint (the failed-attempt case: no boot reached readiness since `taken_at`, §5S.3.12 reuse rule), the offline tool finishes the journal to `applied` with no hooks, because nothing was served since. It deletes `inflight.json` and logs it;
    - **(2)** for `--accept-loss` on a completed checkpoint, it leaves the restore boot with hooks, and `CheckReader` passes a `KindCheckpoint` journal at `swapped` for a candidate whose levels and floors admit the restored store;
    - **(3)** a `KindCheckpoint` restore boot whose DAG fails does not swap back: it keeps the restored store and exits non-zero, because swap-back only brings back the broken store.
  - Add `TestRestoreCheckpointThenRollback` and `TestCheckpointRestoreBootFailingStepKeepsCheckpoint`.

### DATA7-13 — MINOR — Fresh-install one-shots are recorded applied without HEAD's markers

- **Where:** 05-storage §5S.9.1 row 7 (l.1888) and 09 S2b "Data/keys" (l.900): the three marker one-shots become books `Migration`s "each writing HEAD's marker so an older build skips it too", "**recorded applied on a fresh install**".
- **Evidence:**
  - Recorded-applied steps never run, so a store created fresh on S2b+ has no `embed_cover_art_default_on_migrated`, `log_retention_zero_migrated` or `maintenance_window_migrated` marker.
  - A rollback to a pre-S2b build runs HEAD's load, which drops a stored `embed_cover_art=false` and saves the blob (`internal/config/persistence.go:1111-1129`), and also rewrites `log_retention_days=0` (`:1088-1106`).
  - The setting is still lost after the roll-forward.
- **Fix:** Make the three steps `Seed: true` (Additive; they run on a fresh install and only write the markers, because the blob holds no stale value), or have the fresh-install batch stage the three markers through books' version-source adapter. Add `TestFreshInstallWritesHEADMarkers`.

### DATA7-14 — MINOR — `backup.location.json` has three writers and no single-writer rule

- **Where:**
  - 05-backup row "Where" (l.772: rewritten "by every journaling boot …, by `Create` … and by the committed-head anchor").
  - Rows "Head in the store" and "Content marker" (l.774-775).
- **Evidence:**
  - `Create` (a task) and the anchor (a tick, an `Append`, shutdown) each rewrite the whole JSON from their own read.
  - The anchor can therefore rewrite `{P, H′}` over Create's `{P′, H}`: the file reverts to P while the store holds P′. The next boot reads "store ahead, rewrite", which is benign, but an in-place restore of that night's archive (which carries P) then reads "same".
  - This is the DATA5-20 class (`config.yaml`), unresolved here.
- **Fix:** One owner (the journal component) holds a mutex across read-modify-write of the file. `Create` hands its pair to the owner instead of writing the file itself. Add `TestLocationFileWritersSerialized`.

### DATA7-15 — MINOR — The blind-write ≥ 14-day gate is missing from 09's S4 r2, S5 and S7 done-checks

- **Where:**
  - 05-storage D-S2 (l.56) and §5S.9.4 item 2 (l.2010-2013): the flag refuses "from the next release, once that stage's done-check shows zero persisted blind-write violations for ≥ 14 days".
  - 09 states this only for S3 (l.922, l.930) and the config row (l.376).
  - S4's (l.948), S5's (l.965) and S7's (l.993) done-checks name neither the gate nor `TestS4r2APIKeyCheckedWrites`, `TestT0T1RunRowsCheckedWrites` or `TestS7RootStatisticsCheckedWrites`.
- **Fix:** Add, to each of those three "Done when" cells: "zero persisted `op=blind` violations for row 3 / rows 11–13 / rows 19's `import_path:` and `aijob*` for ≥ 14 days of production, and `Test…CheckedWrites` green in count-only and strict mode". The next release's strict flip is the dial step that 09 S2b already uses for claims.

### DATA7-16 — MINOR — DATA6-4 residue: the pre-S8 archive is still ABO's, so its restore is foreign with nil coverage, and P′ starts only at S8

- **Where:**
  - 05-backup "Stages" (l.25-35: `backup` lands at S8) and B7 (l.74: `legacy-abo/0` restores as foreign).
  - §5B.3.5 step 4 (l.704-709, P′).
  - 09 S4 R-test cell (l.948: "the content marker is older … also when the archive was taken during the last journaling boot").
  - `TestOlderBuildInPlaceRestore_SameDirDetected` (05-backup l.1024).
  - 14 K22 ("every core-made archive carries a pair older than the file").
- **Evidence:**
  - From S4 r2 to S7 the journal and the marker exist, but archives are made by ABO's `internal/backup` (HEAD format, `legacy-abo/0`).
  - (1) Every staged restore in that window is a foreign restore: it needs `core:admin` and owner proof, and gets nil coverage, so every API key `needs_confirmation`. That window can be months, and the cost is not in 09 §9.2.1.
  - (2) Those archives carry the current pair, so the S4 cell and the test's "content marker is older" do not hold. Only (2)(b) catches the case, and only if a revocation followed the archive.
- **Fix:**
  - Add a §9.2.1 row (S4): "until S8, restoring any archive needs every API key re-confirmed".
  - Optionally, have ABO's create from S4 r2 write `JournalSeq` and `ServedBoot` into a sidecar and advance P′, as §5B.3.5 step 4 does.
  - Reword the S4 cell, the test and K22 to "the head is below the anchor (from S8 also the marker)".

### DATA7-17 — MINOR — Factory-reset bookkeeping: owners disagree, the lineage verdict is misstated, the keyring rewrite has no trigger

- **Where:**
  - 06 §6.5.1 P0a (l.1536): P0a "sets the revocation journal aside per DATA4-15 and rewrites the lineage and location files".
  - 05-storage §5S.3.15 "Journal and lineage" (l.1576): P4d step 2 sets the journal aside; `storage.boot.json` is "rewritten at Run (3b)"; "its verdict is **none**".
  - 05-config l.1463-1465: "the reset boot, and only it, rewrites the keyring header's `install_id`".
- **Evidence:**
  - P0a cannot locate the journal, because `core.backup.dir` is a DB override attached at P4d step 1, and it cannot know the new `install_id`, which is chosen at step 5.
  - At step 2 the empty store has no marker, so the lineage file is compared by `db_path` alone. "Store has no record while the file has one" gives **behind**, not none (l.1004). The effect is a benign round of fallbacks, but the stated verdict is wrong.
  - Nothing tells `secrets.Open` that the boot is the reset boot, because the reset journal's lifetime past P0a is unstated.
  - `StageFactoryReset` and stage-restore do not refuse each other, so both intents can be pending at once.
- **Fix:**
  - Delete the journal and lineage clause from 06 P0a and cite §5S.3.15.
  - In §5S.3.15, say that P0a deletes `storage.boot.json`, so the verdict is none.
  - Keep `reset/<id>/journal.json` at `set_aside` until the reset boot's P4d step 1 has rewritten the keyring header, then mark it `applied`.
  - `StageFactoryReset` refuses while a restore is pending or in flight (`ErrRestorePending`), and stage-restore refuses while a reset is pending.

### DATA7-18 — MINOR — Name and phase drift

- 05-storage l.1010: the open stamp says "a number **above** the record's `num` that is not its `pending`" ⇒ foreign. A directory whose highest `OPTIONS-<n>` is **below** `num` is a replaced directory that this rule passes. Say "differs from both `num` and `pending`".
- 06 P0a (l.1536) says "so **P4b** reads its own rewrite" and records only sidecar identities. 05-storage step 2 compares at P2, and 05-backup step e also records the highest `OPTIONS-<n>`. Align 06 with 05-backup step e.
- 14 K22 says a failed-hook restore "**restores** `backup.location.json`". 05-backup chose "written only after the hooks" (step h). Use 05-backup's wording.
- The `TouchAPIKey` skip predicate has three forms:
  - "absent, revoked or **disabled**" (05-storage row 3, l.1883);
  - "absent, revoked or **inactive**" (05-authn l.576, l.883);
  - "absent, revoked or **epoch-stale**" (§5S.9.3 l.1988).

  05-authn §3.4 is the owner: copy its wording.
