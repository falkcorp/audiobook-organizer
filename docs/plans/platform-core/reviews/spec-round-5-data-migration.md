<!-- file: docs/plans/platform-core/reviews/spec-round-5-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: c17242e3-1896-46ba-9acb-370c09fbf5da -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 5: DATA, STORAGE AND MIGRATION (`DATA5-*`)

This is an antagonistic review of `docs/specs/platform-core/` after the round-4 revision: 03 v1.7.1, 05-storage
v1.5.1, 05-config v1.5.1, 05-backup v1.4.0, 09 v1.4.1. R3-1 and R4-1/R4-2 are binding and are not attacked as
designs: books' legacy stores, books-owned extension records with `legacy_ref` and Widening suspension, the
secret sentinel, the deferred cutover. A finding attacks the design only where the text does not carry it
through, where two files disagree, or where it breaks against code.

Code anchors are at `748fd787a`. Spec anchors take the form `file §section (l.N)`.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | DATA5-1 |
| MAJOR | 8 | DATA5-2 … DATA5-9 |
| MINOR | 11 | DATA5-10 … DATA5-20 |

| ID | Sev | Title |
|---|---|---|
| DATA5-1 | BLOCKER | The legacy families that store interfaces compare-and-set on are not `Serialized`, so `Transition`, `Advance`, `Insert` and `Extension.Put` cannot stage their preconditions, and the claims-table test pins the wrong modes |
| DATA5-2 | MAJOR | API-key extensions hash the whole row, which HEAD rewrites on every authenticated request, so every `abk_` key with an extension that is used during a rollback reads `needs_confirmation` on roll-forward |
| DATA5-3 | MAJOR | A key revoked on the older build reads `needs_confirmation` (re-enableable) on roll-forward, and the legacy encoding of `needs_confirmation` collides with hotfix 3's store guard |
| DATA5-4 | MAJOR | `Suspended` has three incompatible meanings: storage's `Put` never clears it, tasks/AI/logs rely on a routine write clearing it, and `auth.Store` has no way to clear it |
| DATA5-5 | MAJOR | Where a new (`mck_`) key lives is contradictory, and the `inactive` empty-scope row 05-authn writes can be activated by HEAD and then read as `LegacyFullScope` on roll-forward |
| DATA5-6 | MAJOR | The served ledger keeps only `last`, rewritten at §5S.4 step 9 before every consumer runs: "every boot since X" is not computable, and a boot that fails in between erases the rollback signal |
| DATA5-7 | MAJOR | DATA4-7's generation classifier calls a relocated current store "replaced" and runs install-wide `KindExternal` hooks |
| DATA5-8 | MAJOR | DATA4-5 half-fixed: 05-storage row 9 stops books' scheduler at T0+T1, while 05-tasks and 09 keep it ticking until T7 and core schedules no adapted kind before T7 |
| DATA5-9 | MAJOR | The one-shot S7 `Local` grant runs on URLs no S3+ build vetted: pre-S3 blob values, a HEAD store that skips to S7+, and a pre-S7 archive restored on S7+ |
| DATA5-10 | MINOR | An older build's factory reset wipes `c/` and `m/` (09 row 10 says it does not), and a sentinel with no record is then read as a real secret |
| DATA5-11 | MINOR | From S2b, row 7's `CheckedWrites` refuses HEAD's blind settings writers, which stay writers until S3; "`Open` writes nothing" contradicts HEAD's loader |
| DATA5-12 | MINOR | Hotfix 5 is missing from 09 S0 and the tracker, and core's stage 8 has no rule for an enabled gate whose password the roll-forward re-imported as a clear |
| DATA5-13 | MINOR | An in-place restore of a pre-S0 archive gets a new `install_id`, so the DATA4-15 check sets the journal aside and known revocations are not re-applied |
| DATA5-14 | MINOR | `Extension.Put` takes no "seen" record, so an extension-only read-modify-write can lose a concurrent update |
| DATA5-15 | MINOR | `aijournal:`, `aijob_payload:` and the "result-journal store" disagree across 09, 05-storage and 06 |
| DATA5-16 | MINOR | Name drift: `legacy_ref`/`legacy_fp`/`legacy_hash`, two names for the R1(c) test, 03's "never through `app.Options`", 01's glossary |
| DATA5-17 | MINOR | "Books' one legacy step" at S7 contradicts the `LegacyCap` rule |
| DATA5-18 | MINOR | After the legacy-session fallback is deleted, raw `sess:` tokens an older build writes are never removed by the current build |
| DATA5-19 | MINOR | An older build restoring an S3+/S4+ archive pays the rollback costs, but the "one complete list" does not say so |
| DATA5-20 | MINOR | Concurrent `Committed` rewrites of `<db dir>/config.yaml` are not serialised |

## Regression check (round-4 BLOCKER/MAJOR, this lens)

