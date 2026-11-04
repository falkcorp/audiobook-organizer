<!-- file: docs/plans/platform-core/reviews/spec-round-4-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c42cbf7-c23e-4828-923d-8d45bf5f2168 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4: simplicity and scope (SIMP4)

Lens: over-engineering, YAGNI, duplicated mechanisms, scope creep. Four jobs this round:

1. Regression check of every round-3 SIMP finding (BLOCKER/MAJOR in full, MINORs briefly).
2. Remnants of the removed dual-format machinery (R3-1) anywhere in the spec.
3. The "one X" resolutions (round-1 cross-cutting 4, R2-7, R2-8) and the deferred-build boundary
   (03 §3.4: no stage S0–S10 builds a package **or member** whose only consumer is deferred).
4. New complexity the R3-1 adapter/grant design introduced, where a simpler design meets every goal.
   Such a finding names the trade.

R3-1 itself (one stored format per family, no adoption, cutover out of the extraction) is not
attacked. Findings 2 and 3 are about how it is implemented, not whether.

Line numbers are from the files at this review (03 v1.6.1). Code anchors are at `748fd787a`.

**Counts:** 0 BLOCKER, 6 MAJOR, 6 MINOR.

## Regression check (round-3 SIMP findings)

| Round 3 | Status | Notes |
|---|---|---|
| SIMP3-1 reflector (MAJOR) | **Fixed** | 03 R4, 05-http-api lines 83–90 and 136, and 05-config lines 867–884 all say `jsonschemax.Base().With(config.Overrides()).With(httpapi's)`. `time.Time` is only in `Base()`. `TestSchema_OverrideSetsDisjoint` and `TestOpenAPI_UsesConfigOverrides` are present. |
| SIMP3-2 deferral grep (MAJOR) | **Half-fixed** | The test exists, but its list source and lifetime are defined four different ways, and one prose grep is still there. See SIMP4-1. |
| SIMP3-3 compat ledger (MAJOR) | **Fixed** | `app.Options.CompatLedger` appears in 03, 04, 05-config, 05-http-api, 05-metrics, 06 and 09. A new inconsistency in what the core fragment holds is SIMP4-10. |
| SIMP3-4 download `Add`/`Remove` (MAJOR) | **Fixed in 05-download** | Lines 42–53: phase-1 `Client` is `Caps`/`Status`/`Items`, and `Adder`/`Remover` are phase 2. The deferral test does not cover these members (SIMP4-1). |
| SIMP3-5 `Deps.Notify` at S6 | Fixed | 06 line 1142; 05-events line 304. |
| SIMP3-6 S6/S7/S8 overlap | Fixed | 09 line 523. |
| SIMP3-7 `max_delivery_age` | Fixed | No occurrence left. |
| SIMP3-8 idempotency stores | Fixed | 05-events history line 726; 05-tasks lines 816 and 1491. |
| SIMP3-9 schedule-key residue | Fixed | 03 §3.6 example is `modules.books.scan.workers`; 10 line 845; 05-tasks line 717. Every `ScheduleFromConfig("…")` literal still matches R2-7. |
| SIMP3-10 `internal/plugin` | Fixed | Deleted at S7 in both 04 (lines 457, 851) and 09 S10 (line 751). |
| SIMP3-11 deferral residue | Fixed | `ProblemFacade*` are only a comment (06 lines 1015–1016); `InstanceConfig.Tags` is deferred (05-metadata line 448); README line 47. |
| SIMP3-12 stale history | Fixed | 09 line 1045, 08 line 621 and 05-rbac line 1208 carry superseded markers. |

**"One X" items re-checked and holding:** one topic catalog, one schedule mechanism and key rule
(R2-7), one compat window, one reflector, one entity reference, one keyring, one boot-phase table
(R2-8), and one store guard. One instance store holds in substance, though 03's wording is stale
(SIMP4-12).

