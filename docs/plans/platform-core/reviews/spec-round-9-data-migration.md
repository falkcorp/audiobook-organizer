<!-- file: docs/plans/platform-core/reviews/spec-round-9-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: db0f95f5-e8d3-44e8-a7d4-f03470aa71d1 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 9: DATA, STORAGE AND MIGRATION (`DATA9-*`)

Antagonistic confirmation review of `docs/specs/platform-core/` after the advisor pass (A-1 … A-17). Files read
in their current versions: 09 v1.9.1, 05-storage v1.10.1 (§5S.3.2, §5S.3.7, §5S.3.11, §5S.3.15, §5S.3.16, §5S.4,
§5S.9), 05-backup v1.9.1 (§5B.3.4, §5B.3.6), 05-authn v1.10.1 (§3.4, §4.4, §4.14, §7, §11), 05-config v1.10.1
(§1.3.1, §1.6, §1.12.2, §1.12.5), 05-tasks v1.10.1 (§5T.3.13, §5T.6.2), 06 v1.10.1 (§6.4.11, §6.5.1, §6.11.6),
05-http-api §10, 12 §12.4.4, 14 K22/K25. A-1 … A-17 and R3-1 … R8-6 are binding; nothing below asks for deleted
machinery back unless HEAD parity breaks. Spec anchors are `file §section (l.N)`; code anchors are `path:line`
at `748fd787a` (the checkout's `internal/` and `cmd/` are byte-identical to it).

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | DATA9-1 |
| MAJOR | 7 | DATA9-2 … DATA9-8 |
| MINOR | 7 | DATA9-9 … DATA9-15 |

| ID | Sev | Title |
|---|---|---|
| DATA9-1 | BLOCKER | A-3 as applied to 05-authn removed the only thing that keeps HEAD's identity lock discipline once writes stage into a caller's batch: from S4 r2 a last-use touch can again undo a revocation, one invite can be accepted twice, and a deactivation can be reverted |
| DATA9-2 | MAJOR | HEAD's locks are per `*PebbleStore` instance, but the spec builds several instances (every `legacy.Stores` constructor at P4c, books' own store at P6) and never says they share one; the counter rule still leans on deleted `CheckedWrites` |
| DATA9-3 | MAJOR | `inflight.json` is created at step g in 05-backup and §5S.3.16 but "from `previous_moved`" in §5S.3.7; between c and g only `restore_pending` is reported, so the documented "safe" override lands an older build on a half-swapped directory |
| DATA9-4 | MAJOR | §5S.3.16 lets a reset journal at `set_aside_started`/`moved` be rejected by step a; step a's "never rejected" protection names only restore steps; a half-moved store then boots as an empty fresh install and `previous/` is swept after 168 h |
| DATA9-5 | MAJOR | Machine 1 has no legal path to `applied` for a restore boot that has no P4d steps 2–3 (S2b, S3, S4 r1), and `marker_written` writes `c/backup/served_boot` before that keyspace exists |
| DATA9-6 | MAJOR | `Alias.Legacy` conflates the YAML file key with the blob field; HEAD's `json:"-"` keys (the peer allow-list) have the first and not the second, so routing is undefined, and an admin-set peer allow-list is lost on rollback (empty admits every peer) |
| DATA9-7 | MAJOR | `ReplaceOpRowsPrefix`: 05-storage row 13 puts Fixer rows under `opsMu`, 05-tasks says HEAD's lock-free path stays, `ErrRunsNotLocked` refuses it, and `RunStore` has no chunked self-committing row writer |
| DATA9-8 | MAJOR | H1–H3 are "one-way" by raising owners the guard does not model ("books' identity owner", "the config owner"); an owner a build lacks is dormant, not refused; H2's four encrypted rows have three answers |
| DATA9-9 | MINOR | Machine 4 lacks R8-2's offline-close-after-foreign transition; I5 forbids the documented foreign verdict after an offline-session crash |
| DATA9-10 | MINOR | 09 §9.2.3's list of new core rows omits `c/config/` and `c/backup/`, which ABO writes from S3 and S4 r2 |
| DATA9-11 | MINOR | 09 S2b's reset text is stale against 06/05-storage: `ResetArgs` "for `ResetFactory`" only, staging through "storage's Admin method", and a wrong §9.2.6 cite |
| DATA9-12 | MINOR | Dangling references to the deferred P−1 boot check in 05-backup and 05-storage, including a required test that no S0–S10 build can run |
| DATA9-13 | MINOR | §9.2.1 claims to be the complete list of rollback and roll-forward costs but omits the replaced-store hook's cost at roll-forward and the loss of restore coverage that every gap causes |
| DATA9-14 | MINOR | `staging-restore` of a pre-S0 archive: 09 §9.3 runs it with `cur`, 05-storage's staging-marker row refuses it |
| DATA9-15 | MINOR | Drift: `Record.InstallID` step, `configBlobSaveMu` "replaced" vs "kept", 05-backup's S2b–S7 restore-cost sentence |

