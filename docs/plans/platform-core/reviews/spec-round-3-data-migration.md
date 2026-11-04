<!-- file: docs/plans/platform-core/reviews/spec-round-3-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 86fa9584-23ce-40a4-9168-389a30fc1201 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 3: DATA, STORAGE AND MIGRATION (`DATA3-*`)

This is an antagonistic review of `docs/specs/platform-core/` after the round-2 revision and integration pass:
03 v1.4+, 09 v1.2.1, 05-storage v1.3.1. Code anchors are at `748fd787a`. Spec anchors take the form `file §section (l.N)`.
Cross-cutting resolutions 1–12 and R2-1…R2-8 are binding and are not attacked. A finding attacks them only
where the text fails to carry them through, or where they break internally. D1–D9 are not attacked.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 3 | DATA3-1 … DATA3-3 |
| MAJOR | 10 | DATA3-4 … DATA3-13 |
| MINOR | 11 | DATA3-14 … DATA3-24 |

| ID | Sev | Title |
|---|---|---|
| DATA3-1 | BLOCKER | Checkpoint reuse picks an earlier upgrade's completed-but-unconsumed checkpoint (or another store's), so `restore-checkpoint` silently rolls the store back by days |
| DATA3-2 | BLOCKER | The claim-ledger orphan rule refuses every rollback that crosses a hand-over, so the R2-1 floor builds cannot boot |
| DATA3-3 | BLOCKER | C3 deletes the only stored copy of the integration-bound settings, and after C3 nothing can read or mirror the blob that M1.4 step 2a needs |
| DATA3-4 | MAJOR | The tasks Teach build (S4 r3) drops `migrated_to` on rewrite, and HEAD's Retry re-queues an adopted run in place |
| DATA3-5 | MAJOR | P4d step 2 (journal re-apply) runs before the keyring exists and before DB overrides; the journal path can be set in the UI and reloads live |
| DATA3-6 | MAJOR | A crash after restore step g loses the restore: the hooks never run, swap-back is impossible and the rollback tooling stops refusing |
| DATA3-7 | MAJOR | From S2b to S8 the staged restore and the post-restore hooks have no place to run |
| DATA3-8 | MAJOR | Restores done by the identity Teach build re-apply nothing on that build and get no hooks after roll-forward |
| DATA3-9 | MAJOR | Restore staging never checks `UpgradeFloor`, so "read: permanent" is false and a fresh host ends up with an empty fresh-install store |
| DATA3-10 | MAJOR | `UpgradeFloor` cannot express S10's floor ("these contracts have run"), and no level proves it |
| DATA3-11 | MAJOR | 05-tasks' C5 deletes the legacy `Requires` stamps before raising the level; 09 and 05-storage raise first |
| DATA3-12 | MAJOR | Restoring an old archive (or a host-loss restore) breaks install lineage and secret recovery |
| DATA3-13 | MAJOR | Retiring a derive key breaks the revocation journal's chain |
| DATA3-14 | MINOR | `core/log#1` is an adopt step with no Teach build or `Floor` |
| DATA3-15 | MINOR | 09 and 05-storage give contracts different phase names |
| DATA3-16 | MINOR | Seed-role handling in 09 S4 contradicts 05-rbac's lock |
| DATA3-17 | MINOR | 01 glossary: contracts check "stamped level" rather than `Adopts` records |
| DATA3-18 | MINOR | 05-authn writes during P5, but 06 says P5 writes nothing |
| DATA3-19 | MINOR | The S0 hotfix list includes the revocation journal, which needs the S3 keyring |
| DATA3-20 | MINOR | Foreign-open detection has gaps (S0–S2 record nothing; false positive after a crash) |
| DATA3-21 | MINOR | Fresh-install stamping is not stated to be atomic; "no mirror is ever active on a fresh store" is false |
| DATA3-22 | MINOR | The checkpoint record's write order is unspecified, so a partial checkpoint can be reused |
| DATA3-23 | MINOR | C6, C7 and C8 cannot satisfy "every contract declares `Adopts`"; C8 ties the restore-bound raise to dashboard checks |
| DATA3-24 | MINOR | 09 §9.3 gives S3's build level as 2, but S3 r1 serves 1 |

## Level renumbering check (identity Teach S4 r1 = 3, Floor 4 by `core/tasks#1`, C5 → 5, legacy restore bound 5, C8 → 6)

The numbers are consistent in every normative place checked:

- 09 §9.2.2: level table (l.183+), Teach table (l.198-201), legacy restore bound and the C3/C4/C5/C8 rows of §9.5
- 05-authn §11.1 (l.1114-1118)
- 05-storage §5S.9.2 mirror procedure (l.1484-1486)
- 05-tasks §5T.6 (l.1106) and the T0+T1 row (l.1177)
- 05-backup B3 (l.489-490)
- 05-config §1.12.2 (l.867-869)
- 05-metadata M1.10 (l.600)
- 02 N13 and 03 §3.7, which carry no numbers