**Dual-format remnants scanned.** I searched for Teach, mirror, adopt, side record, hand-over,
`ClaimMigrating`, `migrated_to` and floor build, outside the review-history sections. The prose is
clean: every hit is either a "does not exist (R3-1)" statement or an unrelated use of the word. The
remnants that are left sit in **mechanisms**, not prose: SIMP4-4, SIMP4-6 and SIMP4-9.

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP4-1 | MAJOR | 03 §3.4 l.314–319; 06 §6.4.9 l.1119–1125; 05-authn l.527–530; 05-http-api l.568–572; 08 l.38; 09 l.594; 05-metadata l.451–452 | SIMP3-2 half-fixed. `DeferredMembersAbsent` has four different source lists and two lifetimes. Deleting it "with `arrfacade`" leaves the download, AI and notify deferred members unguarded, and 05-metadata keeps its own S7 grep. |
| SIMP4-2 | MAJOR | 05-storage §5S.3.11, §5S.9.1; 05-authn §3.4, §11; 05-tasks §5T.6; 04 l.52–58, l.571; 05-config l.374–376; 05-logging l.351–353 | Legacy-format adapters come in three patterns. The core-resident ones put ABO's key names and row shapes into core, against the spec's own rule "core holds no audiobook key name". App-supplied store implementations meet every goal without the grant machinery. |
| SIMP4-3 | MAJOR | 05-storage l.1207 "Adapter selection"; 05-authn §3.4 l.449–470; 05-tasks §5T.6; 05-rbac l.835; 06 l.1150–1153 | Apps without a grant "use the native keyspace", but no native user, API-key, role or run store is specified. hello needs them from S4/S5 (`rbac.Need`, `hello.count`). |
| SIMP4-4 | MAJOR | 05-storage l.631–639, 652, 663–666, 723, 925; 09 S10 l.751–754 | Remnants: `Served`/`RequiresServed` with per-level served runs, and `UpgradeFloor.Requires` with the sidecar `done` lines. Their consumers were the removed contracts. Now only the deferred cutover uses them, yet they are built from S0/S2b. |
| SIMP4-5 | MAJOR | 05-backup B3 l.43, §5B.3.3 l.500–520, l.852, l.932, l.982–986; 09 l.728, l.1002 | Backup encryption is built at S8 but can never become active during the extraction: the legacy restore bound `{core, 0}` is lifted only by the deferred cutover. This is a built member whose only consumer is deferred. |
| SIMP4-6 | MAJOR | 05-rbac §4.8 l.548–590, l.219–230, 275, 526, 1051, 1061 | The seed-merge diff (`Added`/`Removed`/`Tombstone`/reset) cannot be reached while seed roles are locked. `PendingSeed` is a dead end because `accept-seed` is locked too, although accepting a seed addition is rollback-safe. |
| SIMP4-7 | MINOR | 09 §9.2.3 stage table l.288–297 vs 05-storage §5S.9.1 rows 12, 19; 05-tasks l.1179 | The grant list is stated twice and the copies disagree: 09 omits `core/ai`, `core/providerhttp` and the S5 v1 read grant, and `opstate:` is in the grant in 05-storage but "not touched" in 05-tasks. |
| SIMP4-8 | MINOR | 05-storage l.1202 vs 06 l.1009, l.1381; 05-logging l.351 | A grant to an absent grantee is "inert, logged" in one file and fails boot with `ProblemGrant` in another. |
| SIMP4-9 | MINOR | 05-storage l.1206 vs l.1209; 05-library l.663–666; 05-tasks l.1135; 05-metadata l.571 | "No side records keyed by legacy IDs", yet five extension families are keyed by legacy IDs, each with its own divergence rule. |
| SIMP4-10 | MINOR | 03 l.265, 09 l.440–441 vs 05-config l.570–574 vs 05-metrics l.130–133 | Three descriptions of what `core/compat/core-ledger.md` holds. |
| SIMP4-11 | MINOR | 05-config l.383–385; 06 l.795–808, l.1137 | `config.Legacy.Rows` is used only at S2, but stays in the public SDK type "unused" from S2b on. |
| SIMP4-12 | MINOR | 03 §3.3 `integrations` row l.266 vs 05-metadata l.40–50 | 03 still calls `integrations` "the one store for integration instances". During the extraction ABO's instances are legacy-backed in the config blob. |

---

## Findings

### SIMP4-1 — MAJOR — SIMP3-2 half-fixed: one test, four lists, two lifetimes, and one leftover grep

**Where:** 03 §3.4 (lines 314–319); 06 §6.4.9 (lines 1119–1125); 05-authn §3.5 (lines 527–530);
05-http-api §3.6 (lines 568–572); 08 §8.0 (line 38); 09 (line 594); 05-metadata (lines 451–452).

