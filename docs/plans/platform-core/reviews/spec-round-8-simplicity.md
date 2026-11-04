<!-- file: docs/plans/platform-core/reviews/spec-round-8-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e259b3b-87e4-4580-a22b-7f7d3b31a85f -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8: simplicity and scope (SIMP8)

Lens: over-engineering, YAGNI, duplicated mechanisms and scope creep. This round has two jobs:

- **(A)** A regression check of every round-7 SIMP finding: MAJORs in full, MINORs briefly.
- **(B)** New issues that the round-7 revision introduced. The focus areas were:
  - the `legacy` boundary after the SIMP7-8 renames;
  - the one "stays in SDK" list;
  - duplicated mechanisms (the reset journal against the restore journal, and capability detection);
  - the deferred-build boundary;
  - unused S0–S10 features;
  - whether the round-7 additions are the smallest correct designs: `kernel.OpenOffline`, `config.NarrowStored`, the
    post-reset hook and `staging-scrub`.

R7-1..R7-8 are binding. Where a finding touches one of them, it either shows the text as written is broken or keeps
every property the resolution states and changes only how it is carried out. Nothing below asks for removed machinery
back.

Revision reviewed: commit `f33ec2ef0` (03, 05-storage, 05-backup, 05-config, 05-library, 06 and 09 as of the round-7
integration pass). Line numbers refer to that revision. Code anchors are at `748fd787a`.

**Counts:** 0 BLOCKER, 2 MAJOR, 8 MINOR.

## (A) Regression check: round-7 SIMP findings

| Round 7 | Status | Evidence |
|---|---|---|
| SIMP7-1 mirror test in `export_test.go` (MAJOR) | **Fixed** | 03 l.404–417 states the pattern once: `export_test.go` exports aliases only, and `legacy_mirror_test.go` in `package <owner>_test` asserts both directions. Every owner cites it: 05-authn l.670, 05-rbac l.475, 05-config l.514 and l.1952, 05-metadata l.80 and l.978, 05-notify l.196 and l.335, 05-library l.293, 05-tasks l.1314 (`executor_test`), 06 l.1578. A grep finds no remaining "`export_test.go` assertion" outside history bullets. |
| SIMP7-2 root suspension with no consumer (MAJOR) | **Fixed, then overshot** | 05-library l.135–145 (`RootSpecLegacyState` keeps only `Ext`), l.299–303, and the §5L.13 deferred table at l.851–854. 03 l.446–452 and l.508–509 and README l.70 now cite the deferral. The fix added a refusal mechanism (`library.ErrLegacyRootShares`, its test, a `legacytest` case) for a field that no S0–S10 app can use at all. See SIMP8-5. |
| SIMP7-4 mirror-typed SDK field; library input unspecified (MAJOR) | **Fixed** | `secrets.Options.LegacyStore any` (05-config l.1931–1937) and `library.Options.LegacySource any` with `library.New(opts Options)` (05-library l.255–268). The one sentinel `storage.ErrLegacyStoreType` is defined in 05-storage l.1445–1449. The cutover wording at 05-config l.1624–1626 names `legacy` only. |
| SIMP7-3 three "stays in SDK" lists | Fixed | One table in 05-storage l.1420–1438. 03 l.430–440 and 14 K23 cite it with one example per reason. New members are missing from it (SIMP8-6). |
| SIMP7-5 capabilities detected twice | **Half-fixed** | 05-storage l.1385 and 06 l.1567–1576 say "detected once". The count of what the kernel itself asserts now differs between files, and R7-2 added a capability that the kernel calls (SIMP8-3). |
| SIMP7-6 `storage_extension_suspended` | Fixed | The only remaining hit is a deletion note (05-tasks l.1641). |
| SIMP7-7 `LegacyMigration` dead SDK | Fixed | 05-storage l.1437–1438. 09 l.1157 (T8). 04 l.884 (S5 row). |
| SIMP7-8 naming rule | **Fixed, one new violation** | `RBACAtomWriter`, `TasksRecoverer`, `LibraryRootSource`, `RootSpecLegacyState` and `TestConfigEntryRoots_RoundTrip` are used everywhere outside history. The new `legacy.ResetRun` breaks the rule (SIMP8-7). |
| SIMP7-9 wrong section cited | Fixed | 03 l.507 cites §1.12.5. |
| SIMP7-10 `StandDownStore` missing | Fixed | 03 l.468 (`tasks` row). |
| SIMP7-11 residues | Fixed | 04 l.1642 (aitest), and 05-storage l.1385 says "notify keeps no Widening record". |

