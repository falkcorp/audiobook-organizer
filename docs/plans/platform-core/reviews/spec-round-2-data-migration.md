<!-- file: docs/plans/platform-core/reviews/spec-round-2-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f0efeff-bf28-440c-9d33-3545221a6967 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2 — lens DATA, STORAGE AND MIGRATION (`DATA2-*`)

Antagonistic review of the revised `docs/specs/platform-core/` (03 v1.4.0 normative; 05-storage v1.2.0,
05-backup v1.1.0, 09 after the integration pass). Code anchors are at `748fd787a`; spec anchors are
`file §section (l.N)` in the current files. Cross-cutting resolutions 5–8 of
`spec-round-1-dispositions.md` are taken as binding; findings below attack only places where they are
not carried through, or are internally broken. D1–D9 are not attacked.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 6 | DATA2-1 … DATA2-6 |
| MAJOR | 14 | DATA2-7 … DATA2-20 |
| MINOR | 9 | DATA2-21 … DATA2-29 |

| ID | Sev | Title | Primary location |
|---|---|---|---|
| DATA2-1 | BLOCKER | The "one ownership table" is static: S2b gives core families to owners that do not exist yet, and adopt/contract "claim flips" have no mechanism | 05-storage §5S.9.1, §5S.3.4, §5S.3.11; 09 S2b |
| DATA2-2 | BLOCKER | Skipped release: books legacy steps 59–66 write core-owned ops families and call code T8 deletes; "no stepping-stone release" is false | 05-storage §5S.3.5; 05-tasks T8; 09 §9.2.2 |
| DATA2-3 | BLOCKER | The rollback build can re-activate a mirror-disabled or natively revoked API key: empty legacy scopes = full access, and "legacy wins" then widens the native key | 05-storage §5S.3.11; 05-authn §11; 05-rbac §6.3 |
| DATA2-4 | BLOCKER | Post-restore revocations are native-only: the next boot's rebuild from legacy undoes them; restored legacy sessions/invites stay valid | 05-authn §4.14; 05-backup §5B.3.4 h; 05-storage §5S.3.11 |
| DATA2-5 | BLOCKER | The revocation journal never sees rollback-era revocations, nor restores done by an older build | 05-backup §5B.3.6; 05-storage §5S.3.11 |
| DATA2-6 | BLOCKER | S0 writes `c/` keys without `c/storage/install`, so S2b's first core boot halts with `ErrForeignKeys` | 09 S0; 05-storage §5S.3.4 rule 4, §5S.4 step 5 |
| DATA2-7 | MAJOR | Restore boundary is a wall-clock `Manifest.CreatedAt` taken after the checkpoint; post-archive password changes are silently reverted | 05-backup §5B.3.2, §5B.3.5, §5B.3.6; 05-authn §4.14 |
| DATA2-8 | MAJOR | Invites: mirrored (05-storage) vs read-in-place, not mirrored (05-authn, 09); the per-boot rebuild would wipe native invites | 05-storage §5S.9.1 row 4; 05-authn §11; 09 §9.2.3 |
| DATA2-9 | MAJOR | `RequiresServed` and "the build that booted before" are not computable from the served ledger: no ≥-level rule, no reset on rollback, pre-guard builds invisible | 05-storage §5S.3.5, §5S.3.7; 09 §9.2.2; 05-config §1.12.2 |
| DATA2-10 | MAJOR | Integration instances: one-shot import and no blob mirror; 09 S7's rollback claim is false and rollback-era edits are lost | 09 §9.2.3, S7; 05-config §1.12.2; 05-metadata M1.4 |
| DATA2-11 | MAJOR | Secret edits on the rollback build: the T overlay shadows T's own blob writes, and a cleared secret is never re-imported | 05-config §1.12.2 phases T/X |
| DATA2-12 | MAJOR | DATA-15 repeated: `op_result:`, `opsummary:`, `operation:` have 20 live books writers but are core/tasks `AgingOut` | 05-storage §5S.9.1 row 13; 05-tasks §5T.6; 04 |
| DATA2-13 | MAJOR | C5 removes the tasks reconcile without a final reconcile; P11 is an operator check, not a store precondition | 09 §9.5 C5; 05-tasks §5T.6 |
| DATA2-14 | MAJOR | A contract that resumes after a crash re-runs "one more rebuild" over half-deleted legacy rows and deletes identities | 05-storage §5S.3.11 Contract; 09 §9.5 |
| DATA2-15 | MAJOR | Over-cap rebuild path never removes natives whose legacy row vanished; run-once data-migration state contradicts "every boot" | 05-storage §5S.3.11, §5S.3.6 |
| DATA2-16 | MAJOR | Audit copy-forward after a restore collides with the restored `Sequence` high-water mark | 05-rbac §7.1–§7.2; 05-storage §5S.3.2 |
| DATA2-17 | MAJOR | Unencrypted archives carry the operator config file, i.e. plaintext secrets | 05-backup §5B.3.1, B3 |
| DATA2-18 | MAJOR | Checkpoint consumption needs the upgrade boot's own process to live 7 days; checkpoints accumulate until the free-space check halts boot | 05-storage §5S.3.12, §5S.4 step 14 |
| DATA2-19 | MAJOR | 06 contradicts 05-storage: storage opened under `DataDir` with `os.OpenRoot`, and an automatic checkpoint restore on failure | 06 §6.5.1 P2/P4, `Options.DataDir` |
| DATA2-20 | MAJOR | A seed role narrowed on the new build is widened again on rollback by HEAD's per-boot `SeedRoles` overwrite | 05-rbac §4.8, §6.3; 09 S4 |
| DATA2-21 | MINOR | `Mirror.Stage` checks only the primary row; uniqueness of legacy index rows is unguarded | 05-storage §5S.3.11 |
| DATA2-22 | MINOR | Guard mechanics: projection stamp needs a books `counter:` ID; raises not stated monotone | 05-storage §5S.3.7, §5S.9.1 row 10 |
| DATA2-23 | MINOR | Row 7 "exact keys" cannot express the data-dependent `metadata_sources.<id>.credentials.<k>` rows | 05-storage §5S.9.1 row 7; 05-config §2.2 |
| DATA2-24 | MINOR | No single ordered list of every-boot writers relative to checkpoint, DAG and handle issue | 06 P3/P4; 05-config §1.4; 05-authn §5; 05-storage §5S.4 |
| DATA2-25 | MINOR | Pre-boot restore resolves paths from flags/env only; revert/checkpoint restores have no revocation "since" | 05-backup §5B.3.4; 06 P0a |
| DATA2-26 | MINOR | Offline dry run must checkpoint the live store read-only; `TestDryRunMatchesLiveRun` as written is unachievable | 05-storage §5S.3.6 |
| DATA2-27 | MINOR | 05-backup mechanics: orphaned sidecars after old-build pruning; journal name not app-scoped; derived "changed during the capture" | 05-backup §5B.3.1, §5B.3.5, §5B.3.6 |
| DATA2-28 | MINOR | Fresh installs have no initial-level rule: a month of mirror phase and inactive encryption | 05-storage §5S.3.7; 05-backup B3; 09 §9.2.2 |
| DATA2-29 | MINOR | Stale or contradictory cross-references left by the revision | 09, 05-tasks, 05-storage, 03, 06, 11 |

## Regression check (round-1 BLOCKER and MAJOR, this lens)