**Evidence:**
- **The list sources disagree.**
  - 03: "Each 05 file lists its deferred members under a 'Deferred-build' heading … its list is taken
    from those headings".
  - 06: "generated from the 'Deferred-build' headings of 06, 05-authn, 05-http-api, 05-rbac and
    05-metadata".
  - 05-authn: "this file, 05-http-api §3.6 and 08".
  - 05-http-api: "this file, 05-authn §3.5 and 08".
  - None of the lists includes 05-download, 05-ai or 05-notify. Each of those built packages has
    deferred members: SIMP3-4's `Adder`, `Remover`, `GrabRequest`, `ResolvedGrab` and `GrabID`
    (05-download line 45); the AI usage ledger, `Router.Usage` and `MonthlyTokenCap` (05-ai line 21);
    and notify's inbox and subscriptions (05-notify line 24). None of these files has a heading named
    "Deferred-build", so a heading-driven generator finds nothing in them.
- **The lifetimes disagree.**
  - 03: "the PR that builds the deferred package (e.g. `arrfacade`) deletes **its entries**".
  - 06 line 1124, 05-authn, 05-http-api and 09 line 594: "the PR that adds `arrfacade` deletes **the
    test**".
  - Under the second rule, the download phase-2, `acquire`, AI-ledger and notify members lose their
    guard as soon as the facade lands.
- **A per-file prose grep survives.** 05-metadata lines 451–452: "The S7 done-check greps for
  `FacadeWriter` under `core/integrations` and must find nothing". That is the kind of per-file grep
  SIMP3-2 replaced.

**Fix:**
1. 03 §3.4 stays the single definition. Every file with deferred members in a built package gets a
   heading exactly named `Deferred-build members`: add one to 05-download §1 (the phase-2 row's
   identifiers), 05-ai §1, 05-notify §1 and 05-storage (SIMP4-4).