**`legacy` boundary scan after the renames.** I checked every member of 05-storage's one list (l.1396–1412) against
06's `legacy.Stores` (l.1492–1503), 03's naming paragraph (l.373–384) and the owner files. Spelling and lifetime
agree. The two new members R7-2 added are the exceptions: `legacy.Maintenance` (SIMP8-4) and the reset-hook carrier
(`legacy.ConfigResetArgs`, `legacy.ResetRun`, `ConfigOpenEnv.Reset`/`ResetUnfinished`; SIMP8-7).

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP8-1 | MAJOR | 05-storage §5S.3.15 (l.1736–1745); 05-backup l.616–618, l.685–703, l.363–364, l.1003–1004; 05-storage l.1100–1126; 09 l.340–350, l.966 | The factory reset is a second copy of the restore swap: a parallel `reset/` slot, journal, set-aside directory, token pair, override, sentinel and precedence rule. The copy has already drifted from the original in four places, and one "mutual exclusion" claim is false from S2b to S7. |
| SIMP8-2 | MAJOR | 09 §9.3 steps 1 and 3 (l.843–878); 05-storage l.1953–1956; 05-metadata l.866–873; 06 l.1047–1058; R7-6 | `staging-scrub` cannot run as ordered. A staged archive restore puts nothing at the DB path before the first boot, and `app.OpenOffline` refuses while that restore is pending, or while the scrubbing binary has steps the archive lacks. Clearing native URLs under `c/integrations/` is a core-keyspace write that an ABO subcommand cannot make (R8), and nothing needs it. |
| SIMP8-3 | MINOR | 03 l.394–402; 05-metadata l.77; 05-authn l.664, l.1408; 05-rbac l.471; 05-config l.386, l.507; 05-notify l.193; 05-tasks l.1311–1313; 06 l.1556, l.1569; 05-storage l.1385, l.1736–1737 | The set of capabilities the kernel asserts is stated three ways (two, three, and a fourth called by "the kernel's staging code"). The owner of routed stores says "two", although the kernel asserts `Covering` on exactly those stores. |
| SIMP8-4 | MINOR | 05-storage l.1560–1573, l.1716, l.1736; 06 l.727–732, l.1405, l.1516–1533; 05-backup l.34–37 | `legacy.Maintenance` is wider and lives longer than its callers. `UpgradeFloors()` returns a value books already declares in its own manifest. `StageFactoryReset` has no caller from S4 r2 until it is deleted at S8. |
| SIMP8-5 | MINOR | 05-library l.121, l.278–284, l.299–303, l.627, l.773, l.854; 05-storage l.1433 | `RootSpec.ReadShares` has no consumer in S0–S10 on any root. SIMP7-2's fix adds a sentinel, two refusals, a test and a suite case to stop a legacy root taking shares that no app can grant. |
| SIMP8-6 | MINOR | 05-config l.161–179, l.334–338; 05-authn l.1533; 05-storage l.1420–1438 | `config.NarrowStored` and `Store.NarrowStored` serve only an older build's legacy blob field, but they are missing from the one "stays in SDK" list. On `core.auth.mode` the same predicate is declared twice. |
| SIMP8-7 | MINOR | 05-config l.541–552, l.1622–1630; 05-storage l.1401, l.1575–1597, l.1742; 06 l.1535–1550; 14 Q-K | The post-reset hook's carrier has a redundant field (`ResetUnfinished` ≡ `Reset != nil`) and a name that breaks the naming rule (`ResetRun`). It also has a lifetime tied to the legacy *config* store, while two of its three side effects are not about config. |
| SIMP8-8 | MINOR | 03 l.286; 05-backup l.31, l.516–519; 05-storage l.1749–1752 | The swap journal's record types have two public spellings: `restorejournal.Record` and the `backup/journal` aliases `JournalRecord` and `SidecarIdentity`. |
| SIMP8-9 | MINOR | 06 l.1151; 05-storage l.1953–1966; 06 l.727–732, l.1058 | `OpenOffline`: 06 says it runs "the checks of P−1 to P3", but P−1 is `updater.Boot`. The offline handles also carry `Maintenance`, whose `StageFactoryReset` "requests a graceful restart" of a process that is not serving. |
| SIMP8-10 | MINOR | 09 §9.3 step 5 (l.880–883); 09 S8 Done-when (l.1106) | The updater check offers two fake-server designs. One is a staging-only build with a compiled-in test key, so the S8 done-check would exercise a binary that is never shipped. |