## Regression check (round 8 MAJORs in this lens; advisor findings touching it)

| Item | Status | Residual |
|---|---|---|
| DATA8-1 (reset failure path) | **Fixed.** Pre-validation at staging (§5S.3.15 "Stage"), set-aside beside itself, failure rule with move-back, hook never fails boot, kernel `hook_done` for no-hook apps; 06 P0a, 09 §9.2.1 row 1 and S2b agree | DATA9-4 (rejection mid-set-aside, introduced by §5S.3.16's recovery text) |
| DATA8-2 (stripes per key) | **Superseded by A-3.** Ops families are plain, non-`Serialized` claims (§5S.9.1 rows 11, 13; §5S.3.2 step 2) | DATA9-7 (the `ReplaceOpRowsPrefix` lock rule now contradicts itself) |
| DATA8-3 (offline open launders a foreign open) | **Fixed** in §5S.4 "Offline open" step 4 and the open-stamp row | DATA9-9 (diagram) |
| DATA8-4 (offline user list) | **Fixed** (§5S.4 "Users") | — |
| DATA8-5 (open checkpoint offline finish) | **Fixed** (`applied_offline`, step-e identities; 05-backup rule (1)) | — |
| DATA8-6 (`failed` checkpoint journal) | **Fixed** (`failed_by_build`; machine 1 `failed --> opened`) | — |
| DATA8-7 (swap-back vs journal) | **Fixed** (05-backup step x restores `storage.boot.json`; `RestoreSeq` lines never re-applied; journal not moved before `applied`) | — |
| ADV-3 / ADV-S1 (A-2) | **Applied** across 03, 05-authn, 05-config, 05-storage, 06, 09; residual references to the deleted machinery are confined to review-history sections (checked: `NarrowStored`, `operator-names`, `ExtConfirmer`, `m/books/ext`, `DeleteLegacySignIns`, `reset_inflight`, `ROLLBACK_IGNORE_RESET`, `c/compat/`; `OperatorSecretOrigin` survives only for native rows) | DATA9-1 (a deleted `Expect` is still needed), DATA9-6 |
| ADV-S2 (A-3) | **Applied** in wording; not sound in mechanism | DATA9-1, DATA9-2, DATA9-7 |
| ADV-4 (A-8) | **Applied** (§5S.3.16 diagrams, I1–I7, `TestBootCrashMatrix` required at S2b, 06 boot-writer table) | DATA9-3, DATA9-4, DATA9-5, DATA9-9, DATA9-12 |
| ADV-6 (A-10) | **Fixed** (staging marker sidecar + key, `--confirm-destroy-credentials`, refusal writes nothing) | DATA9-14 |
| ADV-8 (A-16) | **Fixed** (no "second copy in `config.yaml`" clause remains; `TestCoreWritesOnlyHEADGapFillKeys`) | — |
| ADV-S5 (A-6) | **Applied** in 06 and 09 | DATA9-12 |
| ADV-S8 (A-14) | **Fixed** (no `c/compat/` usage records outside history) | — |

## Simulations (what was run; failures noted)

**Forward / rollback / roll-forward, per stage.**
- **S0–S2.** Guard rows only; HEAD ignores `c/`. HEAD's in-request `Reset` wipes them, and the next guard-aware boot reads "missing record" (fail-closed). Correct.
- **S1.** Staged restore. Rollback while staged: refused (`restore_pending`). A crash between steps c and g plus the override: DATA9-3.
- **S2b.**
  - Lineage, the reset swap and the three marker one-shots as `Seed` steps all hold, and HEAD tolerates the higher `db_version` (`internal/database/migrations.go:511-532`).
  - A staged restore on S2b has no legal journal path to `applied` (DATA9-5).
  - A reset whose set-aside crashes and is then booted after 24 h, or by another binary: DATA9-4.
- **S3.**
  - The blob and the four encrypted rows are written in place, with a `GetForUpdate` CAS (05-config §1.6). Correct.
  - `c/config/` rows are ignored by older builds, which row 2 covers, except for HEAD keys that are file- or env-only (DATA9-6).
- **S4 r1.** Core writes nothing, and gin's HEAD code keeps every mutex across its own direct commits. Correct.
- **S4 r2.**
  - Every identity write stages into core's batch, so HEAD's mutexes no longer span a commit (DATA9-1).
  - The P4c and P6 store instances do not share locks (DATA9-2).
  - r2 → r1 → r2 is a binary swap. Correct.
- **S5.**
  - `opsMu` through `legacy.TasksWriteLock` is sound **if** the executor and every other ops writer share one taskstore instance (DATA9-2).
  - `ReplaceOpRowsPrefix` (DATA9-7).
  - Rollback: HEAD's code reads every run. Correct.
- **S6, S8, S9, S10.** No legacy-row shape change. S8 archives are restorable by older builds (`legacy-abo` layout). Correct.
- **S7.**
  - The AI job store and the root source compare-and-set voluntarily (05-ai, 05-library). Correct.
  - `counter:import_path` has two allocators (DATA9-2).
- **H1–H3.** DATA9-8.

**Restores.**
- **Staged (S1+).**
  - Steps a–h and x are idempotent, and step x restores the location and lineage files. Correct.
  - Gaps: the c–g window (DATA9-3), and the pre-S4 path to `applied` (DATA9-5).
  - From S4 r2 a staged restore of a `legacy-abo/0` archive has nil coverage: sessions are deleted and every key reads HEAD's `"inactive"`. That is stated in 05-backup as a restore cost.
- **HEAD in-place (rollback below S1).** On roll-forward, served-boot pair (a), anchored head (b) or behind lineage (c) classify the store as replaced. The tail is re-applied, then the hook runs with nil revocations: sessions deleted, every key `"inactive"`. The mechanism is correct; the cost is missing from §9.2.1 (DATA9-13).
- **Older-build archive on a newer build.** Foreign, nil coverage, the same hook. Pre-swap restrictions win. Correct.

**Factory resets.**
- **`ResetStore` and `ResetFactory` from S2b.** Staging is pre-validated, there is one slot, and `fresh_rows` hold HEAD's factory defaults, so a stale `ENABLE_AUTH=false` loses. The hook never fails boot. Correct, except DATA9-4 and the stale 09 S2b text (DATA9-11).
- **An older build's reset.**
  - Pre-S2b: a whole-range delete. On roll-forward the store either inherits the install ID or reads as another install. This is the stated residual (05-storage open item 8).
  - S2b+ older build: it stages a reset itself.

**A crash at each boot phase, against §5S.3.16.**
- **P0a.** Restore c–g: DATA9-3. Reset set-aside: DATA9-4. Swap-back after a P2, P4b or hook failure: correct.
- **P0.** An unparsable boot record is quarantined (fail-open). Correct.
- **P2 → step 9.** The next boot reads a foreign open (the documented fail-closed window). It costs a `gap` line, so later restores lose coverage (DATA9-13).
- **P4a, P4b.** Atomic batches; the checkpoint is reused. Correct.
- **P4d 1.** The reset header rewrite is idempotent. Correct.
- **P4d 2, P4d 3.** Idempotent re-apply and hooks; `marker_written` precedes `applied`. Correct from S4 r2, but missing before it (DATA9-5).
- **Run 2.** A crash reruns the reset hook. Correct.
- **Run (3b).** Correct.

---

## Findings

### DATA9-1 — BLOCKER — A-3 as applied to 05-authn deleted the compare-and-set that HEAD's identity locks need once writes stage into a caller's batch

**Where.**
- 05-authn §3.4 store comment (l.535-543): "a legacy store keeps its app's own lock discipline … ABO: HEAD's keyed mutexes, inviteMu, bootstrapMu and apiKeyMu".
- 05-authn §4.4 (l.821-828): every `apikey:` writer "runs under `apiKeyMu` … so a revoke and a touch never interleave".
- 05-authn §7 (l.1373-1383); error row l.1346 ("ABO's store: both run under `apiKeyMu`").
- 05-authn review history A-3 (l.1928): "`Expect`-based single use and lost-update checks are native-store only".
- 09 §9.9 (l.1323-1328) and S4 "Data/keys" (l.1069).

**The contradiction.**
- The same `auth.Store` stages every write into the caller's `*storage.Batch` (`PutUser`, `PutAPIKey`, `TouchAPIKey`, `DeleteSession`, `TakeOneTime`, `RevokeSignInsBefore`; 05-authn §3.4 l.547-590). Core then commits, with the journal line and audit record, after the store method has returned.
- 05-storage says exactly this: "Staging into the caller's batch moves the commit out of HEAD's in-store mutexes, so a legacy store method either holds its pre-core mutex across that commit (05-tasks' `legacy.TasksWriteLock` over `opsMu`) or compare-and-sets voluntarily" (§5S.3.11 l.1392, §5S.3.2 l.379-388).
- 05-storage §5S.9.1 row 3 (l.2325) says "books' identity store stages an `Expect` on the row it read where it writes into auth's batch (05-authn §3.4)".
- 05-authn §3.4 says the opposite. No `legacy.AuthWriteLock` exists either: `TasksWriteLock` is the only lock-spanning capability (grep over the spec).

**Code.** Each HEAD lock covers a read and a **direct** commit:
- `TouchAPIKeyLastUsed` holds `apiKeyMu` from `GetAPIKey` to `p.db.Set` (`internal/database/pebble_store_auth.go:1284-1302`).
- `ConsumeInvite` holds `inviteMu` and `userWriteMu` across its plan and commit (`:1565-1569`).
- `MarkInviteEmailSent` re-reads and rewrites the invite under `inviteMu` (`internal/database/pebble_store_invite_delivery.go:159-180`).
- `RevokeSession` holds `sessionWriteMu` (`:1696-1708`).
- `userWriteMu` serializes every user-row rewrite so that "a write built from a copy read before a password change or a deactivation" cannot land after it (`:509-515`).
- `bootstrapMu` is a server variable, not a store lock (`internal/server/bootstrap.go:135-136`).

`userWriteMu` appears nowhere in the spec. 05-storage's lists (§5S.2 l.121, §5S.9.3 l.2428) name only `inviteMu`, `apiKeyMu`, `sessionWriteMu` and `userMergeApplyMu`.

**Failure from S4 r2.** A mutex released when the staging method returns protects nothing at commit:
1. *Touch vs revoke.* The touch reads R0 and stages R0+use. The revoke reads R0, stages R0+revoked and commits. The touch commits R0+use (`status: active`). This is early fix 6's defect reopened (SEC6-3), and `TestLegacyRevokeRacesTouch_NeverUnrevokes`, an S4 done-check (09 l.1077), cannot pass.
2. *Invite.* Two accepts both read the invite and both stage `Delete`, so both commit and one invite yields two accounts (`TestLegacyInviteDoubleAcceptOneWins`, 05-authn l.1509). The same holds for a sent-mark that resurrects a consumed invite.
3. *User row.* A deactivation and a password change both stage whole rows read before either commit. The later commit restores `status: active`.

**Fix.**
- 05-authn §3.4, §4.4, §7 and §11 (and the A-3 history line): **every** write ABO's legacy identity store stages into a caller's batch takes the voluntary compare-and-set of 05-storage §5S.3.2:
  - `GetForUpdate`, or `Expect` on the bytes it read, for `u:`, `apikey:`, `sess:`, `invite:`, `oauthid:`, `inviteallow:` and the bootstrap rows;
  - `Expect(key, seen)` plus `Delete` for `TakeOneTime`, with the native store's conflict rules (a touch is dropped; a status or credential change re-derives; the second take fails).
  - HEAD's mutexes stay only for writers that still commit their own batch (gin at S4 r1, books' ABS rehash, invite delivery).