| Round 4 | Status in round 5 | Residual |
|---|---|---|
| DATA4-1 (B) Basic gate opened by a blanked secret | **Fixed** by R4-2: the sentinel is written into every legacy copy in ABO's cipher (05-config §1.12.2, l.1171-1173), so HEAD's `DecryptValue` succeeds and its corrupt-secret delete path (`internal/config/persistence.go:1197-1262`) is not taken | DATA5-10, DATA5-11, DATA5-12 |
| DATA4-2 (B) S4 r1 grant / `ErrGrantMissing` | **Fixed** by R4-1: books supplies its legacy stores in r1 and r2, so there is no grant and no backend switch (09 S4 "Releases"; 06 P2) | — |
| DATA4-3 stale gap-fill secret wins | **Fixed**: the `config.yaml` copy also holds the sentinel, so HEAD's gap fill never fires (`persistence.go:73-89` fills only empty fields) | DATA5-20 |
| DATA4-4 blind books writes on shared rows | **Fixed** for row 7 and `import_path:` (`CheckedWrites`, field-merge writers, memdb kept current) | DATA5-11 (S2b) |
| DATA4-5 S5 one writer / scheduler | **Half-fixed**: one-writer rule kept, but row 9 contradicts 05-tasks/09, and rows 9 and 11 lack `Serialized` | DATA5-8, DATA5-1 |
| DATA4-6 S2b–S7 restore swap | **Fixed**: `core/internal/restoreswap` runs at P0a from S2b, with floors from `storage.Admin.UpgradeFloors()` (05-backup "Stages", l.24-35) | — |
| DATA4-7 head-lag classifier | Head is now monotone (`max` through `GetForUpdate`, 05-backup l.713); **the generation classifier that replaced head lag over-fires** | DATA5-7 |
| DATA4-8 stale core-only records | **Fixed in shape** (`storage.Extension`, `legacy_ref`, Independent/Derived/Widening, orphans swept) | DATA5-2, DATA5-4, DATA5-14 |
| DATA4-9 invites/sessions atomicity | **Fixed** (rows 4a/6 `Serialized`, `Take*` stage `Expect`+`Delete`, merge deletes legacy invites) | — |
| DATA4-10 HEAD store-open writers | **Fixed** (homes listed in §5S.9.3; constructors write nothing) | — |
| DATA4-11 `admin` parked in `PendingSeed` | **Fixed** (05-rbac §4.8: `admin` outside the merge; reach-implied additions are not restricted) | — |

Round-4 MINORs: DATA4-12, 16, 17, 18, 19, 20, 21 and 22 are fixed. DATA4-13 is fixed for the restore hook, but the
stale-extension path reopens it (DATA5-3). DATA4-14 is fixed in design but is crash-unsafe (DATA5-6). DATA4-15 is
fixed, with the residual in DATA5-13.

## Simulations

**(a) Forward S0 → S10.**

- S0–S2: clean. The guard's `c/` records are ignored by HEAD (`TestLegacyToleratesNamespacedKeys`).
- S2b strict: row 7's `CheckedWrites` meets HEAD's blind `SetSetting` writers (DATA5-11).
- S3: the sentinel holds. Core's stage 8 behaviour for a cleared password is unspecified (DATA5-12).
- S4:
  - core's own coalesced last-use writes and HEAD-format touches change the hashed key row (DATA5-2);
  - where an `mck_` key lives is contradictory (DATA5-5).
- S5:
  - `Transition`/`Advance` need `Serialized` on rows 9 and 11 (DATA5-1);
  - the scheduler hand-over is contradictory (DATA5-8).
- S7:
  - the `Local` step runs on unvetted URLs (DATA5-9);
  - the AI-job and provider rows lack `Serialized` (DATA5-1);
  - `aijournal:` naming (DATA5-15);
  - "legacy step" (DATA5-17).
- S8: a DB relocation reads as a replaced store (DATA5-7).
- S10: `sess:` residue (DATA5-18).

**(b) Roll back by binary swap at each stage, write on the older build, roll forward.**

- S3+ → older: secrets fail closed (correct); a factory reset wipes `c/secrets/` and `m/books/ext/` (DATA5-10).
- S4 → S3:
  - an `abk_` key that has an extension and is used on S3 reads `needs_confirmation` on roll-forward (DATA5-2);
  - a key revoked on S3 becomes re-enableable (DATA5-3);
  - an admin re-enable never clears the suspension (DATA5-4);
  - an `inactive` no-atom row activated on S3 reads as `LegacyFullScope` (DATA5-5);
  - a custom role edited on S3 is suspended until `reconfirm` (correct).
- S5 → S4: stale clock and run extensions reset to their defaults (correct), but stay "suspended" in health for good (DATA5-4).
- S7 → S6: a URL change suspends `Local` (correct).
- Any stage: a roll-forward boot that fails between §5S.4 step 9 and P4d loses the gap line and the session revocation (DATA5-6).

**(c) Stepping-stone path.**

- Correct for S0+ updaters and at boot (`UpgradeFloor{books, LegacyCap}` checked on `.storage-applied`).
- A HEAD store that skips straight to S7+ runs the `Local` step on HEAD-written URLs (DATA5-9).

**(d) Restore of an old ABO archive.**

- On S8+, staging, floors, install hand-over, re-apply and hooks are correct.
- A pre-S7 archive re-runs the `Local` step (DATA5-9).
- A pre-S0 archive extracted in place by an older build loses journal re-application (DATA5-13).
- An older build restoring an S3+ archive pays the rollback costs unstated (DATA5-19).
- The live `config.yaml` sentinel can be read as a value (DATA5-10).