---

## Findings

### SIMP8-1 — MAJOR — The factory reset duplicates the restore swap, and the copy has already drifted

**Where:**

- 05-storage §5S.3.15: "Stage" l.1737, "Set aside" l.1738, "Precedence" l.1739, "Rollback tooling" l.1743, "Undo"
  l.1745.
- 05-backup: step 1 at l.616–618, the precedence paragraph at l.685–689, step a at l.693, and the "same journaled
  swap" paragraph at l.703.
- `backup` sentinels at 05-backup l.363–364, and the keys `core.backup.restore.keep_previous` and
  `core.backup.restore.intent_ttl` at l.1003–1004.
- `CheckReader` and the probe at 05-storage l.1100–1126. 09 l.340–350 and l.966.

**Evidence.** R7-2 made the reset "a staged, restart-bound swap, like a restore", run by the same
`core/internal/restoreswap` with the same `storage/restorejournal` record type. Instead of one more swap kind, it
added a parallel set of everything around the swap:

- `reset/pending.json` beside `restore/pending.json`;
- `reset/<id>/journal.json` and `reset/<id>/inflight.json` beside their `restore/` counterparts;
- `<db parent>/.mediacore-reset/<id>/previous/` beside `.mediacore-restore/<id>/previous/`;
- the tokens `reset_pending`/`reset_inflight` beside `restore_pending`/`restore_inflight`;
- `ROLLBACK_IGNORE_RESET` beside `ROLLBACK_IGNORE_RESTORE`;
- `ErrResetPending` beside `ErrRestorePending`;
- a five-glob sudoers probe;
- a cross-check in both staging paths;
- a P0a precedence rule and `TestRestoreAndResetBothStaged`.

The restore side already has the kinds pattern (`KindRevert`, `KindCheckpoint`, 05-backup l.705–707). The parallel
copy has drifted from the original in four places:

1. **Open-stamp rejection exists for resets only.** A reset intent is rejected when the DB directory's highest
   `OPTIONS-<n>` moved since staging (05-storage l.1738, SEC7-5). A restore intent is rejected only on `expires_at` or
   `staged_by_build` (05-backup step a, l.693). So the case SEC7-5 closed for resets stays open for restores:
   1. a staged restore;
   2. `make rollback` with `ROLLBACK_IGNORE_RESTORE=1`;
   3. `prev` serves and writes;
   4. a roll-forward to the staging build;
   5. the restore applies "by surprise" and discards `prev`'s writes.
2. **The mutual-exclusion claim is false from S2b to S7.** 05-backup l.617–618: "Both staging paths check the other's
   files under one kernel staging mutex, so one process never leaves both intents." Until S8, the restore staging
   path is ABO's own `internal/backup` (05-backup l.33–37). It receives only `legacy.Maintenance`, which exposes
   `UpgradeFloors` and `StageFactoryReset` and no mutex (05-storage l.1570–1573). `<app> restore --archive` (l.614)
   runs in another process in any case. So two requests can both pass their checks in normal operation, not only "a
   crash between two processes or a hand-made file" (05-storage l.1739). The precedence rule then silently rejects a
   restore that was already answered `202`.