- Alternative: a `legacy.AuthWriteLock` capability that core auth holds across every commit containing an `auth.Store` write, mirroring `TasksWriteLock`.
- Name `userWriteMu` in 05-storage §5S.2 and §5S.9.3, and correct 09 §9.9's "write in the caller's batch … and keep HEAD's lock discipline".
- Make `TestLegacyRevokeRacesTouch_NeverUnrevokes`, `TestLegacyInviteDoubleAcceptOneWins` and a new `TestLegacyUserDeactivateRacesPasswordChange` run through `auth.Store` and core's commit, not through HEAD's direct methods.

### DATA9-2 — MAJOR — HEAD's locks are per instance; the spec builds several instances and never requires them to share locks

**Where.**
- 06 §6.4.11 (l.1524-1529): each `legacy.Stores` constructor is called at P4c with `legacy.Handles`, and again at P4d step 3 over the pre-swap store.
- 06 §6.11.6 (l.2278-2289): separate `newAuthStore`, `newTaskStores`, `newRootSource` and others.
- 05-storage §5S.9.3 (l.2426): books' own store is `database.NewPebbleStore(h module.StorageHandles)`, issued at P6.
- 05-storage §5S.9.3 (l.2431): "a counter that a legacy store and another books path both allocate from is `Serialized`".
- 05-tasks §5T.6.2: "`opsV2TimelineTrusted` moves onto the taskstore type".

