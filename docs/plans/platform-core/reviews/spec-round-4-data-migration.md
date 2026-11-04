<!-- file: docs/plans/platform-core/reviews/spec-round-4-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 78401e26-f0e8-4728-b297-bf15bfa5bc8e -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4: DATA, STORAGE AND MIGRATION (`DATA4-*`)

This is an antagonistic review of `docs/specs/platform-core/` after the round-3 revision: 03 v1.6.1, 05-storage
v1.4.1, 09 v1.3.1. It works under resolution R3-1, which is binding here: one stored format per family;
legacy-format adapters reached through `Grant`/`GrantMode`; native-only data kept additively in legacy rows or
in core-only families; secrets moved and blanked; cutover deferred. R3-1 is not attacked as a design. A finding
attacks it only where the text does not carry it through, or where it breaks.

Code anchors are at `748fd787a`. Spec anchors take the form `file §section (l.N)`.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 2 | DATA4-1, DATA4-2 |
| MAJOR | 9 | DATA4-3 … DATA4-11 |
| MINOR | 11 | DATA4-12 … DATA4-22 |

| ID | Sev | Title |
|---|---|---|
| DATA4-1 | BLOCKER | Moving `basic_auth_password` and deleting its legacy row opens HEAD's Basic gate to an empty password after a rollback |
| DATA4-2 | BLOCKER | S4 r1 reads identity rows with no grant, and rolling back from r2 to r1 fails boot with `ErrGrantMissing` |
| DATA4-3 | MAJOR | An older build re-saves the stale gap-fill secret into the legacy row, and the read order then lets it beat the newer core secret on roll-forward |
| DATA4-4 | MAJOR | Shared (non-exclusive) read-write grants still allow blind books writes: config blob and `import_path:` lost updates, plus a stale books memdb |
| DATA4-5 | MAJOR | S5's `Exclusive` ops and clock grants contradict books residue writers that stay until T7/T8 |
| DATA4-6 | MAJOR | DATA3-6 and DATA3-7 are only half-fixed for S2b–S7: `PreBootRestore` cannot swap back or complete a restore, and `restore-checkpoint` has no swap |
| DATA4-7 | MAJOR | Detecting a replaced store by `journal_head` < end gives false positives (a concurrent-commit head regression or a crash after `Append`) that lock out every key, and false negatives |
| DATA4-8 | MAJOR | Core-only records keyed by legacy rows go stale after writes on the older build; "current again on roll-forward" is false |
| DATA4-9 | MAJOR | Legacy invites and sessions cannot be consumed atomically, and `StageUserMerge` cannot rewrite `invite:` rows |
| DATA4-10 | MAJOR | HEAD's store-open writers (verified-email reconcile, import-path key migration, counters) have no boot phase and collide with S4's exclusive grant |
| DATA4-11 | MAJOR | Re-seeding legacy seed rows that lack `SeedDigest` (on the first S4 boot or after a rollback) parks `core:admin` in `PendingSeed`, so `admin` is left with no permissions |
| DATA4-12 | MINOR | `auth.BootWrites`' expiry stamp cannot read HEAD's one-shot done flag |
| DATA4-13 | MINOR | The nil-coverage restore hook turns already-revoked keys into re-enableable `needs_confirmation` |
| DATA4-14 | MINOR | "One re-login on roll-forward" does not hold for core sessions issued before the rollback |
| DATA4-15 | MINOR | The journal is re-applied before its `install_id` is checked |
| DATA4-16 | MINOR | The checkpoint `install_id` on a marker-less store is unspecified (crash loop, disk leak) |
| DATA4-17 | MINOR | Restore step e leaves `.storage-applied` describing the pre-restore store |
| DATA4-18 | MINOR | The stepping-stone wording assumes an S0+ updater, and the T8 text contradicts itself |
| DATA4-19 | MINOR | The legacy cap "66" is hard-coded although S0–S2 last months |
| DATA4-20 | MINOR | The grant lists in 06 §6.11.6 and 09 §9.2.3 disagree with 05-storage §5S.9.1; "activity" and the "settings families" are named as core-served |
| DATA4-21 | MINOR | The checkpoint rule in 03 §3.7 and the phase of the fresh-install batch contradict 05-storage and 06 |
| DATA4-22 | MINOR | Restore staging's `k0-legacy` check passes for a foreign archive whose legacy rows it cannot decrypt |

## Regression check (round-3 BLOCKER/MAJOR, this lens)

| Round-3 | Status in round 4 | Residual |
|---|---|---|
| DATA3-1 (B) checkpoint reuse | Fixed: §5S.3.12 reuse needs the same `install_id` and `epoch`, no `completed_at`, and no readiness since; tests named | DATA4-16 (marker-less store) |
| DATA3-2 (B) claim-ledger orphans | Resolved by R3-1 (no hand-over) | **DATA4-2**: its replacement, adapter-selection `ErrGrantMissing`, refuses the r2 → r1 rollback |
| DATA3-3 (B) C3 deletes blob | Resolved (no step deletes the blob) | DATA4-4 (blob still has a second, blind writer) |
| DATA3-4 `migrated_to` | Resolved (no copy) | — |
| DATA3-5 journal before keyring | Fixed (06 P4d step 1; journal location move) | — |
| DATA3-6 crash after step g | Fixed for S8+ (`inflight.json`) | **DATA4-6** for S2b–S7 |
| DATA3-7 S2b–S8 restore owner | Half-fixed (`PreBootRestore`) | **DATA4-6** |
| DATA3-8 Teach-build restores | Replaced by "replaced store" detection | **DATA4-7** (false positives and negatives) |
| DATA3-9 floor at staging | Fixed for S8+ (§5B.3.4 step 4, §5S.4 step 1) | DATA4-6 (T8–S7 staging has no floor input) |
| DATA3-10 S10 floor | Fixed (`UpgradeFloor.Requires`); S10 now sets none | — |
| DATA3-11 C5 order | Superseded; the cutover phases are specified | — |
| DATA3-12 lineage, `k0-legacy` | Fixed (install_id hand-over, `KeyIDsReferenced`) | DATA4-15, DATA4-22 |
| DATA3-13 derive key and journal | Fixed (journal kids in the in-use set; `RetireUnused` in P5; prune re-chains) | — |