The interplay is also sound. `core/integrations#1` stamps `Floor` 5 before C5 runs; that locks out S4 r3 and turns C5's raise into a no-op stamp. This works because level 5 is defined as a reader capability ("with or without the reconcile").

Residuals:

- 09 §9.3 still gives S3 as "2" (DATA3-24).
- The tasks-row justification for S4 r3 as a Teach build is false (DATA3-4).
- `core/log#1` has no level at all (DATA3-14).
- 05-tasks' review-history line "only contract C5 raises the level (to 4)" (l.1368) is historical and needs no change.

## Regression check (round-2 and round-1 BLOCKER/MAJOR, this lens)

| Round-2 | Status in round 3 | Residual |
|---|---|---|
| DATA2-1 (B) static ownership | Fixed: §5S.9.1 has *Before*/*Hand-over*/*After*; `Claim.From/Adopt/Contract`; per-store resolution | **DATA3-2** (claim ledger vs. rollback builds), DATA3-14 |
| DATA2-2 (B) skipped release / legacy steps | Fixed at boot (grant, `After: books#66`, `UpgradeFloor`, `min_upgrade_from`) | **DATA3-9** (restore path), **DATA3-10** (S10 floor not expressible) |
| DATA2-3 (B) re-activation widens | Fixed (`Restriction`, sentinel scope, decoy hash, `mirror_reason`, Teach item 1) | — |
| DATA2-4 (B) post-restore native-only | Fixed in text (hooks through `Mirror.Stage`, verification rebuild) | **DATA3-6**, **DATA3-7** (hooks can be skipped or are unwired) |
| DATA2-5 (B) journal blind to rollback-era changes | Half-fixed: Teach build journals and the store head triggers re-apply | **DATA3-8** (restores done by the Teach build), **DATA3-5** (re-apply ordering and path), **DATA3-13** |
| DATA2-6 (B) S0 install marker | Fixed | — |
| DATA2-7 restore boundary | Fixed (`JournalSeq`, `AuditHighWater`, `password.changed`) | — |
| DATA2-8 invites | Fixed (row 4a `AgingOut`, 09 §9.6 row 25) | — |
| DATA2-9 served ledger | Fixed (uninterrupted run, foreign open, `.prev` probe) | DATA3-20 |
| DATA2-10 integration instances | Fixed for S7…C7 | **DATA3-3** (broken by C3's ordering) |
| DATA2-11 T overlay | Fixed (T writes, clears detected) | — |
| DATA2-12 v1 ops writers | Fixed (row 12 books RW, `TestLegacyOpsWriters`) | — |
| DATA2-13 C5 final reconcile | Fixed | **DATA3-11** (05-tasks deletes before the raise) |
| DATA2-14 contract resume | Fixed (persisted phases) | DATA3-15 (phase names differ) |
| DATA2-15 over-cap rebuild | Fixed (generations; adopt step does not rebuild) | — |
| DATA2-16 audit sequence collision | Fixed (`Sequence.AdvancePast`) | — |
| DATA2-17 plaintext config file | Fixed (redacted copy) | — |
| DATA2-18 checkpoint consumption | Consumption fixed; **the new reuse rule regresses** | **DATA3-1** |
| DATA2-19 06 vs 05-storage path | Fixed (06 P2 at `core.storage.path`, no automatic restore) | — |
| DATA2-20 seed role widened | Fixed in 05-rbac (seed roles locked) | DATA3-16 (09 text stale) |

Round 1: the round-2 table mapped every round-1 BLOCKER and MAJOR to fixed or to a DATA2 residual. Those residuals are covered above.

Round-1 items whose mechanism regressed or is still incomplete:

- DATA-10 (checkpoint) → DATA3-1
- DATA-23 (boot-phase restore) → DATA3-6 and DATA3-7
- DATA-28 (claim ledger) → DATA3-2
- DATA-6 (one-shot imports) → DATA3-3

## Simulations

**(a) Forward S0 → S10 from a store at books migration v58 (below 59).**

- S0 runs 59–66 with HEAD's runner; this is fine (59–61 are no-ops on Pebble, `internal/database/migrations.go:1140-1175`).
- A v58 store jumping straight to S2b–S5: 59–66 run under the legacy grant before every adopt step. This is fine.
- A v58 store jumping to a T8+ release is refused before any write, and the operator installs the named release. It adopts and stamps `Floor` 4–5, then the T8 release boots in mirror phase while C5 is deferred. This is fine.

What breaks:

- The rollback floors (S3 r1/S2b, S4 r1, S4 r3) cannot boot on the adopted store because of the claim ledger (DATA3-2).
- C3 runs months before S7 and deletes Deluge, AI, metadata-source and webhook settings (DATA3-3).
- S10's floor cannot be declared with the given type, and the updater cannot see it (DATA3-10).
- Any second upgrade within 7 days reuses the previous upgrade's checkpoint (DATA3-1).

**(b) Forward → rollback to each Teach build → writes → roll forward.**

These writes behave correctly:

- Deactivate, revoke and create on S4 r1 are honoured by the rebuild and the journal head.
- Re-activation attempts are refused (Teach item 1).
- Settings changes on S0/S3 r1 are re-imported (05-config T/X).

What breaks:

- S3 r1, S2b, S4 r1 and S4 r3 fail boot on the persisted claim records (DATA3-2).
- On S4 r3, Retry of an S5-adopted run executes it twice and overwrites the run's history on roll-forward (DATA3-4).
- A restore of an older archive on S4 r1 serves revoked keys, raw legacy sessions and invites until roll-forward, and the hooks never run afterwards (DATA3-8).
- From S2b to S7 the staged restore has no boot-phase owner at all (DATA3-7).
- After C3, rolling back S7 → S6 leaves S6 with no integration settings (DATA3-3).

**(c) Restore an old ABO archive on a fresh host with the keyring export.**

- An archive below the build's upgrade floor passes staging, fails after the swap, and leaves an empty fresh-install store behind (DATA3-9).
- A pre-S0 archive gets a new `install_id`, which breaks journal and provenance lineage. For a pre-S3 archive, `KeyIDsReferenced` misses `k0-legacy` (DATA3-12).
- The journal from the old host is re-applied at P4d step 2 before the keyring is open (DATA3-5).

**(d) Crash at each boot phase.**

- **P4a:** a partial checkpoint directory is reusable (DATA3-22); a non-atomic fresh install is half-stamped (DATA3-21).
- **P4b:** the C5 delete can happen before the raise (DATA3-11); a crash between the sidecar write and identity record gives a false foreign open (DATA3-20). The other steps are covered by atomic steps, persisted contract phases and idempotent legacy steps.
- **P4c:** no writes.
- **P4d:** step 2 cannot verify lines (DATA3-5). On a restore boot, a crash anywhere between P0a step g and journal `applied` means the hooks never run (DATA3-6).

---

## Findings

### DATA3-1: BLOCKER: Checkpoint reuse picks an earlier upgrade's completed checkpoint, or another store's

- **Where:**
  - 05-storage §5S.3.12 "Crash loops" (l.1192), "Consumed and pruned" (l.1193), "How" (l.1191)
  - 09 §9.2.2 "Disk headroom" (l.267)
  - 05-backup §5B.3.4 step x (l.586)
- **Evidence:**
  - **The rule ignores completion.** A boot reuses "the oldest unconsumed checkpoint whose from-vector is an ancestor of the current vector". `completed_at` is not excluded. A checkpoint is consumed only once it is ≥ 7 days old and a later boot has run. So: upgrade A at t0 takes C1 (vector V0) and completes. Upgrade B at t0+2 days has pending steps, its vector V1 ≥ V0, so it reuses C1 and takes no checkpoint of its own. B then fails in P4b. The operator follows the documented recovery, `storage restore-checkpoint`, and `.migration-checkpoint` names "the oldest unconsumed", which is C1. The store returns to t0, and two days of library edits, users, keys and run history are lost without warning. RCs are cut on every push and minors are promoted every 10 RCs (09 §9.1), so two pending-step boots inside 7 days is the normal cadence, not an edge case.
  - **The rule ignores which store a checkpoint came from.** The record holds no store identity. A failed restore swaps back (step x) and leaves the older archive's checkpoint beside the DB. Its from-vector is ≤ the live store's, so it is an "ancestor", and the next upgrade of the original store reuses a checkpoint of a different store.
- **Fix:**
  - Reuse only a checkpoint that has no `completed_at` and whose record matches the live store's `{install_id, store_epoch}`. `store_epoch` is a counter in `c/storage/epoch`, bumped in the batch or journal of every swap (restore, revert, checkpoint restore, swap-back) and copied into the record.
  - Never reuse a completed checkpoint. A new upgrade boot takes its own (the free-space check already covers this) and prunes completed-but-unconsumed predecessors beyond `checkpoint_keep`.
  - Step x moves the restored store's checkpoints into `restore/<id>/failed/`.
  - `.migration-checkpoint` names the checkpoint of the current, uncompleted upgrade.
  - Tests: `TestSecondUpgradeTakesFreshCheckpoint`, `TestSwapBackCheckpointNeverReused`.

### DATA3-2: BLOCKER: The claim-ledger orphan rule refuses every rollback that crosses a hand-over

- **Where:**
  - 05-storage §5S.3.4 rule 5 (l.587-591), §5S.4 steps 4 and 9, §5S.5 ("orphan claimed without `TakesOver`" is a boot error)
  - 06 §6.5.1 P2 (l.970: "a claim on disk but absent from the build stays orphaned until a declared claim sets `Claim.TakesOver` … never silently re-owned")
  - in conflict with 09 §9.2.1 ("Rollback to any build from the Teach build on is always safe"), the §9.2.2 Teach table (l.198-201), and §5S.9.1's generator ("A build that does not contain a row's hand-over declares no core claim for it")
- **Evidence:**
  - After S3 r2, S4 r2 and S5 r1 run, step 9 persists `c/storage/claims/core/config`, `…/core/auth`, `…/core/rbac` and `…/core/tasks` with their hand-over claims.
  - The rollback targets that open the store through core storage do not declare those claims. Their generated books claims cover the same ranges in *Before* mode:
    - S3 r1 and S2b, for config
    - S4 r1, for identity (its hand-over lands in r2)
    - S4 r3, for tasks (`core/tasks` does not exist before S5 T0)
  - `make rollback` passes the level guard. The floor build then halts with `ErrClaimConflict`. Read leniently ("orphaned, no write path reaches it"), it cannot use `u:`, `apikey:`, `setting:config_blob` or `opv2:`, so login, settings and tasks break.
  - Only S0–S2, which have no core storage, are unaffected. R2-1's floor is therefore unusable for the identity and tasks sequences.
- **Fix:**
  - Persist `From`, `FromMode` and `Adopt` with each claim in `c/storage/claims/<owner>`.
  - In rule 5 and 06 P2: a persisted claim with `From` set is not an orphan in a build that does not register its `Adopt` step and whose own claim on the range is held by `From` in `FromMode`. That build serves the range in the pre-adoption view (the one the legacy grant already defines) and, at step 9, leaves that owner's ledger record untouched.
  - Orphan treatment stays for claims without `From` (a removed module).
  - Test: `TestRollbackAcrossHandOverBoots` for S3 r2→S3 r1, S3 r2→S2b, S4 r2→S4 r1 and S5→S4 r3, plus the roll-forward of each.

### DATA3-3: BLOCKER: C3 deletes the only stored copy of the integration-bound settings, and after C3 the blob M1.4 step 2a depends on is gone

- **Where:**
  - 05-config §1.12.2 "Integration-bound keys" (l.899-912), mirror rule 1 (l.914+), phase C (l.869)
  - 09 §9.5 C3 row (l.776)
  - 05-metadata M1.4 step 2a (l.468+), M1.10 import table
  - 09 §9.2.2 Teach table, Integrations row (l.201); S7 Rollback (l.684)
  - 05-storage §5S.9.1 row 7 (C3 → Retired)
- **Evidence:**
  - Before S7, config imports only the **secret** part of Deluge, AI endpoints, metadata sources, SMTP and webhooks. URLs, the endpoint list and the source configuration are "carried forward in the blob". The blob is their only stored copy, and S7's import reads them from "the config blob through `configstore`".
  - C3's preconditions are `core/config#1` applied, level 2 served for 30 days, P8, and the S1 restore. None depends on S7 or C7. With S4–S6 in between, C3 normally runs first and deletes `setting:config_blob`: S4–S6 lose those settings and S7 imports nothing.
  - If S7 ships first, C3 turns the blob and the legacy secret rows into `Retired` claims (`Set` refused). M1.4 step 2a's mirror writes then fail, and under 09 §9.2.3 rule 4 every integration `Put` fails.
  - A rollback to S6 (still openable, level 5) "reads … the blob", which no longer exists, contradicting the S7 Rollback row and the Integrations Teach row.
- **Fix:**
  - Add "`core/integrations#1` applied **and** C7 applied" to C3's preconditions (09 §9.5, 05-config phase C). Alternatively split C3: C3 deletes the legacy secret rows and re-renders the blob with only the integration-bound keys (the blob claim stays `Migrating` until C7), and C7 deletes the blob.
  - State in 05-config and 05-metadata who owns the blob between C3 and C7.
  - R-test cells: "C3 before S7: S7 imports every URL" and "S7 → S6 after C3".

### DATA3-4: MAJOR: The tasks Teach build (S4 r3) drops `migrated_to` and re-runs adopted runs

- **Where:**
  - 09 §9.2.2 Teach table, Tasks row (l.200: "S5 writes only legacy shapes older builds read"), §9.2.1 Teach (a) ("preserve on rewrite"), S5 Rollback (l.658)
  - 05-tasks §5T.6 copy-and-mark and boot-reconcile rows (l.1080-1082)
- **Evidence:**
  - `updateOperationV2Status` decodes into the typed `OperationV2Row` and re-marshals it (`internal/database/pebble_store_ops_v2.go:336-380`). The struct (`internal/database/iface_ops_v2.go:39`) has no catch-all field, so `migrated_to` is lost on any status write.
  - `RetryInterrupted` re-queues **the same row** for every `interrupted_*` status, `interrupted_dropped` included (`internal/operations/registry/retry.go:24-50`).
  - On a rollback to S4 r3, a Retry of an S5-adopted row runs the work again while its copy runs (or has run) in `c/tasks/`.
  - On roll-forward, the reconcile sees no `migrated_to` and copies the row again "keeping RunID" over the existing `c/tasks/run/<id>`. The result is a double execution and overwritten run history, which contradicts "copy-and-mark (no double execution)".
- **Fix:**
  - Make the tasks Teach build explicit: S4 r3, or a release before S5 r1. It ships:
    - `OperationV2Row.MigratedTo` (JSON `migrated_to,omitempty`), preserved on rewrite
    - Retry, Discard and the boot resume sweep refuse or skip a row that has `migrated_to` (409 `run_migrated`)
  - Copy-and-mark stages an `Expect` that `c/tasks/run/<id>` is absent. On conflict it re-marks the legacy row and copies nothing.
  - R-test cell "Retry an adopted run on prev"; test `TestReconcileNeverOverwritesExistingRun`.

### DATA3-5: MAJOR: Journal re-apply (P4d step 2) runs before the keyring and before DB overrides

- **Where:**
  - 06 §6.5.1 P4(d) order (l.972); 05-storage §5S.4 step 11 (b) and (c)
  - 05-backup §5B.3.6 Format (l.673), Boot re-apply (l.678); §5B.4 (l.718: "the derive function from `secrets`"); §5B.7 `core.backup.dir` (hot-reload "yes", admin)
  - 05-config §2.4 step 1 (`Open` after the DAG, which is P4d step 3)
  - HEAD `backup_dir` is UI-settable (`internal/config/protected_fields.go:95`, `internal/config/config.go:1082`)
- **Evidence:**
  - **The keyring is not open yet.** Every line's MAC needs `DeriveKeyFor(kid, …)` from the secrets store, which opens only at step 3. Step 2 can therefore verify no line, so it either re-applies nothing ("stops at a chain break") or applies unverified lines.
  - **The path may be wrong.** The journal sits under `core.backup.dir`. When that key is set in Settings it is a DB override, attached at step 3, so step 2 reads the default directory.
  - **A live change splits the journal.** Changing the directory at runtime starts a new journal while the store's head points into the old one. The new journal looks truncated, and coverage is nil for good.
- **Fix:**
  - Load the keyring (`secrets.Open`, no import) at the start of P4d, before step 1; the config import stays at step 3.
  - Make `core.backup.dir` restart-bound and resolved at P0 from operator layers and the boot record (the UI writes the boot record, as with enablement). Alternatively, keep the journal at `<state_dir>/revocations.journal` and write a copy beside every archive.
  - When the directory changes, the next boot moves the journal (copy, verify the chain, rename) before step 2.
  - Test: `TestJournalReapplyWithUISetBackupDir`.

### DATA3-6: MAJOR: A crash after restore step g loses the restore

- **Where:**
  - 05-backup §5B.3.4 step g (l.584: "remove `pending.json`. Boot continues"), h, x, and l.591 ("A crash at any step resumes from the journal at the next boot")
  - 06 P0a (l.967: runs only "if `<state_dir>/restore/pending.json` exists"); P4d step 5 ("only on a boot that applied a restore")
  - 05-storage `CheckReader` (l.924: refuses while `pending.json` exists)
- **Evidence:**
  - `pending.json` is removed inside P0a, before P2, the P4b DAG (long for an old archive) and the P4d hooks. Suppose the process crashes, is OOM-killed or loses power between g and h.
  - The next boot finds no `pending.json`. P0a does nothing and the kernel does not know this is a restore boot. The hooks never run: no session or invite deletion, no `needs_confirmation` (every `legacy-abo/0` restore is foreign, so `Revocations` is nil), no audit copy-forward.
  - A later failure can no longer trigger step x.
  - `CheckReader` passes again, so `make rollback` to the Teach build serves the restored store without its hook writes.
- **Fix:**
  - Rename `pending.json` to `restore/<id>/inflight.json` at step g instead of deleting it, and delete it at journal `applied` or `swapped_back`.
  - P0a resumes any journal whose state lies between `previous_moved` and `applied`, and the kernel decides "restore boot" from that journal.
  - `CheckReader`, `make rollback` and `deploy` refuse while such a journal exists.
  - State that the hooks are idempotent.
  - Test: `TestCrashBetweenSwapAndHooksResumesHooks`, with `CrashClone` at P2, P4a, P4b and each P4d step.

### DATA3-7: MAJOR: From S2b to S8 the staged restore and the hooks have no place to run

- **Where:**
  - 06 §6.4.10 (`Backup` lands at S8; "before that it is absent … the kernel phase that consumes it is skipped")
  - 06 §6.5.1 Stages (l.981: covers P2/P4 until S2b and P−1 until S8; says nothing about P0a)
  - 09 S1 (l.570: staged restore "implemented in `internal/backup` before it moves at S8"), §9.4.1 row S1
  - 05-backup step h; 03 `backup/journal` lands at S4
- **Evidence:**
  - At S1–S2 books opens the store in a start hook, so ABO can swap the directories first.
  - From S2b the kernel opens the store at P2, before any module hook, and 05-backup's P0a does not exist until S8. ABO's swap therefore runs after the store is open, or not at all.
  - P4d step 5 has no signal that a swap happened. Yet S4 r2–S7 are exactly the builds whose identity mirror needs the hooks (R2-2).
- **Fix:**
  - 06 Stages: "from S2b, P0a runs ABO's `internal/backup` swap through `app.Options.PreBootRestore func(stateDir string) (applied bool, err error)` (removed at S8); from S4, P4d step 5 runs the auth, rbac and audit hooks when it reports a swap." Alternatively, land the swap and restore journal with `backup/journal` at S2b.
  - Add an S2b and an S4 done-check: "staged restore rehearsal on core storage, hooks observed".

### DATA3-8: MAJOR: Restores done by the identity Teach build re-apply nothing and get no hooks

- **Where:**
  - 05-authn §11.1 item 4 (l.1160-1166: "Every restore the Teach build performs appends a `restore` record, so the next new-build boot re-applies the journal tail")
  - 05-backup §5B.3.6 Writers (l.675), Boot re-apply (l.678)
  - cross-cutting resolution 8
- **Evidence (simulation b):**
  - Sequence: S4 r3 → `make rollback` → S4 r1; revoke key K (journaled); restore archive A on r1. A predates K's revocation and that of key L, which was revoked on r3.
  - For the whole rollback, r1 serves A's legacy rows: K and L are active, and A's raw `sess:` tokens and `invite:` rows are valid.
  - On roll-forward the head mechanism re-applies the journaled lines. But that boot did not apply the swap, so step 5 does not run:
    - A's sessions stay valid through the legacy-token fallback (7 days), and its invites stay valid.
    - Keys are not set `needs_confirmation` when coverage is incomplete.
    - Audit records between A and the restore are not copied forward.
  - Re-applying a `restore` line has no defined effect.
- **Fix:**
  - (a) Either, on the boot after a restore it applied, the Teach build applies the verified journal lines after the restored head to legacy rows (revoke, disable, bump epoch, delete `sess:`/`invite:`) before it serves; or its restore refuses while the store's level is ≥ the identity floor ("restore an adopted store on an S4 r2+ build").
  - (b) When P4d step 2 re-applies a `restore` line that no hook run has acknowledged (`c/backup/restore_hooked/<seq>`), it runs the auth and rbac hooks with `Boundary` set to the line's boundary and `Previous` set to nil.
  - R-test cell "restore an older archive on prev, roll forward: K, L and A's sessions refused".

### DATA3-9: MAJOR: Restore staging never checks `UpgradeFloor`

- **Where:**
  - 05-backup §5B.3.4 step 4 (l.540: refuses only archives that are *newer*); §5B.3.3 (l.436: "restorable by every ABO build")
  - 05-storage §5S.3.5 Upgrade floor (l.710)
  - 09 §9.2.2 minimum-upgrade-from; S10; 09 §9.6 row 30 (l.817: "read: **permanent**")
- **Evidence:**
  - From T8, builds declare `UpgradeFloor{books, 66}`. From S10 there is also a floor on contracts.
  - An archive with books < 66, or one taken before C3/C4/C5 ran, passes step 4 because its versions are older, not newer. It is swapped in and then refused at P2 step 3, and step x swaps back.
  - On a fresh host (scenario c) there is nothing to swap back. The restart opens an empty path and stamps a fresh install (§5S.4 step 5), so the host now holds a fresh store and the next attempt is no longer a host-loss restore.
- **Fix:**
  - Step 4 evaluates every `UpgradeFloor` of the build against the staged checkpoint and refuses with `ErrUpgradeFloor{InstallFirst}` before extracting.
  - The host-loss procedure gains: "install the named release, restore, run it until its contracts ran, then upgrade".
  - 09 §9.6 row 30 becomes "read: permanent for archives at or above the build's upgrade floors; older archives through the named release".
  - An offline restore on a host with no store never leaves an empty store behind.
  - Test: `TestRestoreBelowUpgradeFloorRefusedAtStaging`.

### DATA3-10: MAJOR: S10's floor cannot be expressed, and no level proves it

- **Where:**
  - 05-storage §5S.3.5 `UpgradeFloor{Owner, Version, InstallFirst}` (l.648-652: "applied version for Owner is below Version")
  - §5S.3.11 Contract (l.1177: contracts are `Breaking`, `BootBlocking` **data migrations**, with state in `c/storage/dm/<id>`)
  - 09 S10 and §9.2.2; 05-tasks ("the reconcile code is deleted only from a build that refuses a store on which C5 has not run"); 05-updater "Stepping stones"
- **Evidence:**
  - Contracts have no owner version, so `UpgradeFloor` cannot name them.
  - Levels cannot stand in either. `core/integrations#1` stamps `Floor` 5 without C5, and C3/C4 raise only to 2 and 4, below that floor. A level-5 store may therefore have run none of C3, C4 or C5.
  - The updater compares release versions only. A store on the stepping stone whose contracts are still deferred (30 days) gets the S10 release applied, refused at boot and rolled back by `Boot`, and the cycle repeats with the next release.
- **Fix:**
  - `UpgradeFloor` gains `Requires []StepRef` (a `StepRef` already names data migrations); a store is refused while any listed step is not `done`. S10 declares `Requires: [C3, C4, C5]`, plus C8 if its code goes.
  - The signed manifest gains `requires_store`, and `<exe> --print-upgrade-floor` lets the updater check the store, as `ReaderCheck` does, before applying.
  - The message reads: "install X and run it until `storage_contract_pending` clears".

### DATA3-11: MAJOR: C5 deletes the legacy stamps before raising the level

- **Where:**
  - 05-tasks §5T.6 (l.1114-1118: "… and deletes the legacy `Requires` stamps; only then does it raise the level")
  - 09 §9.5 (l.768: `raised` comes "before the first legacy delete"), C5 row (l.778)
  - 05-storage §5S.3.11 Contract
- **Evidence:**
  - `op:completion:` and `op:deprev:` are "potentially library-scale", so the delete is chunked.
  - A crash in P4b after some chunks leaves the store at level 4 (the tasks floor) with stamps missing.
  - `make rollback` to S4 r3 is allowed, and it re-queues prerequisites across the library. That is OPS2-9's hazard, back in a crash window.
- **Fix:**
  - In 05-tasks, run C5 in three phases: `folded` (final copy-and-mark, clock and pause adoption, mirrors stopped) → `raised` (sidecar, then stamp 5) → `deleting` (stamps deleted in chunks with a cursor). These are 09's phases.
  - Test: `TestContractC5_CrashMidDeleteAfterRaise`.

### DATA3-12: MAJOR: Old-archive and host-loss restores break install lineage and secret recovery

- **Where:**
  - 05-backup §5B.3.7 (`Own` needs `install_id` equal to the live store's); §5B.3.6 header `install_id`; §5B.4 ("the revocation journal's header is checked against `c/storage/install`"); host-loss procedure (§5B.3.3)
  - 05-storage §5S.3.4 rule 4 and §5S.4 step 5 (a new marker on a non-empty store that has none)
  - 05-config §2.3 `KeyIDsReferenced` (l.1217: a `*Store` method that scans records); §2.4
- **Evidence:**
  - **New `install_id`.** A pre-S0 archive (floor 2 keeps one) has no `c/storage/install`. After a restore on the same host or a fresh one, the first boot writes a new marker with a new `install_id`. The journal header no longer matches, and the spec does not say what happens next (do appends continue? is coverage always nil?). Every S8+ `Own` archive of this install becomes foreign, which needs `core:admin` and owner proof.
  - **Secrets check misses legacy rows.** `KeyIDsReferenced` scans `c/secrets/`, so for a pre-S3 archive it reports nothing, although the archive's legacy secret rows need `k0-legacy`. With a keyring that lacks that key, step 5 passes, and the outcome of X's import of rows it cannot decrypt is unspecified: a boot failure followed by swap-back, or silent loss.
- **Fix:**
  - When the staged store has no install marker, staging writes the pre-swap store's marker into it. On a fresh host it uses the journal header's `install_id`, and the keyring export records `install_id` for this purpose.
  - `KeyIDsReferenced(ctx, view)` takes the staged view and reports `k0-legacy` for every legacy-format secret row it finds.
  - X records an undecryptable legacy row as "must be re-entered" (no secret written, import hash recorded) and continues the boot.
  - Test: `TestHostLossRestorePreS3Archive`.

### DATA3-13: MAJOR: Retiring a derive key breaks the journal's chain

- **Where:**
  - 05-backup §5B.3.6 Format (l.673: a MAC per line under `DeriveKeyFor(kid, …)`), Retention (l.680), Derivation-key rotation (l.502+)
  - 05-config §2.3 `DeriveKeyFor` (`ErrKeyUnavailable` for retired keys); retire rule (l.1254-1257: a verify key retires once its archives are rewrapped or dropped)
- **Evidence:**
  - `DeriveKeyInUse` counts archives, undo points and checkpoints, not journal lines.
  - After `rotate-derive` and the verify task's rewrap, key d1 retires. Lines under d1 that retention still keeps become unverifiable.
  - Re-application then "stops at a chain break": after a checkpoint restore or a file copy, later revocations are not re-applied, and every later restore gets nil coverage.
- **Fix:** Either `DeriveKeyInUse` also reports every `kid` that appears in the retained journal, or `Rewrap` re-chains the journal under the new key (a new header with `base_seq`/`base_mac` re-MAC'd). Test: `TestRotateDeriveKeepsJournalVerifiable`.

### DATA3-14: MINOR: `core/log#1` has no Teach build or `Floor`

- **Where:**
  - 05-storage §5S.9.1 row 14 and its sentence "05-config and 05-logging own the steps"; 05-logging §5 (defines no step)
  - 09 §9.2.2 Teach table (four sequences only); 09 §9.2.3 ownership view ("first journal-only build ≥ S2b")
  - §5S.3.4 (`Adopt` "must declare `Floor >= 1`"); 09 S2b Rollback ("Binary swap")
- **Evidence:** If the step lands with S2b, it stamps the store's first non-zero level and flips the legacy projection. Pre-S0 builds are then locked out from S2b, not from S3 r2, and neither S2b's rollback row nor 05-config's matrix says so.
- **Fix:**
  - Add a "Logging" row to 09's Teach table: Teach build S0, `Floor` 1, adopt `core/log#1`, no contract.
  - Define the step in 05-logging.
  - Amend S2b's rollback row.

### DATA3-15: MINOR: Contract phase names differ

- **Evidence:**
  - 09 §9.5 (l.768) and its risk row: `folded` / `raised` / `deleting`.
  - 05-storage §5S.3.11 (l.1177): stamps the level **inside** `folded`, with states `folding` / `folded` / `deleting` / `done`.
- **Fix:** Adopt one set in both files, together with DATA3-11's C5 phases.

### DATA3-16: MINOR: 09 S4 seed-role text contradicts 05-rbac

- **Evidence:**
  - 09 S4 Rollback (l.642) says "the edit becomes a custom role … or seed edits are refused until C4". The S4 R-test says "narrow a seed role on cur, roll back, still narrow on prev".
  - 05-rbac §4.8 and its error table lock seed roles during the window (`ErrSeedRoleLocked`), and 05-authn §11.1 item 5 agrees.
- **Fix:**
  - 09 S4: "seed-role edits are refused until C4 (05-rbac §4.8)".
  - R-test: "seed-role edit refused on cur; a custom role narrowed on cur stays narrow on prev".

### DATA3-17: MINOR: 01 glossary uses the old contract precondition

- **Evidence:** 01-summary l.98 says "a later contract step, run only when the store's stamped level proves adoption ran". 02 (decision 13, integration pass) and 05-storage §5S.3.5 check `Adopts` applied records instead.
- **Fix:** "run only when the store's adopt-step applied records prove adoption ran (`Adopts`)".

### DATA3-18: MINOR: 05-authn writes during P5

- **Evidence:** 06 §6.5.1 P5 says "Store writes: none", and its write rule requires every every-boot writer to run in P4d. 05-authn §5 step 2 (l.886) has `auth.New` seed `_system` and stamp legacy keys that lack an expiry, through the mirror, in P5.
- **Fix:** Move both writes into P4d step 1 (after the rebuild, before the verification rebuild), or list them in 06's P5 row.

### DATA3-19: MINOR: The S0 hotfixes include the revocation journal

- **Evidence:**
  - 09 S0 Hotfixes (l.535) lists "the revocation journal".
  - Journal lines are MAC'd with a derive key from the S3 keyring (05-backup l.673), and 05-authn §11.1 says only items 1–3 may ship early.
  - Lines written without a MAC would break the chain from S4 r1 on.
- **Fix:** Drop the journal from S0's list, or specify a separate pre-keyring file that the chain never verifies.

### DATA3-20: MINOR: Foreign-open detection gaps

- **Evidence:**
  - 09 S0 Data/keys (l.534) and the §9.2.2 records table do not have S0–S2 write `c/storage/sidecar_identity`, although §5S.3.7 (l.890) says every guard-aware boot records it. Every rollback to S0–S2 therefore reads as a pre-guard open.
  - The level-step procedure rewrites `.storage-format` at its step 2, but the new identity is recorded only at §5S.4 step 9. A crash in between yields a false "foreign open": the W clock restarts and plaintext is kept.
- **Fix:** S0's guard records the identity, and every sidecar rewrite updates the identity in the same batch as the stamp.

### DATA3-21: MINOR: Fresh-install stamping

- **Evidence:**
  - §5S.4 step 5 (l.1224) does not say that the marker, the stamps and the applied records are one batch. A crash between them leaves "marker present" with no stamps, and the store is then treated as an upgraded one.
  - "No mirror is ever active on a fresh store" is false when the build registers an adopt step but not its contract. A fresh S4 r3 install records `core/auth#1` without C4, so the claims resolve to `Migrating` and the mirror runs.
- **Fix:** Make it one batch, and reword the claim to "mirrors run but are trivially consistent until the contract".

### DATA3-22: MINOR: The checkpoint record's write order is unspecified

- **Evidence:** §5S.3.12 "How" (l.1191) does not order the steps `Checkpoint` → record file (temp + rename + fsync) → `.migration-checkpoint`. A crash during the checkpoint leaves a directory that the reuse rule (DATA3-1) could pick.
- **Fix:** Fix that order, and specify that a checkpoint directory without a record is garbage and is swept at boot.

### DATA3-23: MINOR: C6, C7 and C8 cannot satisfy the contract rule

- **Evidence:**
  - 09 §9.2.2 (l.223) says "Every contract declares `Adopts: [<the stage's adopt step>]`". C6 and C8 have no adopt step, and C7 is not a level step.
  - C8's owner is not named.
  - C8 makes the restore-bound raise (data protection) wait for P6 dashboard checks and alias usage.
- **Fix:**
  - Reword to "every contract that deletes adopted data declares `Adopts`".
  - Split C8 into C8a (raise to 6; `RequiresServed` only; owner `core/backup`) and C8b (aliases and metrics).

### DATA3-24: MINOR: 09 §9.3 gives the wrong level for S3

- **Evidence:** l.487 gives S3's build level as "2". S3 r1 serves level 1; the level table (l.183+) names S3 r2 as the first level-2 build.
- **Fix:** "1 → 2", as S4's row says "3 → 4".