**(e) Crash at each boot phase.**

- P−1, P0a, P0, P1, P2 and P3: no store writes.
- P4a: checkpoint ordering is correct; the fresh-install batch is atomic.
- P4b: steps are idempotent or atomic.
- **§5S.4 step 9** rewrites the served ledger and foreign-open record before every consumer (DATA5-6).
- P4d step 1: re-import and `Committed` self-heal at the next boot.
- P4d steps 2–4: idempotent, but they read a ledger value step 9 already replaced (DATA5-6).
- P7: seed writes are idempotent.
- Run (3a): the derived loader has the same ledger dependency (DATA5-6).

**(f) Concurrent writers (task versus HTTP handler) on one entity.**

- Identity rows: safe (`Serialized` + `Expect`).
- Runs, clocks, AI jobs and provider counters: not safe (DATA5-1). Executor transition versus HTTP cancel loses one
  transition; AI poller versus HTTP cancel stages an apply for a cancelled job.
- Extension-only updates: DATA5-14.
- The gap-fill file: DATA5-20.

---

## Findings

### DATA5-1 — BLOCKER — The legacy families that store interfaces compare-and-set on are not `Serialized`

- **Where:**
  - 05-storage §5S.9.1 rows 9, 11, 13 and 19 (l.1640, l.1642, l.1645, l.1651): no `Serialized` flag on the clock and pause keys, the ops run families, the queue/index families, `aijob:`/`aijob_batch:`/`aijob_payload:`, or `provider_throttle:`/`provider_daily_budget:`.
  - §5S.3.2 `Batch.Expect` (l.268: "Allowed only for keys in Serialized ranges … else `ErrNotSerialized`").
  - §5S.3.11 `Extension.Put` (l.1274: "the caller adds the Expect on the legacy row it read (its claim is Serialized)").
  - `TestABOClaimsMatchOwnershipTable` (l.1761: generated claims, `Serialized` included, "equal §5S.9.1").
  - 05-tasks §5T.3.13:
    - `RunStore` (l.1101-1107: "Preconditions (Transition, Insert's uniqueness) are checked at commit by GetForUpdate/Expect over the implementation's own Serialized range, never by a lock held between calls");
    - `ClockStore.Advance` (l.1161: "checked at commit");
    - §5T.7 (l.1462: transitions are CAS "whichever store implementation serves the install").
- **Evidence:**
  - HEAD's CAS on runs is a mutex around read, compare and its **own** commit: `opsMu` in `TransitionRunV3` (`internal/database/pebble_store_ops_v3.go:1029-1041`). AI jobs use `aijobWriteMu` (`internal/database/pebble_store_aijobs.go:23,96,123`) and quotas `dailyquota.go:160`.
  - Once these methods stage into the caller's `*storage.Batch` (the interface requirement), the lock no longer spans the commit. The only remaining mechanism is `Expect`, which storage refuses on these claims.
  - As written, then, every books `Transition`/`Advance` either fails with `ErrNotSerialized`, so no S5 run can change state, or degrades to a check-then-blind-write. In the second case an executor completion and an HTTP `runs/{id}/cancel` both pass and the later commit silently overwrites the other transition. The same holds for the AI batch poller versus HTTP cancel, and for two concurrent quota increments.
  - `taskstest.StoreSuite` ("asserts … the commit-time preconditions", 05-tasks l.1189-1192) and `TestABOClaimsMatchOwnershipTable` cannot both pass, so S5 cannot meet its own done-check.
  - Extension records on these rows (clock, run and AI-job extensions) also cannot get the row `Expect` that `Put` requires. A concurrent row write therefore leaves the record stamped with a fingerprint of a row that is already gone: a spurious `ExtStale` on the current build.