**Code.**
- `counterMu`, `opsMu`, `apiKeyMu`, `inviteMu`, `collMu` and the book stripe tables are **fields** of `PebbleStore` (`internal/database/pebble_store.go:226-244`), unlike the package-level `userWriteMu`, `sessionWriteMu` (`pebble_store_auth.go:515,525`) and `aijobWriteMu` (`pebble_store_aijobs.go:23`).
- `nextID` is a `Get`, then a direct `Set`, under `p.counterMu` (`pebble_store.go:966-989`).

**Hazard.**
- `TestLegacyFamiliesWrittenOnlyByLegacyStores` is a *package* rule. Two instances of the same package pass it while their mutexes serialize nothing.
- Concretely, books' P6 instance keeps writing `invite:` rows (`MarkInviteEmailSent`, `RecordInviteGuestPolicy`, guest tokens under `inviteMu`, `pebble_store_guest_tokens.go`) while core consumes invites through the P4c instance.
- From S7 import paths are allocated both by `legacy.LibraryRootSource` and by books' `CreateImportPath` callers (`internal/server/handlers/filesystem.go:239`, `internal/importer/path_service.go:40`). With two `counterMu`s, two roots can receive one ID and one row overwrites the other.
- The §5S.9.3 remedy ("is `Serialized`") predates A-3. A `Serialized` legacy claim now only takes a stripe at commit and admits an `Expect`; it never forces one (§5S.3.2 l.379-388). The read that precedes the `Set` is therefore unprotected.
- The same question applies to `opsMu` between the executor's P4c taskstore and any opsbridge, `childop` or ticker code that builds its own instance.