Round-3 MINORs checked and fixed: DATA3-14, 15, 16, 17, 18, 19, 20, 21, 22 and 24. DATA3-23 is superseded.

## Simulations

**(a) Forward S0 → S10.**

- S0–S2b are clean.
- S3 is clean except for the Basic password (DATA4-1).
- S4 r1 cannot run its decode census without a grant (DATA4-2).
- S4's first boot re-seeds HEAD rows that have no `SeedDigest` (DATA4-11).
- S4's exclusive grant refuses HEAD's every-open verified-email reconcile (DATA4-10).
- S5 T0+T1 enforces `Exclusive` while the books scheduler and the opv2/opv3 residue still write until T7/T8 (DATA4-5).
- From S7, `import_path:` has two writers and books' memdb stays stale (DATA4-4).

**(b) Roll back at each stage by binary swap, write on the older build, roll forward.**

- S2b → S2 and S2 → HEAD: clean.
- S3 → S2b/HEAD:
  - the Basic gate is open (DATA4-1);
  - one unrelated settings save on the older build re-persists the stale `config.yaml` secrets, which beat the newer `c/secrets` value on roll-forward (DATA4-3).
- S4 r2 → r1: boot fails (DATA4-2).
- S4 → S3:
  - the older build re-seeds the seed rows, and the roll-forward re-seed loses `core:admin` (DATA4-11);
  - pre-rollback core sessions survive revocation on the older build (DATA4-14).
- S5 → S4: `c/tasks/sched/<kind>` beside a clock the older build advanced is stale (DATA4-8).
- S7 → S6: the `c/ai/batch/<id>` record of a job the older build finished still says `Applying` (DATA4-8).
- Factory reset on an older build: `PebbleStore.Reset` wipes `c/` too (`internal/database/pebble_store.go:6327-6332`). The roll-forward then re-applies another install's journal (DATA4-15), and leaks the old checkpoints (DATA4-16).

**(c) Skipped-release upgrade from below the minimum-upgrade-from release.**

- A store below books v66 on a T8+ build is refused at P2 step 3 before any write (correct).
- A pre-S0 install runs HEAD's updater, which applies anything (DATA4-18).
- The 66 cap does not survive S0–S2 feature work (DATA4-19).

**(d) Restore an old ABO archive.**

- On S8+: staging checks the floors, the boot inherits `install_id`, and the hooks run (correct).
- From S2b to S7 the kernel cannot swap back or finish the journal (DATA4-6).
- Staging passes a foreign archive's legacy rows it cannot decrypt (DATA4-22).
- The `.storage-applied` sidecar overstates until step 9 (DATA4-17).
- An older build's restore with no later revocation is not detected (DATA4-7).
- The nil-coverage hook re-enables revoked keys (DATA4-13).

**(e) Crash at each boot phase.**

- P−1, P0, P2, P3, P5 and P6: no store writes.
- P0a: journaled for S8+; S2b–S7 per DATA4-6.
- P4a: the checkpoint order is fixed; a marker-less store has a gap (DATA4-16).
- P4b: idempotent legacy steps and atomic versioned steps.
- P4d: hooks are idempotent and `restore_hooked` is recorded.
- P7: batch-atomic.
- A runtime crash between the journal `Append` fsync and its commit makes the next boot run external-restore hooks (DATA4-7).

**(f) Concurrent writers of a legacy family (books versus a core grantee).**

- `Exclusive` grants hold by construction.
- Shared grants do not (DATA4-4).
- ReadDelete-granted families cannot use `Expect` (DATA4-9).
- `journal_head` regresses under concurrent commits (DATA4-7).

---

## Findings

### DATA4-1 — BLOCKER — Moving `basic_auth_password` and deleting its legacy row opens HEAD's Basic gate to an empty password after a rollback

- **Where:**
  - 05-config §1.12.2 "Secrets: moved on first write" (l.1031-1041: "the blob field is written empty and the legacy row deleted")
  - 05-config §2.3 read order (l.1415-1420)
  - 09 §9.2.1 "Re-enter moved secrets (S3 on) … the older build sees it unset" (l.137-139)
  - 09 S3 Rollback (l.659); 14 Q-E
- **Evidence:**
  - HEAD's gate admits any request whose Basic credentials equal the stored pair: `subtle.ConstantTimeCompare([]byte(pass), []byte(expectedPass))` (`internal/server/middleware/basicauth.go:81-85`). Two empty slices compare equal.
  - There is no "enabled but empty password" check anywhere in HEAD (`BasicAuthPassword` readers: `basicauth.go:82`, `internal/config/update_service.go:102,386,495`, `internal/config/persistence.go:1249,1814,1958,1993`).
  - `basic_auth_password` lives only in the encrypted row `setting:basic_auth_password` (05-config §2.2 l.1196). It is not in the gap-fill file (`internal/config/persistence.go:78-82` fills only three other keys).
  - `basic_auth_enabled` and `basic_auth_username` stay in the blob.
  - The sequence:
    1. An admin changes the Basic password on any S3+ build, so the secret moves and its row is deleted.
    2. `make rollback` to S0–S2b (or HEAD).
    3. The older build loads `BasicAuthPassword == ""` with the gate on, and any client sending `Basic <username>:` passes the perimeter.
  - With auth off, which is a supported mode, this is full access. "Sees it unset" is a security hole, not a cost.