3. **The reset borrows `backup` members that do not exist until S8.** It is core code from S2b, in L1. Even so:
   - its "Stage" step returns `ErrRestorePending`, which is defined in the L4 `backup` package (05-backup l.363),
     and `backup` lands at S8 (04 l.887);
   - its `previous/` "is kept for `core.backup.restore.keep_previous` … and then swept" (l.1745), but `backup_*`
     becomes `core.backup.*` only at S8 (09 l.1103);
   - its `expires_at` has no stated TTL source (restores use `core.backup.restore.intent_ttl`, also S8);
   - no file names any sweeper for either `previous/` directory. Each is a full copy of the store, and this is new
     disk use: HEAD extracts in place and keeps no copy.
4. **The overrides differ.** The `deploy` recipe's restore override is `DEPLOY_DISCARD_RESTORE=1` (05-backup l.667, 09
   l.966), but `make rollback` uses `ROLLBACK_IGNORE_RESTORE=1` (05-storage l.1124). The reset has
   `ROLLBACK_IGNORE_RESET=1` for both tools (05-storage l.1743). That makes three spellings for two decisions.

**Fix (recommended).** Make the factory reset a fourth kind of the one swap, `KindReset`. Every R7-2 property is
kept: staging only, open stamp, the rollback refusal, mutual exclusion and the hook after `applied`.

- **One slot.** The intent is `restore/pending.json` with `kind: "reset"`, created atomically (temp file plus
  `link(2)`, failing with `EEXIST`, then remove the temp). It needs no mutex and works across processes. A second
  staging attempt of either kind fails with one sentinel, `restoreswap`'s `storage.ErrSwapPending`, defined in
  `storage` (L1) so that both S2b paths can return it. Delete `backup.ErrResetPending`, and keep
  `backup.ErrRestorePending` as an alias of the new sentinel.
- **One rejection rule for every kind (05-backup step a).** Reject on `expires_at`, on `staged_by_build`, or when the
  live directory's open stamp moved since staging. The restore intent gains `pebble_options`. This closes drift 1.
- **One journal and one set-aside directory.** `restore/<id>/journal.json` with the reset's step names, and
  `<db parent>/.mediacore-restore/<id>/previous/`. A `KindReset` swap's in-flight file is
  `restore/<id>/inflight.json`, deleted at `hook_done` instead of `applied`.
- **One token pair and one override.** `restore_pending` and `restore_inflight` (the message names the kind),
  `ROLLBACK_IGNORE_RESTORE`, and a three-glob probe. The `deploy` recipe uses the same variable, and
  `DEPLOY_DISCARD_RESTORE` is deleted.
- **Delete the precedence rule** and `TestRestoreAndResetBothStaged`. Replace that test with
  `TestSwapSlotExclusive`: two concurrent stagers, one wins, the other gets `ErrSwapPending`, across processes.
- **TTL and retention.** Give the swap's TTL and `previous/` retention to `restoreswap` as two core keys that land at
  S2b (`core.storage.swap.intent_ttl`, `core.storage.swap.keep_previous`), and have backup reuse them. Name the
  sweeper: P4d sweeps expired `previous/` directories after a boot reaches readiness.

**Fix (minimum, if the lead keeps two slots).**

- Add the open-stamp rule to restore step a.
- Make the staging exclusion atomic across processes (both stagers create a shared lock file `swap.lock` with
  `O_EXCL`), and delete the "one kernel staging mutex" claim.
- Move both sentinels into `storage`.
- Give the reset its own S2b TTL and retention keys and a named sweeper.
- Use one deploy override per kind.

**Trade:** The recommended fix touches the text of 05-storage, 05-backup, 05-updater, 06 P0a, 09 §9.2.2 and 14. It
deletes more than it adds (one directory tree, one token pair, one override, one sentinel, one rule and one test).

### SIMP8-2 — MAJOR — `staging-scrub` cannot run in the order §9.3 gives, and its native-URL clause is not implementable

**Where:**