| Round-1 | Status in round 2 | Residual finding |
|---|---|---|
| DATA-1 (B) settings families | Fixed in the table (row 16) | DATA2-1 (table not staged) |
| DATA-2 ownership four ways | Mostly fixed: one table | DATA2-8, DATA2-12, DATA2-29 |
| DATA-3 (B) no reconcile | Fixed for steady state (rebuild every boot) | DATA2-4, DATA2-14, DATA2-15 |
| DATA-4 lossy merge | Superseded by resolution 5 | — |
| DATA-5 index rows / codec | Fixed (`LegacyProjection.Rows`, `codec.LegacyJSON`) | DATA2-21 |
| DATA-6 one-shot imports | Half fixed: config re-import, tasks reconcile, clock mirror | DATA2-10, DATA2-11, DATA2-13 |
| DATA-7 three window mechanisms | Fixed (one per-owner level guard) | DATA2-22 |
| DATA-8 legacy migrations | Fixed in form (`LegacyMigration`) | DATA2-2 (claims and T8) |
| DATA-9 DAG | Fixed in 05-storage | DATA2-19, DATA2-24 |
| DATA-10 checkpoint | Partly fixed | DATA2-18 |
| DATA-11 ratchet order | Fixed | — |
| DATA-12 exact resume | Fixed | DATA2-15 (per-boot rebuild vs run-once ID) |
| DATA-13 lost update | Fixed (`Serialized` + `Expect`) | DATA2-29 (09 §9.9 still says "store mutex") |
| DATA-14 dry run | Fixed in principle | DATA2-26 |
| DATA-15 opstate / aging-out | Half fixed: `opstate:` carved out; same defect for three other v1 families | DATA2-12 |
| DATA-16 nesting | Fixed | — |
| DATA-17 reads unchecked | Fixed | — |
| DATA-18 cross-owner batch | Fixed (participant binding) | — |
| DATA-19 (B) Sequence | Fixed (durable refill before use) | DATA2-16 (restore interplay) |
| DATA-20 install-wide format | Fixed (per-owner levels) | DATA2-22 |
| DATA-21 two archive formats | Fixed (legacy-layout main + companion) | — |
| DATA-22 (B) unrecoverable backups | Fixed (opt-in, floor 2, identity export) | DATA2-17, DATA2-28 |
| DATA-23 boot-phase restore | Fixed except (e) and path resolution | DATA2-4 (e: legacy `sess:` and mirror), DATA2-25 |
| DATA-24 catalogue/audit in DB | Fixed | DATA2-16, DATA2-27 |
| DATA-25 derived acceptance | Fixed (generations) | DATA2-27 (wording) |
| DATA-26 key encoding | Fixed; tuple encoding verified by hand (prefix-first, embedded zero lowest) | — |
| DATA-27 adopt-active boot failure | Fixed (per run, over-cap dropped with notice) | — |

## Simulations