- **Fix:**
  - In §5S.9.1 mark row 9's four keys and sub-prefix, row 11, the queue/index sub-prefixes of row 13 that `Insert`/`Transition` touch, and row 19's `aijob*` and `provider_*` families **`Serialized`**. Do not mark them `CheckedWrites`: books' legacy steps 62/65/66 still write row 11 blind in P4b.
  - Add to the §5S.3.11 "Store interfaces" rule: "every legacy family a store interface writes under a commit-time precondition, or annotates with an extension record, is `Serialized`".
  - Extend `TestABOClaimsMatchOwnershipTable` to cross-check every family listed as served through a store interface against that rule.
  - Add `TestLegacyRunTransitionVsCancelOneWins` (`synctest`, books' store) and `TestAIJobPollVsCancelOneWins`.

### DATA5-2 — MAJOR — API-key extensions hash the whole row, which HEAD rewrites on every authenticated request

- **Where:**
  - 05-authn §11 (l.1362: `legacy_ref`, "the SHA-256 of the row bytes they were written with"; no volatile list).
  - 05-storage §5S.3.11 (l.1223: the guarded projection drops "the fields the owner spec lists as volatile (usage timestamps an older build rewrites routinely)"; "the default projection is the whole row").
  - 05-authn §4.4 (l.747-748) and 09 S4 "Done when" (l.779): "roll back and return with no restore: no API key is set `needs_confirmation`".
- **Evidence:**
  - HEAD writes `LastUsedAt`, `LastUsedIP` and `UseCount` into `apikey:<id>` synchronously on every key-authenticated request (`internal/server/middleware/auth.go:382`; `internal/database/pebble_store_auth.go:1284-1302`).
  - `abk_` keys keep working on older builds (§4.4), so any of them that carries an extension record changes its hashed row the first time it is used while rolled back. Such keys include every key the restore hook or a chain break set to `needs_confirmation` that an admin later re-enabled, and any key with display data.
  - On roll-forward the extension is `ExtStale` and the key "reads as `needs_confirmation` until an admin re-enables it". Every automation key used during the rollback is locked out, against 09's R-test cell and absent from 09 §9.2.1's "one complete list".
  - Because 05-authn names no volatile field, the current build's own coalesced last-use write ("coalesced per key per minute", §4.4) suspends its own keys too, unless it goes through `Extension.Put`. Nothing says it does.
- **Fix:**
  - In 05-authn §11, give `apikey:` a guarded projection that excludes `last_used_at`, `last_used_ip` and `use_count`. Restriction fields are never excluded.
  - State that `u:` and `role:` use the whole row: HEAD's `UpdateRole` bumps `Version`/`UpdatedAt` (`pebble_store_auth.go:1053-1063`), which is what makes a re-created row detectable.
  - State that every write path of a row with an extension either goes through `Extension.Put` or changes only excluded fields.
  - Replace "SHA-256 of the row bytes" in 05-authn and 05-rbac with "SHA-256 of the guarded projection (05-storage §5S.3.11)".
  - Add an R-test cell: "re-enable a key on cur, roll back, use it on prev, roll forward: it is not `needs_confirmation`".

### DATA5-3 — MAJOR — A key revoked on the older build reads `needs_confirmation` on roll-forward

- **Where:**
  - 05-authn §11 (l.1365-1368: on a stale extension "its granting data is ignored and its restrictions stay in force … a key reads `needs_confirmation` until an admin re-enables it").
  - §4.4 (l.743-748); §3.2 (l.346: `needs_confirmation` is "stored as legacy status `revoked`").
  - §4.14 step 3 (l.1061-1065).
  - §11.1 item 3 (l.1420-1426: `SetAPIKeyStatus` "refuses any transition to `active` for such a row, so no other caller bypasses the handler").
- **Evidence:**
  - **Un-revocation.** An operator revokes a leaked key on the older build. HEAD sets `status=revoked` and `revoked_at` (`pebble_store_auth.go:1222-1247`), changing the row, so the extension goes stale. On roll-forward the key reads `needs_confirmation`, which §4.4 allows to go to `active`. A terminal revocation becomes one admin click away from reversal, presented in the UI as "needs confirmation". This is DATA4-13's defect (fixed only for the restore hook, `TestRestoreNilCoverageKeepsRevokedTerminal`) re-entering through the stale path.
  - **Encoding collision.** The legacy row of a genuine `needs_confirmation` key is `revoked` with `revoked_at` set. Books' legacy store is HEAD's store code carrying hotfix 3, which refuses at store level any transition to `active` for such a row. The specified re-enable therefore either fails or needs a sanctioned bypass of the guard that makes revocation terminal, and neither file names that bypass.
- **Fix:**
  - Stale rule: a stale key whose legacy row has `status=revoked` or a non-nil `revoked_at` reads **`revoked`** (terminal); only a stale `active`/`inactive` row reads `needs_confirmation`.
  - Store `needs_confirmation` as legacy `inactive` with `deactivated_at`. Every build refuses it (`middleware/auth.go:340-344`), and an older-build admin re-activating it is an admin confirmation.
  - Or keep `revoked` and name the one store method, gated on an `ExtCurrent` record that says `needs_confirmation`, that may clear `revoked_at`.
  - Add `TestStaleKeyRevokedOnPrevStaysRevoked`.

### DATA5-4 — MAJOR — `Suspended` has three incompatible meanings

- **Where:**
  - 05-storage §5S.3.11 (l.1252: `Suspended` "set by a Put after a stale read; cleared only by Confirm"; l.1272-1274: `Put` "keeps Suspended … a routine write never clears it").
  - 05-tasks §5T.6.2 (l.1364-1368: "the re-confirmation R4-1 requires is, for tasks, the next write of that row through the store … no admin step is needed").
  - 05-ai (l.578: "No field is Widening, so no job record is ever suspended").
  - 05-logging §5 (every field Derived).
  - 05-authn §3.4 (no `Confirm` on `auth.Store`) versus §11 ("until an admin re-enables it (audited `core.apikey.set_status`)").
  - 01 glossary (l.107: every extension "is suspended until an admin re-confirms it").
- **Evidence:**
  - With the shared primitive "one rule for every store, through `storage.Extension[T]`" (l.1223), every task clock, run and AI-job record read stale after a rollback is marked `Suspended` by its next `Put` and stays so for good. Health `storage_extension_suspended` then counts them forever, though they have no Widening field.
  - For keys and users the only clearing path is `Extension.Confirm`, which `auth.Store` does not expose. The re-enable 05-authn specifies is a routine `PutAPIKey`, so it keeps `Suspended` and the key keeps reading `needs_confirmation`: a suspended key can never be re-enabled. rbac (`ReconfirmRole`), library (`RootSource.Confirm`) and config (`Store.Confirm`) each have a clearing path; auth does not.
- **Fix:**
  - In §5S.3.11, set `Suspended` only for records whose type declares at least one Widening field (`Extension.Widening bool`, or `T` implementing `storage.Widening`). A record without one is re-stamped by a routine `Put` and reads `ExtCurrent`, with its Derived fields at their defaults until then.
  - Add `ConfirmAPIKey(b, id)` and `ConfirmUser(b, id)` to `auth.Store`, mapped to `Extension.Confirm` and called only by the audited admin re-enable.
  - Correct 01's glossary entry ("Widening records are suspended; Derived fields reset").
  - Add `TestExtensionWithoutWideningClearsOnPut` and `TestSuspendedKeyReEnableClears`.

### DATA5-5 — MAJOR — Where a new (`mck_`) key lives is contradictory, and an `inactive` empty-scope row can become `LegacyFullScope`

- **Where:**
  - 09 §9.2.1 row 4 (l.144: new keys "live wholly in books' extension family") and §9.2.3 rule 1 (l.349).
  - 05-authn §4.4 (l.719-723: a legacy key row with admitted atoms; "a key with no admitted atom is written with legacy status `inactive`"); `TestAPIKey_StoredScopesNeverEmpty` (l.1264: "non-empty … **or** status `inactive`").
  - 05-storage §5S.9.1 row 3 (l.1633: "the row **always** carries explicit, non-empty legacy scopes").
  - 05-authn §11 table (l.1381: "empty legacy scopes → `LegacyFullScope=true`, kept as written").
- **Evidence:**
  - Three files give two designs. Under 09's, HEAD never sees an `mck_` key; under 05-authn's, HEAD lists it and can rename, deactivate, revoke or **activate** it.
  - HEAD's `UpdateStatus` lets the owner or an admin move any non-revoked key from `inactive` to `active` (`internal/server/handlers/apikeys.go:336-372`, `SetAPIKeyStatus` l.1226-1247; hotfix 3 blocks only revoked rows).
  - A no-atom `mck_` row activated on HEAD becomes `active` with `scopes: []`. It cannot authenticate on HEAD (prefix, `middleware/auth.go:76-99`). On roll-forward, however, its extension is stale, the stale rule says "never wider than the legacy row alone", and the legacy row alone is `LegacyFullScope`, meaning all role permissions. A re-enable then widens a one-scope key to its owner's whole role.
- **Fix:**
  - Pick 05-authn's design, and correct 09 row 4 and rule 1 to say "a legacy row with admitted atoms, or status `inactive`, plus the canonical scopes in the extension".
  - State in 05-authn §4.4/§11 that `LegacyFullScope` is inferred **only** for a row with no extension record ever written. A row that has, or had, an extension (record present, stale or orphan, or an `mck_` token) is never `LegacyFullScope`; when stale it grants nothing until re-confirmed with explicit scopes.
  - Align 05-storage row 3 with the `inactive` exception.
  - Add `TestInactiveNoAtomKeyActivatedOnPrevNeverFullScope`.

### DATA5-6 — MAJOR — The served ledger keeps only `last`, rewritten before every consumer runs

- **Where:**
  - 05-storage §5S.3.7 served ledger (l.949: `c/storage/served/<owner>/last` = `{build, supported, duties, at}`; consumers check "that every boot since listed it").
  - §5S.4 step 9 (l.1427: writes "the served ledger's last record (with this build's duties), the foreign-open identity" in P4b).
  - 05-backup "Boots without the journal duty" (l.715: the gap line is appended at P4d step 2).
  - 05-authn §4.3 (l.699-706: revoke-after-rollback in `BootWrites`, P4d step 4).
  - 05-storage §5S.3.9 (l.1165-1168: the derived loader at Run (3a) accepts only when "every boot since the artifact's generation was recorded listed the `gen` duty").
- **Evidence:**
  - "Every boot since T listed D" cannot be computed from one overwritten record. All three consumers run **after** step 9 has replaced `last` with this boot's own record, which lists the duty, and replaced the foreign-open identity. So they must rely on a value captured at step 2, which no file states.
  - A roll-forward boot that writes step 9 and then fails before the consumer (P4d step 1's R4-6 settings validation is Fatal by design, or a power loss) leaves the next boot seeing a previous boot **with** the duty and **no** foreign open:
    - no `gap` line is appended, so a later restore of an archive from before the rollback gets coverage it does not have and resurrects revocations made on the older build;
    - pre-rollback native sessions are never revoked (the DATA4-14 fix is lost);
    - a stale hnsw snapshot that the older build invalidated without touching the generation is accepted.
- **Fix:**
  - Give the ledger record a per-duty `continuous_since` (`duties: {journal: <t>, auth: <t>, gen: <t>, usage: <t>}`). At step 9 each value is carried forward when the previous record listed the duty and no foreign open was detected, and is set to now otherwise.
  - Consumers compare their own anchor with it:
    - the journal appends a `gap` line unless one already covers `continuous_since.journal`;
    - auth revokes sign-ins created before `continuous_since.auth` (idempotent);
    - the loader accepts only if `continuous_since.gen` ≤ the artifact's time.
  - The signal then survives a failed boot. 09's W gate reads `continuous_since.usage` instead of `last`.
  - Add `TestCrashAfterStep9StillRecordsGap`, `TestCrashAfterStep9RevokesSessions` and `TestDerivedRejectedAfterCrashedRollForward`.

### DATA5-7 — MAJOR — DATA4-7's generation classifier calls a relocated current store "replaced"

- **Where:**
  - 05-storage §5S.3.7 store generation (l.945: "copied or replaced by hand … this boot's generation is `epoch + 1`").
  - 05-backup §5B.3.6 "Boot re-apply" (2) and (4) (l.717); 05-authn §4.14 "Replaced store" (l.1080-1090); 14 K22.
- **Evidence:**
  - HEAD expects the database to move between pools while the credentials stay put (`cmd/root.go:324-327`; the key is read from the state directory for that reason).
  - A cross-device move, an rsync to a new disk, a volume migration or a host move with the same data changes `dir_dev`/`dir_ino`. The boot then bumps the generation, 05-backup classifies the store as replaced, and the hooks run with `Revocations == nil`: every active or disabled API key `needs_confirmation`, every credential epoch bumped (every user and ABS client signed out), every session and invite deleted. That is an install-wide lockout for a routine operation that lost nothing.
  - Since DATA4-7's fix made the head monotone, the head **can** tell a copy of the current store from an old one, but the classifier no longer consults it.
- **Fix:**
  - Replaced = generation or identity differs **and** at least one of the following holds:
    - `head_before` is below the journal's last revocation line;
    - the journal holds a `gap` or `restore` line after `head_before`;
    - the store has no head while the journal has revocation lines.
  - Otherwise: the same store, relocated. Record the new identity in `backup.location.json` and run no hooks.
  - DATA4-7's false negative stays covered: a non-journaling build's restore always leaves a gap line, through DATA5-6's `continuous_since`.
  - Add `TestRelocatedCurrentStoreRunsNoHooks` and `TestRelocatedAfterRollbackIsReplaced`.

### DATA5-8 — MAJOR — DATA4-5 half-fixed: the scheduler hand-over contradicts itself

- **Where:**
  - 05-storage §5S.9.1 row 9 (l.1640: "books' own scheduler stops ticking then [T0+T1] (core runs its definitions)").
  - 05-tasks §5T.6.2 (l.1376: "ABO's `internal/scheduler` keeps ticking the 38 `TaskDefinition`s from T0+T1 to T7 … core's scheduler polls only kinds that declare a `Schedule`, and no adapted kind declares one until T7").
  - 09 S5 "Moves and batches" (l.787, the same as 05-tasks).
- **Evidence:**
  - §5S.9.1 declares itself "the only statement of who owns each legacy family, in which mode".
  - Following it, books' ticker stops at T0+T1, while per 05-tasks core's scheduler polls no adapted kind until T7. For the T0–T7 releases nothing schedules the 38 definitions (library scans, maintenance window, retention): the silent-stop class `internal/scheduler/interval_clock.go:116-140` documents.
  - Following 05-tasks instead, row 9's "sole writer" text is wrong but harmless.
- **Fix:**
  - Replace row 9's note with 05-tasks' rule: "books' ticker keeps ticking the 38 definitions through books' `ClockStore` until T7 converts each and deletes its ticker entry in the same PR; `TestOneSchedulerPerKind`".
  - Add to the T0+T1 done-check: "every one of the 38 definitions fires on the staging store within one interval".

### DATA5-9 — MAJOR — The one-shot S7 `Local` grant runs on URLs no S3+ build vetted

- **Where:**
  - 05-metadata (l.764: "it runs once per store … The step sees only admin-written URLs: from S3 every integration-bound blob field … is `ProtectAdmin`").
  - 09 S7 "Data/keys" (l.816); 09 §9.2.1 "Unit of compatibility" ("a skipped release costs nothing"); 05-backup restore of older archives.
- **Evidence:** `ProtectAdmin` from S3 covers only writes made by S3–S6 builds. The step's input also includes:
  - (a) URLs already in the blob before S3, written under HEAD's rule (`PUT /api/v1/config` needs `settings.manage`, which a HEAD custom role can hold);
  - (b) a HEAD store that skips S3–S6 and boots S7+ directly, which is allowed by the store-based compatibility unit;
  - (c) a pre-S7 archive restored on S7+, whose store lacks the step's applied record, so the step runs again on the archive's URLs.
  - In each case an IP-literal private URL that no admin vetted gets `Local=true`, which is exactly the egress trust SEC3-5 gates.
- **Fix:**
  - The S7 step records candidates as `PendingLocal`, a native-only, non-Widening extension member listed on the Integrations page, and grants nothing until an admin confirms them (audited).
  - Or it grants only for loopback literals.
  - State that the step re-runs after a restore of a pre-S7 archive and is subject to the same rule.
  - Add `TestLocalStepSkippedStoreGrantsNothing` and `TestLocalStepAfterOldArchiveRestore`.

### DATA5-10 — MINOR — An older build's factory reset wipes `c/` and `m/`, and an orphaned sentinel is read as a secret

- **Where:**
  - 09 §9.2.1 row 10 (l.150: "An older build's settings reset or factory reset does not touch values that live only in core records").
  - 05-config consequence (b) (l.1233-1236); 05-config read order (l.1774: "… > record without sentinel > legacy primary > secondary > `ErrNotSet`").
- **Evidence:**
  - `PebbleStore.Reset` deletes `[0x00, 0xff 0xff)`, so `c/secrets/`, `m/books/ext/` and `c/storage/install` all go (`internal/database/pebble_store.go:6310-6331`; `internal/server/handlers/system/handler.go:538-560`).
  - The row-10 claim the R-test "asserts exactly" is therefore false for a factory reset.
  - Afterwards `<db dir>/config.yaml`, which is not wiped, still holds the sentinels. HEAD gap-fills them (`persistence.go:73-89`) and its next save persists them into the legacy rows. With no record left, core's read order serves the sentinel as the real provider key and the settings UI shows it as set.
  - The same happens after restoring an old archive whose row is empty while the live file holds a sentinel.
- **Fix:**
  - Correct row 10 and consequence (b): a factory reset on an older build wipes core records too.
  - Make the sentinel recognisable without its record (for example `mc-sentinel:` + 32 random bytes, base64url). Every core reader treats a value with that prefix as unset (`ErrNotSet`), and the reconcile drops it. It still matches no credential on an older build.
  - Add `TestOrphanSentinelReadsUnset`.

### DATA5-11 — MINOR — From S2b, row 7's `CheckedWrites` refuses HEAD's blind settings writers, which stay writers until S3

- **Where:**
  - 05-storage §5S.9.1 row 7 (l.1638: blob and secret rows `CheckedWrites`; "`config.OverrideStore` (S2b reads, S3 writes)"; "the same at every stage").
  - 05-config §1.12.2 (l.1143-1147: until the S3 façade ABO's legacy path "still writes the blob itself … through the books store"; l.1118-1119: "`Open` writes nothing to the store").
- **Evidence:**
  - HEAD's `saveRawBlob` (`persistence.go:883-885`) and `SaveConfigToDatabase`'s secret loop (`:1989-2006`) are blind `SetSetting`s. The loop only warns on failure, so under strict claims a secret change is silently dropped while the blob saves.
  - `LoadConfigFromDatabase`'s corrupt-secret path re-encrypts or deletes rows (`:1233-1262`), and R4-6 runs it inside `Open`, which "writes nothing".
  - Shadow mode may surface these writers, but the spec names none of them.
- **Fix:**
  - List these writers in 09 S2b and rewire them in the S2b PR to `GetForUpdate`, `Expect` and `RetryOnConflict` through books' store.
  - Replace "`Open` writes nothing" with "`Open` defers the corrupt-secret repair to the first S3 `Stage`, logging it" (or allow exactly that write).
  - S2b done-check: "a settings save that changes a secret under strict claims persists it (no `ErrBlindWrite`)".

### DATA5-12 — MINOR — Hotfix 5 is missing from 09 S0 and the tracker, and core's stage 8 has no rule for a cleared password

- **Where:**
  - 05-authn §11.1 item 5 (l.1431-1439: "tracked in `todo.d/2026-11-04_platform_core_followups.md`"); 09 S0 "Hotfixes" (l.661, which lists four items).
  - 05-config roll-forward (l.1196-1199: "an empty or absent [legacy copy] as a clear"); 05-http-api §4.2 stage 8 (no rule for an empty or unset password).
- **Evidence:**
  - The tracker file has no Basic-gate entry.
  - A roll-forward after an older build emptied or removed the row re-imports a clear while `basic_auth_enabled` may still be true. Core's stage 8 behaviour is then undefined: HEAD's comparison would admit `<username>:` (`basicauth.go:81-87`).
- **Fix:**
  - Add item 5 to 09 S0 "Hotfixes" and to the tracker.
  - In 05-http-api stage 8: "enabled with an unset or empty password → 401 on every non-exempt route, health `basic_auth_no_password`".
  - Add `TestCoreBasicGate_UnsetPasswordRefuses`.

### DATA5-13 — MINOR — An in-place restore of a pre-S0 archive gets a new `install_id`, so known revocations are not re-applied

- **Where:** 05-storage §5S.4 step 5 (l.1404-1410: marker absent → "the one a restore boot hands over … else a new one"); 05-backup "Boot re-apply" (0) (l.717: an `install_id` mismatch sets the journal aside and re-applies nothing).
- **Evidence:**
  - A pre-S1 build (HEAD, a K22 rollback target) extracts an archive over the live store (`internal/server/handlers/system/handler.go:824-862`). If the archive predates S0 it holds no marker, so the next core boot mints a new `install_id`.
  - Step (0) then renames the journal aside, and every revocation made after the archive is lost. The replaced-store hook still runs with nil coverage, so a key **revoked** (terminal) after the archive comes back as `needs_confirmation` (re-enableable).
- **Fix:** In step 5, distinguish a reset store (no books rows) from a non-empty marker-less store whose path `backup.location.json` records. The latter inherits the journal header's `install_id` (an in-place restore of this install), so step (2) re-applies the verified tail before the hooks.
  - Test: `TestInPlacePreS0RestoreReappliesJournal`.

### DATA5-14 — MINOR — `Extension.Put` takes no "seen" record

- **Where:** 05-storage §5S.3.11 (l.1270-1275: `Put` "stages … an Expect on the record's previous bytes").
- **Evidence:**
  - `Put(ctx, b, legacyKey, entryID, row, v)` receives no record bytes from the caller, so its `Expect` is on the record as `Put` reads it, not as the caller's `Get` saw it.
  - A read-modify-write of an extension-only field loses a concurrent update whenever the row's bytes are unchanged: a per-entry config record under an unrelated blob change, or two edits of canonical scopes that admit the same atoms when the store does not bump `Version`.
  - This is masked today only where every write also changes the row.
- **Fix:** Give `Put` a `seen ExtRecord[T]` (or raw bytes) parameter from `Get` and stage `Expect` on exactly those bytes.
  - Test: `TestExtensionPutLosesNoConcurrentUpdate` (`synctest`).

### DATA5-15 — MINOR — `aijournal:`, `aijob_payload:` and the "result-journal store" disagree

- **Where:** 09 §9.2.3 (l.332: "`ai.JobStore` and the result-journal store … `aijob:`, `aijob_batch:`, `aijournal:`"); 05-storage §5S.9.1 row 19 (l.1651: `aijob:`, `aijob_batch:`, `aijob_payload:`) and l.1656 (`aijournal:` "served through no core store"); 06 `LegacyStores` (l.368-379: no result-journal field).
- **Fix:** Decide whether `aijournal:` is served. If it is, add a `LegacyStores` field and a §5S.9.1 entry; if not, delete "and the result-journal store" from 09. Use one family list (`aijob_payload:` included) in both files.

### DATA5-16 — MINOR — Name drift across files

- **Evidence:**
  - The extension fingerprint is `legacy_ref = {key, fp}` (05-storage l.1223), `legacy_fp` (05-tasks l.1358) and `legacy_hash` (05-config l.1096).
  - The R1(c) test is `TestCoreHoldsNoAppKeyNames` (03 R1, 14 K23) and `TestCoreHoldsNoBooksKeyName` (05-storage l.1667, l.1764).
  - 03 §3.3 (l.300) says legacy stores are supplied "never through `app.Options`", yet S2 passes `app.Options.LegacyConfigStore` (06 P3, 09 §9.2.3; deleted at S2b).
- **Fix:** Use `legacy_ref.fp` everywhere and keep one test name. Add to 03: "except S2's `app.Options.LegacyConfigStore`, deleted at S2b, when no kernel storage exists".

### DATA5-17 — MINOR — "Books' one legacy step" at S7 contradicts the `LegacyCap` rule

- **Where:** 05-metadata l.764 and 09 S7 (l.816: "books' one legacy step"); 09 §9.2.2 (`LegacyCap`: "HEAD-style steps … are allowed until S2b and forbidden after"; `TestNoNewLegacyMigrations`).
- **Fix:** Call it a books versioned `Migration` (`Additive`, P4b) and state that it writes only `m/books/ext/integrations/`.

### DATA5-18 — MINOR — Raw `sess:` tokens left at rest after the fallback is deleted

- **Where:** 05-authn §11 table (l.1382: "the fallback's code is removed by the first release after S4 + 7 days"); 09 S10.
- **Evidence:** After the deletion, `sess:` rows an older build writes during a rollback (raw bearer tokens) are read and deleted by the current build only through the restore hook's `DeleteLegacySignIns`. HEAD's 10-minute cleanup runs only on HEAD.
- **Fix:** Keep a books retention sweep that deletes expired `sess:` and `idx:sess:user:` rows, and make the claim `AgingOut` from the fallback-removal release.

### DATA5-19 — MINOR — An older build restoring an S3+/S4+ archive pays the rollback costs, unstated

- **Where:** 09 §9.2.1 ("This table is the one complete list"), 09 S8 Rollback ("archives written on S8 are restorable by every older build"), 05-backup B1.
- **Evidence:** Restored with HEAD, an S3+ archive's moved secrets are sentinels, its `mck_` keys and native sessions are void, and its role scopes are absent, which are the costs of rows 1–5. None of this is stated for restores.
- **Fix:** Add a row: "Restoring an archive written on S3+ with an older build has rows 1–5's costs; restore it with a build ≥ the archive's stage".

### DATA5-20 — MINOR — Concurrent `Committed` rewrites of `<db dir>/config.yaml` are not serialised

- **Where:** 05-config §1.12.2 (l.1172-1173: the file is rewritten in `Committed` "as HEAD's `SaveConfigToFile` does", a merge rewrite).
- **Evidence:** Two concurrent secret writes commit separately and then each merges and rewrites the whole file from its own read. One can leave the other's name with the stale plaintext or the previous sentinel until the next boot's reconcile.
- **Fix:** Serialise `Committed` file rewrites with one process-wide mutex in books' store, write by temp file, fsync and rename, and re-read under the lock.
