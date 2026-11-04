<!-- file: docs/plans/platform-core/reviews/spec-round-10-data-migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: bdb6d8c7-0bce-482b-a387-aaf28592074f -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 10: DATA, STORAGE AND MIGRATION (`DATA10-*`)

This is the final confirmation review of `docs/specs/platform-core/` after R9-1 … R9-11, read through the data, storage
and migration lens. Files read at their current versions: 03 v1.12.x (§3.3 `storage` row and "Write locks"), 05-storage
v1.11.1 (§5S.2, §5S.3.2, §5S.3.7, §5S.3.11, §5S.3.12, §5S.3.13, §5S.3.15, §5S.3.16, §5S.9.1, §5S.10), 05-backup v1.10.1
(Stages, §5B.3.4, §5B.3.9), 05-authn v1.11.1 (§3.4, §4.4, §11.2), 05-config v1.11.1 (§1.3.3, §1.6, §1.12.2,
§1.12.5, §2.3–§2.6), 05-tasks v1.11.1 (5T.3.10, 5T.3.13, 5T.6.2), 05-metadata v1.11.1 (M1.3, M1.4, M1.6, M2.3),
05-notify v1.11.1 §3, 05-events §Tx, 06 v1.11.1 (§6.4.6, §6.4.11, §6.5.1), 09 v1.10.1 (§9.2.1–§9.2.3, §9.3 staging,
S0, S1, §9.13) and 12 §12.4.4.

A-1 … A-17 and R3-1 … R9-11 are binding. Nothing below asks for deleted machinery back. Spec anchors are
`file §section (l.N)`. Code anchors are `path:line` at `748fd787a`; the checkout's `internal/` and `cmd/` are
byte-identical to it.

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 6 | DATA10-1 … DATA10-6 |
| MINOR | 3 | DATA10-7 … DATA10-9 |

| ID | Sev | Title |
|---|---|---|
| DATA10-1 | MAJOR | The identity and secrets staging methods take no `ctx`, so `storage.LocksHeld(ctx, l)` cannot be evaluated there. R9-1's "refused outside the hold" guard and its test cannot be built against the frozen `auth.Store` |
| DATA10-2 | MAJOR | HEAD's `configBlobSaveMu` also covered the gap-fill file write. Core runs `Committed` after the lock is released, and only the config `Committer` calls it. A cleared secret can come back, and secrets' own `Put`/`Delete` of a kept name commit outside the lock |
| DATA10-3 | MAJOR | Deadlock freedom is argued only among locks taken through `CommitLocked`. The books rank table is partial, the core-side mutexes are unordered (05-metadata and 05-config disagree on whether the alias commits one batch), and the non-re-entrant self-deadlock is not ruled out |
| DATA10-4 | MAJOR | Machine 1 has no failure edge out of the states R9-7 created between the in-flight rename and the first journalled move. Step c's copy of `backup.location.json`, a file that does not exist on S2b–S4 r1, is a concrete trigger |
| DATA10-5 | MAJOR | `staging-scrub` cannot check the archive writer's `binary_id`: the marker does not carry it and the scrub takes no archive. 09 and the S0 test require that check |
| DATA10-6 | MAJOR | R9-11 half-fixed: H1 and H2 delete only their own attempt's checkpoint. Retained checkpoints and the swap set-asides in their `keep_previous` window keep raw tokens and plaintext secrets, and the canary test cannot see them |
| DATA10-7 | MINOR | "The first hardening cutover lifts `{core, 0}`" is false when that cutover raises only `books` (H3 always; H1 when it rewrites in place) |
| DATA10-8 | MINOR | The ABO-local swap used on S1–S2 r3 is outside R9-7's wording, machine 1's stage notes and the crash matrix, and S1's done-check tests hooks that do not exist yet |
| DATA10-9 | MINOR | Drift: `counterMu` called an identity lock, the claim that the reset's `fresh_rows` are the "same bytes", the merge conflict reported "at commit" |

## Regression check (round-9 BLOCKER/MAJOR in this lens)