**A. Forward → rollback → forward (S4 → S3 → S4), writes on S3: deactivate a user, revoke a key, create
a key, change a setting.** Deactivate, revoke and create are honoured on roll-forward by the rebuild
(legacy authoritative; HEAD's session check enforces epoch and status, `internal/server/middleware/auth.go:200-238`).
What breaks: (1) the S3 owner of a key that S4 mirrored *disabled* re-activates it with
`PATCH /auth/api-keys/:id` and gets every role permission, and the next S4 boot copies that widening
into the native key (DATA2-3); (2) a key S4 revoked can be un-revoked the same way (DATA2-3); (3) the
S3 revocation is never journaled, so a later restore of an older archive brings the key back (DATA2-5);
(4) a seed role narrowed on S4 is reset to HEAD's seed by S3's boot (DATA2-20); (5) "change a setting":
plain keys are re-imported correctly, but a non-4 secret edited on S3 does not take effect on S3 and a
cleared secret is lost on roll-forward (DATA2-11); a Deluge/AI/webhook setting edited on S6 after an
S7 rollback is lost (DATA2-10); (6) if the S4 build already carried C4 and 30 days have passed since its
*first* boot, C4 runs at the very first roll-forward boot and makes the next rollback impossible
(DATA2-9).

**B. Skipped-release upgrade from two releases back** (a store whose books track is below 65, or one
that ran S2b and jumps to an S5+ build). Adopt-first and deferral of contracts work as specified
(resolution 6). What breaks: books legacy steps 62/65/66 write ops families that the build gives to
`core/tasks`, so boot fails; after T8 their code is gone (DATA2-2). A store that ran S0–S2 and first
opens under core storage at S2b halts on the `c/` emptiness proof (DATA-6 → DATA2-6). The 05-config
blanking rule and `RequiresServed` mis-read a pre-guard `.prev` (DATA2-9).

---

## Findings

### DATA2-1 — BLOCKER — The ownership table is static; S2b hands core families to owners that do not exist, and claim "flips" have no mechanism

- **Where:** 05-storage §5S.9.1 (l.1157-1185: "the **only** statement of who owns each legacy family"),
  generator paragraph (l.1195-1197: "emits exactly this table's books rows, modes and `Except` lists"),
  §5S.3.4 `Claim.Mode` (l.403-411, static field), §5S.3.11 "Adopt step … flips the claims to
  `ClaimMigrating`" (l.985), "Contract … flips the claims to `ClaimRetired`" (l.986); 09 S2b (l.418-427)
  and its test "`TestBooksClaimsGenerated` (claim list = `keyfamilies.go`)" (l.427).
- **Evidence:** the table has no "in force from" column. Rows 1–6 (identity, owner `core/auth`/`core/rbac`
  from S4), row 7 (`setting:config_blob`, `core/config` from S3), row 9 (scheduler clocks, `core/tasks`
  from S5), rows 11/13 (ops families, `core/tasks` from S5) are written by **books** code at S2b
  (login and key touch rewrite `u:`/`apikey:`, `internal/database/pebble_store_auth.go:1284-1297`;
  settings save rewrites the blob; every op writes `opv2:`/`opv3:`; the scheduler writes
  `setting:scheduled_task_last_tick.*`, `internal/scheduler/interval_clock.go:50`). A books claim list
  generated from this table carves those families out of books at S2b while no core owner exists, so in
  shadow mode every such write is a persisted violation (S2b's "zero violations ≥ 14 days" gate can never
  pass) and in strict mode it is refused (`ErrOutsideKeyspace`) — ABO stops working at S2b release 2.
  09's own S2b test says the opposite (claim list = all of `keyfamilies.go`). Row 14 (`syslog:`) is the
  only row that states a hand-over ("books holds it ReadWrite until the stage in which…"), which shows
  the table needs staging. Separately, `Claim.Mode` is a compile-time field, so "the adopt step flips the
  claims" and "the contract flips them to Retired" cannot be expressed: a build that registers both the
  adopt step and a deferred contract must serve `Migrating` on one store and `Retired` on another.
- **Fix:** (1) Add two columns to §5S.9.1: *Before* (owner/mode until the hand-over: books `ReadWrite` for
  every row except 10) and *Hand-over* (the flip PR / step that moves it: S3 config adopt, S4
  `core/auth#1`/`core/rbac#1`, S5 tasks adopt, journal stage for row 14). The generator emits books'
  claims per build: a family stays books' until the build contains its hand-over. Align 09
  `TestBooksClaimsGenerated` to "claim list = §5S.9.1 *Before*/*After* for this build's stage". (2) Add
  phase-resolved claims to §5S.3.4: `Claim{…, Adopt, Contract StepRef}`; resolution of owner and mode
  is a function of (build, this store's bookkeeping): before `Adopt` is applied on the store →
  pre-adoption owner (books `ReadWrite`); after → declared owner `Migrating`; after `Contract` →
  `Retired`. The kernel resolves claims at lifecycle step 4 and re-resolves after each adopt/contract
  step inside step 8; handles issued at step 12 use the final view. Test
  `TestClaimPhaseFollowsStoreBookkeeping` and an S2b fixture with books writing every family.

### DATA2-2 — BLOCKER — Skipped release: books legacy steps 59–66 write `core/tasks` families and call code that T8 deletes

- **Where:** 05-storage §5S.3.5 `LegacyMigration` (l.553-561, "Up runs ABO's MigrationFunc through the books
  repository layer over r"), `TestNoNewLegacyMigrations` (l.594), DAG example only for `core/auth#1`
  (l.592); 05-tasks T8 (l.1056: "Delete `OperationDef`, `opsbridge`, v2 RunItems, ABO's opv2 writers"),
  04 `internal/database` row (l.275: `pebble_store_ops_v3_migrate.go` etc. "delete at 05-tasks T8");
  09 §9.2.2 (l.153-154: "No stepping-stone release is required, so release manifests need no
  `min_upgrade_from`").
- **Evidence:** migration 62 sweeps `opv2:` rows, 65 calls `MigrateOpsV2ToV3` to write `opv3:run:`,
  `opv3:prog:` and `opv3:ix:*`, 66 strips plan rows from `opv2:` results
  (`internal/database/migrations.go:479-506,1238-1250,1340-1352,1412-1424`). Under §5S.9.1 rows 11/13
  those families belong to `core/tasks` (`opv3:ix:*` even `AgingOut`, `Set` refused), and books'
  `LegacyRange` holds no claim on them, so on any store below version 65 an S5+ build fails boot at
  step 8 with `ErrMigrationFailed` on every restart (the DAG has no other path). From T8 on, migration 65
  fails even without claims: it returns an error when the store lacks `MigrateOpsV2ToV3`
  (`migrations.go:1341-1344`), and T8 deletes that file. Installs skip releases (09 §9.2.1 l.96-100), so
  "two releases back" plausibly means a store at books version 63–64.
- **Fix:** (1) With DATA2-1's phase-resolved claims, books legacy steps run under the pre-adoption view;
  the tasks adopt step declares `After: {books, 66}` like `core/auth#1`, and the DAG rule says every core
  adopt step that takes families a books legacy step writes must declare it. (2) Replace 09's
  "no stepping-stone" sentence with an explicit **upgrade floor**: from the build containing T8, a store
  whose books legacy track is < 66 is refused with `ErrUpgradeFloor{InstallFirst: <last release before
  T8>}` (same shape as `ErrAdoptMissing`); or keep the three store methods that steps 62/65/66 call in
  the books residue permanently (state which). (3) R-test cell "store at books v64 → cur" and a unit test
  that runs legacy steps 59–66 against a store whose tasks adopt is pending.

### DATA2-3 — BLOCKER — The rollback build can re-activate a mirror-disabled or natively revoked key; "legacy wins" then widens the native key

- **Where:** 05-storage §5S.3.11 Never-widen row (l.980), Side-records row (l.982: "Once the previous build
  rewrote such a row, legacy wins: the native key follows the legacy row"); 05-authn §11 apikey row
  (l.936: "empty scopes → `LegacyFullScope=true`"); 05-rbac §6.3 (l.726-735); 09 S4 R-test row
  (l.465: only "key refused on prev").
- **Evidence:** the projection of a key with no exact legacy scope set is "the legacy row is written
  disabled"; nothing says what its legacy `scopes` field holds, and the stated reason is that the
  projected set "would be empty". HEAD lets the key's owner or an admin set any key back to active:
  `PATCH /auth/api-keys/:id {"status":"active"}` accepts `active` regardless of the current status
  (`internal/server/handlers/apikeys.go:337-376`), `SetAPIKeyStatus("active")` clears `DeactivatedAt`
  and leaves `RevokedAt` (`internal/database/pebble_store_auth.go:1226-1248`), and the middleware checks
  only `Status` (`internal/server/middleware/auth.go:335-344`). A re-activated row with `scopes: []`
  holds every role permission (`auth.go:405-408`). On roll-forward the row is no longer `Fresh`, the
  rebuild "follows the legacy row", and the adoption rule turns empty scopes into `LegacyFullScope=true`
  — the native key is widened permanently. The same PATCH un-revokes a key revoked on the new build
  (projected as legacy `revoked`), and the rebuild, legacy being authoritative, un-revokes it natively.
  `TestMirror_NarrowKeyNeverBecomesEmptyScope` passes trivially because a disabled row's effective set
  is empty at mirror time.
- **Fix:** (1) Normative projection for any key that is mirror-disabled, natively revoked or
  `needs_confirmation`: legacy `status:"revoked"`, `revoked_at`, `expires_at` = mirror time (refused by
  `APIKeyExpiryRefusal`, `auth.go:309-316`), `scopes` = one non-empty sentinel that maps to no atom (so
  `intersectPermissions` yields ∅), and a **decoy** `token_hash` (SHA-256 of 32 random bytes) with the
  matching `idx:apikey:hash:` row; the real hash, scopes and expiry live in the side record. No legacy
  API action can then make the real token valid on the old build. (2) Side records gain
  `Restriction ∈ {none, revoked, needs_confirmation, disabled}`; `Rebuild` sets status = the **most
  restrictive** of legacy and side (revocation is monotone); a non-`Fresh` `MirrorDisabled` row rebuilds
  as **revoked**, never "follows legacy"; `LegacyFullScope` is adopted only for rows with no side record.
  (3) Tests `TestRollbackReactivationNeverWidens` (PATCH active on prev for a mirror-disabled key and a
  natively revoked key: prev refuses the real token; roll-forward keeps the native key revoked) and a
  property test over every legacy mutation reachable through prev's API; add the cell to 09 S4's R-test.

### DATA2-4 — BLOCKER — Post-restore revocations are native-only; the next boot's rebuild undoes them (DATA-23(e) half-fixed)

- **Where:** 05-authn §4.14 (l.744-757: "in one storage batch per family" — re-apply revocations, revoke every
  session and one-time token, bump every epoch, set keys `needs_confirmation`); 05-backup §5B.3.4 step h
  (l.481); 05-storage §5S.3.11 Rebuild row (l.981) and Side-records row (l.982); 05-rbac §7.1 rbac hook
  (l.781).
- **Evidence:** during the window legacy rows are authoritative and every boot rebuilds the native copy
  from them. Nothing requires the hook to write through `Mirror.Stage`, so a key re-revoked, an epoch
  bumped or a key set `needs_confirmation` natively is reverted at the next restart, because the
  restored legacy rows still show the archive's state. `needs_confirmation` is not representable at all:
  status is a "legacy-changeable field" taken from the legacy row, and if it were projected as
  `Disabled` the `MirrorDisabled`+`Fresh` exception would re-activate it natively. "Revoke every session
  and every one-time token" names only native stores: legacy `sess:` rows (raw tokens) and legacy
  `invite:` rows restored from the archive stay valid through the upgrade-on-use fallback and the
  in-place invite lookup (05-authn §11 l.937-938), and on the previous build after a rollback. Round-1
  DATA-23(e) asked for exactly the `sess:` part.
- **Fix:** 05-authn §4.14 and 05-rbac §7.1: during the window every post-restore change is staged with
  `Mirror.Stage` (legacy + native + side `Restriction` from DATA2-3), epoch bumps included; the hook
  deletes every legacy `sess:`/`idx:sess:user:` and `invite:`/`idx:invite:username:` row (both claims
  permit deletes); `needs_confirmation` is a side-record restriction. Test
  `TestPostRestoreSurvivesRestartAndRollback`: restore inside the window, restart, the re-revoked key and
  a restored legacy session are refused; then `make rollback`, prev refuses both.

### DATA2-5 — BLOCKER — The revocation journal is blind to rollback-era revocations and to restores performed by older builds

- **Where:** 05-backup §5B.3.6 Writers (l.555: only `core/auth`, `core/rbac`), Coverage (l.557: complete unless a
  recorded gap overlaps); §5B.3.3 "an operator who rolls back can restore any plaintext archive" (l.381-386);
  05-storage §5S.3.11 Rebuild (l.981).
- **Evidence:** a previous build (S3, or any pre-S8 build) revokes keys, deactivates or deletes users and
  bumps epochs without appending anything and without recording a gap. Sequence: archive A at T0 on the
  new build; rollback; key K revoked on the old build at T1 (say, because it leaked); roll-forward (the
  rebuild honours it); restore A at T2. `Revocations` is non-nil (no gap overlaps), K is not in it, so the
  restored store reactivates K and no key is set `needs_confirmation`. Second path: a restore performed
  **by the old build** during the rollback (legal per §5B.3.3) re-applies nothing, and the next new-build
  boot rebuilds from the restored legacy rows, also re-activating every key revoked after A.
- **Fix:** (1) The per-boot rebuild diffs the previous native copy against the rebuilt one before wiping
  and appends a journal entry (`At` = boot time, `Source: rebuild`) for every restriction it finds
  (user disabled or deleted, epoch raised, key revoked, deactivated or deleted, role removed). (2) Every
  revocation batch also writes `c/backup/journal_applied = <journal line offset>`; at every boot, if the
  store's applied offset is below the journal's length (the store was replaced by any means: older-build
  restore, file copy, checkpoint restore), re-apply the journal tail after that offset through the mirror
  before listeners open; a store with no offset but journal lines newer than its last served-ledger boot
  re-applies all of them. (3) Tests `TestJournalCoversRollbackRevocation` and
  `TestOlderBuildRestoreReappliedAtNextBoot`.

### DATA2-6 — BLOCKER — S0 writes `c/` keys without `c/storage/install`, so S2b's first core boot halts

- **Where:** 09 S0 Data/keys (l.371: "Each boot writes the served-ledger entry (one key under `c/`…)"),
  Rollback (l.376); 05-storage §5S.3.4 rule 4 (l.505-508: on the boot that creates `c/storage/install`,
  `[c/, c0)` and `[m/, m0)` must be empty, else `ErrForeignKeys`), §5S.4 step 5 (l.1017).
- **Evidence:** S0–S2 write `c/storage/served/…` (and S0 ships the guard in `internal/database`, not core
  storage); none of them creates `c/storage/install`. The first core-storage boot (S2b) therefore sees a
  non-empty `c/` range on every store that ran S0 and halts. Also unspecified: the byte encoding S0 uses
  for the served-ledger key. Core keys are tuple-encoded (§5S.3.3, §5S.3.5 Bookkeeping); if S0 writes a
  plain `/`-joined string, S2b cannot read S0–S2 history, which 05-config's blanking rule and
  `RequiresServed` depend on.
- **Fix:** S0's guard creates `c/storage/install` on its first boot (same record shape), or rule 4 exempts
  exactly the reserved keys a pre-core build may write (`c/storage/served/`, `c/storage/install`).
  S0 lands `storage/keys` (pure functions, L1) and writes ledger keys with it; 09 S0 names the exact
  key. Test `TestS2bOpensStoreWrittenByS0`.

### DATA2-7 — MAJOR — The restore boundary is a wall-clock `CreatedAt` taken after the checkpoint; post-archive password changes are reverted

- **Where:** 05-backup §5B.3.2 `Manifest.CreatedAt` (l.161, no definition relative to the checkpoint),
  `RestoreContext.Revocations` "recorded after `Manifest.CreatedAt`" (l.308-310); §5B.3.5 steps 4–5
  (checkpoint first, manifest built afterwards, l.500-507); §5B.3.6 Writers ("Append … before the
  revocation's batch commits", l.555); 05-authn §4.14 step 1 (l.749-751); 05-rbac §7.1 copy-forward
  ("records newer than `rc.Manifest.CreatedAt`", l.778).
- **Evidence:** a revocation journaled at t1, committed at t2 > checkpoint time, is absent from the
  archive but has `At` = t1 < `CreatedAt` if `CreatedAt` is the manifest-build time, so it is not
  re-applied (the key comes back). The same window loses audit records between the checkpoint and
  `CreatedAt`. A clock step makes it worse. Re-applying `credential_epoch.bumped` kills sessions but
  restores the archive's password hash: a password changed after the archive (for example after a
  compromise) is silently reverted to the old one.
- **Fix:** record in the manifest, **before** the checkpoint call, the revocation journal's line offset
  (`journal_offset`) and the audit `Sequence` high-water mark; re-apply every journal line after that
  offset and copy forward audit records above that mark (clock-free). Add journal kind
  `credential.changed`; for each such user the hook sets `must_reset_password` (password login refused
  until reset) instead of only bumping the epoch. Test `TestRevocationCommittedAfterCheckpointReapplied`.

### DATA2-8 — MAJOR — Invites are mirrored in 05-storage but read in place and not mirrored in 05-authn and 09

- **Where:** 05-storage §5S.9.1 row 4 (l.1170: `invite:`, `inviteallow:` + `idx:invite:username:` → `core/auth`,
  Migrating, mirror `auth.invite`), §5S.3.11 Write-path row lists `idx:invite:username:` (l.979);
  05-authn §11 invite row (l.938: legacy invites "readable in place by presented token until they
  expire", native invites "hash-only and not mirrored"); 09 §9.2.3 (l.172 includes `invite:` in the
  mirrored group, l.174 "Native invites … not mirrored"); 09 §9.6 row 25 (l.621).
- **Evidence:** under `ClaimMigrating` the new build can delete a consumed legacy invite only through
  `Mirror.Stage`, which also writes native rows and a side record. If `auth.invite`'s `Native` range
  covers `c/auth/onetime/invite/`, the per-boot rebuild (wipe `Native`, rebuild from legacy, drop side
  records without a legacy row) deletes every invite issued on the new build at each restart. Legacy
  `invite:<token>` holds the raw token (`internal/database/store.go:1004-1012`), which SEC-19's reasoning
  forbids copying.
- **Fix:** split row 4: `invite:` + `idx:invite:username:` → `core/auth` `ClaimAgingOut` (read in place by
  presented token, deleted on accept or expiry, like `sess:`); `inviteallow:` stays a mirror. Remove
  `idx:invite:username:` from the §5S.3.11 write-path list, remove `invite:` from 09 §9.2.3's mirrored
  row and §9.6 row 25, and keep native invites outside every mirror's `Native` range.

### DATA2-9 — MAJOR — `RequiresServed` and "the build that booted before" cannot be computed from the served ledger as specified

- **Where:** 05-storage `Served` (l.531-539: "Level of Owner has served this store for at least For"), served
  ledger `c/storage/served/<owner>/<level>` = `{first_boot, last_boot, build}` (l.743); 09 contract rule
  (l.146-154); 05-config §1.12.2 blanking (l.781-785: "the build that booted this store immediately before
  the current one (09 served ledger)").
- **Evidence:** (a) Whether a build at level 5 satisfies `RequiresServed{core, 2, 30d}` is undefined;
  under an exact-level reading, a store that skipped S3 (S2b → S6) never satisfies C3 and stays in mirror
  phase forever. (b) The clock is not reset by a rollback: S4 first boots at t0, rollback to S3 at t10,
  roll-forward at t12, and a build carrying C4 that boots at t31 sees "30 days since first boot" and runs
  C4 at once — after the operator has just signalled a problem, and removing the next rollback.
  (c) Pre-guard builds write no ledger entry, so a rollback to a HEAD-era `.prev` is invisible: the
  blanking rule then believes the previous build is ≥ T and blanks plaintext secrets for a `.prev` that
  cannot read the legacy-format rows (05-config's own rollback matrix row "X → pre-T" is then violated).
  P10 is a manual check and does not cover Docker or auto-update installs.
- **Fix:** define in 05-storage: `Served{Owner, Level, For}` is met when the latest uninterrupted run of
  boots by builds whose supported level ≥ `Level` has lasted `For` (wall clock from the first boot of that
  run); a boot by a build with lower supported level, or a detected **foreign open**, ends the run. Foreign
  opens are detectable: every pre-guard open rewrites `<db>.storage-format` by temp file + rename
  (`internal/database/pebble_store.go:776-786`, `storage_format.go:285-300`), so each guard-aware boot
  records the sidecar's (device, inode) after writing it and treats a different value at the next boot as
  "another binary opened this store". 05-config's blanking rule should use the actual rollback slot: probe
  `<exe>.prev --print-storage-levels` (the slot the updater and `make rollback` share, 09 S0), falling
  back to "not ≥ T" when the probe fails or a foreign open was detected. Test
  `TestContractClockResetsAfterRollback`.

### DATA2-10 — MAJOR — Integration instances: one-shot import, no blob mirror; 09 S7's rollback statement is false

- **Where:** 09 §9.2.3 integration row (l.177: "first-boot import"), S7 Rollback (l.504: "S6 reads
  Deluge/AI/SMTP settings from the blob, which the mirror keeps current"), S7 Config (l.502);
  05-config §1.12.2 mirror rule 1 (l.775-776: keys config does not own, "such as integration families
  until their import, are carried forward from the stored blob unchanged") and import bullet (l.764-766);
  05-metadata M1.4/M1.10 (no blob mirror; `grep blob` finds nothing in 05-metadata, 05-download, 05-ai,
  05-notify).
- **Evidence:** after the S7 import, edits made through `Integrations.Put` (Deluge URL, AI endpoints,
  metadata sources, webhook URLs) are not written back to the blob, which keeps the pre-import values, so
  a rollback to S6 runs on stale settings. In the other direction, S6 edits made during the rollback land
  in the blob and are never re-imported (one-shot import), except the secret part, which 05-config
  re-imports under its `integrations.*` name — leaving, for example, a new password paired with the old
  URL, and bypassing SEC-15's "URL change drops secrets" rule. This is DATA-6's defect for one more family.
- **Fix:** during the window, give integration-bound legacy keys the config treatment: (a) hash-keyed
  re-import every boot into the instance row through `Put` (respecting `Origin=pinned`), URL and secret
  re-imported together; (b) `Put` on a legacy singleton instance (and `ai/<id>`, `metadata/<id>`,
  `notify/webhook-<n>`) re-renders the corresponding blob keys and import hashes in the same batch.
  Or declare S7 rollback lossy for these settings in 09 S7, the ledger and the R-test. Either way, change
  09 S7's rollback row.

### DATA2-11 — MAJOR — Secret edits on the rollback build: the T overlay shadows T's own writes, and a cleared secret is lost

- **Where:** 05-config §1.12.2 phase T (l.749: overlay rows for every secret field; "Nothing else changes"),
  mirror step 2 (l.777-778: legacy-format rows for every secret T reads), import rule (l.762-763: "an empty
  value is not an import"), rollback matrix "T → X again" (l.793).
- **Evidence:** for any secret outside HEAD's 4 (Deluge password, AcoustID key, OAuth client secrets, metadata
  credentials), the X mirror writes `setting:<legacy-name>`, which the T build overlays on the blob. When
  the operator changes that secret on T, T writes blob plaintext as today; the stale row still overlays
  it, so the change does not take effect on T. A **clear** is worse: blob value empty, row unchanged, T
  keeps the old secret, and on roll-forward the empty value is "not an import", so `c/secrets/` keeps the
  credential the operator removed. The import hash already distinguishes the mirror's own blanking (it
  records `h` of what it wrote), so the "empty is not an import" exception is unnecessary.
- **Fix:** phase T also **writes**: when T saves a secret field it writes (encrypted with the legacy key)
  or deletes the legacy-format row and blanks the blob field. X's re-import treats a hash mismatch on an
  empty value or an absent row whose import record showed it present as a deliberate clear →
  `secrets.Delete`; drop the l.762-763 exception. Add R-test rows "change and clear a non-4 secret on prev".

### DATA2-12 — MAJOR — `op_result:`, `opsummary:` and `operation:` have live books writers but are `core/tasks` `AgingOut` (DATA-15 repeated)

- **Where:** 05-storage §5S.9.1 row 13 (l.1179: v1 `operation:`, `operationlog:`, `op_result:`, `opsummary:` →
  `core/tasks`, AgingOut, "read in place … deleted by retention"); 05-tasks §5T.6 v1 row (l.976: "never
  written"); 04 `operationsStore` row (l.437: "`OperationStore` v1 23 → books *legacy* (deleted when its
  last caller moves)").
- **Evidence (non-test call sites at `748fd787a`):** `CreateOperationResult` (writes
  `op_result:<op>:<book>`, `internal/database/pebble_store_operations.go:438-445`) — 15 sites in 9 files:
  `internal/server/{metadata_ops,metadata_candidate_op,metadata_batch_candidates,bulk_apply_preview,apply_when_scanned_op}.go`
  (e.g. `metadata_ops.go:391,440,1056,1105`) and
  `internal/maintenance/jobs/{backfill_merge_undo_pointers,merge_chapter_groups,bulk_deluge_import,scan_composer_tags}.go`;
  `SaveOperationSummaryLog` (`opsummary:`) — `internal/maintenance/jobs/backfill_book_files.go:197`,
  `recompute_book_aggregates.go:265`, `backfill_file_hashes.go:1440`; `UpdateOperationStatus`
  (`operation:`) — `internal/operations/registry/legacy_op_status.go:244`, `legacy_backfill.go:218`.
  Strict claims refuse every one (`Set` on `AgingOut`, and books holds no claim); shadow mode keeps S2b's
  gate red.
- **Fix:** move `op_result:`, `opsummary:` and `operation:` to books `ReadWrite` carve-outs exactly like row 12
  (`opstate:`), aging out when their writers reach zero; extend `TestOpStateWriters` into
  `TestLegacyOpsWriters`, covering these 20 sites and an AST scan for writes to every ops family under
  `opsbridge` (T0–T7), with a target batch per site (T2/T5/T7); fix 05-tasks' v1 row and keep 04's
  statement, which is the correct one.

### DATA2-13 — MAJOR — C5 removes the tasks reconcile without a final reconcile

- **Where:** 09 §9.5 C5 (l.587: precondition "P11 shows no legacy non-terminal run lacking `migrated_to`";
  action "remove reconcile and clock/pause mirrors"); 05-tasks §5T.6 boot reconcile (l.971: "Until the
  tasks contract (09 C5) removes it"), compat paragraph (l.989-998).
- **Evidence:** P11 is a command an operator runs on a scratch copy of one store (09 §9.7 l.649); contracts run
  automatically on every store once `RequiresServed` holds. A store that rolled back to S4, enqueued
  runs there, and then received a C5 build loses those runs: the C5 build has no reconcile, nothing
  adopts them, and after the level raise no build that could run them can open the store. The identity
  contract has the equivalent safety step ("first runs one more rebuild", 05-storage l.986); the tasks
  contract has none.
- **Fix:** the C5 contract migration runs the per-run copy-and-mark one last time for every legacy
  non-terminal run lacking `migrated_to` (over-cap runs dropped with the existing notice) **before** it
  raises `core` to 4, in the same `BootBlocking` step; the reconcile code is deleted only from a build
  that refuses stores where C5 has not run (S10 rule). Replace "P11 shows…" with this store-side
  precondition; keep P11 as an operator preview.

### DATA2-14 — MAJOR — A contract resumed after a crash re-runs "one more rebuild" over half-deleted legacy rows

- **Where:** 05-storage §5S.3.11 Contract row (l.986: "first runs one more rebuild …, folds side records …,
  deletes legacy primary and index rows in chunks"); §5S.3.6 exact resume (l.662-668); 09 §9.5
  ("idempotent on resume", l.579-581).
- **Evidence:** the rebuild wipes `Native` and recreates natives from legacy. If the process dies after some
  delete chunks committed, the next boot resumes the contract; if the rebuild runs again, every identity
  whose legacy rows were already deleted vanishes natively (users, keys, roles). The step-9 rebuild has the
  same hazard if it still considers the mirror "in phase mirror" while the contract is half done. The
  data-migration state machine (`pending, running, verifying, done, failed, held`) has no state recording
  that the rebuild and fold are done.
- **Fix:** make the contract three persisted phases in `c/storage/dm/<id>`: `folded` (rebuild + fold in one
  committed batch, before the level raise), `raised`, `deleting` (chunks with cursor). Resume skips
  completed phases. Once `folded` is committed, the mirror is out of phase *mirror* and step 9 skips it.
  Test `TestContractCrashMidDeleteKeepsIdentities` with `CrashClone` at every chunk.

### DATA2-15 — MAJOR — The over-cap rebuild path never deletes stale natives, and its run-once state contradicts "every boot"

- **Where:** 05-storage §5S.3.11 Over-the-caps row (l.984: online `core.storage.mirror-rebuild.<name>`, "Every
  boot starts a new rebuild generation", reads switch to the native copy when it completes), Adopt-step row
  (l.985: the versioned adopt step "runs the first rebuild"); §5S.3.6 (`ID … stable forever`, l.624; "One
  run per data migration", state ends at `done`, l.659-661); §5S.3.5 size cap (l.595).
- **Evidence:** the online rebuild visits legacy rows only; a user deleted on the previous build keeps its
  native row, and once reads switch to the native copy that user can authenticate again — the DATA-3 hazard
  on the over-cap path. A data migration whose state is `done` is never run again under the same ID, so
  "every boot a new generation" cannot use this mechanism as specified. The adopt step is a `Migration`,
  so running the first rebuild inside its `MigrationTx` fails with `ErrMigrationTooLarge` exactly for the
  families that need the over-cap path, failing boot.
- **Fix:** the online rebuild writes into `Native/<gen>/`; reads stay on the read-through view until the
  generation completes; one small batch then sets `c/storage/mirror/<name>/current = gen` and
  `DeleteRange`s the previous generation (which removes stale natives). The data-migration ID carries the
  generation (`core.storage.mirror-rebuild.<name>.<gen>`). The adopt step only records adoption and
  creates the side range; the first rebuild happens in lifecycle step 9 under the same cap rules.

### DATA2-16 — MAJOR — Audit copy-forward after a restore collides with the restored `Sequence` high-water mark

- **Where:** 05-rbac §7.1 (l.776-781: audit's hook "copies every record newer than `rc.Manifest.CreatedAt`
  forward into the restored store"), §7.2 (l.785-792: `rec/<seq>` from a `storage.Sequence`); 05-storage
  §5S.3.2 `Sequence` (l.264-276).
- **Evidence:** the restored store's audit sequence is at the archive's high-water mark H_a; the pre-swap
  store's records reach H_p > H_a. If the hook copies records with their keys (the natural reading of
  "copies records forward"), the next allocations H_a+1… blind-write over copied records — audit loss
  exactly where SEC-17 wanted none. If it renumbers, nothing says so.
- **Fix:** specify one of: (a) before copying, raise the restored store's audit sequence to ≥ H_p (a durable
  refill to `max(hw, H_p)`), then copy keys verbatim; or (b) renumber copied records in their original
  order and keep the original seq in a field. Use DATA2-7's manifest high-water mark as the boundary.
  Test `TestRestoreAuditCopyForwardNoSeqCollision`.

### DATA2-17 — MAJOR — Unencrypted archives carry the operator config file, i.e. plaintext secrets

- **Where:** 05-backup §5B.3.1 config row (l.76: class `SystemOfRecord`, "always", companion `config/<file>`);
  B3 / §5B.3.3 (l.416-418: while encryption is inactive "secret-class items are omitted"); `ErrUnencryptedSecrets`
  (l.354); 05-config §2.2 (`smtp_password`, `abs_jwt_secret`, `cf_policy_api_token` are "env / viper file
  only"; `deluge_web_password` and others are settable in the file).
- **Evidence:** upgraded installs default to unencrypted archives (B3), and the config file is copied into
  every companion archive as non-secret. Operators routinely keep exactly these secrets in the YAML file.
  HEAD archives the Pebble directory only (`internal/backup/backup.go:283-428`), so this leak is new, and
  archives are what operators copy off-host.
- **Fix:** 05-config exposes `ConfigFileHasSecrets()`; when true the config-file item is class `Secret`
  (encrypted archives only); unencrypted archives carry a redacted copy (secret fields removed, manifest
  flag `config_redacted`), and restore warns that those values must come from env or be re-entered.
  `<db dir>/config.yaml` (ABO's plaintext mirror) is never archived. Test `TestPlainArchiveHasNoConfigSecrets`.

### DATA2-18 — MAJOR — Checkpoints are consumed only if the upgrade boot's own process lives 7 days

- **Where:** 05-storage §5S.3.12 Crash-loops and Consumed rows (l.1000-1001), lifecycle step 14 (l.1028:
  "after `checkpoint_min_age` of serving: mark **the boot's** checkpoint consumed"), checkpoint record
  (l.746, l.999); 09 §9.2.2 disk headroom (l.163-166).
- **Evidence:** a later boot has no checkpoint of its own, so the upgrade boot's checkpoint is consumed only
  if that process serves 7 days uninterrupted. Restarts are routine (RC builds on every push to main,
  daily auto-update, host reboots), so checkpoints stay unconsumed and "an unconsumed checkpoint is never
  pruned automatically". Each one diverges towards a full copy as compaction rewrites SSTs; the next
  upgrade's free-space check (live size + 2 GiB) then halts boot. Also: the from-vector changes as soon as
  one versioned step commits, so a crash loop that makes progress takes a new checkpoint every boot
  (contradicting `TestCheckpointSurvivesCrashLoop`), and `.migration-checkpoint` then names the newest,
  not the pristine one.
- **Fix:** persist `{completed_at}` on the checkpoint record when its boot finishes all steps; any later boot
  marks it consumed once wall-clock age ≥ `checkpoint_min_age` and the served ledger shows ≥ 1 boot after
  completion; prune unconsumed checkpoints whose from-vector is an ancestor of a consumed one. Reuse rule:
  reuse any unconsumed checkpoint whose from-vector is an ancestor of the current vector (same Pebble
  format, forward progress only); `.migration-checkpoint` names the oldest unconsumed. Align 09 l.165
  ("pruned by age as well as boot count").

### DATA2-19 — MAJOR — 06 contradicts 05-storage on where the store lives and on migration-failure behaviour

- **Where:** 06 `Options.DataDir` (l.534-536: "opened with os.OpenRoot and never escaped (secrets key file,
  Pebble dir, backups)"), P2 (l.790: "Open storage under `DataDir` (`os.OpenRoot`)"), P4 failure column
  (l.792: "checkpoint restored per 05-storage"), error table (l.869), `TestMigrationFailureHaltsBoot`
  (l.935: "storage unchanged after checkpoint restore"); 05-storage §5S.3.12 Where (l.998: the DB may be
  on another filesystem than the data dir), §5S.3.5 Failure (l.597: earlier steps stay committed;
  checkpoint unconsumed), Restore (l.1003: offline only).
- **Evidence:** ABO's DB path is a free flag/env and production was relocated off the state directory
  (`internal/config/config.go:2519-2545`, the 2026-09-09 outage); `--db /data/audiobooks.pebble` in
  `Dockerfile:104`. Opening under an `os.Root` of `DataDir` cannot reach such a DB. 06's automatic restore
  on failure would also conflict with the crash-loop reuse rule and with non-atomic legacy steps.
- **Fix:** 06 P2: open storage at `core.storage.path` resolved per 05-storage §5S.4 step 1 (absolute; confine
  to the DB's parent directory if confinement is wanted); `DataDir` holds state files only. P4 and the
  error row: "fail-closed; checkpoint kept unconsumed for the offline `storage restore-checkpoint`; no
  automatic restore". Rewrite the 06 test to assert committed earlier steps and an unconsumed checkpoint.

### DATA2-20 — MAJOR — A seed role narrowed on the new build is widened again on rollback

- **Where:** 05-rbac §4.8 ("Seed merge that never clobbers operator edits", l.526), §6.3 (seed-role
  `Added`/`Removed` in side records, l.713-721); 09 §9.2.3 rule 1 ("A role may be mirrored narrower",
  l.186); 09 S4 Rollback (l.464).
- **Evidence:** HEAD rewrites every seed role's permission set on every boot
  (`internal/auth/seed.go:80-129`, called at `internal/server/server_lifecycle.go:1102`). An operator who
  removed, say, `books:delete` from `editor` on S4 sees the legacy row mirrored narrower, but the first
  S3 boot after a rollback resets it to HEAD's seed, so every editor regains delete for the rollback's
  duration. The rebuild restores the narrowing on roll-forward (side record kept), so the exposure is
  silent.
- **Fix:** while the role mirror is active, a native edit of a seed role creates a custom role
  (`custom.<seed>-<n>`) holding the edited set and reassigns the seed role's members in one batch, which
  the old build represents faithfully; or refuse seed-role edits until C4. Add the case to 09 S4's
  Rollback row and the R-test.

### DATA2-21 — MINOR — `Mirror.Stage` guards only the primary row; legacy unique indexes are unguarded

- **Where:** 05-storage §5S.3.11 `Stage` (l.971-974: "an Expect on the legacy primary row's prior value"),
  §5S.9.3 (mutexes → `Serialized` + `Expect`, l.1256).
- **Evidence:** two concurrent creates of username "bob" with different IDs each expect only their own new
  `u:<id>` to be absent and both commit; `idx:user:username:bob` is last-writer-wins and the old build
  resolves to whichever won. HEAD serialises these checks with `inviteMu`/`userWriteMu`
  (`internal/database/pebble_store.go:232`).
- **Fix:** `Stage` also adds `Expect` on every unique legacy index row it writes (`idx:user:username:`,
  `idx:user:email:`, `idx:apikey:hash:`, `idx:role:name:`, `idx:oauth:sub:`): prior value, or absent on
  create. Test with `-race`.

### DATA2-22 — MINOR — Guard mechanics: the projection stamp needs a books counter ID, and raises are not stated monotone

- **Where:** 05-storage §5S.3.7 legacy-projection row (l.745), `LegacyGuardProjection` "declared by books only"
  (l.729-736), §5S.9.1 row 10 (l.1176: keys owned by `core/storage`); raise procedure (l.753-762).
- **Evidence:** the stamp is a `UserPreference` row whose `ID` comes from `counter:preference`
  (`internal/database/storage_format.go:338-356`; reset path `pebble_store.go:6346-6357`), a books-owned
  family (row 19). `core/storage` owns the stamp key but cannot allocate that ID. The raise procedure writes
  "the new level" with no `max`: contracts of different core subsystems have no `After` edges between them,
  and the tie order happens to be ascending only by luck.
- **Fix:** the projection writer keeps the existing record's `ID`, and an absent stamp is created by books'
  constructor (or books owns row 10 and the guard calls a books-supplied writer). State `level :=
  max(stored, RaisesLevel)` in the raise procedure, and add `TestRaiseNeverLowersLevel`.

### DATA2-23 — MINOR — Row 7's "exact keys" cannot express the data-dependent metadata-credential rows

- **Where:** 05-storage §5S.9.1 row 7 (l.1173: "exact keys … every legacy secret row `setting:<legacy-name>`");
  05-config §2.2 (legacy row `metadata_sources.<id>.credentials.<k>`).
- **Evidence:** `<id>` and `<k>` come from data, so the key set is not known when the build declares claims;
  each new source would fall back to books' `setting:` claim.
- **Fix:** row 7 adds a sub-prefix claim `setting:metadata_sources.` (ends in `.`; verify that no books key
  starts with it) beside the exact keys.

### DATA2-24 — MINOR — No single ordered list of the every-boot writers

- **Where:** 05-storage §5S.4 (rebuild at step 9, handles at step 12); 06 P3 (l.791: `Store.Load` applies DB
  overrides **before** P4's checkpoint and migrations); 05-config §1.4 steps 5–7 (l.515-521: config import
  after storage open, position relative to the DAG unstated); 05-authn §5 (l.761-764: `auth.New` "rebuilds
  the core copies", i.e. the rebuild owned by auth at service build, not by storage at step 9); 05-tasks
  boot reconcile (unplaced).
- **Evidence:** the config re-import, the identity rebuild, the tasks reconcile, the revocation-journal
  re-apply (DATA2-5) and post-restore hooks all write at boot; whether they precede the checkpoint (making it
  non-pristine), the DAG (contracts reading stale native rows) or handle issue differs between files.
- **Fix:** 05-storage §5S.4 gains one ordered "post-DAG reconcilers" step: (a) mirror rebuilds, (b)
  revocation-journal tail re-apply, (c) config import/re-import, (d) tasks reconcile and clock adoption, (e)
  post-restore hooks; all after the checkpoint and the DAG and before listeners. 06 P3 reads overrides
  after (c); 05-authn §5 says the kernel runs the rebuild.

### DATA2-25 — MINOR — The pre-boot restore resolves paths from flags and env only

- **Where:** 05-backup §5B.3.4 boot phase (l.469-470: "only the data directory and the DB path are resolved,
  from flags and env"), step a (l.474: reject when recorded targets ≠ paths resolved now); 06 P0a (l.787:
  "Only the data directory is resolved"); `RestoreContext` (l.301-313).
- **Evidence:** ABO also reads `database_path` from the YAML config file (`internal/config/config.go:2246,2354`,
  resolution at `:2519-2545`), so on such installs the resolved path never matches and every restore is
  rejected (safe, but the feature never works). For `KindRevert` and `KindCheckpoint` there is no manifest,
  so `CreatedAt` (and therefore `Revocations`) is undefined.
- **Fix:** the boot phase trusts the absolute targets recorded in `pending.json` and verifies after the swap
  that storage opened at the same path (else swap back); align 06 P0a wording with 05-backup. For revert and
  checkpoint restores, define the boundary as the journal offset recorded when the swapped-out store was
  last live (journal record `previous_moved`).

### DATA2-26 — MINOR — The offline dry run must checkpoint the live store read-only

- **Where:** 05-storage §5S.3.6 Dry run (l.683-693: "checkpoints the store … into a scratch directory"),
  `TestDryRunMatchesLiveRun` (l.1317: "live DB byte-identical afterwards").
- **Evidence:** taking a Pebble checkpoint `WithFlushedWAL` needs a read-write open, which writes to the live
  directory (WAL, MANIFEST, OPTIONS), and reusing the kernel open path would also run step 7's format
  ratchet on the live store. 05-storage itself lists the same class of defect for `diagnostics query --raw`
  (§5S.13 l.1369).
- **Fix:** offline dry run opens the live store `ReadOnly` and hard-links or copies its files into scratch
  without a WAL flush (WAL replay happens in scratch), refuses while another process holds the lock, and
  never ratchets; the test asserts unchanged file names and sizes for SSTs and MANIFEST rather than byte
  identity of the directory.

### DATA2-27 — MINOR — 05-backup mechanics: orphaned sidecars, journal name, derived capture wording

- **Where:** §5B.3.5 catalogue (l.519-524), §5B.3.6 Where (l.554: `<backup dir>/revocations.journal`),
  §5B.3.3 shared-directory prefix rule (l.390-391), §5B.3.1 consistency rule (l.91-93: stale "if the
  source's generation changed **during** the capture").
- **Evidence:** during a rollback the old build's retention deletes main archives (they match its listing
  rule, `internal/backup/codec.go:225-245`) but not `.extra`, `.manifest.json` or `.verify.json`; the
  catalogue then lists archives with no main file and counts them in the byte budget. Two apps sharing a
  backup directory (explicitly supported by the prefix rule) write one `revocations.journal` with two
  `install_id` headers, so every restore of either app sees a foreign journal. A derived item captured
  after the checkpoint but before any further touch would be accepted although it reflects post-checkpoint
  writes if "during" is read literally (DATA-25's case).
- **Fix:** catalogue reconciliation drops entries whose main archive is missing and deletes their orphaned
  companions and sidecars (logged); name the journal `<prefix>revocations.journal` with the app's archive
  prefix; reword to "stale unless the source generation at the end of capture equals the checkpoint's".

### DATA2-28 — MINOR — Fresh installs have no initial-level rule

- **Where:** 05-storage §5S.3.7 ("Absent stamp = 0", l.706); 05-backup B3 (l.25: "fresh installs default to
  encrypted"), legacy restore bound (§5B.3.3 rule 3, l.411-414); 09 §9.2.2 bound `{core, 4}` (l.133-136).
- **Evidence:** a brand-new store starts at level 0, runs every adopt step on empty data, enters mirror phase
  (dual-writing legacy rows nobody will read), and waits `RequiresServed` (30 days) before contracts; until
  C8 runs, the legacy restore bound keeps encryption inactive. A fresh install therefore gets a month of
  unencrypted archives, contradicting B3.
- **Fix:** on the boot that creates `c/storage/install` on an empty store, stamp every owner at the build's
  supported level and record every adopt and contract step as applied (as HEAD stamps a fresh store at the
  build's format, `internal/database/storage_format.go:338-352`); mirrors are then never active on fresh
  stores. State it in 05-storage §5S.4 step 5 and 09 §9.2.2.

### DATA2-29 — MINOR — Stale or contradictory cross-references left by the revision

- 09 §9.2.3 rule 2 (l.191-192) still says 05-rbac §6.3 and 05-authn §11 "state the unconditional drop"; both
  now carry the `MirrorDisabled`+`Fresh` exception (05-rbac l.717-721, 05-authn l.926-930). Delete the note.
- 09 §9.9 (l.682-683): "Mirror writes are staged … under the family's existing store mutex" vs 05-storage
  S9/§5S.3.2 (correctness from `Expect` under commit stripes, no mutex dependency). Also its "storage
  migration lock" is defined nowhere.
- 05-tasks T0+T1 (l.1049: "raises the level (5T.6)") vs §5T.6 (l.991: the adopt step "raises no level") and
  09 (only contracts raise). Fix T0+T1's text.
- 05-tasks terminal-history row (l.972: `opv2:op` "Never written", `Set` refused) vs 05-storage row 11
  (`opv2:op:` `ReadWrite` for copy-and-mark).
- Bookkeeping keys: 09 §9.2.2 (l.143) and 05-tasks (l.990) write `c/storage/ks/core/applied/<v>`; 05-storage
  (l.593) keys bookkeeping by owner `core/<subsystem>`; step names `core.auth#1` (09, 06) vs `core/auth#1`
  (05-storage). Pick `c/storage/ks/<owner>/…` with owner `core/auth` and one spelling.
- Bootstrap: 05-storage row 8 (l.1174: "the legacy keys are the only form") vs 05-authn §11 (l.940) and §4.1
  (l.455): `c/auth/bootstrap`. Pick one; if native, row 8 becomes `AgingOut`.
- 05-storage §5S.9.1 (l.1198) and §5S.9.3 (l.1251): "`census_families.go` stays ABO" vs 04 (l.275, l.535):
  census mechanism moves to `storage`. The file holds no key names, so 04 is right; fix 05-storage.
- 06 P2 (l.790): orphans need "an explicit adoption migration" vs 05-storage rule 5 (`Claim.TakesOver`).
- 03 R8 (l.270): example "the shared `idx:`/`pref:` families" — `pref:` has no carve-out since DATA-1.
- 11 row 4 (l.73): "compat level and storage format" and "per-owner format" → "per-owner reader levels".
- 05-storage row 12 (l.1178): `opstate:` "carved out of the tasks claims" — no tasks claim covers `opstate:`
  (`op:` is not a prefix of it); say "a books claim".