- **Fix:**
  - (1) Add to S0's hotfix list (09 S0 "Hotfixes"; 05-authn §11.1) a fifth item: HEAD's Basic gate fails closed (503 plus a log line) when it is enabled and the expected password is empty. Every rollback target from S0 on is then safe, and the release note names the pre-S0 exposure as for the other hotfixes.
  - (2) Stop deleting the four `IsSecret` rows on move. They are already encrypted at rest (`internal/database/settings.go:189-235`). Make them the stored format of those four secrets until the settings cutover: configstore reads and writes them in place with HEAD's cipher, with `k0-legacy` acting as that family's write key; only plaintext blob fields move and blank. This also removes DATA4-3's root cause for three of the four keys.
  - R-test cell: "change the Basic password on cur, roll back, an empty password is refused on prev".

### DATA4-2 — BLOCKER — S4 r1 reads identity rows with no grant, and rolling back from r2 to r1 fails boot with `ErrGrantMissing`

- **Where:**
  - 09 S4 "Releases" (l.667): "r1 — dark: `auth` (`internal/authstore`) and `rbac` (`internal/rbacstore`) land and read every identity row through the legacy-format adapters at boot … r2 — flip: the S4 grants come into force … Rollback from r2 to r1 is a binary swap"
  - 09 §9.2.3 grant table, S4 row (l.297)
  - 05-storage D-S1 (l.45: "Holding the handle *is* the permission — for reads as well as writes")
  - §5S.3.4 "Reads are checked too" (l.609-616)
  - §5S.3.11 "Adapter selection" (l.1207)
  - §5S.4 step 4 and step 10
- **Evidence:**
  - **r1 cannot read.** P4c issues a grantee `LegacyRange` only "per storage grant" (06 l.1199 (c)), and "a core subsystem never reads a family it holds no grant on". r1 declares no grant, so its decode census has no handle: either `ErrOutsideKeyspace` or no census at all.
  - **r2 → r1 is refused.**
    - "A core subsystem that has a legacy-format adapter uses it exactly when the build declares a grant to it; without a grant it uses its native keyspace … a build whose choice differs from the store's record fails boot with `ErrGrantMissing`."
    - r2 records `backends: legacy` for `core/auth` and `core/rbac` at step 9. r1 has both adapters and no grant, so its choice is `native` and it fails boot at P2.
    - `CheckReader` cannot see the in-DB ledger (it reads only sidecars), so `make rollback` and the updater's `Boot` pass, and the restored `.prev` crash-loops into `StartLimitBurst`. This is exactly the rollback 09 promises.