**Fix.**
- In 06 §6.11.6 and 05-storage §5S.3.11, state that books' constructors return views over **one** memoised `*store.PebbleStore` (and one taskstore) per `legacy.Handles` value, the same instances that books' `Register` receives at P6. The P4d step-3 call over the pre-swap handles is the one sanctioned second instance.
- Or move every lock that guards a legacy family to package level.
- Replace §5S.9.3's counter sentence with "allocation stays under HEAD's `counterMu` on the shared instance".
- Add `TestLegacyStoresShareOneInstance`: the P4c constructors and books' P6 store return the same lock owners.

### DATA9-3 — MAJOR — `inflight.json` timing contradicts itself, and the documented override lands an older build on a half-swapped directory

**Where.**
- 05-storage §5S.3.7 "Reader check" (l.1106-1108): `restore_inflight` "from `previous_moved` until `applied`".
- 05-backup §5B.3.4 step g (l.735): the rename of `pending.json` to `inflight.json` happens **at `swapped`**.
- §5S.3.16 (l.1820-1822): `inflight.json` exists "exactly while a restore journal is between `swapped` and a terminal state, … so `CheckReader`'s `restore_inflight` is a pure function of this diagram".
- 05-backup step 9 (l.699-702), 09 §9.2.2 (l.358-361) and S1 "Deploy" (l.998): `ROLLBACK_IGNORE_RESTORE=1` passes `restore_pending`, "which is safe because the intent is rejected later".

**Hazard.** A crash, or a deterministic panic, inside P0a after step c and before step g leaves `pending.json` in place. The probe (one `find` over the three paths) therefore reports `restore_pending`, and the operator's documented override passes it. `make rollback` then boots a pre-S2b build, which ignores the journal:
- **Between c and d** (DB path empty), the older build creates a fresh empty store there. On roll-forward step a never rejects a journal at `previous_moved` or later. Step d skips (the destination now exists), and a swap-back skips for the same reason. The original store stays in `previous/` and is swept after `core.storage.swap.keep_previous` (168 h).
- **Between d and g**, the older build serves the restored archive with no hooks, so revoked keys come back. It also writes into a store that the roll-forward then treats as the staged one.

**Fix.**
- Rename `pending.json` to `restore/<id>/inflight.json` **before** step c (journal `rechecked`, then rename, then `previous_moved`), as the reset already does at `set_aside_started`.
- Make step g only journal `swapped`, and state in §5S.3.16 that `inflight.json` exists from `rechecked` (restore) or `set_aside_started` (reset) until a terminal state.
- Align §5S.3.7, 05-backup steps c, g and 9, and 09 §9.2.2.
- Add the crash-matrix case "kill between c and g, run `prev` with the override: still refused".

### DATA9-4 — MAJOR — a reset journal mid-set-aside may be rejected without a move-back

**Where.**
- §5S.3.16 legal-recovery text (l.1816-1819): "a journal whose last step precedes `previous_moved` (restore kinds) or `set_aside` (a reset) is re-run from its last step **or rejected by step a's rule**".
- 05-backup step a (l.729): "A journal that already shows `previous_moved` or later is never rejected". That names a restore step, and a reset has none.
- Machine 1: `set_aside_started --> rejected` is labelled only "rename fails, moved back".
- 06 P0a (l.1788): "P0a re-runs those renames only while the journal's last step precedes `set_aside`".

**Hazard.** A crash after some targets moved (journal `set_aside_started` or `moved`) can be followed by one of two boots:
- a boot after `expires_at` (`intent_ttl` 24 h; for example a crash loop that systemd stops after `StartLimitBurst` and the operator returns next day);
- a boot by another binary (a recreated container on a newer image).

Step a's rule then rejects the journal, and nothing says what was moved goes back. The DB path is empty, so the boot is an ordinary fresh install. It runs without the reset's `fresh_rows`, so a stale `ENABLE_AUTH=false` wins (the SEC8-5 hole). The old store sits in `previous/` and is swept after 168 h, and I3 ("never a mix") is violated. The crash matrix does not catch this: it reboots `cur` immediately, inside the TTL.