2. 06 §6.4.9, 05-authn, 05-http-api, 08 and 09 say "the list is generated from every `Deferred-build
   members` heading under `docs/specs/platform-core/` (03 §3.4)". Drop the per-file source lists.
3. Lifetime: "the PR that builds a deferred member deletes **that member's** entry; the test is deleted
   when the list is empty". Apply this in 06 line 1124, 05-authn line 529, 05-http-api line 571 and
   09 line 594.
4. Replace 05-metadata lines 451–452 with "absent per `archtest.DeferredMembersAbsent` (03 §3.4)".

**Trade:** None. One generator rule replaces five prose descriptions.

### SIMP4-2 — MAJOR — Legacy-format adapters come in three patterns; the core-resident ones put ABO's on-disk formats into core, and a simpler design avoids the grant machinery

**Where:**
- 05-storage §5S.3.11 (lines 1162–1214), §5S.9.1 (lines 1470–1528, esp. line 1524);
- 05-authn §3.4 (lines 449–470), §11 (lines 1163–1196);
- 05-tasks §5T.6 (lines 1102–1127);
- 04 headline 1 (lines 52–58) and §4.5 (line 571);
- 05-config §1.3.3 (lines 374–410);
- 05-logging (lines 351–353);
- 05-metadata M2.10 (lines 733–736, 913);
- 05-library (line 657); 05-ai (lines 492–495);
- 03 §3.4 bucket table (line 342).

**Evidence: three patterns for one job ("a core service uses an ABO family in place").**

1. **The app supplies the format; core gets a grant.**
   - Config: `app.Options.LegacyConfig *config.Legacy`, a `LegacyBlob` codec and row names supplied
     by books. "Core holds none of these names (R1)" (05-config lines 374–376).
   - Log: "A books-supplied `ReadAdapter` decodes the rows … so core holds no ABO row shape (R1)"
     (05-logging lines 352–353).
2. **The app supplies raw row access and no grant is involved.** Config at S2: `LegacyRows`
   (05-config lines 383–385, 408–411).
3. **Core hard-codes ABO's format and gets a grant.**
   - `core/internal/authstore`: "rows are decoded from and encoded to ABO's own JSON shape
     (internal/database/store.go)", over `u:`, `idx:user:username:`, `apikey:` and `sess:`
     (05-authn lines 452–466). It is 3,053 LOC moved from `internal/database` (04 line 571).
   - `core/internal/rbacstore` (05-rbac line 777).
   - `core/internal/executor/legacystore`: "HEAD's own opv2/opv3 storage code … moved", including
     `setting:library_scan_full_last_run`, `op:completion:{type}:{id}:{opType}` and `taskV2DefIDs`
     (05-tasks lines 1112–1127). The 39 methods are re-implemented in core, and "their ABO copies stay
     in the residue until 05-tasks T8 deletes them" (04 lines 55–58), so two copies of the ops-format
     code exist from S5 to T8.
   - `library`'s `import_path:` + `counter:import_path` adapter writing HEAD's `ImportPath` JSON
     (05-library line 657).
   - `ai`'s `aijob:` adapter (05-ai line 493).
   - The kernel-built `providerhttp.LegacyGateStore` over `provider_throttle:` (05-metadata
     lines 733–736).
   - `core/index` over `idx:sidx:dirty:`.

   At HEAD these key names are ABO store code (`internal/database/pebble_store_auth.go:169,173`
   writes `u:` and `idx:user:username:`; `pebble_store_importpaths.go:102` writes `import_path:%d`).

**Pattern 3 contradicts the spec's own rule:**
- 05-storage line 1524: "`keyfamilies.go` stays ABO code: core holds no audiobook key name (R1,
  03 §3.4)".
- 05-config line 162 and 05-metrics line 131 say the same.
- 03's bucket table (line 342) puts "everything audiobook-specific" in the app.

R1's mechanical check passes only because the code is copied rather than imported. S10 keeps the
adapters ("Not deleted: … the legacy-format adapters", 09 line 756) until per-family cutovers, which
are outside the extraction and undated. So at the split (12, which never mentions them) `mediacore`
ships ABO's identity, ops, import-root and AI-job formats to every TV/Movies app.

**The grant machinery exists only because core code touches books' keys:**
- `Grant`, three `GrantMode`s plus `Invalid`, and `Exclusive`.
- Four `ErrGrant*` errors and `ProblemGrant`.
- `storage_grants.golden` with an owner sign-off.
- Claim-ledger `grants`, `mediacore_storage_grant_ops_total`, and the "inert grant" rule.
- A second grant kind, the **legacy-migration grant**, which exists only so books' own legacy steps can
  write ranges its `Exclusive` grants made read-only to books (05-storage lines 554 and 1360).
- Tests: `TestGrantEnforced`, `TestStorageGrants` and `TestGrantsLoggedAndRecorded`.

**The seams for the simpler design already exist:**
- `auth.Store` is a public interface whose every write "takes the caller's `*storage.Batch`"
  (05-authn lines 449–470).
- `rbac.RoleStore` (05-rbac line 249); `config.OverrideStore` (05-config line 814);
  `providerhttp.LegacyGateStore` (05-metadata line 736).
- The auth capability interfaces are "declared in public `auth`" (04 line 557).
- Multi-owner atomic batches by participant binding exist (05-storage D-S10).

**Fix (one pattern):** generalize pattern 1 to all families.

1. 05-storage §5S.3.11 becomes "**App-supplied legacy stores** (R3-1)". A core subsystem that can serve
   pre-core data declares a public store interface in its own package. Most already exist; add
   `tasks.RunStore`, `library.RootSource`, `ai.JobStore` and `index.DirtySet`.
2. Core implements only the **native** store (SIMP4-3).
3. The app passes a legacy implementation in `app.Options.LegacyStores{Auth, Roles, Runs, Roots,
   AIJobs, Gates, Dirty, Config, LogReader}`. Each entry is a constructor
   `func(storage.LegacyRange) X`. The kernel calls it at P4c with **books' own** handle over its own
   claims. For ABO the implementations are the existing `internal/books/store` methods, which stay in
   ABO with their ratchets (`TestOpsV2RowWritesGoThroughStageOpRow`).
4. Core enforces its invariants above the interface: never-empty scopes, upgrade-on-use and the
   staging rules. It ships `authtest.StoreSuite`, `taskstest.RunStoreSuite` and the other suites, and
   ABO's implementations run them with HEAD fixtures (today's `TestGrantedFamilyGolden`).
5. Keep the claim-ledger `backends` record and `ErrGrantMissing` under a new name
   (`ErrLegacyStoreMissing`).
6. Delete grants, `GrantMode`, `Exclusive`, the legacy-migration grant and their errors, metric, golden
   and tests from the extraction. Move them to §5S.3.13 as **deferred-build members of the cutover**,
   the only step that needs core to read books' rows.
7. 04 headline 1: the 108 methods stay in the books residue behind the core interfaces rather than
   moving or being re-implemented. §4.5's `internal/authstore` and `legacystore` rows go away.

**Minimum alternative**, if the owner keeps grants: move every format to the app as config and log
already do (codecs and key names in `app.Options`). That way at least "core holds no audiobook key
name" becomes true, and pattern 2 is folded into pattern 1.

**Trade:**
- Cost (a): the store interfaces become frozen SDK surface. `tasks.RunStore` is wide, and the
  API-stability gate will measure it.
- Cost (b): runtime write exclusivity on ABO's ranges is enforced by package structure (an ABO archtest:
  only the legacy-store package writes those families) instead of by storage.
- Cost (c): the first cutover must re-introduce a read-delete grant.
- Gain: ABO formats never enter `mediacore`; there is no 3,000-plus LOC re-implementation and no
  duplicate ops store from S5 to T8; R8 stays "own keyspace or own claims"; about a dozen storage
  mechanisms are deleted from S0–S10; and the core-only additions (`c/auth/`, `c/tasks/`) still bind
  to the same batch through participant binding.

### SIMP4-3 — MAJOR — "Without a grant it uses its native keyspace", but no native store is specified for users, keys, roles or runs

**Where:** 05-storage line 1207 ("Adapter selection … without a grant it uses its native keyspace (an
app with no pre-core history, 10)"); 05-authn §3.4 lines 449–470 ("Store … has two halves and no third
copy": the legacy adapter and `c/auth/` for sessions, one-time tokens and bootstrap only); 05-tasks
§5T.6 (only the legacy families plus core-only side data; the only native run key is the cutover target
at line 1192); 05-rbac line 835 (`c/rbac/` named only as the cutover target); 06 lines 1150–1153.

**Evidence:**
- hello gains "the route's `rbac.Need(PermRead)` at S4, `hello.count` at S5" (06 lines 1152–1153).
- `TestHelloEndToEnd` (06 line 1403) runs a task under `-race`, and `TestDisabledModuleWithRunsBoots`
  (06 line 1374) seeds `hello.count` runs.
- G7 (`coretest` with zero modules) has no books grant.
- 10 says nothing about where TV/Movies keep users or runs.

An app with no grant therefore has no specified home for users, API keys, roles, task runs, AI jobs or
provider holds. Either an implementer invents a native schema with no spec or review, or core writes
ABO's legacy formats into an app that has no legacy, which would make `u:` and `opv3:` the platform
format. The R3-1 rewrite deleted the native halves together with the mirrors.

**Fix:**
1. Give each grantee file a short "**Native store (apps without a legacy store)**" table: keys under
   `c/<subsystem>/`, `codec.JSON[T](1)` of the core type, and index rows. Cover users, keys and
   identities (05-authn), roles (05-rbac), runs, queues, pages, clocks and stamps (05-tasks), AI jobs
   (05-ai) and holds (05-metadata M2, which already has `c/providerhttp/holds/`).
2. State the stage. Under 03 §3.4 rule (b) hello and `coretest` are core consumers, so each native
   store lands with its subsystem: auth and rbac at S4, tasks at S5.
3. 05-authn §3.4: "three halves: legacy (granted, or app-supplied per SIMP4-2), native, and core-only".
4. Add `TestNativeBackend_RoundTrip` per subsystem.

**Trade:** Real scope: native schemas for four subsystems that ABO never uses in production during the
extraction. The alternative is to defer them and run hello and `coretest` on an in-memory `coretest`
implementation of the same store interface (cheaper, but the native schema is then unreviewed until
TV/Movies). Either way the spec must say which; today it says neither.

### SIMP4-4 — MAJOR — Remnants of the removed contracts: `Served`/`RequiresServed` and `UpgradeFloor.Requires` now serve only the deferred cutover

**Where:**
- 05-storage `Served` (lines 631–639: "09 decides which steps use it (a cutover, §5S.3.13)");
  `Migration.RequiresServed` (line 652); `UpgradeFloor.Requires` (lines 663–666: "data migrations (a
  cutover) … (DATA3-10, OPS3-7)").
- 05-storage "Skipped releases" (line 723); served-ledger per-level runs
  `c/storage/served/<owner>/<level>` (line 925); cutover preconditions (line 1243); history line 1768
  ("served ledger and `RequiresServed` kept").
- 09 S10 (lines 751–754: "S10 sets no `min_upgrade_from`"); 05-tasks line 1188 (books' only floor is
  `UpgradeFloor{Version: 66}`).

**Evidence:**
- `RequiresServed` was added for contracts that had to wait for an uninterrupted run (OPS-2, DATA2-9).
  `UpgradeFloor.Requires` was added for S10's "these contracts have run" floor (DATA3-10, OPS3-7).
  R3-1 removed both consumers.
- No step in S0–S10 sets `RequiresServed` (09 has no current occurrence), and no floor uses
  `Requires`.
- They still cost: a public `storage` type and field; per-level run bookkeeping written by every boot
  from S0 (09 line 202); the deferral and dependant-skip logic; the `<owner> done <id>` sidecar lines;
  `UpgradeRequirement.steps` in 05-updater; and `TestStepClockResetsAfterRollback` (05-storage
  line 1667).
- Under 03 §3.4 these are built members whose only consumer is deferred, the same class as SIMP3-4.
- The served ledger itself still has live consumers: the W recording-build rule (09 lines 116–118),
  `duties` for the journal gap and derived-index generations, and checkpoint consumption. None of
  these reads the per-level runs.

**Fix:**
1. Move `Served`, `Migration.RequiresServed`, `UpgradeFloor.Requires`, the per-level run records, the
   sidecar `done` lines and `UpgradeRequirement.steps` under a `Deferred-build members` heading in
   05-storage §5S.3.13 (they land with the first cutover) and into the SIMP4-1 list.
2. Keep `c/storage/served/<owner>/last` with `duties`, and add a `usage` duty. The W rule then reads
   "every boot in the 14 days listed `usage` and no foreign open was detected" (09 §9.2.1).
3. Strike `RequiresServed` from the "Skipped releases" row and from line 1350.

**Trade:** Re-adding a precondition and a floor field with the first cutover is additive. Nothing an
extraction build does changes.

### SIMP4-5 — MAJOR — Backup encryption is built at S8 but cannot become active during the extraction

**Where:** 05-backup B3 (line 43), §5B.3.3 rule 3 (lines 510–520: "ABO archives stay plaintext for the
whole extraction and until … the first per-family cutover"), `core.backup.encryption` (line 852:
`none` "while the app's legacy restore bound holds (ABO during the extraction)"),
`TestEncryptionInactiveUntilExportAndBound` (line 932), open item 4 (lines 982–986); 09 S8 Backup row
(line 728: "ABO archives stay unencrypted for the whole extraction"), line 1002; 03 §3.4 (lines
312–314).

**Evidence:**
- Books declares `LegacyRestoreBound{core, 0}`, and "no step of the extraction raises the `core` level",
  so encryption is inactive for upgraded **and fresh** ABO stores (line 932) until a deferred cutover.
- The only other activator is "apps without a pre-core history", meaning TV/Movies (deferred). hello is
  not a backup product.
- S8 still builds the age encryption path, operator recipients (`core.backup.recipients`), the archive
  identity derived from the keyring, the encrypted restore path, the legacy-restore-bound mechanism and
  the "never prune the last verified plaintext archive until an encrypted one is verified" rule
  (cross-cutting 8).
- During S0–S10 none of this can run in production.

**Fix:**
1. In 05-backup, mark these as `Deferred-build members`: encryption (B3 activation rules 1–3,
   `LegacyRestoreBound`, `core.backup.encryption`/`recipients`, the age writer and reader, the prune
   rule tied to encryption, and the encryption tests).
2. S8 builds plaintext archives with B5 provenance (HMAC over a keyring-derived key; keep it), the
   catalogue and staged restore.
3. 09 S8: "Encryption lands with the first app or cutover that can activate it (05-backup B3)".
4. Add the identifiers to the SIMP4-1 list.

**Trade:** The encrypted format is not exercised against real data until it can be switched on. It
lands additively, and the user-visible result during the extraction is the same: plaintext archives.

### SIMP4-6 — MAJOR — The seed-merge diff cannot be reached during the extraction, and `PendingSeed` is a dead end

**Where:** 05-rbac §4.8 (lines 548–590); `Role` fields `SeedDigest`, `PendingSeed` (lines 219–230);
`AcceptSeed` (line 275); `POST /roles/{name}/accept-seed` (line 526); `TestSeedRole_EditRefused`
(line 1051: `accept-seed` → 409); `TestSeedMerge_RestrictedAdditionHeldPending` (line 1061); HEAD
`internal/auth/seed.go:80-129`.

**Evidence:**
- "`UpdateRole`, `ResetRole`, `AcceptSeed` and `DeleteRole` on a seed role answer 409
  `seed_role_locked` … until the role family's cutover" (line 581), and the cutover is undated
  (02 N15).
- So during S0–S10 and after it: `Added` and `Removed` are always empty, `Tombstone` cannot occur,
  `reset` has nothing to clear, and the §4.8 table reduces to HEAD's overwrite, which line 569 itself
  calls "the special case diff always empty".
- Worse, "restricted additions wait in `PendingSeed` … until the cutover makes `accept-seed`
  available" (lines 586–587). Any release in the window that adds a `Sensitive`, `AuthRequired` or
  `Confining` scope, or a `*:settings.manage` or `*:integrations.manage`, to an existing seed role can
  never make it effective. HEAD applies it on the next boot (`seed.go:117-127`).
- The lock's rationale (an older build's re-seed would silently **widen** an operator narrowing) does
  not apply to `accept-seed`. Accepting adds a scope the older build does not know, and its re-seed
  (`seed.go:117-127`) overwrites the row with its own seed, which is narrower. Fail-closed.
- `Locked` (admin, `ErrLockedRole`) and `ErrSeedRoleLocked` are two lock notions that mean the same
  thing during the window.

**Fix:**
1. In §4.8, split the rules:
   - **During the extraction:** seed row = seed (HEAD's overwrite, plus the namespaced strings of
     §6.3). Restricted additions go to `PendingSeed`. **`AcceptSeed` is allowed** (rollback-safe, as
     above). `UpdateRole`, `ResetRole` and `DeleteRole` on seed roles return `ErrSeedRoleLocked`.
   - **Deferred-build (lands with the role cutover):** `Added`/`Removed`, `Tombstone`, `ResetRole` and
     `POST /roles/{name}/reset`, with their tests.
2. Change `TestSeedRole_EditRefused` to expect 200 for `accept-seed`.
3. Optionally, fold `ErrLockedRole` into `ErrSeedRoleLocked` until the cutover.

**Trade:** None for safety. SEC-25 is kept and becomes usable. The operator-diff code is postponed to
the only step that can use it.

### SIMP4-7 — MINOR — The grant list is stated twice and the copies disagree

**Where:** 09 §9.2.3 stage table (lines 288–297) vs 05-storage §5S.9.1 (row 12: `core/tasks` Read on
`opstate:`, `op_result:`, `opsummary:`, `operation:` at S5; row 19: `core/ai` and `core/providerhttp`
ReadWrite Excl. at S7); 05-tasks line 1179 (`opstate:` "Not touched by `core/tasks`").

**Evidence:**
- 09 says "05-storage §5S.9.1 is the normative grant list; the stage at which each grant comes into
  force is this file's". But 05-storage already prints a stage on every grant.
- 09's S5 row omits the v1 read grant.
- 09's S7 row omits `core/ai` and `core/providerhttp`.
- 05-storage grants `core/tasks` read access on `opstate:`, which 05-tasks says it never touches.

**Fix:**
1. Delete 09's stage table and say "stages: 05-storage §5S.9.1's Grant column (09 decides; changing a
   stage edits that column)".
2. Drop `opstate:` from row 12's grant, or have 05-tasks say `legacyhist` reads it.
3. If SIMP4-2 is accepted, the table becomes the `LegacyStores` list instead.

**Trade:** None.

### SIMP4-8 — MINOR — Absent grantee: inert or fatal?

**Where:** 05-storage line 1202 ("a grantee absent from the build makes the grant inert, logged") vs 06
line 1009 (`ProblemGrant`: "to an owner absent from the build"), line 1381 (`TestStorageGrants`: "a
grant … to an absent owner … fails P2"), and 05-logging line 351 ("fails boot").

**Fix:** Keep one rule, **fail** (`ProblemGrant`). Books declares only the grants of subsystems that
have landed (`TestBooksClaimsGenerated`), so "inert" serves no case. Delete the "inert" clause in
05-storage.

**Trade:** None.

### SIMP4-9 — MINOR — "No side records keyed by legacy IDs", while five extension families are keyed by legacy IDs

**Where:**
- 05-storage line 1206 (native-only homes include `c/integrations/ext/` and `c/ai/batch/`) vs
  line 1209 ("No subsystem … keeps side records keyed by legacy IDs").
- 05-library lines 663–666 (`c/library/rootext/<root-id>`); 05-tasks line 1135 (`c/tasks/sched/<kind>`
  "beside a settings clock"); 05-ai line 495 (`c/ai/batch/<id>`, HEAD's job ID).
- 05-metadata line 571 (its own divergence rule: "Legacy URL origin differs from the extension
  record's … `integration_local_reset`").

**Evidence:** These are side records in everything but name, and each file invents its own rule for
the case where an older build edits or deletes the legacy row. That per-family edge-case design is the
class R3-1 set out to delete. The extension records themselves are justified: older builds rewrite the
legacy rows and drop unknown members (05-storage line 1206).

**Fix:**
1. Reword line 1209: "… keeps **no copy of a legacy field** in a side record".
2. State one **extension-record rule** in §5S.3.11:
   - keyed by the legacy primary key;
   - never holds a field the legacy row holds;
   - ignored while its legacy row is absent, and deleted on the next write of that key;
   - a field whose meaning depends on a legacy field (e.g. `Local` on the URL) records the value it was
     derived from and is void when that differs.
3. 05-library, 05-tasks, 05-ai and 05-metadata cite the rule instead of their own.

**Trade:** None.

### SIMP4-10 — MINOR — Three descriptions of the core ledger fragment

**Where:** 03 line 265 and 09 lines 440–441 ("core metric and config aliases"); 05-config lines
570–574 ("the permanent `OTEL_*` ecosystem aliases"); 05-metrics lines 130–133 (metric aliases name an
app's families, "so the app supplies them … rows are in the app's ledger").

**Evidence:** Under R1 every metric alias and every core-key alias with an app name is the app's. So
the only rows core can own are permanent ecosystem names (`OTEL_*`, 05-config line 1117).

**Fix:** 03 and 09 say "core's fragment holds only names every app shares (the permanent `OTEL_*`
env aliases, 05-config C7); every window alias is the app's". Add a test that every core-fragment row
has gate `permanent`.

**Trade:** None.

### SIMP4-11 — MINOR — `config.Legacy.Rows` stays in the SDK after S2b as an unused member

**Where:** 05-config lines 383–385 ("From S2b the kernel reads through the storage grant and Rows is
unused"); 06 lines 795–808, line 1137 ("`LegacyConfig.Rows` unused").

**Evidence:** `Options.PreBootRestore`, the same kind of stage bridge, is "Removed at S8" (06 line 815).
`Rows` and the `LegacyRows` interface are kept forever as a frozen, dead SDK member.

**Fix:** "`Legacy.Rows` and `LegacyRows` are deleted in the S2b PR (v0 permits it, 12 §12.4.1)." Apply
this in 05-config §1.3.3 and in 06's S2b row.

**Trade:** None.

### SIMP4-12 — MINOR — 03 still calls `integrations` "the one store for integration instances"

**Where:** 03 §3.3 `integrations` row (line 266) vs 05-metadata lines 40–50 ("every instance has
exactly one stored form: Native … Legacy-backed … ABO's legacy keys, read and written in place through
05-config's legacy-format adapter"); 09 line 278.

**Fix:** In 03 line 266, change "the one store for integration instances" to "the one registry for
integration instances (native ones in `c/integrations/instances/`; during the extraction ABO's existing
instances are legacy-backed in their blob fields, 05-metadata M1)".

**Trade:** None.