- 09 §9.3 step 1 (l.843–847: "Accepts only an archive restore (05-backup's staged restore …)").
- Step 3 (l.855–878: "Scrubs the copy offline, before its first boot … from S2b it opens the store through
  `app.OpenOffline`"), and (c) at l.869–873 ("from S7 the URL fields of native instances in
  `c/integrations/instances/`").
- 05-storage l.1953–1958 (OpenOffline step 1: "refuses while `CheckReader`'s restore or reset tokens would refuse";
  step 2: `ErrOfflinePendingSteps`).
- 06 l.1051–1060 (`Offline.Storage(id module.ID) (module.StorageHandles, error)`).
- 05-metadata l.866–873.
- R7-6.

**Evidence:**

1. **Order.** From S1 an archive restore is staged: steps 1–9 write `restore/pending.json`, and P0a of the **next
   boot** swaps the archive into place (05-backup l.614, l.691–699; 09 S1 l.965). Before that first boot, the DB path
   of the copy holds nothing from the archive. The extracted store sits in `.mediacore-restore/<id>/staging/`.
   - A scrub "before its first boot" therefore has nothing at `--db` to scrub.
   - From S2b, `app.OpenOffline` on that host refuses with `restore_pending`, by its own step 1.
   - Scrubbing after the first boot means that boot ran unscrubbed, with its schedules started. The firewall makes
     it egress-limited, but the plaintext secrets and `sess:` bearer rows were live on it, which is exactly what R7-6
     orders the scrub to prevent.
2. **Binary.** §9.3 never says which binary runs `staging-scrub`. Done-checks test `cur` against a production
   archive written by `prev`.
   - From S2b, a `cur` that registers any versioned step the archive has not applied is refused with
     `ErrOfflinePendingSteps` (05-storage l.1957–1958). That covers every stage that adds a core owner or a books
     step.
   - A `prev` scrub works, but the procedure has to say so.
3. **Native rows.** Clause (c) has ABO's subcommand write URL fields in `c/integrations/instances/`, which is core's
   keyspace.
   - `Offline.Storage(id)` hands out module handles only, and R8 refuses every read or write outside `m/<ID>/` and
     the module's claims (06 l.752).
   - So the clause cannot be written as specified.
   - It is also unneeded. Only an S7-or-later binary reads native instance rows, and every staging boot of such a
     binary sets `core.integrations.disabled=true` in the operator layer (05-metadata l.870–871). That value locks
     the key, and `providerhttp` refuses every `Acquire` before dialling.
   - The firewall drops egress for the host's life (step 2).
   - The `prev` that 05-metadata l.872–873 worries about (S6 during S7's rollout) cannot read native rows at all.

**Fix.** Rewrite steps 1 and 3 of §9.3:

- **Step 1:** "Restores the archive by extraction, not by staging. `provision.sh` verifies the archive's manifest
  (`manifest_sha256`, entry sizes, the 05-backup step 2/b checks) and extracts it directly into the copy's DB
  directory. A disposable host gains nothing from the staged swap's `previous/` and swap-back, and a staged intent
  would leave nothing at the DB path to scrub before the first boot. A `config.yaml` beside the copy refuses (as
  now)."
- **Step 3:** "`staging-scrub` runs with **the production binary that wrote the archive** (`prev`), which has no
  pending step on it, through `app.OpenOffline` from S2b."
- **Clause (c):** delete "and from S7 the URL fields of native instances in `c/integrations/instances/`", and in
  05-metadata l.867–869 delete "(native rows under `c/integrations/instances/` too, …)". Keep the operator-layer
  switch and the firewall as the controls for native instances, and say in one sentence why they are sufficient
  (only S7+ binaries read native rows, and every such staging boot carries the switch).
- **Amend R7-6's wording** ("every legacy and native integration URL … is cleared") to "every legacy integration
  URL", with this finding as the reason.
- **Alternative,** if the lead wants native rows cleared anyway: make it a core offline command (`<app> integrations
  scrub-urls`) on `kernel.OpenOffline`'s core stores, not an ABO write.

**Trade:** `provision.sh` gains about 20 lines of archive verification, and in exchange the procedure becomes
executable. One redundant mechanism is removed.

### SIMP8-3 — MINOR — The kernel's capability assertions are listed three ways

**Where and evidence:**

- 03 l.394–402: "The kernel asserts only **three** capabilities: … `ConfigOpener` and `ConfigSecrets`; and
  `legacy.Covering` … (the router still asserts it through its own mirror)".
- 05-authn l.664 and l.1408, 05-rbac l.471, 05-config l.386 and l.507, 05-metadata l.77, 05-notify l.193, 05-tasks
  l.1311–1313, and 06 l.1569 ("only what it uses itself: the **two** capabilities it drives"). 06 later adds
  `Covering` in the same bullet.
- **05-metadata, the owner of two of the three routed stores, says "the kernel asserts only the two capabilities it
  drives"**, but the kernel asserts `Covering` on exactly those stores' legacy halves (COR7-4).
- 06 l.1556 and 05-config l.555–557 add a fourth capability that the kernel calls: `legacy.ConfigResetArgs`, "called
  by the kernel's staging code inside `StageFactoryReset`".
- 05-storage l.1736 puts `StageFactoryReset` on `storage.Admin` (L1), with the signature `(ctx, requestedBy)`. L1
  cannot name or call an L5 `legacy` capability, and the signature has no way to receive its result.

**Fix:**

- 03 §3.3 keeps the one list, which is the kernel's four: `ConfigOpener`, `ConfigSecrets`, `ConfigResetArgs`, and
  `Covering` on a routed store's legacy half.
- Every owner file replaces its count with "the kernel asserts only the capabilities 03 §3.3 lists".
- 05-storage: `storage.Admin.StageFactoryReset(ctx, requestedBy string, hookArgs []byte)`. The kernel calls
  `ResetArgs` and passes the bytes in, so storage never names `legacy`.

**Trade:** None.

### SIMP8-4 — MINOR — `legacy.Maintenance` is wider and lives longer than its callers

**Where:**

- 05-storage l.1560–1573 and 06 l.1516–1533 (`UpgradeFloors()`, `StageFactoryReset`; "deleted in the S8 PR").
- 06 l.727–732 (`module.StorageHandles.Maintenance`) and l.1405.
- 05-backup l.34–37.

**Evidence:**

- `UpgradeFloors()` returns "the build's declared upgrade floors". During the extraction the only floor is "books'
  `Version` floor (SIMP4-4)" (05-storage l.1716), which books declares in its own `Manifest.Storage.UpgradeFloor`
  (l.584). The only caller is ABO's own `internal/backup`, which already holds that value.
- `StageFactoryReset`'s only caller is books' gin handler from S2b until S4 r2. After that the legacy path is the
  core route's `Alias` (05-storage l.1736), so from S4 r2 until the S8 PR the method has no caller.
- These two methods put an SDK field (`StorageHandles.Maintenance`) typed by an L5 type on every module's handles for
  six stages.

**Fix:**

- Delete `UpgradeFloors()`. ABO's `internal/backup` reads books' manifest `Storage.UpgradeFloor` directly (05-backup
  step 4: "the floors books declares").
- `legacy.Maintenance` keeps only `StageFactoryReset`, and it and `module.StorageHandles.Maintenance` are deleted in
  the **S4 r2 PR** with the gin handler.
- Update 05-storage l.1400 and l.1736, 06 l.727–732, l.1405 and l.1605, 04 l.887, and 09 S8 "Moves".

**Trade:** None. Both deletions happen before v1.

### SIMP8-5 — MINOR — `ReadShares` has no S0–S10 consumer, and SIMP7-2's fix guards it with new machinery

**Where:**

- 05-library l.121 (`ReadShares []storage.Owner // config-entry roots only`), l.627 ("Read: owner + `ReadShares`").
- The SIMP7-2 additions:
  - `ErrLegacyRootShares` (l.278–284);
  - the refusal in both library and the source (l.299–303, l.326–331);
  - `TestLegacyRootRefusesReadShares` (l.773);
  - a `legacytest.LibraryRootSourceSuite` case (l.350);
  - a row in the stays-in-SDK table (05-storage l.1433);
  - "lifting `ErrLegacyRootShares`" in the deferred table (l.854).

**Evidence:**

- A share names a second module that may read a root. ABO ships one module throughout S0–S10 (03 R3: "for ABO there
  is one module").
- The TV and Movies proofs are separate single-module apps, and multi-app in one binary is lowest priority (D9).
- `examples/hello` reads no one else's roots.
- So `ReadShares` is unused on **every** root, config-entry roots included.
- The fix answered "a legacy root must not take shares" with an exported sentinel, two refusal sites, a test and a
  suite case. Each of them refuses a value that no S0–S10 app has a reason to set.
- This is SIMP7-2's own class (03 §3.4: no member whose only consumer is deferred), one level up.

**Fix:**

- Move `library.RootSpec.ReadShares` to the §5L.13 deferred table, with the land-with condition "the first app with
  two modules that shares a root".
- Under "also deferred", list the read clause "+ `ReadShares`", the `core.library.roots` schema field, and the root
  suspension members already there.
- Delete `ErrLegacyRootShares`, both refusals, `TestLegacyRootRefusesReadShares`, the suite case and the 05-storage
  l.1433 row.
- 05-library l.627 becomes "Read: owner".

**Trade:** A future multi-module app adds the field and its legacy refusal additively, together.

### SIMP8-6 — MINOR — `NarrowStored` is missing from the one "stays in SDK" list, and its predicate is declared twice

**Where:**

- 05-config l.170–179 (`config.NarrowStored`, a `SectionOption`) and l.334–338 (`(*Store).NarrowStored`).
- l.161–167 (`SafeLegacyValue`).
- 05-authn l.1533: `core.auth.mode` declares both, `SafeLegacyValue(…, value == "required")` and
  `NarrowStored(…, narrow = value == "required", …)`.
- 05-storage l.1420–1438 (the one list).

**Evidence:**

- `NarrowStored` "applies only to a key with a legacy blob name (`Alias.Legacy`), the field an older build reads". It
  writes through the legacy config store's committer. It is a legacy-serving SDK member of frozen `config`, and it
  is inert once no key has a legacy blob field (after the settings cutover).
- It cannot move to `legacy`, because the section owners (auth, httpapi) declare it and they may not import `legacy`.
- So it belongs in 05-storage's table, which SIMP7-3 made the one list. It is not there, and neither is the
  `Store.NarrowStored` method the kernel calls.
- On `core.auth.mode`, the same predicate is written twice, once per option.

**Fix:**

- Add a row to 05-storage's table: "`config.NarrowStored`, `config.Store.NarrowStored` | lower-layer type: core
  section owners declare it | inert after the settings cutover (no key keeps a legacy blob field)".
- In 05-config l.170–179, let `narrow` be nil to mean "use the key's `SafeLegacyValue` predicate". `core.auth.mode`
  then declares `NarrowStored("core.auth.mode", nil, nil)`, and the basic gate keeps its explicit predicate.

**Trade:** None.

### SIMP8-7 — MINOR — The post-reset hook's carrier: a redundant field, a name off the rule, and the wrong lifetime

**Where:**

- 05-config l.541–552: `ConfigOpenEnv.ResetUnfinished bool` and `ConfigOpenEnv.Reset *ResetRun`.
- 05-storage l.1401, l.1575–1597, l.1742; 06 l.1535–1550.
- 05-config cutover paragraph l.1622–1630.
- 14 Q-K.

**Evidence:**

1. **Redundant field.** `ResetUnfinished` is "true from the reset boot … until the app's post-reset hook has
   completed". `Reset` is set "with ResetUnfinished, on the reset boot … and on every later boot whose reset journal
   reads applied without hook_done". These are the same condition, so `ResetUnfinished == (Reset != nil)` always.
2. **Name.** `ResetRun` is handed only through the config capability `ConfigOpenEnv`, so it is owner-specific. It is
   not in the cross-owner list (03 l.380–382), so the rule makes it `legacy.ConfigResetRun`.
3. **Lifetime.**
   - The carrier lives in `legacy` and is deleted with the legacy config store at the settings cutover (05-config
     l.1624).
   - Only one of the hook's three side effects is a config concern: the gap-fill file, which goes away with the
     legacy config store.
   - The other two, the Open Library data and the library `RootDir` wipe, are books behaviour that Q-K recommends
     keeping permanently.
   - After the settings cutover they would silently stop, and neither the cutover paragraph nor Q-K says so.

**Fix:**

- Delete `ResetUnfinished`. The books store tests `env.Reset != nil` (05-config l.541–544, l.1584; 06 l.2946).
- Rename `ResetRun` to `ConfigResetRun` in 05-storage, 05-config and 06.
- Add to 05-config's cutover paragraph and to Q-K: "the post-reset hook reaches books only through
  `legacy.ConfigOpenEnv`; the settings cutover must give the Open Library and `RootDir` wipes another carrier, or end
  them, and the release note says which".

**Trade:** None for S0–S10.

### SIMP8-8 — MINOR — Two public spellings of the swap journal's record types

**Where:** 03 l.286 (`storage/restorejournal`, public, L0); 05-backup l.31 and l.516–519 (`type JournalRecord =
restorejournal.Record`, `type SidecarIdentity = restorejournal.SidecarIdentity` in `backup/journal`); 05-storage
l.1749–1752.

**Evidence:**

- COR7-10 moved the types down to an L0 leaf so that L1 readers need not import L4. The aliases in `backup/journal`
  are then a second exported name for the same frozen type.
- They also sit in the package of the revocation journal, which is a different journal, so `journal.JournalRecord`
  reads as a revocation record.
- Nothing needs the alias: `backup` (L4) can import `storage/restorejournal` (L0) directly.

**Fix:** Delete the two aliases. 05-backup refers to `restorejournal.Record` and `restorejournal.SidecarIdentity`.
03 l.286 drops "which re-exports them as aliases".

**Trade:** None.

### SIMP8-9 — MINOR — `OpenOffline`: one wrong phase range, and a handle that should be nil offline

**Where:** 06 l.1151 ("It runs the checks of P−1 to P3 of §6.5.1 without acting on them"); 06 l.1674 (P−1 is
`updater.Boot`); 05-storage l.1953–1956 (storage steps 1–4 plus `CheckReader`'s swap tokens); 06 l.1058
(`Offline.Storage(id) (module.StorageHandles, error)`) and l.727–732 (`StorageHandles.Maintenance`).

**Evidence:**

- `OpenOffline` is otherwise the smallest correct design: one open, a promotion batch at a clean close, and no
  migrations.
- The two texts disagree on what it checks. "P−1" is the updater's rollback decision, which has no meaning in an
  offline session; 05-storage correctly lists only storage's checks and the swap tokens.
- `Offline.Storage` returns full `StorageHandles`, which include `Maintenance` for the module that declares claims.
  That handle's `StageFactoryReset` writes an intent and "requests a graceful restart" (05-storage l.1737). In an
  offline session there is no serve process to restart, and the CLI exits with a reset staged behind it.

**Fix:**

- 06 l.1151: "It runs the checks of P2 and P3 of §6.5.1 (storage's steps 1–4, 05-storage §5S.4) and refuses on
  `CheckReader`'s swap tokens, without acting on them."
- 06 l.1058: "`Maintenance` is nil in an offline session." This becomes moot if SIMP8-4 deletes the handle at S4 r2.

**Trade:** None.

### SIMP8-10 — MINOR — The S8 updater check has two fake-server designs, one of them a staging-only build

**Where:** 09 §9.3 step 5 (l.880–883: "signing with a test key compiled into a staging build, **or** re-serving the
newest signed release with the real key"); 09 S8 Done-when (l.1106: "applies the fake signed release and `Boot`
confirms it").

**Evidence:**

- The first alternative needs a second build variant of the S8 binary, with a test public key compiled in. The S8
  done-check would then measure a binary that is never shipped, and the variant would need its own build target.
- The second alternative alone has nothing to apply when the staging host already runs the newest release.
- The spec leaves the choice open, so the done-check's pass condition is undefined.

**Fix:** Pick one in step 5: "staging runs `prev` (the newest published signed release). The fake server re-serves
the S8 release candidate signed by the real release pipeline, so the shipped verification code and key are what
apply it." Delete the test-key alternative. 05-updater's own unit test keeps its fake key.

**Trade:** None. It removes a build variant.
