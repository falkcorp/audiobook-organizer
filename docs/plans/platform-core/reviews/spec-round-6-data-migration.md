<!-- file: docs/plans/platform-core/reviews/spec-round-6-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d63258a-e749-4453-96b8-8c2cd31dafb4 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6: DATA, STORAGE AND MIGRATION (`DATA6-*`)

This is an antagonistic review of `docs/specs/platform-core/` after the round-5 revision: 03 v1.8.1,
05-storage v1.6.1, 05-backup v1.5.1, 05-authn v1.6.1, 09 v1.5.1. R3-1, R4-1/R4-2 and R5-1 … R5-7 are binding.
None of them is attacked as a design. A finding attacks the text only in three cases: it does not carry a
resolution through, two files disagree, or it breaks against code at `748fd787a`.

Spec anchors take the form `file §section (l.N)`. Code anchors are `path:line` at `748fd787a`.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | DATA6-1 |
| MAJOR | 7 | DATA6-2 … DATA6-8 |
| MINOR | 6 | DATA6-9 … DATA6-14 |

| ID | Sev | Title |
|---|---|---|
| DATA6-1 | BLOCKER | DATA5-1 half-fixed. Only `Transition`/`Insert`/`Advance` get commit-time checks. Progress ticks, the chunk-ledger commit and the other run writes still do HEAD's `opsMu` read-check-write, now staged blind, so they revert a concurrent cancel or terminal transition |
| DATA6-2 | MAJOR | Every staged restore (archive, revert, checkpoint) reads as a foreign open and a behind lineage. P4d step 2 then appends a `gap` line before the hooks run, so coverage is nil and every API key needs confirmation |
| DATA6-3 | MAJOR | A restore whose hook fails swaps back, but `served_boot`, `backup.location.json` and the `restore` line still describe the restored store. The original store then reads as replaced, or its aborted restore is "resumed" |
| DATA6-4 | MAJOR | SEC5-6 half-fixed. The content marker is written once per boot, so it misses the common case: an archive taken during the last journaling boot. 09's R-test cell, 05-backup's test and K22 claim detections the design cannot make |
| DATA6-5 | MAJOR | DATA5-7 half-fixed. 06's normative P4d step 2 and 05-authn §4.14/§6 still classify "generation differs ⇒ replaced". 06 omits `served_boot`. 05-backup ignores the lineage verdict that 05-storage says it uses |
| DATA6-6 | MAJOR | Factory reset from S2b is unspecified. HEAD's `Reset` cannot run under claims: strict mode gives a 500, the shadow release wipes `c/` and then fails the next boot with `ErrForeignKeys`, and after the floor release a reset store is refused |
| DATA6-7 | MAJOR | The W gate restarts at `since`, the **start** of the unrecorded interval, so the days a rollback build ran count as clean. The restart point is not persisted once `owed.usage` is cleared |
| DATA6-8 | MAJOR | Coalesced last-use writers write whole records through `PutSession`/`PutAPIKey` with no precondition. A flush after a logout or revoke re-creates the session or reverts the key's status |
| DATA6-9 | MINOR | `gen` is one owed entry per owner, cleared by the first index's rebuild enqueue, so other derived indexes accept stale artifacts. A crash after the enqueue accepts them too. `owed` is one map key that several consumers rewrite |
| DATA6-10 | MINOR | DATA5-13's install-id inheritance contradicts 05-backup's DATA4-15 error row and test. K22 names the wrong source (journal header, not the lineage file) |
| DATA6-11 | MINOR | DATA5-11 residue: 05-config says `Open` runs the load **without** the corrupt-secret repair, but 05-storage row 7 and 09 S2b call that repair a blind writer converted at S3 |
| DATA6-12 | MINOR | `CheckedWrites` timing: the flags get no discovery period, the "secret save persists" check sits at S2b where no flag exists, and no S3/S7 check exists. The generator emits by stage, but the conversion is "in the PR" |
| DATA6-13 | MINOR | 05-authn's replaced-store rule depends on "the state directory recorded an r2 boot", but no file records that |
| DATA6-14 | MINOR | Drift after DATA5-4/R5-6: 03 (users confirmable), 09 §9.8 (every stale extension suspended), 05-tasks ("default guarded projection"), and 05-authn §4.3 ("this boot's start") vs 05-storage (`detected`) |

## Regression check (round-5 BLOCKER/MAJOR, this lens)