**Fix.**
- In §5S.3.16 and 05-backup step a: a `KindReset` journal at `set_aside_started` or later is **never** rejected. Whichever core build runs it resumes it forward to `set_aside`. If a rename fails, the failure rule moves the targets back.
- Expiry and `staged_by_build` apply only to `pending.json`.
- State that rows rendered at staging stay valid for a resuming build (they are HEAD's factory defaults).
- Add `TestFactoryResetResumeAfterExpiryCompletes`.

### DATA9-5 — MAJOR — no legal journal path to `applied` for a restore boot without P4d steps 2–3

**Where.**
- §5S.3.16 machine 1 (l.1793-1797): `opened --> restore_line: P4d 2 --> hooks_done: P4d 3 --> marker_written --> applied`, and "the only legal transitions".
- 06 §6.5.1 "Stages", P0a bullet (l.1859-1877): "P4d step 3 runs the hooks from S4 … P4d step 2 exists from S4".
- 06 S4 r1 (l.1879-1885): P4d steps 2–4 skipped.
- 05-backup "Stages" (l.25-47): "step h's bookkeeping up to `applied`" is core code from **S2b**. The `c/backup/` keyspace is registered only from S4 (l.48-60).
- 05-backup step h (l.736): `marker_written` writes `c/backup/served_boot` **and** `c/storage/boot`.
- 09 S2b "Crash matrix" and §5S.3.16 scenario "a staged restore of each kind … with hooks".

**Hazard.**
- On S2b, S3 and S4 r1 a restore boot cannot reach `restore_line` or `hooks_done`. So either the journal never reaches `applied` and `inflight.json` stays, which makes `make rollback`, `deploy` and the updater refuse for good, or the implementer takes an arrow the diagram calls a defect.
- `marker_written`'s `c/backup/served_boot` write fails with `ErrOutsideKeyspace` before S4.
- The S2b required test `TestBootCrashMatrix` cannot be written against the diagram as drawn.

**Fix.**
- Add `opened --> marker_written: no restore hooks in this build (S2b–S4 r1)` to machine 1.
- State in 05-backup step h that before S4, and at S4 r1, `marker_written` writes only `c/storage/boot` and then `storage.boot.json`.
- Make the S2b matrix scenario "staged restore without hooks", with a "with hooks" scenario added by the S4 r2 PR.

### DATA9-6 — MAJOR — `Alias.Legacy` conflates the YAML key with the blob field; HEAD's file-only keys have no defined storage, and the peer allow-list is lost on rollback

**Where.**
- 05-config §1.3.1 `Alias.Legacy` (l.169): "flat key in the operator YAML file **and** in config_blob … `""` if env-only".
- §1.12.2 "Where each value lives" (l.1363-1364) and "the one storage rule" (l.1370-1372): a key with a legacy name goes to the blob, any other key to `c/config/`.
- 05-http-api §10 (l.1253-1254): `core.http.peer_allowlist` and `core.http.trusted_proxies` are `admin` (DB-settable).
- 05-http-api l.1279-1284: "keep the legacy name set to the same value **or save the setting through the settings page**".
- 09 §9.2.3 rule 1 (l.452-456): "No value an older build reads may relax a check when blank."

**Code.**
- `AllowedPeerCIDRs` and `TunnelConnectorAddrs` are `json:"-"` with a `mapstructure` YAML key (`internal/config/config.go:1705-1719`). HEAD therefore never stores them in the blob, and its API cannot set them (env-only refusal, `internal/config/update_service.go:632-635,724-725,814-817`).
- An empty allow-list "allows every peer".

**Hazard.**
- With `Legacy` set, a UI write becomes a blob field that HEAD's format has no place for. That violates A-2's "no legacy row gains a field", and older builds drop the value.
- With `Legacy: ""`, the operator file's `allowed_peer_cidrs:` is no longer read at all, which differs from HEAD at boot.
- Either way an admin who follows 05-http-api's advice and saves the allow-list in Settings has it stored in `c/config/`. A rollback below S3 then admits every peer. §9.2.1 row 2 lists only keys with no legacy name, and row 3 covers canonical env names.

**Fix.**
- Split the field: `Alias.LegacyFile` for the YAML key and `Alias.LegacyBlob` for the blob field (`""` for `json:"-"` keys). Routing follows `LegacyBlob`.
- Through S10, give the HEAD file- and env-only perimeter keys (`peer_allowlist`, `trusted_proxies`) `ProtectOperator` on legacy-backed instances (HEAD parity: env or file only).
- Delete the "save the setting through the settings page" advice, and add `TestFileOnlyLegacyKeysNeverDBStored`.

### DATA9-7 — MAJOR — `ReplaceOpRowsPrefix`'s lock rule contradicts itself across 05-storage and 05-tasks

**Where.**
- 05-storage §5S.9.1 row 13 (l.2337): "… marks, row marks, **Fixer rows** … are written under HEAD's `opsMu` through books' `legacy.TasksWriteLock` … so `ReplaceOpRowsPrefix`'s bulk write takes no commit stripe".
- 05-tasks §5T.6.2 (l.1582-1584): "A writer HEAD commits without `opsMu` keeps doing so: `ReplaceOpRowsPrefix`".
- §5T.3.13 (l.1371-1379): a store with the capability "refuses a staged write whose ctx carries no hold (`ErrRunsNotLocked`)".
- `RunStore.PutRow(ctx, b, id, r)` stages into the caller's batch (l.1239). Only `Prune`/`Recover` commit their own batches.

**Code.** HEAD clears with `DeleteRange` and commits rows in chunks (`opRowBatchRows`/`opRowBatchBytes`) **without** `opsMu`, "holding the lock for tens of megabytes of writes would stall every other operation write" (`internal/database/pebble_store_op_rows.go:107-166`).

**Hazard.** As written, books' `PutRow` outside a hold is refused (`ErrRunsNotLocked`). Inside `WriteRuns` it holds `opsMu` across one multi-megabyte `Sync` batch, which stalls every run transition, progress tick and cancel. Both outcomes are a HEAD-parity regression that A-3 forbids.

**Fix.**
- Add a maintenance-class `RunStore.ReplaceRows(ctx, id, prefix, n, at)` that commits its own chunked batches (like `Prune`) and that books implements as HEAD's lock-free `ReplaceOpRowsPrefix`. Exempt it from `ErrRunsNotLocked`.
- Remove "Fixer rows" from row 13's `opsMu` list.
- Add `TestLegacyReplaceRowsTakesNoRunLock`.

### DATA9-8 — MAJOR — H1–H3's one-way property rests on owners the guard does not model

**Where.**
- 09 §9.13 table (l.1459-1463): H1 raises "books' identity owner level", H2 and H3 "the config owner's level".
- 05-storage §5S.3.7 (l.943-948): levels are per owner, core subsystems share one `core` level, and "Owners present in the store but not in the build are reported and left **dormant**".
- 09 §9.13 l.1446: "the first one [cutover] is also the level step past books' legacy restore bound", which is `{core, 0}`.
- H2's four encrypted rows: "05-config's decision" (09), "move into `c/secrets/`" (05-config §1.12.5 l.1749), "stay as they are" (12 §12.4.4 l.434).
- 05-config: H3 "ships with or after the settings cutover". 09: "Their order is the owner's; H2 and H3 may share a release".

**Hazard.** "Every older build refuses the store" holds only for an owner that every older build declares; the legacy projection covers pre-guard builds alone. If H2 or H3 raises an owner that the S0–S2 guard builds (or any build) do not contain, those builds open the store:
- after H2, with blob secrets gone, an older build silently has no credentials;
- after H3, `enable_auth: false` wins again, which is exactly what H3 says no rollback can do.

**Fix.**
- Name the concrete owners in 09 §9.13, 05-authn §11.2 and 05-config §1.12.5: `books` for every rewrite of a books legacy family (H1 in place, H2's blob fields, H3's flags), plus `core` when the cutover changes `c/` rows. The first cutover also lifts the `{core, 0}` restore bound.
- Require `TestHardeningCutoverRefusedByEveryOlderRelease`: every release tag from S0 to the previous one refuses a store stamped by each cutover, through boot and `CheckReader`.
- Reconcile H2's four rows in all three files, and copy H3's settings-cutover dependency into 09.

### DATA9-9 — MINOR — machine 4 omits the R8-2 transition, and I5 forbids a documented verdict

**Where.**
- §5S.3.16 machine 4 (l.1885-1887) has `clean --> clean: offline close` but no arrow out of `foreign` for an offline close. §5S.4 "Offline open" step 4 (l.2113-2124) stages the owed entries, then promotes.
- I5 (l.1920) allows a foreign verdict only for "a kill between the boot's own `pebble.Open` and step 9", while step 4 says "A crash before the close is a foreign open".

**Fix.**
- Add `foreign --> clean: offline close stages owed entries (machine 2), then promotes its own number`.
- Extend I5 with "or a kill inside an offline session before its close batch".

### DATA9-10 — MINOR — the list of new core rows written on ABO is incomplete

**Where.**
- 09 §9.2.3 last table row (l.434): `c/storage/`, `c/outbox/`, `c/audit/`, `c/secrets/`, `c/integrations/instances/`, `c/tasks/def/`, `c/tasks/idem/`.
- 05-storage row 7 (l.2330): `c/config/` "is therefore written on ABO installs too".
- 05-backup (l.48-60): `c/backup/` (`journal_head`, `served_boot`, `journal_gap/`, `restore_hooked/`) from S4.
- 09 §9.3 S4 (l.812) names only `c/audit/`.

**Fix.** Add `c/config/` (S3) and `c/backup/` (S4 r2) to §9.2.3 and to §9.3's S3 and S4 cells, and make `TestLegacyToleratesNamespacedKeys` seed both.

### DATA9-11 — MINOR — 09 S2b's reset text is stale against 06 and 05-storage

**Where.** 09 S2b "Data/keys" (l.1028): "`kernel.stageReset` (it calls `legacy.ConfigResetArgs` **for `ResetFactory`**, then **storage's Admin method** …)". Compare 06 P0a (l.1788) and 05-storage §5S.3.15: `ResetArgs` runs for **both** kinds and returns `fresh_rows` for both, and staging goes through `restoreswap.Swapper.StageReset`; `storage.Admin` has no staging method (COR8-5). The same cell says the hook is "stated as not undoable (§9.2.6)"; §9.2.6 is "Permission atoms".

**Hazard.** An implementer following 09 stages a `ResetStore` with no `fresh_rows`. The reset boot then has no blob, so a stale `ENABLE_AUTH=false` wins (SEC8-5).

**Fix.** Reword the sentence to 06's: "it calls `ResetArgs` for both kinds, then `restoreswap.Swapper.StageReset`". Cite §9.2.1 row 1.

### DATA9-12 — MINOR — references to the deferred P−1 boot check are still live

**Where.**
- 05-backup B2 (l.81), §5B.3.4 boot-phase prose (l.712-714: "The updater's pre-boot check P−1 runs first"), l.955.
- 05-backup test `TestUpdaterRollbackRejectsFailingBuildsIntent` (l.1088): "P−1 rolls back …"; no S0–S10 build has P−1.
- 05-storage §5S.4 code block (l.1941): "06 P−1 updater.Boot … may restore .prev".
- §5S.3.16 and the test rows (l.1904, 2484-2485): "from P−1"; 09 S2b says "from P0 to Run (3b) (P−1 is deferred-build)".

**Fix.**
- Mark each occurrence deferred-build (A-6).
- Move `TestUpdaterRollbackRejectsFailingBuildsIntent` to 05-backup's deferred-build list.
- Make the crash-matrix range "P0a to Run (3b)" in 05-storage, matching 09.

### DATA9-13 — MINOR — §9.2.1 is "the one complete list" but omits the replaced-store and lost-coverage costs

**Where.**
- 09 §9.2.1 (l.136-142): "This table is the one complete list of what a rollback and the roll-forward after it cost or change … the R-test asserts exactly these rows".
- 05-authn §4.14 (l.1256-1260); 05-backup §5B.3.6 "Coverage at restore" (l.856).
- 14 K22 (l.46).

**Hazard.** Both costs fall on users and integrations without a release note. Neither is a stored-format effect; both are consequences of a rollback:
- After a rollback below S1, an in-place restore by that build makes the roll-forward boot delete every session and invite and set **every** `abk_` key to `"inactive"`, so every scraper, ABS client and integration key fails until each is re-enabled.
- Any rollback below S4 r2, and any crash between P2 and step 9, writes a `gap` line. Every later staged restore whose boundary precedes it then has nil coverage, with the same effect.

**Fix.** Add a §9.2.1 row (From: S4 r2) with release-note text: "If you restore a backup while rolled back, or restore a backup made before a rollback, everyone signs in again and every API key must be re-enabled by its owner or an admin." Add the matching R-test cell; one already exists as `TestOlderBuildInPlaceRestore_SameDirDetected`, so assert the key status too.

### DATA9-14 — MINOR — staging a pre-S0 archive: 09 and 05-storage disagree

**Where.** 09 §9.3 step 2 (l.886-889): "for a pre-S0 archive they run with `cur`, whose open is then a plain open". 05-storage §5S.3.7 "Staging marker" (l.1026): `staging-restore` "refuses an archive whose store has no `c/storage/install`".

**Fix.** Choose one rule. Refusal is safer under §5S.3.4 rule 4. If refusal stays, 09 says "a pre-S0 archive cannot be staged; take one after the reference host runs S0" and drops the `cur` clause.

### DATA9-15 — MINOR — drift

- **The `set_aside` record.** §5S.3.15 "Set aside" (l.1673) journals `set_aside_started` "its `InstallID` the old install's". But `restorejournal.Record.InstallID` is documented "(`set_aside` only)" (l.1755), and the undo row reads it from `set_aside` (l.1681). Pick one step.
- **The config save mutex.** 05-config §1.6 (l.1028-1036) says `Apply`'s store mutex is "replacing ABO's `configBlobSaveMu`", and then that the books store "keeps … `configBlobSaveMu`". State both: core config's `Apply` mutex, plus books' save mutex during staging. Correctness rests on the blob `GetForUpdate`.
- **The S2b–S7 restore cost.** 05-backup "Stages" (l.41-44) says "every staged restore in S2b–S7 … every API key needs an admin to re-enable it". The hook runs only from S4 r2 (06 l.1875-1885). On S2b–S4 r1 restored keys keep their archived status, which is HEAD's behaviour. Change it to "S4 r2–S7".