| Item | Status | Residual |
|---|---|---|
| DATA9-1 (identity locks across core's commit) | **Fixed in mechanism.** Books' identity store implements `legacy.WriteLock` with the six lock names and mappings (05-authn §3.4 l.649-712). Every identity batch commits inside `storage.CommitLocked` (05-storage §5S.3.2 l.385-421). `userWriteMu` is named in §5S.2, §5S.9.1 row 1 and §5S.9.3. The race tests run through `auth.Service` | DATA10-1: the guard that refuses a write staged outside the hold cannot be implemented as specified. DATA10-3: the order is incomplete |
| DATA9-2 (one instance per family) | **Fixed** (05-storage item 4 l.417-421, §5S.3.11 row; 06 §6.4.11 l.1653-1666; 05-tasks 5T.6.2 l.1697-1703; `TestLegacyStoresAreSingletons`). Verified at HEAD: `counterMu`, `opsMu`, `apiKeyMu` and `inviteMu` are fields of `PebbleStore` (`internal/database/pebble_store.go:226-232`). `userWriteMu`, `sessionWriteMu` and `userMergeApplyMu` are package variables (`pebble_store_auth.go:515,525`; `user_merge.go:403`), `configBlobSaveMu` is a package variable in `internal/config` (`persistence.go:1897`), and `bootstrapMu` is one in `internal/server` (`bootstrap.go:136`) | — |
| DATA9-3 (`inflight.json` timing) | **Fixed.** The rename happens before c in 05-backup step b l.742, §5S.3.7 l.1200-1205, §5S.3.16 l.2072-2079, 06 P0a l.2017, 09 §9.2.2 l.390-394 and 05-updater. The override passes only `restore_pending`. A crash-matrix case covers it | DATA10-4 (new failure states) |
| DATA9-4 (reset rejected mid-set-aside) | **Fixed** (§5S.3.15 "Set aside" and "Failure rule" l.1826-1827; machine 1 `set_aside_started --> rejected` moves back first; `TestFactoryResetResumeAfterExpiryCompletes`) | DATA10-4 (reset at `staged` with the in-flight file); DATA10-9(b) |
| DATA9-5 (no path to `applied` without hooks) | **Fixed** (`opened --> marker_written` for S2b, S3 and S4 r1; 05-backup step h; 06 l.2027) | DATA10-8 (S1–S2 r3 not covered) |
| DATA9-6 (`Alias.Legacy` conflation) | **Fixed** (`LegacyYAML`/`LegacyBlob`, `FileOnly()`, 05-http-api l.1315, `TestFileOnlyLegacyKeysNeverDBStored`) | — |
| DATA9-7 (`ReplaceOpRowsPrefix` lock rule) | **Fixed** (`RunStore.ReplaceRows` l.1270-1281, its one rule in 5T.6.2 l.1685-1695, §5S.9.1 row 13; HEAD `pebble_store_op_rows.go:107-166`) | — |
| DATA9-8 (H1–H3 owners) | **Fixed** (owners `books` plus `core`; `TestHardeningCutoverRefusedByEveryOlderRelease`; H2's four rows reconciled in 05-config §1.12.5, 09 and 12). One-way holds: every guard-aware build declares both owners, and pre-guard builds are refused by the legacy projection | DATA10-6, DATA10-7 |
| DATA9-9 … DATA9-15 (minors) | Machine 4's offline-close edge and I5: done. §9.2.3 lists `c/config/` and `c/backup/`: done (09 l.468). The S2b reset text: done. P−1 is marked deferred: done. Row 11: done. The pre-S0 refusal is aligned in 09, 05-storage, 05-backup and 06: done. `Record.InstallID` and the two config locks: done | DATA10-9 |

## Simulations (what was run)

**Locks across core's commit (verified against HEAD).**
- **Identity.**
  - `TouchAPIKeyLastUsed` holds `apiKeyMu` across its own write (`pebble_store_auth.go:1284-1302`).
  - `UpdateUser` and the e-mail writers take `inviteMu`, then `userWriteMu` (`:591-593`, `:668-670`, `:1566-1568`).
  - `mutateUserCredentials` takes `userWriteMu`, then `sessionWriteMu` (`:745,760`), and `RevokeSession` takes `sessionWriteMu` (`:1697`).
  - `ApplyUserMergeOps` takes `userMergeApplyMu`, then `inviteMu`, then `apiKeyMu` (`user_merge.go:508-529`).
  - `RedeemBootstrapToken` holds `bootstrapMu` while `findOrCreateAdminUser` takes `inviteMu` and `userWriteMu` (`bootstrap.go:212-223,364`).
  - Every lock is mapped and spans the commit, except for the guard (DATA10-1) and the order (DATA10-3).
- **Runs.** HEAD's `opsMu` sites are mapped to `WriteRuns`. Clocks, pause and stand-down share the hold, `Prune`/`Recover` take it per chunk, and `ReplaceRows` stays lock-free as at HEAD. Correct.
- **Config, instances and notify rules.** `configBlobSaveMu` is held from the snapshot through the **file** step at HEAD (`persistence.go:1940-1944` and the `SaveConfigToFile` call after the secrets loop). The commit is covered, the file step is not, and there are writers that skip the lock (DATA10-2).
- **`counterMu`.** `nextID` is a leaf lock around its own `Sync` (`pebble_store.go:966-989`); `mintNarratorLocked` nests it under a name lock (`pebble_store_authors.go:1226`). Shared through the one instance. Correct.
- **HEAD mutexes not listed.** `aijobWriteMu` is replaced by `Expect` (sole writer). `DailyBudget.mu` is replaced by the Registry mutex across the gate store's own commit. Roles have no HEAD lock (`pebble_store_auth.go:1002-1097`). The search dirty set has no lock (`pebble_store_search_dirty.go`). Nothing is missing.

**Swap and reset journals, crash at each step (machine 1).**
- **Restore, in this order:**
  1. `staged`, then step a: correct.
  2. A crash between journalling `rechecked` and the rename: `pending.json` remains, so it is re-run or rejected. Correct.
  3. A crash after the rename: the journal resumes at c. Correct.
  4. A **failure** inside c has no legal edge (DATA10-4).
  5. From c to `marker_written`, x restores the location and lineage copies: correct.
  6. Hookless and hooked paths to `applied`: correct.
- **Reset, in this order:**
  1. The rename comes before `set_aside_started`.
  2. A crash at `staged` with the in-flight file resumes forward. Correct.
  3. A failure before `set_aside_started` has no edge (DATA10-4).
  4. A failure after it moves back, then `rejected`: correct.
  5. `set_aside --> reset_applied` after P4d 1; `hook_done` comes from the hook or the kernel. Correct.
- **Checkpoint restore.** The open-checkpoint offline finish and `failed` resuming on another binary are correct.

**Restores, including from S4 r2.**
- **On S4 r2.**
  - A `legacy-abo/0` restore has nil coverage, so every `abk_` key reads `inactive` and sessions and one-time tokens are deleted.
  - The bootstrap rows are deleted and re-minted by `BootWrites` at P4d 4, so an admin still gets in with a password or a bootstrap token. A crash between hook batches reruns the idempotent hooks.
  - Correct, and row 11 carries it.
- **On S4 r1, after a rollback from S4 r2.** The hookless edge writes no `served_boot`. The next S4 r2 boot classifies the store as replaced (the served-boot pair differs), so the hook runs with nil coverage, which row 11 covers. Correct.
- **HEAD in place.** As round 9. Correct.

**Staging.**
- The binary check for `staging-restore`: correct. The marker is written key first, then the sidecar: correct. The pre-S0 refusal is aligned in every file: correct.
- The scrub's binary check cannot be implemented (DATA10-5).

**Forward, rollback and roll-forward per stage.**
- **S0–S2.** Guard rows only.
- **S1–S2 r3.** The ABO-local swap (DATA10-8).
- **S2b.** The kernel swap, resets and the hookless restore (DATA10-4 trigger).
- **S3.** Config commits under `configBlobSaveMu` (DATA10-2).
- **S4 r1.** Core writes nothing.
- **S4 r2.** Identity goes through `CommitLocked` (DATA10-1, DATA10-3). r2 → r1 → r2 is a binary swap.
- **S5.** Runs go through `WriteRuns`. Rollback reads every run.
- **S6.** The relay goes through `WriteRuns`, and handlers write only their own core keyspace. Correct.
- **S7.**
  - Instances and rules commit under `configBlobSaveMu` (DATA10-2, DATA10-3).
  - `import_path:` uses GetForUpdate, and `counter:import_path` is taken through the shared `counterMu`. Correct.
- **S8–S10.** No legacy-row shape change. Correct.
- **R9-1 adds no stored state**, so every rollback stays a binary swap.

**H1–H3.**
- Each is one-way: the guard models both owners, and pre-guard builds are refused through the projection.
- The cleanup claim is incomplete (DATA10-6), and so is the restore-bound claim (DATA10-7).

---

## Findings

### DATA10-1 — MAJOR — The identity and secrets staging methods take no `ctx`, so the R9-1 refusal guard cannot be evaluated

**Where.**
- 05-storage §5S.3.2 l.459-462: `LocksHeld(ctx context.Context, l Lock) bool` — "a legacy store refuses a write staged
  outside its hold with it".
- §5S.3.11 `WriteLock` comment (l.1594-1610): "a store refuses a write staged outside the hold (storage.LocksHeld)".
- 05-authn §3.4 l.714-718: "A staged identity write whose ctx does not carry the hold of its lock
  (`storage.LocksHeld`) is refused". Error row l.1468. `TestLegacyStagedIdentityWriteOutsideLockRefused` (l.1592).
- 05-config: the books store's in-place secret writes, under the same lock (`LockConfigSettings`, "the blob and the
  four secret rows").

**Contradiction.** The frozen SDK signatures through which those writes are staged carry no context:
- `auth.Store`: `PutUser(b, u)`, `IssueSession(b, s)`, `DeleteSession(b, s)`, `TouchSession(b, …)`,
  `RevokeSignInsBefore(b, t)`, `IssueAPIKey(b, k)`, `PutAPIKey(b, k)`, `TouchAPIKey(b, id, at, ip)`,
  `IssueOneTime(b, t)`, `TakeOneTime(b, kind, token)`, `PutIdentity(b, …)`, `DeleteIdentity(b, …)`, `StageMerge(b, plan)`
  (05-authn l.555-597).
- `legacy.AuthAtomWriter.IssueAPIKeyAtoms(b, k, atoms)` (l.647).
- `secrets.(*Store).Stage(b, w)` (05-config l.2196), and `legacy.SecretsStore.Write(b, n, v, clear)` (l.2163), through which a
  `LockConfigSettings`-guarded encrypted row or plaintext blob field is written.

The store has no context in which to call `LocksHeld`. Only 05-tasks' `RunStore`, `OverrideStore.Stage`, `RuleStore.Put` and
`InstanceStore.Put` carry `ctx`. Round 9's BLOCKER was exactly a write whose HEAD mutex silently did not span the
commit. This guard is the one mechanism that turns a missed `CommitLocked` back into a test failure, and it cannot
be written for the identity family.

**Second defect, even with a context.** A context-carried hold proves "this goroutine is inside some `CommitLocked`".
It does not prove "this batch is the one that call commits". A stage function can stage an identity write into a batch
captured from an outer caller, and that batch then commits after the hold is released. `LocksHeld(ctx)` reads `true`
throughout.

**Fix.**
- Bind the hold to the **batch**. `CommitLocked` records the locks it took (and joined) on `b` before it calls
  `stage`, and clears them when `Commit` returns. Change the signature to `storage.LocksHeld(b *Batch, l Lock) bool`.
  Every legacy staging method already receives `b`, so books' identity store, run store and config store, and
  `SecretsStore.Write`, refuse a write to a guarded family when `b` does not hold the lock.
- Keep the ctx-carried set only for a nested `CommitLocked` to join held locks.
- Update 05-storage §5S.3.2 and §5S.3.11, 05-authn §3.4 and its error row, 05-tasks 5T.3.13 (`ErrRunsNotLocked` checks
  `b`), and 05-config §1.3.3 and §2.3. A store that has none of these methods needs no change.
- This must be settled before S4, because `auth.Store` is frozen by the API-stability gate. The alternative, adding
  `ctx` to every staging method, is a breaking change once it is frozen.
- Extend `TestLegacyStagedIdentityWriteOutsideLockRefused` with the captured-batch case: a write staged inside a hold
  into another batch is refused.

### DATA10-2 — MAJOR — `configBlobSaveMu` no longer covers the gap-fill file step, and two writer paths skip the lock

**HEAD.**
- `SaveConfigToDatabase` holds `configBlobSaveMu` "from before the snapshot until the file step is done"
  (`internal/config/persistence.go:1940-1944`). It writes the blob, the four encrypted rows and then `config.yaml`
  inside one hold, so the file always carries the latest save.
- HEAD's gap fill refills an **empty** OpenAI, Google Books or Hardcover key from that file (`persistence.go:73-91`).

**Spec.**
- The `Committer` calls `legacy.ConfigCommitObserver.Committed` "then", after `RetryOnConflict` and therefore after
  `CommitLocked` released the lock (05-config §1.3.3 l.437-441).
- A set's secondary copy is rewritten in `Committed`. A clear removes the file key **before** its commit, inside the hold
  (l.1624-1631, l.2161-2163).
- Read order step (5) serves a non-empty secondary copy when the row is empty (l.2271-2276).
- `Committed(ctx)` takes no argument, and DATA5-20's fix merges each commit's own keys (l.1646-1650, l.2449).
- 05-metadata's `Registry.Put` and 05-notify's `RuleStore` commit through `CommitLocked` and never call `Committed`
  (no occurrence in either file).
- `secrets.(*Store).Put`/`Delete` of a **kept** name call `SecretsStore.Write` in their own batch (l.2157, l.2194-2195,
  §2.6 l.2388), outside any `CommitLocked`. That contradicts §1.6's "a render staged by a writer outside the lock (none
  exists from S3)" (l.1166-1167).

**Failure.** The Apply mutex serialises `Committed` only between `Apply` calls. From S7 the Google Books and Hardcover
keys and the OpenAI key are also written by `Registry.Put` (05-metadata M1.4 step 2a), which takes no Apply mutex.
1. A set of key K = v1 through the legacy config alias commits, and the lock is released. Its `Committed` has not run yet.
2. A clear of K through the integrations route takes `configBlobSaveMu`, removes K from the file, deletes the row and commits.
3. The first set's `Committed` now merges K = v1 back into `config.yaml`.

The row is empty, so core's read order (5) serves v1 at once, and after a rollback HEAD's gap fill reloads v1 and its
next save re-encrypts it. A key the admin cleared, for example after a leak, is live again.

Separately, a `Registry.Put` **set** never rewrites the file copy. The file keeps the previous key, and HEAD's
key-regeneration path (`persistence.go:1229-1262`) and gap fill would bring that stale key back after a rollback.
At HEAD neither can happen.

**Fix.**
- Run the observer **inside** the hold. Give `storage.CommitLocked` an optional `after func(ctx context.Context)`
  that runs after a successful `Commit` and before the release. Alternatively, state that each committer calls
  `Committed` inside its own `CommitLocked` hold.
- Require that **every** committer of a batch the books config store staged into does this: the config `Committer`,
  `integrations.Registry`, `notify` and `secrets.(*Store).Put`/`Delete` for a kept name. The last two must commit
  through `CommitLocked` with `LockConfigSettings`. Secrets' per-name keyed mutex is then taken inside `stage`, after
  `configBlobSaveMu`, never before it, to avoid an inversion with `Apply` → `Stage`.
- `Committed` renders the three file keys from the rows it reads inside the hold, never from a commit's own values.
- Correct §1.6's "none exists from S3".
- Add `TestGapFillClearNotUndoneByLateSet` (a set and a clear of one kept name raced through two routes leave neither
  the row nor the file key) and `TestRegistryPutRewritesGapFillCopy`.

### DATA10-3 — MAJOR — Deadlock freedom is argued only among `CommitLocked` locks; the order is partial and core-side mutexes are unordered

**What holds.** HEAD takes its locks in this order:
- `inviteMu`, then `userWriteMu` (`pebble_store_auth.go:591-593` and every e-mail writer);
- `userWriteMu`, then `sessionWriteMu` (`:745,760`; HEAD's comment says "nothing takes them the other way round");
- `userMergeApplyMu`, then `inviteMu`, then `apiKeyMu` (`user_merge.go:508-529`);
- `bootstrapMu`, then `inviteMu`, then `userWriteMu` (`bootstrap.go:212`, then `CreateUser`).

05-storage's order (l.407-416: `userMergeApplyMu`, `inviteMu`, `userWriteMu`, `apiKeyMu`) and 05-authn's (l.706-708,
adding `sessionWriteMu`) are consistent with these orders. The direct HEAD writers that remain from S4 r2 (invite delivery,
guest tokens, `RehashUserPassword` under `userWriteMu` at `:861`, `RetireMergedUser`) take single locks or
`inviteMu`→`userWriteMu`. So no identity inversion exists today.

**Gaps.**
1. **The rank table is not total.**
   - `ErrLockOrder` fires when "two distinct locks share a rank" (l.464, l.2500), so every one of books' eight locks
     needs a distinct rank.
   - No file ranks `bootstrapMu`, `opsMu` or `configBlobSaveMu`, or orders `sessionWriteMu` against `apiKeyMu`, and
     05-storage's list omits `sessionWriteMu`.
   - A batch that holds `LockBootstrap` with `LockUsers` and `LockAPIKeys` (the bootstrap redeem, 05-authn l.697), or
     the post-restore hook holding five names, depends on ranks three owner specs would each choose.
   - `bootstrapMu` must rank below `inviteMu`, because HEAD takes it first.
2. **Core-side mutexes around or inside a hold are ordered only for config `Apply`** (05-config §1.6 l.1165: taken
   first).
   - The integrations Registry swaps its snapshot "under a writer mutex" (05-metadata M1.6 l.935-937), and nothing
     says whether `Put` holds it across `CommitLocked`.
   - 05-metadata M1.4 step 2a (l.796-806) says the legacy config alias stages a config write and an instance write
     into **one** batch ("a batch both staged into (the legacy config alias) holds it once").
   - 05-config §1.12.2 (l.1667-1670) says the alias **splits** each request into `Registry.Put` and `Store.Apply`.
   - Under 05-metadata's reading, the Registry stages inside `Apply`'s `configBlobSaveMu` hold (`configBlobSaveMu` →
     writer mutex), while a native `Registry.Put` takes the writer mutex and then `CommitLocked` (writer mutex →
     `configBlobSaveMu`). That is an inversion.
   - Secrets' keyed mutex is already taken inside the `Apply` hold (`Stage`).
3. **Non-re-entrancy outside `CommitLocked`'s view.**
   - The join logic knows only locks taken through `CommitLocked`. A stage function that calls a books method that
     takes a held mutex **directly** deadlocks on itself, and `ErrLockOrder` cannot see it. Examples:
     `MarkInviteEmailSent` or `RecordInviteGuestPolicy` under `inviteMu` (`pebble_store_invite_delivery.go:160,183`),
     or `RehashUserPassword`.
   - "Staging takes no mutex" (05-authn l.714) constrains only the staging methods.

**Fix.**
- Put **one rank table** in 05-storage §5S.3.2 for books, any total order that extends HEAD's, with its derivation.
  For example: `userMergeApplyMu` < `bootstrapMu` < `inviteMu` < `userWriteMu` < `sessionWriteMu` < `apiKeyMu` <
  `configBlobSaveMu` < `opsMu`. Cite it from 05-authn, 05-config and 05-tasks.
- State the **core-side rule**: Apply's mutex, the Registry writer mutex, the rule store's and the gate Registry's
  mutexes are taken before `CommitLocked` and never inside a `stage`. Secrets' keyed mutex is the one exception,
  taken only inside a `stage`, after its config lock (DATA10-2).
- Resolve the alias contradiction. Either keep 05-config's split and delete 05-metadata l.796-806's one-batch
  sentence, or specify a `Registry.StageIn(ctx, b, …)` that takes no writer mutex and swaps the snapshot after the
  outer commit.
- Add the rule "no `stage` function calls a books method that takes a HEAD mutex of a family the hold covers".
- Add `TestLockRanksTotalAndHEADConsistent` (it checks the table against the four HEAD orders above) and a `synctest`
  case that races a native `Registry.Put` against a legacy `PUT /api/v1/config` that writes integration-bound and
  plain keys.

### DATA10-4 — MAJOR — Machine 1 has no failure edge out of the states R9-7 created; step c's state-file copy is a concrete trigger

**Where.**
- §5S.3.16 machine 1 (l.2040-2066): `rechecked --> previous_moved: c`, and x is drawn only from `previous_moved`
  onward ("stands for one from each state between `previous_moved` and `marker_written`").
- For a reset, `staged --> set_aside_started` and `set_aside_started --> rejected` is the only failure edge.
- "Step a's rejection rule reads only `pending.json`" (l.2079-2085).
- 05-backup step c (l.743): "Copy `<state_dir>/backup.location.json` and `<state_dir>/storage.boot.json` to
  `restore/<id>/` … Rename live items …".
- §5S.3.15 "Set aside" (l.1826): after the rename, "copies `<state_dir>/backup.location.json` into `restore/<id>/`",
  then journals `set_aside_started`.

**Failure.**
- Since R9-7 the in-flight file exists from the end of step b (restore) or before `set_aside_started` (reset). A
  non-crash failure in that window has no legal transition:
  - an I/O error on either copy, or `EACCES`/`EBUSY`/`EXDEV` on a live rename after staging pre-validated it;
  - the journal still reads `rechecked` (restore, some items possibly moved) or `staged` (reset, nothing moved);
  - step a cannot reject it, because `pending.json` is gone;
  - x is not drawn from `rechecked`;
  - the reset's failure edge starts at `set_aside_started`.
- Each boot retries and fails. `CheckReader` reads `restore_inflight`, which has no override, so `make rollback`,
  `deploy` and the updater refuse, and no documented offline step applies (`restore --revert` is only for a failed
  swap-back). For a restore, a partial c also leaves the DB path empty, which breaks I3.
- **Trigger.** `backup.location.json` has one writer, the journal component (05-backup l.1109, l.1351), which writes it
  from S4 r2 (06 l.2025). On S2b–S4 r1, and on a host-loss restore, the file does not exist, and neither step says
  "if present". An implementation that treats the missing source as an error wedges **every** restore and reset on
  S2b–S4 r1.
- The crash matrix kills; it never returns an error, so it cannot catch this.

**Fix.**
- In 05-backup step c and §5S.3.15 "Set aside": "copy … **if present**". Step x and the manual undo restore only the
  copies that were saved.
- Add `rechecked --> swapped_back: x (a failure inside c after the in-flight rename; x moves back whatever c moved,
  rename-if-exists)`.
- Add `staged --> rejected: KindReset, in-flight file present, failure before set_aside_started (nothing moved;
  inflight.json → rejected.json)`.
- State in 05-backup x that "after step c" includes failures inside c.
- Give `crashmatrix.Injector` an error-return mode (`ENOSPC`, `EACCES` on the N-th operation) beside the kill. Add the
  scenario "a fault on every op from the in-flight rename to the first journalled move", and
  `TestRestoreWithoutLocationFileS2b`.

### DATA10-5 — MAJOR — `staging-scrub` has no way to check the archive writer's `binary_id`

**Where.**
- 09 §9.3 step 2 (l.968-972): "`staging-restore` and `staging-scrub` run with the binary that wrote the archive … Both
  refuse unless the running binary's `buildid.Binary` equals it".
- S0 done-check (l.1013-1014): "a binary whose `buildid.Binary` differs from the archive's writer is refused by both
  subcommands".
- 05-backup l.629: "both run with the binary of the release that wrote the archive". R9-9 names only `staging-restore`.

**Contradiction.**
- The scrub's interface is `staging-scrub --db <copy> --fields … --confirm-destroy-credentials`, with no archive.
- The only link between the copy and its archive is the staging marker, `{archive_sha256, nonce, at}` (05-storage
  l.1104 and the `storage.StagingMarker` struct at l.1117-1124; 06 `WriteStagingMarker(archiveSHA256)` l.1119).
- So the scrub cannot learn the writer's `binary_id`. The S0 test cannot pass, and the protection the round-8 and
  round-9 decisions rely on is absent for the scrub. That protection is that an older binary scrubs with an older list
  of credential families (R6-7).
- `TestSecretClassListMatchesLegacyStore` checks the field list against the **running** build, not the archive's.

**Fix.**
- Add `BinaryID string \`json:"binary_id"\`` to `storage.StagingMarker`, in both the key and the sidecar.
- `staging-restore` writes its own `buildid.Binary`, which it has just verified equals the manifest's.
- Change `WriteStagingMarker`'s argument to the marker, or add the field.
- `staging-scrub` refuses unless both halves agree on hash, nonce **and** `binary_id`, and that `binary_id` equals
  its own `buildid.Binary`.
- S0's pre-S2b path writes the same field.
- Extend `TestStagingScrubRefusesUnmarkedStore` with "a marked copy and another binary → refused, nothing written".

### DATA10-6 — MAJOR — R9-11 half-fixed: H1 and H2 delete only their own attempt's checkpoint

**Where.**
- 09 §9.13 l.1568-1572: "H1 and H2 leave no old copy in the live directory … they delete the pre-cutover
  checkpoint and the old Pebble files".
- 05-storage §5S.3.13 l.1730-1739: "deleting the attempt's pre-cutover checkpoint and its record … so no SST, WAL
  segment or checkpoint under the DB directory keeps the old bytes".
- 05-authn §11.2 l.1817-1827; 12 §12.4.4.
- Done-checks `TestH1_NoRawTokenOnDisk` and `TestH2_NoPlaintextSecretOnDisk`: "a planted canary, run the cutover, scan
  every file under the DB directory and its checkpoint directories".

**Copies that survive.**
- **Other checkpoints.** §5S.3.12 (l.1705) keeps the newest `checkpoint_keep` (default 1) **consumed** predecessor
  and never auto-prunes an open checkpoint. The checkpoint from the last pre-H upgrade therefore remains under
  `<db parent>/.mediacore-checkpoints/`, with every raw `sess:` key and the plaintext blob.
- **Swap set-asides inside `core.storage.swap.keep_previous` (168 h; 05-backup l.753):**
  - a restore's `<db parent>/.mediacore-restore/<id>/previous/`;
  - its `restore/<id>/failed/`;
  - a reset's `<state_dir>/restore/<id>/previous/` and its beside-itself `<parent>/.mediacore-restore/<id>/previous/`
    (§5S.3.15 "Stage").

  These are full pre-H stores outside the DB directory.
- **The canary test cannot see them.** The canary is planted immediately before the cutover, so no older copy can
  contain it, and the test passes while real tokens remain.

**Fix.**
- H1 and H2 delete **every** checkpoint of this store, not just the attempt's.
- H1 and H2 refuse to start (`ErrCutoverResidue`, naming the paths) while any swap `previous/` or `failed/` set-aside
  exists, unless the operator passes a flag that sweeps them in the cutover boot first. The release note names the
  paths.
- The done-check plants the canary **before** a fixture upgrade, restore and reset, so retained checkpoints and
  set-asides contain it. It then scans the DB directory, `.mediacore-checkpoints/`, `.mediacore-restore/` beside the
  DB and every recorded beside-itself destination, and `<state_dir>/restore/`.
- State the same in 09 §9.13, 05-storage §5S.3.13, 05-authn §11.2, 05-config §1.12.5 and 12 §12.4.4.

### DATA10-7 — MINOR — "The first hardening cutover lifts `{core, 0}`" does not hold when that cutover raises only `books`

**Where.**
- 09 §9.13 l.1567-1568 ("The first cutover also lifts books' legacy restore bound `{core, 0}`").
- 05-storage l.1729-1730; 05-config §1.12.5 ("The first of them"); 05-authn l.1813; 12 §12.4.4.

**Contradiction.**
- The bound is passed only when the store's **`core`** level exceeds 0 (05-backup §5B.3.9 rule 3, l.938-945).
- H3 raises `books` only (09 table; 05-config §1.12.5), and H1 raises `core` only "when the families move".
- "Their order is the owner's". If H1 (rewriting in place) or H3 ships first, the bound is not passed, and archive
  encryption stays off. That is the safe direction, but the statement is false.
- 05-backup says the bound is passed by "the first per-family cutover", a different event.

**Fix.** Either:
- every hardening cutover also raises `core` (harmless, since every guard-aware build declares it), or
- replace the sentence with "the first cutover that raises `core`", in all five files.

### DATA10-8 — MINOR — The S1–S2 r3 ABO-local swap is outside R9-7's wording, machine 1 and the crash matrix

**Where.**
- 09 S1 "ABO-local fixes" (l.1092): the staged restore is "implemented in `internal/backup`". "The intent becomes an
  in-flight journal **when the swap starts**". The kernel's swap takes over only from S2b.
- 05-backup l.1191; 03 l.313: `storage/restorejournal` "Lands at **S2b**".
- Machine 1's hookless note names "S2b, S3, S4 r1".
- `TestBootCrashMatrix` is required from S2b.
- S1's done-check (l.1096) asserts "a crash between the swap and the hooks resumes them", but no hook exists before
  S4 r2.

**Gaps.** For S1–S2 r3 (three or more production releases):
- "When the swap starts" is not "before the first rename", which DATA9-3 requires.
- There is no legal path to `applied`, because `c/storage/boot` exists only from S2b (§5S.3.7 boot lineage row).
- Nothing tests the c–g window.
- Nothing says whether S2b's P0a resumes, or refuses, a journal that the ABO-local code left in flight. That case
  arises from a manual image change, since the tooling refuses.

**Fix.**
- In 09 S1, state that the ABO-local swap follows 05-backup §5B.3.4 exactly: the in-flight rename after `rechecked`
  and before c; its terminal step is `applied` after the swap with no lineage write; it uses the `restorejournal`
  JSON shape (copied, since the package lands at S2b).
- Make S2b's P0a resume such a journal and test it (`TestS2bResumesS1Journal`).
- Replace S1's hook test with `TestInflightBeforeFirstRename` and a kill at each rename on the S1 code.

### DATA10-9 — MINOR — Drift

- **(a) `counterMu` called an identity lock.**
  - 05-storage §5S.2 l.121 lists `counterMu` among "owner locks for identity writes".
  - §5S.3.2 item 1 (l.396-399) says "the identity store and 05-library's root source both allocate through" it.
  - 05-authn l.708-712 says "no identity row takes an ID from it". HEAD's identity rows use ULIDs
    (`pebble_store_auth.go:152,240,1010,1105,1576`; `oauth_identity.go:59`).
  - Fix: delete "the identity store and" and move `counterMu` out of the identity row of §5S.2.
- **(b) The reset's `fresh_rows` are "the same bytes".**
  - §5S.3.15 "Next boot" (l.1828) says the rows "are rendered at staging by the binary the rejection rule requires to boot
    them, so they are the bytes the reset boot would render".
  - Since DATA9-4 the set-aside, and so the fresh-install batch, may run on **another** core build. "Every build of the
    extraction renders the same" (l.1826) is also untrue for defaults added later.
  - Fix: "rendered by the staging binary; a resuming build writes them as given. They are factory defaults, so a key
    they lack takes the resuming build's default".
- **(c) The merge conflict reported "at commit".**
  - 05-authn `Admin.StageUserMerge` comment (l.215-223): "a record changed since the plan was built fails the commit with
    `storage.ErrConflict`".
  - For ABO's store, 05-storage l.474-480 says the merge commits "with no `Expect`". HEAD detects the change at staging
    under the locks (`UserMergeConflictError`, `user_merge.go:533-545`).
  - Fix: "native store: `storage.ErrConflict` at commit; ABO's store: HEAD's conflict error at staging, inside the
    hold; both reported as `auth.ErrMergeConflict`".