- **Fix:**
  - r1 declares the S4 identity grants as non-exclusive `GrantRead` (its census needs exactly that). r2 widens them to the §5S.9.1 modes.
  - Define adapter selection as `legacy` whenever the build declares **any** grant to the subsystem for that family group, so read-only counts.
  - A subsystem that has an adapter but no grant while the ledger says `legacy` is a boot error only if the subsystem is started. A dark subsystem is not started (no native keyspace is served).
  - Add `TestRollbackR2ToR1Boots` and `TestDarkReleaseHasReadGrant`, and apply the same rule to any other stage whose adapter lands before its grant (05-library's "introduce `library` with roots read in place", l.671).

### DATA4-3 — MAJOR — An older build re-saves the stale gap-fill secret into the legacy row, and the read order then lets it beat the newer core secret on roll-forward

- **Where:**
  - 05-config §2.3 read order (2) (l.1415-1418): "a non-empty legacy primary copy … was written by an older build after the core build's last write, and is the newer value"
  - §1.12.2 rollback matrix (l.1059-1060)
  - 09 §9.2.1 (l.137-139); 09 §9.10 R-test row (l.941)
- **Evidence:**
  - After a move, the legacy row is deleted. For `openai_api_key`, `google_books_api_key` and `hardcover_api_token`, HEAD's `LoadConfigFromFile` then fills the empty field from `<db dir>/config.yaml` (`internal/config/persistence.go:42-92`), which holds the value from before the move. Core never writes that file ("read, never written", l.997).
  - The **first unrelated settings save** on the older build persists every non-empty secret from its snapshot into the encrypted rows (`persistence.go:1989-2006`) and rewrites `config.yaml` (`:2011`).
  - On roll-forward, read order (2) treats that stale, non-empty row as newer and serves it ahead of the rotated value in `c/secrets/`. A key the user rotated (and revoked at the provider) silently comes back.
  - The premise "a non-empty legacy copy can only be a re-entry" is false.
  - The R-test checks only "a secret re-entered on prev wins".
- **Fix:**
  - Either DATA4-1 fix (2): the four rows are written in place and never blanked, so nothing is stale;
  - or (if moves stay) the `c/secrets/v/<name>` record stores `legacy_fp`, an HMAC under a derive subkey of each legacy copy and the gap-fill value it blanked or saw at move time. A non-empty legacy copy whose fingerprint equals a recorded one is stale: ignored, with a health warning `legacy_secret_stale`.
  - Correct 09 §9.2.1 and 14 K13: for the three gap-fill keys the older build sees the **pre-S3 value**, not "unset".
  - R-test cell: "rotate a key on cur; roll back; save an unrelated setting on prev; roll forward: cur uses the rotated key".

### DATA4-4 — MAJOR — Shared (non-exclusive) read-write grants still allow blind books writes: config blob and `import_path:` lost updates, plus a stale books memdb

- **Where:**
  - 05-storage §5S.3.11 "Enforcement" (l.1203: "a non-exclusive `GrantReadWrite` … must be `Serialized`, so both commit with `Expect`")
  - §5S.9.1 rows 7 and 19 (l.1495, l.1508)
  - 05-config §1.12.2 "Access" (l.992-998: "The blob therefore has exactly one core write path")
  - 09 §9.2.1 `TestGrantedFamilyWritesGoThroughAdapter` (l.161-165)
  - 05-library l.657 and l.639
- **Evidence:**
  - `Serialized` gives stripes and permits `Expect`; it does not force a writer to use `Expect`.
  - Books keeps raw `Set` through `internal/database/legacyrange.go` (§5S.9.3 l.1582).
  - **Blob:** `POST /operations/set-internal-flag` writes `setting:<any key>` from the request body (`internal/server/handlers/operations/handler.go:378-397`, route `internal/server/wire_operations_routes.go:108`).
    - With `key=config_blob` it overwrites the blob with no render or `Expect`. With `key=openai_api_key` it writes a plaintext row that read order (2) then prefers.
    - An AST ratchet cannot see a dynamic key, and storage allows the write because row 7's grant is not `Exclusive`, although from S3 configstore is meant to be the only writer.
  - **`import_path:`:**
    - `UpdateImportPath` writes the whole struct read earlier (`internal/database/pebble_store_importpaths.go:187-231`).
    - The scanner writes it after a scan (`internal/scanner/service.go:677`), as do `internal/server/folder_autoscan_op.go:147` and `internal/server/handlers/filesystem.go:247,319`.
    - A rename or disable by `core/library` during a scan is reverted at scan end. `TestImportRootsStayLegacyFormat` checks only the reverse direction.
  - **memdb:** `GetAllImportPaths` serves books' live go-memdb table `import_paths` (`pebble_store_importpaths.go:21-26`, `memdb_schema.go:19`), kept current only by books' own writes (`memdb_sync.go:499`). Library's grantee writes never reach it, so books scans stale roots until restart.
  - `counter:import_path` is granted but not `Serialized`, while books' `nextID` (`pebble_store.go:966-990`) keeps its own mutex.
- **Fix:**
  - (a) Make row 7's blob and four-row grants `Exclusive` from S3 (S2b keeps `GrantRead`), and make `SetInternalFlag` refuse any key inside a granted range.
  - (b) Add a storage rule `ErrBlindWrite`: a `Set` on a key under a **shared** `GrantReadWrite` range must carry an `Expect` on that key in the same batch, for either party. Rewire books' `import_path` writers to a field-merge `GetForUpdate` + `RetryOnConflict` that updates only `LastScan`/`BookCount`, and add `TestSharedGrantRefusesBlindWrite`.
  - (c) At S7, books drops the memdb `import_paths` table and reads roots through `library`, or library stages a books participant that updates memdb in the same commit.
  - (d) Mark `counter:import_path` `Serialized`.

### DATA4-5 — MAJOR — S5's `Exclusive` ops and clock grants contradict books residue writers that stay until T7/T8

- **Where:**
  - 09 §9.2.3 S5 row (l.298: ops families, "the clock and pause keys" read-write `Exclusive`)
  - 09 S5 Data/keys (l.686: "ABO's opv2/opv3 store files stay in the books residue as legacy writers until T8")
  - 05-tasks §5T.8.2 T8 ("ABO's remaining opv2 writers outside `legacystore`", l.1255); §5T.6 rule 5 (l.1156-1159: the adapter is the only writer)
  - 04 `internal/scheduler` row (l.245: "moves when 05-tasks T7 rewires its last ABO importer")
  - 05-storage `TestS2bBooksWritesEveryFamily` (l.1618)
- **Evidence:**
  - From the T0+T1 flip, storage refuses every books runtime write to rows 9, 11, 13 and 13a.
  - ABO's `internal/scheduler` keeps running the 38 `TaskDefinition`s until T7. Its interval clock writes `setting:scheduled_task_last_tick.<name>` (`internal/scheduler/interval_clock.go:116-140`).
    - On a write failure it only warns and falls back to an in-memory clock.
    - The cadence then reverts to process lifetime: the 2026-09-09 defect that file documents (13 M activity rows), now silent again.
    - If core's copied scheduler also ticks the same kinds, two schedulers run.
  - Any opv2/opv3 residue writer still called before T8 fails with `ErrClaimMode`.
  - 05-tasks also contradicts itself about T8:
    - §5T.6 (l.1186-1190) says the release that deletes the legacy-step store methods is T8 and the floor release;
    - the T8 row says those methods are **not** deleted by T8.
- **Fix:**
  - State that T0+T1 routes **every** runtime write of rows 9, 11, 13 and 13a through `legacystore`. Books' residue may keep only code called under the legacy-migration grant, plus dead code awaiting T8.
  - Books' scheduler stops ticking at T0 (core runs the 38 definitions through `opsbridge`).
  - `TestS2bBooksWritesEveryFamily` with the S5 grants is in T0+T1's done-check.
  - Otherwise, move row 9's `Exclusive` to T7 with a `Serialized` shared grant before it.
  - Align the 05-tasks T8 row with §5T.6 and 09 §9.2.2.

### DATA4-6 — MAJOR — DATA3-6 and DATA3-7 are only half-fixed for S2b–S7: `PreBootRestore` cannot swap back or complete a restore, and `restore-checkpoint` has no swap

- **Where:**
  - 06 §6.4.5 `PreBootRestore func(stateDir string) (applied bool, err error)` (l.810-816)
  - 06 P0a row and "Stages" (l.1194, l.1220-1222)
  - 05-backup "Stages" (l.24-30), steps g, h and x (l.635-637), step 4 (l.575-581)
  - 05-storage §5S.3.12 "Restore" (l.1226: "performs the journaled directory swap of 05-backup §5B.3.4 directly")
- **Evidence:**
  - Step x (swap back after a P2 guard refusal, a failing migration or a failing hook) and step h's completion (`hooks_done`, `applied`, deleting `inflight.json`) run after `PreBootRestore` has returned. The kernel has no handle to ABO's swap or journal code, so:
    - if ABO deletes `inflight.json` before returning, a crash between swap and hooks loses the hooks (DATA3-6 regresses);
    - if it does not, nobody deletes it. Every later boot is then a restore boot: hooks re-run, sign everyone out, bump every epoch and, with nil coverage, set every key `needs_confirmation`, and `CheckReader` refuses `make rollback` forever with `restore_inflight`.
  - From T8 (S5) to S7, step 4's floor check needs the kernel's `UpgradeFloor` list, which ABO's staging has no input for.
  - `storage restore-checkpoint` needs 05-backup's swap, which core lacks until S8, so a failed migration in S2b–S7 has no documented recovery.
- **Fix:**
  - Land the journaled swap (P0a resume, step x, step h bookkeeping, `KindCheckpoint`) at S2b in core (for example `kernel/restoreswap` behind `storage.Admin`). ABO's `internal/backup` then only stages (writes `pending.json`), and the kernel reads `--print-upgrade-floor` data from its own manifests.
  - Alternatively, replace the func with `PreBootRestore interface{ Swap(stateDir) (Journal, error); SwapBack(Journal) error; HooksDone(Journal, seq) error; Floors() []storage.UpgradeFloor }`.
  - Add S2b and S5 done-checks: "restore whose boot fails at P4b swaps back; crash after swap resumes hooks once; `inflight.json` gone after hooks; `restore-checkpoint` works".

### DATA4-7 — MAJOR — Detecting a replaced store by `journal_head` < end gives false positives (a concurrent-commit head regression or a crash after `Append`) that lock out every key, and false negatives

- **Where:**
  - 05-backup §5B.3.6 "Head in the store" (l.725) and "Boot re-apply" (l.729: "Replaced store … or a revoking commit failed after its append … `Revocations` nil … every key needs confirmation")
  - `RevocationJournal.Append` (l.391-400)
  - 05-authn §4.14 "Replaced store" (l.955-963)
  - 05-storage §5S.6 ("Commit order is not key order")
- **Evidence:**
  - **Head regression.** Revocation A appends seq 10 and B appends seq 11 (lines are appended under the journal's lock). B commits `journal_head={11}`, then A commits `{10}`. Nothing makes the head monotone: no max rule, no `Serialized` declaration on `c/backup/`, no `Expect`.
  - **Crash.** A power loss or SIGKILL between the line's fsync and its batch commit, which is a window on every revocation.
  - Either case leaves the head below the end on an unreplaced store. The next boot then runs `KindExternal` hooks with nil coverage: every API key `needs_confirmation`, every epoch bumped, every session and invite deleted. That is an install-wide lockout of automation.
  - **False negative.** A restore by a non-journaling build (S1–S3's staged swap, HEAD's in-place extract) when no revocation followed the archive leaves the head equal to the end, so the hooks never run on that replaced store.
- **Fix:**
  - Declare `c/backup/journal_head` `Serialized`, and have `Append`'s staged write be `max(head, seq)` through `GetForUpdate` (a conflict retries the revoking batch; duplicate lines are harmless).
  - Decide "replaced" from store identity rather than head lag: record the store generation (`c/storage/epoch`, §5S.3.7) and directory identity in `<state_dir>/backup.location.json` at every journaling boot.
    - Same generation and head behind: re-apply the verified tail only (narrowing, idempotent), with no hooks.
    - Different generation with no staged restore: `KindExternal` hooks.
  - Tests: `TestConcurrentRevocationsHeadMonotone`, `TestCrashAfterAppendNoExternalHooks` and `TestNonJournalingRestoreDetectedByGeneration`.

### DATA4-8 — MAJOR — Core-only records keyed by legacy rows go stale after writes on the older build; "current again on roll-forward" is false

- **Where:**
  - 05-storage §5S.3.11 "Native-only data" (l.1206: "A core-only family is invisible to an older build and current again on roll-forward") and "No copy, no rebuild" (l.1209: no "side records keyed by legacy IDs")
  - 02 decision 13 ("no side record")
  - 05-tasks §5T.3.8 (l.746-750), §5T.6 rule 4 (l.1154)
  - 05-library l.664-666; 05-ai l.494-498
- **Evidence:** each of these annotates one legacy row that the older build keeps changing:
  - **`c/tasks/sched/<kind>`** sits "beside a settings clock". The rule "an older build that rewrites a legacy clock drops the additive fields; absent, they default" holds only for extra JSON members, not for this separate record. After a rollback that advanced the settings clock, `missed`, `gate_closed_at`, `last_run`, `last_fire` and `schedule_hash` are stale on roll-forward.
  - **`c/ai/batch/<id>`** (the `Applying` step and apply attempts). An older build finishes the job with its own callback; on roll-forward the core record still says `Applying`, so the apply run can be staged again.
  - **`c/library/rootext/<root-id>`** keeps `Sentinel`, `AppDir` and `Quarantine` for a root the older build renamed or deleted, and orphaned records are never swept.
  - Only `c/integrations/ext/` has a staleness rule (the URL change resets `Local`). That is the model to generalise.
- **Fix:** add one rule to §5S.3.11. A core-only record that annotates a legacy row carries `legacy_ref = {key, fp}`, the SHA-256 of the legacy row bytes (or its `Version`) at write time, written in the same batch. On read:
  - a mismatch puts the legacy-dependent fields at their stated defaults:
    - clock: `missed` 0, `gate_closed_at` nil;
    - ai: derive the state from the legacy status and never re-stage a terminal job;
    - library: keep the config-like fields, drop the path-bound ones and report;
  - a missing legacy row makes the record an orphan, swept by its owner.

  Add `TestSideRecordStaleAfterPrevWrite` per family, and delete "current again on roll-forward" in 05-storage, 05-tasks and 05-library.

### DATA4-9 — MAJOR — Legacy invites and sessions cannot be consumed atomically, and `StageUserMerge` cannot rewrite `invite:` rows

- **Where:**
  - 05-storage §5S.9.1 rows 4a and 6 (l.1491, l.1494: not `Serialized`)
  - `GrantMode` (l.1188-1189: `Expect` only under `GrantReadWrite`); `Batch.Expect` (l.252-255: `Serialized` only)
  - §5S.6 (no isolation for indexed batches); §5S.9.3 (l.1583: `inviteMu` → "`Serialized` claims + `Expect`")
  - 05-authn §3.4 `TakeLegacyInvite`/`TakeLegacySession`/`TakeOneTime`, §7 (l.1032: "atomic get+delete in a single storage batch"), §3.2 `Admin.StageUserMerge` (l.187-198)
- **Evidence:**
  - HEAD serialises invite consumption and the merge's invite owner rewrite under `inviteMu` (`internal/database/user_merge.go:510-520,536-555`).
  - In core, a get followed by a delete in one batch is not atomic without `Expect`, and a ReadDelete grantee on a non-`Serialized` range may not use one. Two concurrent accepts of one legacy invite both commit, giving two accounts from a single-use token. A legacy cookie upgraded twice mints two sessions.
  - `c/auth/onetime/` and `c/auth/session/` are not declared `Serialized` either.
  - `StageUserMerge` "rewrites the owner of every row … `invite`" with "one Expect per row". Under a ReadDelete grant a `Set` is `ErrClaimMode` and the `Expect` is `ErrNotSerialized`, so any merge whose source user has an open legacy invite fails.
- **Fix:**
  - Mark rows 4a and 6 `Serialized`, and allow `Expect` (delete-with-precondition) under `GrantReadDelete`.
  - Declare `Spec.Serialized` for `c/auth/onetime/` and `c/auth/session/`, and state that each `Take*` stages `Expect(key, seen)` plus `Delete`.
  - For the merge, specify "legacy invites of the source are deleted (audited), not re-owned" and drop `invite` from `MergeRow.Family`, or grant `invite:` read-write for owner rewrites only.
  - Tests: `TestLegacyInviteDoubleAcceptOneWins` (`synctest`), `TestUserMergeWithLegacyInvite`.

### DATA4-10 — MAJOR — HEAD's store-open writers (verified-email reconcile, import-path key migration, counters) have no boot phase and collide with S4's exclusive grant

- **Where:**
  - 05-storage §5S.9.3 (l.1581: `NewPebbleStore` → books module constructor), §5S.2 (l.98 names the init phase)
  - 06 P6 row (l.1201: "Store writes: none"), write rule (l.1165-1174)
  - 05-authn §5 `auth.BootWrites` (l.974-976: `_system` and the expiry stamp only)
- **Evidence:** HEAD's `initPebbleStore` writes on every open:
  - the `library:`→`import_path:` key migration and the `counter:library` move (`internal/database/pebble_store.go:722`, `:1060-1105`);
  - counter initialisation (`:727-742`);
  - the identification-index state (`:747`);
  - `ReconcileVerifiedEmails` (`:759`), which stamps `email_source` on `u:` rows that a rollback build rewrote and rewrites `idx:user:email` (`internal/database/pebble_store_verified_email_backfill.go:310-330`).

  The spec gives none of these a phase. At S4 `u:` is `Exclusive` to `core/auth`, so the reconcile is refused (it logs and serves on), and nothing restamps `email_source`. Verified-email status, which OAuth linking reads, then degrades. A books constructor in P6 that still runs the conversions breaks the write rule.
- **Fix:** list HEAD's init-phase writers in §5S.9.3, each with a home:
  - the import-path and counter conversions become a books `Migration` (`Additive`) in P4b;
  - counter seeding moves into the fresh-install batch;
  - `ReconcileVerifiedEmails` becomes an `auth.BootWrites` item (P4d step 4) through auth's grant, with `email_source` pinned in `TestGrantedFamilyGolden`;
  - the storage-format stamp is the guard's projection;
  - state that books' constructor writes nothing.

### DATA4-11 — MAJOR — Re-seeding legacy seed rows that lack `SeedDigest` (on the first S4 boot or after a rollback) parks `core:admin` in `PendingSeed`, so `admin` is left with no permissions

- **Where:**
  - 05-rbac §6.3 "Seed roles" (l.807-811: "treating a row without `SeedDigest` as one whose last applied seed is the translation of its atoms (restricted additions go to `PendingSeed`)")
  - §4.8 merge table and the restricted rule (l.554-565)
  - seed table (l.595: `admin` = `core:admin`); §4.6 (`core:admin` is `AuthRequired`)
- **Evidence:**
  - HEAD rewrites every seed row each boot without extra members (`internal/auth/seed.go:80-129`). Every S4 first boot, and every roll-forward after a rollback, therefore meets an `admin` row with no `SeedDigest`.
  - The prior seed is the translation of HEAD's 13 atoms (§6.1), which never contains `core:admin`. The new seed is `{core:admin}`, so the only addition is `core:admin`, which is `AuthRequired` and therefore restricted.
  - The rule gives `SeedPermissions = new seed minus restricted additions = {}` and `PendingSeed = {core:admin}`. `accept-seed` is unavailable until cutover. Effective = (∅ ∪ `Added`) \ `Removed` = ∅.
  - "Locked" is not stated as an exemption from the merge. `TestLegacyRoleReachUnchanged` catches this only if it seeds from HEAD-written rows, not from a fresh store.
- **Fix:**
  - State in §4.8 that a **locked** role (`admin`) is outside the merge: its row is always written as `{core:admin}` plus the row-rule atoms.
  - The restricted-addition rule compares against the translated legacy reach (an addition already implied by the translated atoms' reach, as `core:admin` is for HEAD's `admin`, is not restricted).
  - Add `TestReseedFromHEADRows_AdminKeepsCoreAdmin` and run `TestLegacyRoleReachUnchanged` on a HEAD-seeded store.

### DATA4-12 — MINOR — `auth.BootWrites`' expiry stamp cannot read HEAD's one-shot done flag

- **Where:** 05-authn §5 (l.974-976); 05-storage §5S.9.1 rows 8, 9 and 16.
- **Evidence:**
  - HEAD stamps keys once, recorded in `setting:system:migration:apikey_expiry_stamp_v1_done`, and after that only logs never-expiring keys found later, such as restored ones (`internal/server/apikey_expiry_stamp.go:23,40-54`).
  - That key is in no grant, so core cannot read it.
  - Run every boot, the stamp would impose expiries HEAD deliberately leaves alone.
- **Fix:** give `core/auth` a `GrantRead` on that exact key, or keep a `c/auth/expiry_stamp_done` marker that the first S4 boot seeds from the legacy flag. State the one-shot semantics.

### DATA4-13 — MINOR — The nil-coverage restore hook turns already-revoked keys into re-enableable `needs_confirmation`

- **Where:** 05-authn §4.14 step 3 (l.948-951: "set every API key to `needs_confirmation`"); §4.4 (l.657-663: "`revoked` is terminal … `needs_confirmation` can only go to `active`").
- **Evidence:** A key revoked before the archive was taken is restored as revoked. Step 3 then adds `restriction: needs_confirmation`, and an admin "confirm" revives a key whose revocation predates the archive.
- **Fix:** Step 3 applies only to restored keys whose status is `active` or `disabled`. Add the test `TestRestoreNilCoverageKeepsRevokedTerminal`.

### DATA4-14 — MINOR — "One re-login on roll-forward" does not hold for core sessions issued before the rollback

- **Where:** 09 §9.2.1 (l.134-136: "and so does the roll-forward"); 05-authn §4.3 (l.624-628), §11 (l.1189).
- **Evidence:**
  - HEAD lists and revokes only `sess:` rows (`internal/server/handlers/auth.go:540-567`).
  - A hashed session issued before the rollback (for example a stolen cookie) is invisible to the older build. It is valid again on roll-forward unless an epoch bump hit that user.
- **Fix:** Add an `auth` duty to the served ledger (§5S.3.7). The first auth-duty boot after a boot without that duty, or after a foreign open, revokes every `c/auth/session/` record created before that interval, with a journal line. If the owner rejects this, correct the claim.

### DATA4-15 — MINOR — The journal is re-applied before its `install_id` is checked

- **Where:** 05-backup §5B.3.6 "Boot re-apply" (l.729: a store with no head gets "every verified line … re-applied"); §5B.4 (l.782-783: the header is checked "after services are built").
- **Evidence:**
  - A factory reset on any older build wipes `c/` (`internal/database/pebble_store.go:6327-6332`; `internal/server/handlers/system/handler.go:538-560`), and so does a deleted DB directory. Both produce a new `install_id` and no head, while the backup directory keeps the old install's journal.
  - That journal's MACs verify with the same keyring. P4d step 2 then replays another install's revocations (for example `role.deleted` on a reused `custom.<slug>`).
- **Fix:** P4d step 2 first compares the header's `install_id` with `c/storage/install`. On a mismatch it renames the file aside, starts a new journal with a `gap` line and re-applies nothing.

### DATA4-16 — MINOR — The checkpoint `install_id` on a marker-less store is unspecified (crash loop, disk leak)

- **Where:** 05-storage §5S.3.12 "How", "Upgrade attempt and reuse" and "Completed, consumed, pruned" (l.1222-1224); §5S.4 step 5 (l.1283-1285).
- **Evidence:**
  - The marker is written after step 6, so the record's `install_id` is undefined. A crash between the checkpoint and the marker gives a new `install_id` on each boot: no reuse, a new full checkpoint per boot, and the free-space check eventually halts.
  - A checkpoint whose `install_id` is not the live store's (after a reset, or after the crash above) is in no prune class.
- **Fix:**
  - Choose the `install_id` at step 5 (hand-over or new), write it into the checkpoint record, and have a marker-less boot reuse the `install_id` of an open record for the same store generation.
  - Prune checkpoints of another `install_id` after `checkpoint_min_age`.

### DATA4-17 — MINOR — Restore step e leaves `.storage-applied` describing the pre-restore store

- **Where:** 05-backup §5B.3.4 step e (l.634: rewrites only `.storage-levels` and `.storage-format`); 05-storage §5S.3.7 applied-steps row (l.924: "never lists a step that did not run").
- **Evidence:** Between the swap and the restore boot's step 9, the sidecar lists the newer store's versions and header `install`/`epoch`. The invariant is broken, and `StoreState`/`CheckReader` read the wrong store if the `inflight` probe is unavailable or overridden (`ROLLBACK_IGNORE_RESTORE=1`).
- **Fix:** Step e rewrites `.storage-applied` from the staged bookkeeping, or deletes it so readers fail closed with `sidecar_unreadable`.

### DATA4-18 — MINOR — The stepping-stone wording assumes an S0+ updater, and the T8 text contradicts itself

- **Where:**
  - 03 §3.7 (l.495: "the updater refuses to skip it"); 02 N14; 14 K18
  - 01 glossary (l.111: defined by release, not by store)
  - 05-storage §5S.3.7 (l.924: "boot the current build once, which rewrites it")
  - 05-tasks §5T.6 versus §5T.8.2 T8
- **Evidence:**
  - Installs below books v66 run pre-S0 releases, whose updater swaps any newer binary with no check (`internal/updater/updater.go:214-280`). Such an install takes a T8+ release, which exits 78 with no automatic way back.
  - For a pre-S0 store no current build writes the sidecar, so the "boot the current build once" advice is wrong.
- **Fix:**
  - Scope the updater refusal to S0+ updaters, and say what pre-S0 auto-update installs see (the T8 release note and the Docker last-line message).
  - Change the advice to "boot any release ≥ S0 once".
  - Make 01 store-based as in N14, and align the T8 row with §5T.6 and 09 §9.2.2 (the first T8 release is the bound and deletes the legacy-step code).

### DATA4-19 — MINOR — The legacy cap "66" is hard-coded although S0–S2 last months

- **Where:** 05-storage §5S.3.5 Atomicity (l.716: `TestNoNewLegacyMigrations` pins 66); §5S.9.3 (l.1578: `UpgradeFloor{Version: 66}`); 09 §9.2.2 (l.223-226).
- **Evidence:**
  - Before S2b there is no core storage, so any books migration added in S0–S2 must be HEAD-style (67, 68, …). HEAD tolerates `db_version` > 66 (`internal/database/migrations.go:511-532`).
  - Such steps make the pin and the T8 floor wrong.
- **Fix:** Define `LegacyCap` as the highest HEAD-style step at the S2b flip. The pin and `UpgradeFloor{books, LegacyCap}` use it, and the spec states that HEAD-style steps are allowed until S2b and forbidden after.

### DATA4-20 — MINOR — The grant lists in 06 §6.11.6 and 09 §9.2.3 disagree with 05-storage §5S.9.1; "activity" and the "settings families" are named as core-served

- **Where:**
  - 06 §6.11.6 code block (l.1542-1557)
  - 09 §9.2.3 stage table (l.292-299)
  - 02 decision 13 (l.44); 03 §3.7 (l.477-480); 06 l.1177-1179; 14 K2 (l.21); README (l.44)
- **Evidence:**
  - 06 omits `core/index`, `core/library` (plus `counter:import_path`), `core/ai`, `core/providerhttp`, `core/tasks`' v1 `GrantRead` (row 12) and its `operationlog:` ReadDelete.
  - 09's S7 row omits `core/ai` and `core/providerhttp`, and its S5 row omits row 12.
  - 02 says core reaches "`setting:`/`preference:`/`pref:`", against row 16 and DATA-1.
  - Five files say core serves "activity", but row 15 gives `act:` no grant (only `syslog:` is `core/log`'s).
- **Fix:** Generate both lists from §5S.9.1 (or replace them with a pointer to it). In 02, 03, 06, 14 and the README, say "the config blob and secret rows, the clock and pause keys" and "system log rows (`syslog:`)".

### DATA4-21 — MINOR — The checkpoint rule in 03 §3.7 and the phase of the fresh-install batch contradict 05-storage and 06

- **Where:** 03 §3.7 (l.488-490: the checkpoint "is taken before the first write of that boot, by any subsystem"); 05-storage §5S.3.12 "When" (l.1220: "A boot with nothing to run takes none"); §5S.4 (l.1259 "06 P2 — read only", step 5 writes the fresh-install batch); 06 P2 "Store writes: none", P4 (a) (fresh install in P4a).
- **Evidence:** The normative 03 implies a checkpoint on every boot that writes, but 05-storage and 06 take one only for steps, a ratchet or the marker. The fresh-install batch sits in P2 (05-storage) or P4a (06).
- **Fix:** Reword 03: "a boot that will run a step, ratchet or marker write takes the one checkpoint before its first write". Move §5S.4 step 5's empty-store branch under the 06 P4a label.

### DATA4-22 — MINOR — Restore staging's `k0-legacy` check passes for a foreign archive whose legacy rows it cannot decrypt

- **Where:** 05-backup §5B.3.4 step 5 (l.583-589); 05-config §2.2 (l.1175: HEAD's cipher has no key ID), §2.4 (`k0-legacy` import).
- **Evidence:** `KeyIDsReferenced` reports the fixed ID `k0-legacy` for every legacy row. A foreign or host-loss archive's rows were encrypted with another install's `.encryption_key`, yet the ID matches this keyring, so staging passes and the rows fail later as "must be re-entered" with no warning at staging.
- **Fix:** At staging, trial-decrypt one row per legacy secret name with `k0-legacy`. If that fails, report it as `ErrUnreadableSecrets`, which needs `AcceptUnreadableSecrets`.