| Round 5 | Status in round 6 | Residual |
|---|---|---|
| DATA5-1 (B) legacy CAS families not `Serialized` | **Half-fixed.** Rows 9, 11, 13 and 19 (`aijob*`) are `Serialized` (05-storage §5S.9.1 l.1729-1740), and `TestABOClaimsMatchOwnershipTable` cross-checks them. But only `Transition`, `Insert` and `Advance` are given commit-time checks (05-tasks l.1123, l.1383-1395). Every other run write that HEAD ran under `opsMu` is a stale read-modify-write once staged | DATA6-1 |
| DATA5-2 (M) whole-row key hash | **Fixed** (05-storage row 3, 05-authn §4.4/§11: the guarded projection excludes `LastUsedAt`/`LastUsedIP`/`UseCount`) | DATA6-8 (the new coalesced last-use write path) |
| DATA5-3 (M) revoked-on-prev re-enableable | **Fixed** (05-authn §4.4 stale rule: monotone; one sanctioned exit from `"revoked"`) | — |
| DATA5-4 (M) three meanings of `Suspended` | **Fixed** (`Extension.Widening`, `auth.KeyConfirmer`) | DATA6-14 (03 and 09 §9.8 text) |
| DATA5-5 (M) `mck_` location, `inactive` empty-scope row | **Fixed** (`["none"]`; no `LegacyFullScope` with an extension; 09 row 4 aligned) | — |
| DATA5-6 (M) served ledger overwritten before consumers | **Fixed in shape**: `owed` entries plus the Run (3b) commit (05-storage l.978, l.1542). Three consumers are wrong: the usage anchor, the `gen` scope, and spurious owed entries on restore | DATA6-2, DATA6-7, DATA6-9 |
| DATA5-7 (M) relocation classified replaced | **Half-fixed.** 05-backup l.732 and 14 K22 are updated. 06 P4d step 2 (normative), 05-authn §4.14 (l.1198) and §6 (l.1276) still say "different generation ⇒ replaced". The "gap line" evidence does not exist yet when step (2) classifies | DATA6-4, DATA6-5 |
| DATA5-8 (M) scheduler hand-over | **Fixed** (row 9 now states 05-tasks' rule and `TestOneSchedulerPerKind`) | — |
| DATA5-9 (M) S7 `Local` on unvetted URLs | **Fixed** (`PendingLocal`; nothing granted unconfirmed; re-runs after an old-archive restore) | — |

Round-5 MINORs:
- Fixed: DATA5-10 (`mc-sentinel:`), DATA5-12, DATA5-14 (`seen`), DATA5-15, DATA5-17, DATA5-18, DATA5-19 (row 15) and DATA5-20.
- DATA5-11 is fixed in 05-config but contradicted elsewhere (DATA6-11).
- DATA5-13 is fixed with a stated residual, but contradicts 05-backup (DATA6-10).
- DATA5-16 is fixed except for 05-tasks' "default projection" (DATA6-14).

## Simulations

**(a) Forward S0 → S10.**
- S0–S2 and S2b are clean apart from the factory reset (DATA6-6).
- S3: `CheckedWrites` turns on with no discovery period (DATA6-12).
- S4: the coalesced last-use writes (DATA6-8).
- S5 (T0+T1):
  - progress ticks and ledger commits race with transitions (DATA6-1);
  - `gen` is consumed by one index only (DATA6-9).
- S7: `import_path:` `CheckedWrites` (DATA6-12).
- S8: every staged restore is uncovered (DATA6-2).

**(b) Rollback by binary swap and roll forward, at each stage.**
- Rollback costs match §9.2.1 rows 1–15.
- The W gate counts the days the rollback build ran (DATA6-7).
- A run that the current build cancelled can be resumed by the older build, because the v2 mirror row was reverted (DATA6-1).

**(c) Stepping-stone path.**
- Correct: floors are checked on `.storage-applied` before apply, at boot, and at staging (09 §9.2.2).
- A factory reset on a post-floor build leaves books at version 0, so the boot refuses with `ErrUpgradeFloor` (DATA6-6).

**(d) Old archive restore.**
- Staged restore (S2b+), boot sequence:
  - P0a step e rewrites `.storage-format`;
  - step 2 then sees a foreign open, and a behind lineage for any archive older than the last core boot;
  - so the journal, auth, gen and usage duties are owed;
  - P4d step 2 (5) appends a `gap` line;
  - P4d step 3 runs the hooks with `Revocations` nil (DATA6-2).
- A hook failure followed by a swap-back poisons the original store (DATA6-3).
- HEAD restoring in place an archive taken during the last journaling boot:
  - the marker, lineage and generation all match;
  - the store is classified "same store, lagging head", so no hooks run;
  - the 09 S4 R-test cell expects the opposite (DATA6-4).
- A pre-S0 archive restored in place inherits the install id, as fixed by DATA5-13. An older build's factory reset now looks the same (DATA6-10).

**(e) Crash at each boot phase, Run (3b) included.**
- P−1 to P3 write nothing.
- P4a: the fresh-install batch is atomic.
- P4b step 9: `owed` and the pending identity survive a later failure.
- P4d step 2: `served_boot` is committed before the file, so a crash in between reads "ahead" (benign).
- Run (3a): a crash after the `gen` rebuild enqueue accepts the stale artifact on the next boot (DATA6-9).
- Run (3b): a single batch, then the lineage file; a crash in between reads "ahead" (benign).
- Run steps 4–8: idempotent consumers.
- Failed restore boot: P4d step 2's marker write before step 3's hooks is the defect in DATA6-3.

**(f) Concurrent writers on `Serialized` families.**
- Identity `Take*`, `Extension.Put` with `seen`, clocks and `counter:import_path`: safe.
- Run progress, ledger and checkpoint versus `Transition`: unsafe (DATA6-1).
- Session `LastSeen` and API-key last-use flush versus revoke or logout: unsafe (DATA6-8).
- The `owed` map rewritten by several consumers (DATA6-9).

---

## Findings

### DATA6-1 — BLOCKER — DATA5-1 half-fixed: the run store's other read-modify-writes lose a concurrent transition

- **Where:**
  - 05-tasks §5T.3.13 `RunStore` comment (l.1123): "Preconditions (Transition, Insert's uniqueness) are checked at commit". `Progress(ctx, b, id, done, total, current)` (l.1136), `SaveCheckpoint`, `SaveChunk` and `Strike` have no stated precondition.
  - §5T.6.2 "What changes in HEAD's code: the compare-and-set mechanism" (l.1383-1395) names only `Transition` and `Advance`.
  - §5T.7 (l.1551-1554). 05-storage §5S.9.1 rows 11 and 13 (Serialized, **not** `CheckedWrites`).
- **Evidence:**
  - HEAD serialises **every** run write on `opsMu`, not only transitions:
    - `UpdateOpProgressV2` reads the whole `opv2:op:` row (Status included), sets the progress columns and writes the row back under `opsMu` (`internal/database/pebble_store_ops_v2.go:718-751`). Result-data, checkpoint and resume-count writers do the same (`:337`, `:451`, `:517`, `:606`, `:687`, `:704`, `:769`, `:797`).
    - `CommitLedgerV3` takes `opsMu`, loads the run, **refuses a terminal run** ("its ledger is closed") and then `put(cur, &next)` from that read (`pebble_store_ops_v3.go:1655-1666`, `:1711-1721`).
  - 05-tasks keeps "HEAD's own ops storage code" and stages it into the caller's batch, so `opsMu` no longer spans the commit (05-tasks l.1385-1388 says this itself).
  - An HTTP cancel's `Transition` can therefore commit between a progress tick's read and its commit:
    - On the v2 path (the `opsbridge` adapter until T8), the tick then writes `Status=running` back over the cancel in the `opv2:op:` row, and the paired v3 stage follows it.
    - On the v3 path, the tick writes `opv3:prog:` with the stale `V2RowHash`. At the next boot `Recover` (HEAD's `ReconcileOpsV3WithV2`) rebuilds the v3 record from the v2 row. `stopping` mirrors as `running` (`pebble_store_ops_v3.go:52-56`), so a cancel request is undone.
  - A ledger commit that raced a terminal transition writes ledger, checkpoint and marks onto a closed run, which HEAD forbids.
  - After a rollback, the older build reads only the v2 rows and resumes the "running" run that the user cancelled.
  - 05-storage §5S.3.2 (l.338) and 09 §9.9 say generically that read-modify-writers of `Serialized` keys use `GetForUpdate`. 05-tasks' explicit, narrower list overrides that for the one store that matters.
  - `TestLegacyRunTransitionVsCancelOneWins` races two `Transition`s and cannot see this.
  - The native store has the same hole: `c/tasks/run/<id>` holds the whole `RunRecord` and is "Serialized: Transition and Insert check at commit" (05-tasks §5T.6.1).
- **Fix:**
  - In 05-tasks §5T.3.13 and §5T.6.2, add: "Every `RunStore` method that stages a write derived from a run it read stages `GetForUpdate` on that run's record — books: the `opv3:run:` record **and** the paired `opv2:op:` row; native: `c/tasks/run/<id>`. This covers `Progress`, `SaveChunk` (HEAD's `CommitLedgerV3`, whose terminal check becomes that `Expect`), `SaveCheckpoint`, `PutRow`/`MarkRow`, `Strike`, `Debounce` and result-data writes."
  - `ErrConflict` on a progress tick re-reads and retries. A tick against a terminal run is dropped with `ErrRunConflict`.
  - Prefer making the run-row families `CheckedWrites` from T0+T1, so storage enforces the rule. Steps 62, 65 and 66 already move into `internal/books/taskstore` in that PR and can stage `Expect`, which removes the stated reason for not doing so (05-storage row 11).
  - Add `TestLegacyProgressVsCancelKeepsState` and `TestLedgerCommitAfterTerminalRefused` (`synctest`, books' store and native) to `taskstest.StoresSuite`.

### DATA6-2 — MAJOR — Every staged restore reads as a foreign open, so its own `gap` line makes it uncovered

- **Where:**
  - 05-storage §5S.3.7, rows "Boot lineage" (l.974) and "Foreign-open record" (l.979).
  - §5S.4 steps 2 and 9 (l.1497).
  - 05-backup §5B.3.4 step e (l.636).
  - §5B.3.6 "Boots without the journal duty" (l.730), "Boot re-apply" (5) (l.732) and "Coverage at restore" (l.733).
- **Evidence:**
  - P0a step e "rewrite[s] … for books `.storage-format`" from the staged store. That is a new file with a new inode and ctime. P0a writes nothing to the store, so the restored store's `c/storage/sidecar_identity` still names the archive boot's sidecar. Step 2 sees a mismatch, which is a foreign open (fail-closed).
  - Independently, any archive taken before the last core boot carries a lower `c/storage/boot.n` than `storage.boot.json`, which reads **behind**, and behind counts as a foreign open (l.974).
  - Either way, step 9 records every duty as owed, with `since` = the archive boot's `last.at`.
  - P4d step 2 (5) then appends `gap {since, reason: non_journaling_build}`, because no `gap` covers that `since`. Step 3's hooks follow. "Coverage at restore" yields `Revocations` only when "no gap … lies after Boundary".
  - So the restore being applied gets nil coverage if `Revocations` is read at step 3. In any case, every later revert of it (`Boundary` = its `staged` head) and every later restore of an older archive does.
  - The 05-authn §4.14 step 3 consequence: every `active`/`disabled` API key is set to `needs_confirmation` after **every** staged restore, revert and `storage restore-checkpoint`.
  - The same spurious owed entries also restart the W window and revoke native sessions a second time.
  - 05-storage already exempts its own rewrites elsewhere: "Every level step records the identity of the `.storage-format` it just rewrote … so a crash … never reads as a foreign open" (l.979). P0a's rewrite has no such record.
- **Fix:**
  - At step e, restoreswap records the full stat identity of the `.storage-format` it wrote in `restore/<id>/journal.json` (step `sidecars`).
  - On a restore boot (journal between `swapped` and `applied`), 05-storage step 2:
    - compares the sidecar against that identity;
    - gives the lineage verdict `restored`, never `behind`;
    - records owed duties only from the restored store's own evidence (its `last` lacking a duty, or its own `owed` entries), never from the swap.
  - State in 05-backup that a restore boot appends no `non_journaling_build` gap for its own swap.
  - Add `TestStagedRestoreKeepsCoverage` (intact journal: only keys revoked after the archive are revoked, no `gap` line) and `TestCheckpointRestoreAppendsNoGap`.

### DATA6-3 — MAJOR — A restore that fails in its hooks swaps back, but leaves the restored store's marker and journal lines behind

- **Where:**
  - 05-backup §5B.3.6 "Content marker" (l.728) and "Boot re-apply" (5) (l.732): `served_boot`, `backup.location.json` and the `restore` line are written at the end of P4d step 2. Also l.732: "A `restore` line without its `c/backup/restore_hooked/<seq>` record is resumed by the next boot".
  - §5B.3.4 step x (l.640): a failing hook swaps back. `TestRestoreFailureSwapsBack` (l.957).
- **Evidence:**
  - On a restore boot, P4d step 2 does all of this before step 3's hooks:
    - writes `served_boot = {n+1, nonce}` into the **restored** store and then into `backup.location.json`;
    - records the restored store's generation;
    - appends the `restore` line.
  - A hook then fails, and step x renames `previous/` back. The original store's pair `{n}` is now older than the file's `{n+1}`, so the next boot classifies the **original** data as replaced:
    - `KindExternal` with `Revocations` nil;
    - every key set to `needs_confirmation`, every epoch bumped, every session and invite deleted.
  - The generation rule reaches the same verdict: the recorded generation is the restored store's, and the journal now holds a `restore` line after `head_before`.
  - Independently, the original store has no `restore_hooked/<seq>` for that line, and the text says such a line "is resumed by the next boot". Restore hooks would run on the original data with a `Previous` that no longer exists. The hooks "fail boot on error" (05-authn §4.14), which risks a crash loop.
  - `TestRestoreFailureSwapsBack` only asserts that the original data boots.
- **Fix:**
  - On a restore boot, write `served_boot` and the `backup.location.json` generation and pair only after step 3's hooks commit (journal `hooks_done`). Alternatively, P0a saves `backup.location.json` into `restore/<id>/` at step c and step x restores it.
  - Step x appends (or the next boot appends, once the keyring is open) a `restore_aborted {seq}` line.
  - A `restore` line whose restore journal is `swapped_back`, or that has a `restore_aborted` line, is neither resumed nor journal evidence.
  - Extend `TestRestoreFailureSwapsBack`: the next boot is "same store", runs no hook, and no key needs confirmation.

### DATA6-4 — MAJOR — SEC5-6 half-fixed: the per-boot content marker misses the common in-place restore, and three texts claim otherwise

- **Where:**
  - 05-backup "Content marker" (l.728): the marker is written once per journaling boot, and "the archive carries [the pair] from the boot before it was taken".
  - `TestOlderBuildInPlaceRestore_SameDirDetected` (l.973). 09 S4 "Done when" R-test cell (l.876): "on prev, back up, revoke a key, restore that backup in place; roll forward: keys need confirmation, legacy sessions are deleted (SEC5-6)".
  - 14 K22 (l.42): "This catches the main case".
  - 05-backup l.732 (2): "A non-journaling build's restore always leaves a `gap` line …, so it cannot pass for a relocation".
- **Evidence:**
  - An archive carries the pair of the boot **during** which it was taken. A nightly archive taken during the last journaling boot (the usual "restore last night's backup after a rollback") therefore carries the current pair. The lineage reads `same` too, because (3b) of that boot precedes the archive. An in-place restore keeps the generation.
  - The boot is classified "same store, head behind": the tail is re-applied, **no hook runs**, no key needs confirmation, and legacy sessions in the archive stay accepted by upgrade-on-use.
  - So `TestOlderBuildInPlaceRestore_SameDirDetected` (archive on cur, revoke, rollback, restore) fails unless the test inserts an extra boot. The 09 cell, which the 05-backup history says was reworded, was not reworded: an archive taken on prev "after the last journaling boot" is 05-backup's own stated-undetectable residual.
  - The case also drops the fallback 05-authn §4.14 exists for: "a journal truncated after the archive's boundary verifies cleanly". The store that anchored the head is gone, yet the boot is treated as covered.
  - The "gap line" evidence is appended at (5), after (2) classifies, so on the boot that first sees the rollback no such line exists yet.
- **Fix:**
  - Anchor the journal's committed end outside the store. `backup.location.json` gains `head {seq, mac}`, rewritten (fsync) after each revoking commit; it may lag but is never ahead.
  - At (2), a store whose `head_before` is below the file's `head` is **replaced**, whatever its marker. This catches the archive-of-current-build case regardless of boot boundaries.
  - At (2), count this boot's owed `journal` entry or its foreign-open verdict as journal evidence (not only lines already in the journal).
  - Reword the 09 S4 cell to the detectable case (archive on cur, revoke on cur, roll back, restore in place), and correct K22's "main case" sentence.
  - Have the owed-`auth` consumer also call `DeleteLegacySignIns`, so the undetectable residual cannot honour pre-cut-off legacy sessions.

### DATA6-5 — MAJOR — DATA5-7 half-fixed: the normative boot table and 05-authn keep the old classifier

- **Where:**
  - 06 §6.5.1 P4 row (l.1423), P4d step 2: "a store whose generation or directory identity differs from the one `backup.location.json` records is a **replaced store** … otherwise it is the same store". No `served_boot`, and only "records this store's generation".
  - 05-authn §4.14 bullet 2 (l.1198): "Different generation with no staged restore … runs this hook with `KindExternal`". §6 (l.1276): "Replaced store (different generation, …)".
  - 05-storage §5S.4 step 11(b) (l.1514): "classify the boot by store generation".
  - 05-storage l.974: "A **behind** verdict … and 05-backup classifies the store as replaced".
- **Evidence:**
  - 06 declares itself the only normative boot statement (l.1362-1366). Its rule runs install-wide hooks on a relocated store, which is exactly DATA5-7. It also never writes the SEC5-6 marker, so an implementation of 06 has no content marker.
  - 05-backup's step (2) lists the marker and generation-plus-journal evidence, but not the lineage verdict that 05-storage says feeds it.
- **Fix:**
  - Replace 06 P4d step 2's classification clause with a pointer to 05-backup §5B.3.6 (2), and add "writes `c/backup/served_boot`, then `backup.location.json`" to its write list.
  - Rewrite 05-authn §4.14 bullet 2 and the §6 row as "replaced per 05-backup (2); a relocated store runs no hook".
  - Change 05-storage step 11(b) to "classify per 05-backup".
  - Either add "a behind lineage (not on a restore boot, DATA6-2)" to 05-backup (2), or delete the claim from 05-storage l.974.

### DATA6-6 — MAJOR — Factory reset from S2b is unspecified, and HEAD's implementation cannot run under claims

- **Where:** No spec defines a core build's factory reset.
  - 05-config consequence (b) (l.1371) and 09 §9.2.1 row 12 (l.157) describe only an older build's reset.
  - 05-authn §7 ("`HasUsers` … cleared only by factory reset") and 05-rbac l.935 (`POST /system/factory-reset` → `core:system.manage`) assume it exists.
  - 05-storage `LegacyRange.DeleteRange` ("the whole range must resolve to this handle"; `c/` and `m/` "never reachable", l.505-515). §5S.9.4 shadow mode (l.1835-1848). Step 5 and rule 4 (`ErrForeignKeys`).
- **Evidence:**
  - Books keeps HEAD's handler (`internal/server/handlers/system/handler.go:538-560`). It calls `PebbleStore.Reset`, which commits `DeleteRange([0x00], [0xff 0xff])` plus counters and a fresh format stamp (`internal/database/pebble_store.go:6310-6360`), through the `legacyrange.go` adapter.
  - Strict claims refuse that range (`ErrOutsideKeyspace`), so factory reset returns 500 from S2b strict on.
  - In the S2b shadow release (14+ days of production) the wipe is allowed and deletes `c/storage/install`, the claim ledger, the stamps and the served ledger under a live kernel. The coalescing claim-violation writer then writes `c/storage/claimviol/…` for that very `DeleteRange` (l.1841), and compat flushes write `c/compat/`. The next boot finds a non-empty `c/` without the marker and halts with `ErrForeignKeys`.
  - A reset narrowed to books' claims would leave `c/secrets/`, books' extension records, `c/audit/` and core's applied records behind. It would also wipe `preference:db_version`. After the floor release, books' version 0 < `LegacyCap` gives `ErrUpgradeFloor`, and the reset install never boots again.
  - The install id question (DATA4-15 versus DATA5-13) is also left open for a core build's own reset.
- **Fix:**
  - Specify core factory reset as a staged, restart-bound operation owned by storage and backup:
    - `storage.Admin.StageFactoryReset` writes `<state_dir>/reset/pending.json`;
    - P0a moves the DB directory, its sidecars and the derived directories to `reset/<id>/previous/`;
    - the next boot is a **fresh install** (new `install_id`, the fresh-install batch, Seed steps run);
    - the revocation journal is set aside per DATA4-15;
    - the lineage and location files are rewritten;
    - keyring handling is an owner decision recorded in 14.
  - Books' handler calls it from the S2b PR on, and `PebbleStore.Reset` is deleted.
  - Add `TestFactoryResetUnderClaims` (shadow and strict: no `ErrOutsideKeyspace`, next boot fresh) and `TestFactoryResetPostFloorBoots`.

### DATA6-7 — MAJOR — The W gate restarts at `since`, so the unrecorded interval counts toward removal

- **Where:**
  - 05-storage l.978: "09's recording-build rule W restarts its window at `since` (reading `owed.usage`, not `last`)", with `since` = "the previous `last.at`".
  - 09 §9.2.1 (l.120-124): "days run by a non-recording build … do not count, and an owed `usage` duty … restarts the clock at its `since`".
  - 09 `Usage.RecordingContinuousSince` "computed from this record and the served ledger" (l.673). §9.2.1 row 14.
- **Evidence:**
  - `since` is when the last recording boot started. The non-recording interval lies **between** `since` and `detected`. Restarting the 14-day clock at `since` counts the rollback build's days as unused.
  - Example: 30 days on S5, then 20 days on HEAD, then roll forward. The gate is met at once, which contradicts the sentence before it and row 14's release note.
  - Each consumer deletes its owed entry. Once (3b) replaces `last`, nothing records that the window restarted, and nothing names the usage consumer or what it persists.
  - Separately, `since` is undefined when no previous `last` exists (the first S0 boot on a HEAD store).
- **Fix:**
  - Restart at `detected`. The usage consumer (06 P4d step 5) writes `c/compat/recording_since/<owner>` = `detected` in the batch that deletes `owed.usage`, and `RecordingContinuousSince` reads it.
  - Define `since` = zero ("unbounded") when there is no previous `last`.
  - Add `TestUsageGateRestartsAfterUnrecordedInterval`.

### DATA6-8 — MAJOR — Coalesced last-use writers resurrect revoked sessions and revert revoked keys

- **Where:**
  - 05-authn §4.3 (l.761: `LastSeen` "updated at most once per minute per session (in-memory coalescing …)") and §7 (l.1307: "buffered map … flush every 60 s").
  - §4.4 (l.804: key last-use "coalesced per key per minute. It writes only `last_used_at`, `last_used_ip` and `use_count`").
  - §3.4 `Store` (l.540-549): the only write methods are `PutSession(b, *SessionRecord)` and `PutAPIKey(b, *APIKey)`. Neither has a `seen` parameter, and `Take*` is the only stated `Expect`.
- **Evidence:**
  - The flusher holds whole `SessionRecord`s up to 60 s old. A logout or an admin "revoke session" deletes the record (`DeleteSession`). The user's session was used seconds before the logout, so it is in the buffer, and the flush's `PutSession` re-creates it: a lost device's cookie works again.
  - A key's coalesced write through `PutAPIKey` rewrites the whole row, `status` included, from an earlier read, and can revert a revoke. HEAD prevented this with `apiKeyMu` around read and write (`internal/database/pebble_store_auth.go:1273-1302`).
  - 05-storage §5S.9.3 says these mutexes become `Serialized` plus `Expect`, but no interface method can carry that `Expect` for a field-only write.
- **Fix:**
  - Add `TouchSession(b, hash, at)` and `TouchAPIKey(b, id, at, ip)` to `auth.Store`. Each stages `GetForUpdate` on the current record, skips a record that is absent, revoked or epoch-stale, and changes only the usage fields.
  - Make the flushers use them.
  - Add `TestLastSeenFlushNeverResurrectsRevokedSession` and `TestKeyLastUseNeverRevertsRevoke` (`synctest`) to `authtest.StoreSuite`.

### DATA6-9 — MINOR — The `gen` consumer is scoped per owner and per enqueue, not per index and per completed rebuild

- **Where:** 05-storage §5S.3.9 (l.1199-1202: "rejects every artifact while the served ledger owes the `gen` duty … and clears that entry in the batch that enqueues the rebuild"); l.978 (`owed` = one record `{<duty>: …}` per owner).
- **Evidence:**
  - Books registers several derived indexes (memdb, hnsw and chromem, §5S.9.3). The first index's enqueue deletes `owed.gen`, and the next index's loader then accepts its stale artifact. That is the false accept the generation mechanism exists to prevent (DATA-25).
  - A crash after the enqueue and before the rebuild completes also accepts the stale artifact on the next boot.
  - `owed` is one map key per owner that the journal, auth, usage and gen consumers each rewrite in their own batches, and no text says which owner's entry each consumer reads.
- **Fix:**
  - Key the entries `c/storage/served/<owner>/owed/<duty>`, and for `gen` add one entry per derived index.
  - Clear a derived index's entry in the batch that records its rebuild's **completion** (`c/storage/derived/…` state `built`).
  - State that the global duties (journal, auth) are read and cleared under every owner that has them.

### DATA6-10 — MINOR — DATA5-13's install-id inheritance contradicts 05-backup's DATA4-15 row and test

- **Where:**
  - 05-storage step 5 (l.1472-1477) and §5S.11 item 8 (l.1972).
  - 05-backup §5B.5 "Journal header names another install (factory reset, deleted DB directory) | … nothing re-applied" (l.875) and `TestJournalOfOtherInstallNotReplayed` (l.982).
  - 14 K22 ("inherits the journal header's `install_id`").
- **Evidence:**
  - An older build's factory reset leaves a non-empty marker-less store, because `Reset` re-seeds the counters and the stamp (`pebble_store.go:6335-6360`). That store now inherits the install id from `storage.boot.json`, and the old journal **is** re-applied, for example `role.narrowed` on the re-seeded `editor` (`internal/auth/seed.go:33-36`).
  - 05-backup's row and test still promise the opposite. K22 names a different source.
- **Fix:**
  - Narrow 05-backup's row and test to "a core build's reset (DATA6-6) or a deleted DB directory", and add a residual row citing §5S.11 item 8.
  - Make K22 say "the lineage file's `install_id`".

### DATA6-11 — MINOR — DATA5-11 residue: the corrupt-secret repair is both removed and converted

- **Where:**
  - 05-config §1.12.2 (l.1216): "`Open` runs the load **without that repair**".
  - 05-storage §5S.9.1 row 7 (l.1727): "the corrupt-secret repair that runs inside the legacy `Open` (`:1233-1262`) — and the S3 PR converts each".
  - 09 S2b "Data/keys" (l.831): the repair is one of "HEAD's blind writers [that] stay writers until S3".
- **Fix:** Pick 05-config's design (no repair in `Open`). Delete the repair from row 7's and 09 S2b's writer lists, and leave "`Open` writes nothing" as the one statement.

### DATA6-12 — MINOR — `CheckedWrites` flags turn on with no discovery period, and their checks sit at the wrong stage

- **Where:**
  - 05-storage §5S.9.4 (shadow mode for one release at S2b) and `TestS2bBooksWritesEveryFamily` (l.1860: "in shadow mode an `ErrBlindWrite` is a recorded violation"; "a settings save that changes a secret … persists").
  - 09 S2b "Done when" (l.833). Row 19 / 09 l.363: `import_path:` `CheckedWrites` "from S7, in the PR that converts …", while the generator emits the flag "from its row's stage" (l.1754).
- **Evidence:**
  - At S2b no row has `CheckedWrites` (row 7 starts at S3, row 19 at S7). So the S2b `ErrBlindWrite` and "secret persists" checks are vacuous.
  - At S3 and S7 the flags go straight to strict with no persisted-violation period. HEAD's secret loop "only warns on a failed write" (`persistence.go:1989-2006`), so one missed writer silently drops a secret.
  - "From its row's stage" (the flip PR) and "in the PR that converts" can be different PRs.
- **Fix:**
  - Give each `CheckedWrites` flag one count-only release: `ErrBlindWrite` is recorded in `c/storage/claimviol/` with `op=blind` and allowed.
  - Move the "secret save persists, no `ErrBlindWrite`" check to S3 and add a root-statistics one to S7, each also requiring "zero persisted blind-write violations ≥ 14 days".
  - Tie the generator's flag to the converting PR, which must be the stage's flip PR.

### DATA6-13 — MINOR — The "state directory recorded an r2 boot" condition names no record

- **Where:** 05-authn §3.4 `LegacySignIns` comment (l.572-577), §4.14 (l.1192), and `TestMissingCutOffAfterServedIsReplacedStore` (l.1391).
- **Evidence:** No file in the state directory records "an r2 boot". `backup.location.json`'s `served_boot` pair (written only by journaling boots, which are r2 and later) is the only candidate. `storage.boot.json` exists from S2b.
- **Fix:** State "when `backup.location.json` holds a `served_boot` pair", and cite 05-backup §5B.3.6.

### DATA6-14 — MINOR — Suspension and anchor drift after DATA5-4 and R5-6

- **Where and evidence:**
  - 03 §3.3 "Extension records" (l.348) says users can be suspended: "through a confirmation path its store exposes (for API keys and users …)". 05-authn §3.4 `KeyConfirmer` says the opposite: "The user record declares no Widening field, so it is never suspended".
  - 09 §9.8 (l.1085) says every changed row suspends its extension: "Extension record whose legacy row an older build changed | closed: the extension is suspended". Under DATA5-4 only Widening records are suspended, and Derived fields reset instead.
  - 05-tasks §5T.6.2 (l.1435) gives task records a default projection: "`fp` covers the whole row: the default guarded projection". 05-storage l.1256 says "There is no default projection: every `Extension` declares its `Hash`".
  - 05-authn §4.3 (l.749) revokes sign-ins "created before **this boot's start**", while 05-storage l.978 uses `owed.auth.detected` (the first detection, "the same `t` on a retry").
- **Fix:**
  - 03: "for API keys, `ConfirmAPIKey`".
  - 09 §9.8: "a Widening extension is suspended until confirmed; Derived fields reset".
  - 05-tasks: "each task `Extension` declares `Hash` over the whole `opv3:run:` record or clock row (no per-tick field lives there)".
  - 05-authn §4.3: "created before `owed.auth.detected`".
