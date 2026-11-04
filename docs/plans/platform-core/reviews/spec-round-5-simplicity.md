<!-- file: docs/plans/platform-core/reviews/spec-round-5-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b764c66-efd6-4d6a-8492-81e60a9d498c -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 5: simplicity and scope (SIMP5)

Lens: over-engineering, YAGNI, duplicated mechanisms, scope creep. This round has four jobs:

1. Regression check of every round-4 SIMP finding (MAJORs in full, MINORs briefly).
2. Check that the store-interface design (R4-1) is **uniform and minimal**, with no third data-access pattern
   left.
3. Find remnants of removed machinery (R3-1's dual format, round 3's grants, SIMP4-4's served runs).
4. Check the deferred-build boundary (03 §3.4): nothing built in S0–S10 may have only a later consumer.

R4-1 itself is not attacked: core defines store interfaces, books supplies the legacy implementations on
`module.Manifest.LegacyStores`, there are no grants during the extraction, and the interfaces are frozen
SDK. The findings below are about how R4-1 is written down, not whether it holds.

Files are at the reviewed revision (03 v1.7.1, 05-storage v1.5.1, 06 v1.5.1, 09 v1.4.x). Code anchors are
at `748fd787a`.

**Counts:** 0 BLOCKER, 6 MAJOR, 8 MINOR.

## Regression check (round-4 SIMP findings)

| Round 4 | Status | Notes |
|---|---|---|
| SIMP4-1 deferred-members list (MAJOR) | **Half-fixed** | One generated list and per-member lifetime are now stated in 03 §3.4, 06 §6.4.9, 05-authn §3.5, 05-http-api §3.6, 08 and 09. 05-download, 05-ai, 05-notify, 05-storage, 05-backup and 05-updater have headings, and the 05-metadata grep is gone. **But the tables cannot be fed to the test as written.** They list built identifiers, entries without a package, and non-identifiers, and most headings are numbered, which does not satisfy "named exactly". See SIMP5-6. |
| SIMP4-2 three adapter patterns (MAJOR) | **Fixed in substance** | No core-resident adapter is left: `legacystore`, `legacyhist`, the grant-based `authstore` and `LegacyGateStore` appear only in history. Grants are deferred (05-storage §5S.3.14). `Manifest.LegacyStores` has one constructor shape, `func(storage.LegacyHandles) (X, error)`. Residues: a fourth carrier for the dirty set (SIMP5-2); a phantom AI store (SIMP5-3); two store-selection models (SIMP5-1); and the S2 bridge on `app.Options`, which contradicts 03's "never through app.Options" (SIMP5-10). |
| SIMP4-3 native stores (MAJOR) | **Fixed** | Every owner file has a "Native store" paragraph and a round-trip test, and 10 §10.1.2 states that TV/Movies use them. Residues: test and suite names drift (SIMP5-9); library's "native store" is config entries, not `c/library/` (SIMP5-1). |
| SIMP4-4 served-run remnants (MAJOR) | **Fixed** | `Served`, `RequiresServed`, `UpgradeFloor.Requires`, the `done` lines and `UpgradeRequirement.Steps` are under 05-storage §5S.3.14 and 05-updater "Deferred-build members". The W rule reads the `usage` duty (09 l.118). One residue is left in the failure table (SIMP5-12). |
| SIMP4-5 backup encryption (MAJOR) | **Fixed** | 05-backup §5B.3.9 and B3; 09 S8 (l.830) builds plaintext archives. The table has entry-format defects (SIMP5-6). |
| SIMP4-6 seed merge (MAJOR) | **Fixed** | `AcceptSeed` is allowed (05-rbac l.294, l.634–642). `TestSeedRole_EditRefused` expects 200 for `accept-seed` (l.1208). The operator diff, `Tombstone`, `ResetRole` and the reset route are in 05-rbac §3.6. That table also lists built methods (SIMP5-6). |
| SIMP4-7 grant list twice (MINOR) | Superseded by R4-1 | The same defect came back for the store-interface list (SIMP5-8). |
| SIMP4-8 inert vs fatal grant | Fixed | 05-storage §5S.3.13: a grant to an absent grantee fails boot. |
| SIMP4-9 extension-record rule | Fixed | One rule in 05-storage §5S.3.11. 05-library l.786, 05-metadata M1.4 step 2a and 05-ai cite it. |
| SIMP4-10 core ledger fragment | Fixed | 03 `compat` row (history l.721). |
| SIMP4-11 `Legacy.Rows` | Fixed | Deleted. `app.Options.LegacyConfigStore` is deleted at S2b (06 l.886–894). The same class of member is now inside `config.OverrideStore` (SIMP5-5). |
| SIMP4-12 `integrations` wording | Fixed | 03 l.274. |

**Remnant scan.** Outside the history sections I searched for: Teach, mirror, adopt, hand-over,
`migrated_to`, `Exclusive`, `GrantReadWrite`, legacy-migration grant, `ErrGrant*`, `ProblemGrant`,
`storage_grants`, `legacystore`, `legacyhist`, `ReadAdapter`, legacy-format adapter, per-level and
`RequiresServed`. Every hit is a "does not exist" statement, a deferred-build table row, or one of three
leftovers: the `ReadAdapter` comment (SIMP5-7), the failure-table row (SIMP5-12) and "books legacy step"
(SIMP5-11).

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP5-1 | MAJOR | 05-storage §5S.3.11 "Adapter selection" l.1224; 06 §6.11.6 l.1742–1745; 05-metadata l.343–356, l.640–642, l.862–884; 05-notify l.179–191; 05-library l.175–198, l.631; 03 §3.3 l.319 | R4-1 is written as "legacy **or** native per subsystem". Four of the eleven interfaces actually run **both** at once and route each record with a store method that has three different names. |
| SIMP5-2 | MAJOR | 05-storage §5S.3.8 l.1033, l.1099–1102 vs 03 l.321, 06 l.379, l.1251, l.1704, 09 l.334 | The full-text dirty set has two carriers, `index.Spec.Dirty` and `Manifest.LegacyStores.Dirty`, and one manifest field cannot say which index it serves. |
| SIMP5-3 | MAJOR | 04 l.236–238, l.343, l.100; 09 §9.2.3 l.332; 03 l.416 vs 03 §3.3 table, 06 `LegacyStores`, 05-ai | A phantom "`ai` result-journal store": books "implements" it and core "re-implements the mechanism", but no file defines the interface. |
| SIMP5-4 | MAJOR | 05-authn §3.4 l.540–560; 05-rbac l.374–376; 05-config l.373–410; 05-tasks l.1147; 05-metadata l.350–384, l.882; 05-notify l.187; 05-library l.184–197; 14 K23 | The frozen SDK store interfaces carry legacy-only members that every native implementation stubs. After the cutovers they are dead but frozen in v1. |
| SIMP5-5 | MINOR | 05-config l.392, l.401–407, l.1122, l.1145; 06 l.1246 | SIMP4-11 class again: `OverrideStore.Changed` (S2–S3 only) and `OpenEnv.Stage` (an extraction-stage string) are stage bridges inside a frozen SDK type. |
| SIMP5-6 | MAJOR | 03 §3.4 l.357–363; 06 l.1222; 05-rbac §3.6 l.493–495; 05-backup §5B.3.9 l.764; 05-updater l.307–314; 05-storage §5S.3.14 l.1366–1370; 05-ai l.35–46; 05-notify l.41–50 | SIMP4-1 half-fixed. The generated list contains built identifiers (`rbac.Service.UpdateRole`/`DeleteRole`, `Service.Rewrap`, `Meets`, `Added`), so the test would fail the S4 and S8 PRs. Entries lack packages or are not identifiers. Headings are not "named exactly". |
| SIMP5-7 | MAJOR | 05-storage §5S.3.6 l.727–870, l.523–528, l.543, D-S8; 05-tasks 5T.3.12 l.1007–1042; 06 l.289–309, l.1249, l.1537 | `Online` data migrations, with read adapters, the kernel-synthesised `<ID>.datamigrate.<name>` kind and three tests, are built at S5. No S0–S10 consumer exists: R3-1 removed adopt/fold, release B and the cutover are `BootBlocking`, and hello's migration exists only to test it. |
| SIMP5-8 | MINOR | 04 l.52–60; 09 §9.2.3 l.322–334; 14 K2 l.23; 05-config l.394 | The store-interface list is restated six times and drifts. 04 lists 9 stores, and 09 drops `notify.RuleStore` and adds the phantom. `secrets.LegacyStore` is an unlisted twelfth interface. |
| SIMP5-9 | MINOR | 05-storage l.1219–1220, l.1769; 05-tasks l.1192, l.1556; 05-authn l.1299; 05-rbac l.1216; 09 l.1042 | The conformance-suite and round-trip test names in 05-storage's rule match no owner file. |
| SIMP5-10 | MINOR | 03 §3.3 l.300 vs 06 l.886–894, 05-config l.369, 09 l.326 | 03 says legacy stores come "never through `app.Options`", but S2 uses `app.Options.LegacyConfigStore`. |
| SIMP5-11 | MINOR | 05-metadata l.764, l.1378, l.1396; 09 l.889, l.1329 vs 05-storage l.715 (`TestNoNewLegacyMigrations`) | The S7 one-time `Local` grant is called a "books legacy step", which `TestNoNewLegacyMigrations` forbids after S2b. |
| SIMP5-12 | MINOR | 05-storage l.1490 | SIMP4-4 residue: "every level's served run restarts" refers to the deferred per-level runs. |
| SIMP5-13 | MINOR | 06 §6.11.6 l.1733–1737 vs 05-storage §5S.3.13 l.1319, l.1324 | Deferred cutover contract: 06 removes the `LegacyStores` field in the cutover PR, but 05-storage's cutover reads through that store and removes it in a later build. |
| SIMP5-14 | MINOR | 05-logging l.239–258 vs 03 R5; 07 l.444 | (Outside this lens; found in passing.) Four exported package-level slices in `log` violate R5 and would fail `TestNoGlobals`. |

---

## Findings

### SIMP5-1 — MAJOR — Two store-selection models, specified as one

**Where:** 05-storage §5S.3.11 "Adapter selection and the `backends` ledger" (l.1224: "A core subsystem
uses the legacy implementation exactly when the build supplies one, else its native implementation") and
"Native implementation" (l.1219: "Core ships one per interface over `c/<subsystem>/`"); 06 §6.11.6
l.1742–1745 ("Which of the two a subsystem uses is fixed by the build's manifests"); 03 §3.3
store-interface table.

**Evidence:** The owner files implement two different models.

- **Either-or** (as 05-storage says): `auth.Store`, `rbac.Store`, `tasks.Stores`, `config.OverrideStore`,
  `log.JournalStore` and `ai.JobStore`. For AI, "the router then creates every new job in it too"
  (05-ai l.439–440).
- **Union with per-record routing**, where both stores serve at once:
  - `integrations.InstanceStore`: "The Registry sends each instance to the legacy store when that store
    Covers it, else to the native store" (05-metadata l.347–356). New instances of non-list families
    are native (l.640–642). `notify/email` is native for ABO (l.786).
  - `providerhttp.GateStore`: `Options{Store, Legacy}`, "A scope or counter it does not Cover goes to
    Store" (l.862–868); `Covers(scope, counter)`, "NativeGateStore: always true" (l.883).
  - `notify.RuleStore`: "sends a rule to the legacy store when it Covers the rule's ID, else to the
    native store" (05-notify l.179–191).
  - `library.RootSource`: the routing predicate is `Accepts(spec)` (05-library l.184–187). The native
    half is **not an implementation of the interface** but config entries `core.library.roots`/`mappings`
    (l.176, l.631, `TestNativeRootStore_RoundTrip` l.681). 03 l.319 nevertheless gives library a native
    keyspace `c/library/`.

So for ABO the native `InstanceStore`, `GateStore` and `RuleStore` and the native root entries run **in
production during the extraction**. The `backends` ledger records one value per interface, and 06's "which
of the two" sentence is false for these four. Three routing methods (`Covers` ×3 with different
signatures, `Accepts`) do one job.

**Fix:**

1. 05-storage §5S.3.11 "Adapter selection": state both models. **Exclusive** stores are `auth`, `rbac`,
   `tasks`, `config`, `log` and `ai` jobs; the legacy implementation replaces the native one. **Routed**
   stores are `integrations`, `providerhttp`, `notify` rules and `library` roots; the native store always
   serves, and the legacy one additionally serves the records it covers. List which interface is which in
   03 §3.3 with a "Model" column.
2. Give routed stores one predicate name and shape: `Covers(id string) bool` on the interface, with the
   record ID the owner spec defines (gate scope+counter joined, instance ref, rule ID, root spec key).
   Rename `RootSource.Accepts` to `Covers`.
3. Define the `backends` value for routed stores (`legacy+native`). `ErrLegacyStoreMissing` keeps its
   meaning: the legacy half disappeared.
4. 06 l.1742–1745: "which implementation serves a record is fixed by the build's manifests (exclusive
   stores) or by the legacy store's `Covers` (routed stores)".
5. 03 l.319 `library` row: native column "`core.library.roots`/`mappings` config entries (no `c/library/`
   root records)". Alternatively, give library a real `NativeRootSource` over `c/library/roots/` so that
   05-storage's "one native implementation per interface" holds. Pick one.

**Trade:** None. This writes down what the owner files already do.

### SIMP5-2 — MAJOR — The dirty set has two carriers

**Where:** 05-storage §5S.3.8: `index.Spec.Dirty DirtySet // nil = the native set …; an app with a pre-core
dirty set supplies its own (R4-1)` (l.1033), and "An index whose `Spec.Dirty` is set uses that
implementation instead: books supplies one …" (l.1099–1102). Against that: 03 §3.3 l.321 (`Dirty` field);
06 `LegacyStores.Dirty func(storage.LegacyHandles) (index.DirtySet, error)` (l.379), §6.4.10 S7 (l.1251)
and §6.11.6 example (l.1704); 09 §9.2.3 l.334.

**Evidence:**

- The owner file passes the legacy dirty set **per index**, as a value the module puts in the `Spec` it
  hands to `Deps.Storage.Indexes.Open` at P6/Run.
- 03, 06 and 09 pass it **per app**, as a manifest constructor the kernel calls at P4c.
- A module may open several indexes (`Spec.Name`), but one `LegacyStores.Dirty` field cannot say which
  index it belongs to.
- Neither text says which wins when both are set.

This is the third pattern R4-1 was meant to remove: the same interface reaches core through two
different doors.

**Fix:** Keep `index.Spec.Dirty` and delete `LegacyStores.Dirty`, from 06 l.379, l.1251 and l.1704, from
03 l.321 (move the row's content into a note "per index through `index.Spec.Dirty`, 05-storage §5S.3.8"),
and from 09 l.334. `storage/index` is a per-module service that the module opens with its own handles,
so the module already holds what the constructor would receive, and the per-index binding is explicit.
If the lead prefers the manifest door for uniformity, the field must become
`map[string]func(storage.LegacyHandles) (index.DirtySet, error)` keyed by index name, and `Spec.Dirty`
must go. Either way, one door.

**Trade:** With `Spec.Dirty`, the `backends` ledger does not cover the dirty set. Losing it degrades to a
rebuildable index missing some marks, never data loss.

### SIMP5-3 — MAJOR — A phantom "`ai` result-journal store"

**Where:**

- 04 l.236–238: "`internal/ai/resultjournal` and `internal/metadata/dailyquota` … stay as books' legacy
  implementations of the `ai` result-journal and `providerhttp` quota stores, and core re-implements the
  mechanisms over its own store interfaces".
- 04 row l.343: "books' legacy implementation of the `ai` result-journal store over `aijournal:`".
- 04 l.100.
- 09 §9.2.3 l.332: "`ai.JobStore` and the result-journal store (05-ai) | `aijob:`, `aijob_batch:`,
  `aijournal:`".
- 03 l.416: "books legacy stores".

**Evidence:**

- 03 §3.3's table, 06 `module.LegacyStores` (l.369–380) and 05-storage's interface list (l.1218) contain
  no result-journal interface.
- 05-ai never uses the word "journal". Its only store is `JobStore` (l.443–450).
- `internal/ai/resultjournal` exists at HEAD (`internal/ai/resultjournal/`).

Either a twelfth store interface exists that no owner file defines and no manifest field carries, or core
"re-implements" a durable result-journal mechanism that no core consumer is specified to use. The second
would be a built member without a consumer.

**Fix:** Decide in 05-ai.

- (a) The result journal is books-internal (batch apply runs in a **module** task, `ai.BatchApplyTask`,
  05-ai l.378–392, so the module can journal its own results). Then strike "result-journal store" from
  04 l.100, l.236–238 and l.343 and from 09 l.332 ("`aijournal:` stays a books family written only by
  books' apply handlers; no core interface"), and keep 03 l.416's package as "stays ABO".
- (b) Core needs it. Then add `ai.ResultJournal` to 05-ai (interface plus native implementation), to 03
  §3.3, to 06 `LegacyStores` (`AIJournal`) and to 05-storage l.1218.

(a) is the minimal reading of the current owner file.

**Trade:** None for (a).

### SIMP5-4 — MAJOR — The frozen store interfaces carry legacy-only members that every native implementation stubs

**Where:** 05-authn §3.4 l.540–560; 05-rbac §3.4 l.374–376; 05-config l.373–410; 05-tasks l.1147;
05-metadata l.350–384, l.882–884; 05-notify l.187–188; 05-library l.184–197; 14 K23 l.44.

**Evidence:** R4-1 accepts that the interfaces are frozen SDK (12 SC1: three stable checkpoints before the
split; the cutovers are undated and come after it, 02 N15). Each interface, however, mixes the native
contract with members that only a pre-core app's implementation can give meaning:

| Interface | Legacy-only members (native implementation's behaviour) |
|---|---|
| `auth.Store` | `TakeLegacySession`, `TakeLegacyInvite`, `DeleteLegacySignIns` ("A native store has none and answers ErrNotFound", l.542), `Reconcile` ("a native store does nothing", l.559) |
| `rbac.Store` | `PutRole(…, legacyAtoms []string)` ("a native store ignores it", l.374), `Reconfirm` (extension suspension) |
| `config.OverrideStore` | `Open` ("The native store does nothing here", l.376), `Secrets` ("nil = none (the native store)", l.393), `Suspended`, `Confirm`, `Committed`, `Changed` (SIMP5-5) |
| `tasks.RunStore` | `Recover` ("native: no-op", l.1147) |
| `integrations.InstanceStore` | `Covers` (native: every ref), `Confirm` ("Native stores … return nil", l.360); `StoredInstance.Moved`, `LegacySecrets`, `Ext`, `Suspended` |
| `providerhttp.GateStore` | `Covers` ("NativeGateStore: always true", l.883) |
| `notify.RuleStore` | `Covers` |
| `library.RootSource` | `Accepts`, `Confirm` |

After the last cutover these members have no implementation that does anything, but removing them is a
v1 major change. 14 K23 says the interfaces "speak core types … never ABO rows" yet does not mention them,
so the freeze cost is understated.

**Fix:** Use one pattern in every owner file:

1. The SDK interface has only the native contract.
2. Legacy-only capabilities are **optional interfaces** in the same package, detected by type assertion
   at P4c: `auth.LegacySignIns`, `auth.Reconciler`, `rbac.LegacyAtomWriter`, `config.LegacyOpener`,
   `config.LegacySecrets`, `tasks.Recoverer`, and `storage.ExtConfirmer` shared by every extension-record
   store. Routed stores keep `Covers` per SIMP5-1, but the native store need not implement it: absence
   means "covers everything".
3. Put one table in 05-storage §5S.3.11, "Legacy capabilities", listing each optional interface, its
   owner, and "removed with that family's cutover".
4. Add a sentence to 14 K23 naming this as the mitigation.

**Minimum alternative:** keep the members, but list them per interface in 05-storage §5S.3.11 as "legacy
members", and add to 12 SC1 that a cutover may deprecate them. Then the cost is at least visible.

**Trade:** Type assertions at one place (P4c) in exchange for native interfaces that stay minimal forever
and for native implementations with no stubs. The conformance suites split the same way (`StoreSuite`
plus `LegacySignInsSuite`).

### SIMP5-5 — MINOR — Stage-bridge members inside `config.OverrideStore` (SIMP4-11 class)

**Where:** 05-config l.392 (`Changed() <-chan struct{}` fires only when "the app's own legacy config path"
writes, "S2 to S3"; from S3 the books store is the only writer, l.1158); l.406 (`OpenEnv.Stage string //
the extraction stage of this build ("S2", "S2b", "S3", …)`), used at l.1122; 06 §6.4.10 S2b row l.1246.

**Evidence:**

- `Changed` is nil for every implementation from S3 on.
- `OpenEnv.Stage` puts the **ABO extraction plan's stage names** into a frozen core SDK type, to tell a
  books store something the books binary already knows: each stage is a different ABO build, and at S2
  ABO's `main` constructs `LegacyConfigStore` itself.
- SIMP4-11 deleted `Legacy.Rows` for the same reason (a frozen member with a stage-limited consumer).

**Fix:**

- Delete `OpenEnv.Stage`. Books' store takes its stage behaviour from its own build (a constructor option
  ABO's `main` passes at S2, and plain code from S2b).
- Make `Changed` an optional interface, `config.ExternalWriter`, that exists from S2 and is deleted in the
  S3 PR (v0 permits it, 12 §12.4.1).
- Update 05-config l.392, l.406, l.1122 and l.1145, and 06 l.1246.

**Trade:** None.

### SIMP5-6 — MAJOR — SIMP4-1 half-fixed: the generated deferred-members list is not implementable as written

**Where:** 03 §3.4 l.357–363 ("a heading named exactly `Deferred-build members`, as **package-qualified**
identifiers … fails … if any listed identifier exists in `core/`"); 06 l.1220–1228; the tables in 05-rbac
§3.6, 05-backup §5B.3.9, 05-updater, 05-storage §5S.3.14, 05-ai, 05-notify and 05-metadata.

**Evidence:**

1. **Built identifiers are listed, so the test fails a stage that is correct.**
   - 05-rbac l.495 lists "`rbac.Service.UpdateRole` / `DeleteRole` on seed roles". Both methods are
     built at S4 for custom roles (05-rbac l.292–293), so the S4 PR fails root `make ci`.
   - 05-rbac l.493 lists "`Added` on a seed role". `rbac.Role.Added` is built (l.235).
   - 05-backup l.764 lists "the re-encryption half of `Service.Rewrap`". `Rewrap` is built at S8
     (l.330).
   - 05-updater l.312 lists "the `Steps` half of `Meets`". `Meets` is built (l.240).

   A behavioural restriction on a built member cannot be checked by an absence test.
2. **Entries without a package**, against 03's own rule: `Manifest.Encrypted`, `Manifest.Recipients`,
   `Archive.Encrypted`, `Service.ExportPlain`, `ErrDecrypt` (05-backup); `UpgradeRequirement.Steps`,
   `StoreApplied.StepsDone` (05-updater). 05-storage spells the same member a second way,
   `UpgradeRequirement.steps`.
3. **Entries that are not identifiers**: routes (`GET /api/core/v1/ai/usage`, `POST
   /roles/{name}/reset`), config keys, metric names, `storage_grants.golden`, `c/storage/served/…`, an
   adapter name `core.inbox`, and test names. The scan covers "non-test `.go` files", so test-name entries
   are never checked.
4. **Heading match.** Six of the eleven headings are numbered ("### 3.5 Deferred-build members",
   "### 5S.3.14 …"; 05-authn, 05-backup, 05-http-api, 05-rbac, 05-storage and 06), so "named exactly"
   matches only the other five.

**Fix:**

1. 03 §3.4, as the one grammar: "a heading whose text, after an optional section number, is exactly
   `Deferred-build members`. In its table the **first column holds only package-qualified Go identifiers
   that must not exist in non-test files under `core/`** (`pkg.Type`, `pkg.Type.Member`, `pkg.Func`).
   Every other deferred item (routes, keys, metrics, tests, files) goes in a column `Also deferred (not
   checked)`."
2. 05-rbac: move the `UpdateRole`/`DeleteRole` row and "`Added` on a seed role" out of the table into the
   §4.8 prose (they are checked by `TestSeedRole_EditRefused`).
3. 05-backup: replace "re-encryption half of `Service.Rewrap`" with the deferred helper's own name (for
   example `backup.Service.RewrapEncrypted`), or move it to prose. Qualify the rest as `backup.…`.
4. 05-updater: drop "the `Steps` half of `Meets`" from the table and qualify `updater.UpgradeRequirement.Steps`
   and `updater.StoreApplied.StepsDone`. 05-storage cites 05-updater instead of re-listing the field.
5. Add a generator self-test: every first-column entry parses as a qualified identifier, and the list is
   empty at HEAD's built set (S2).

**Trade:** None.

### SIMP5-7 — MAJOR — `Online` data migrations are built at S5 with no S0–S10 consumer

**Where:** 05-storage §5S.3.6 (l.736–757, the synthesised definition; l.768 `Online`; l.775–783; l.792,
`StepWriter` precondition-only mode; l.827–853), `ReadAdapter`/`Spec.ReadAdapters` (l.523–528, l.543),
D-S8 (l.60); 05-tasks 5T.3.12 / 5T.4 step 0 (l.1007–1042, the dispatch exemption); 06 `Manifest.DataMigrations`
comment (l.289–309), S5 landing (l.1249), `TestOnlineDataMigrationIsModuleKind` (l.1537); tests
`TestModuleOnlineMigrationRunsAsModuleKind`, `TestOnlineMigrationYieldsPastTimeout`,
`TestModuleOnlineMigration_OwnerWritesSurvive`.

**Evidence:**

- The `Online` mode was designed for the round-2/3 adopt and fold copies, which R3-1 removed.
- No stage of 09 declares an `Online` migration. 09 l.1011 says only "a library-scale books rewrite is a
  `DataMigration`".
- The one named future consumer, the storage-efficiency release B (09 l.44–47, D-S9), is a physical
  re-encoding that raises a level, so it is `Breaking` and therefore `BootBlocking` (l.851–853). The
  cutover is `BootBlocking` too (§5S.3.13).
- The only `Online` migration in the spec is hello's, which exists to test the mechanism. 03 §3.4 says
  hello "never pulls a package forward".
- Round 4 still spent two MAJORs (COR4-5, COR4-6) on this unused path.
- The `ReadAdapter` comment "and for read-in-place legacy families" (l.524) is an R3-1 remnant: legacy
  families are now read by the app's legacy stores.

**Fix:**

1. Move to 05-storage §5S.3.14 `Deferred-build members`, landing with the first `Online` consumer:
   `datamigrate.Online`, `storage.ReadAdapter`, `storage.Spec.ReadAdapters`,
   `datamigrate.DataMigration.Writes`, `datamigrate.DataMigration.RewritesInPlace`, the precondition-only
   `StepWriter` rule, the kernel-registered `<ID>.datamigrate.<name>` and `core.storage.migrate` kinds,
   05-tasks' dispatch exemption, and the three tests.
2. Keep `BootBlocking` data migrations, the dry run and `datamigrate.DerivedBuildTask` (live consumers:
   release B if it lands during the window, and derived-index builds).
3. 06: `TestOnlineDataMigrationIsModuleKind` becomes deferred. hello keeps a `BootBlocking` migration if
   a core-consumer test is wanted.
4. Delete "and for read-in-place legacy families" from l.524.

**Trade:** The `Online` design (COR4-5/6) is kept at proof depth and lands additively. Nothing an extraction
build does changes.

### SIMP5-8 — MINOR — The store-interface list is stated six times, and the copies drift

**Where:**

- 03 §3.3 table (11 rows); 06 `LegacyStores` (11); 05-storage l.1218 (11); 01 glossary l.106 (11).
- 04 headline 1 l.52–60: 9 rows, no `integrations.InstanceStore` and no `notify.RuleStore`.
- 09 §9.2.3 l.322–334: no `notify.RuleStore` row, plus the phantom result journal (SIMP5-3).
- 14 K2 l.23: no instances, rules or dirty set.
- `config.OverrideStore.Secrets() secrets.LegacyStore` (05-config l.394): a twelfth app-implemented
  interface (with `Supersede`, `Check`) that appears in no list.

**Fix:**

1. 03 §3.3 is the one list. 04, 09, 14 and 01 cite "03 §3.3 'Store interfaces'" instead of restating it.
2. 09 §9.2.3 keeps only its ABO-specific columns (families, extension contents, rollback), keyed by the
   03 row name, and adds the `notify.RuleStore` row.
3. Add `secrets.LegacyStore` to 03's `config (with secrets)` row as "reached through
   `OverrideStore.Secrets`".

**Trade:** None.

### SIMP5-9 — MINOR — Suite and round-trip test names in 05-storage's rule match no owner file

**Where:** 05-storage l.1219 ("Each owner spec has `TestNativeStore_RoundTrip`") and l.1220
(`taskstest.RunStoreSuite`); 05-tasks l.1192, l.1556 (`taskstest.StoreSuite`). Owners use
`TestNativeBackend_RoundTrip` (05-authn l.1299, 05-rbac l.1216), `TestNativeStores_RoundTrip`,
`TestNativeRootStore_RoundTrip`, `TestNativeJobStore_RoundTrip`, and so on. 09 l.1042
(`TestLegacyStoreSuites`) and 05-storage l.1769 (`TestLegacyStoreGolden`) name the ABO side two ways.

**Fix:** In 05-storage §5S.3.11, state one naming rule, `<pkg>test.<Interface>Suite` and
`TestNative<Interface>_RoundTrip`, and rename the outliers: `taskstest.StoresSuite`, auth and rbac's
`TestNativeBackend_RoundTrip` → `TestNativeStore_RoundTrip`. Alternatively, say "named by the owner
spec". Use one ABO umbrella name in both 09 and 05-storage.

**Trade:** None.

### SIMP5-10 — MINOR — 03 says "never through `app.Options`", but S2 uses `app.Options.LegacyConfigStore`

**Where:** 03 §3.3 l.300 ("legacy implementation … on `module.Manifest.LegacyStores` (06), never through
`app.Options`"); 06 l.886–894; 05-config l.369; 09 l.326 ("S2 read (`app.Options.LegacyConfigStore` …)").

**Fix:** Append to 03 l.300: "(sole exception: the S2-only bridge `app.Options.LegacyConfigStore`, needed
because S2 has no kernel storage and deleted in the S2b PR, 06)".

**Trade:** None.

### SIMP5-11 — MINOR — The S7 initial-`Local` grant is called a "books legacy step"

**Where:** 05-metadata l.764 ("At S7, books ships one legacy step (… 05-storage §5S.3.4 …)"), l.1378,
l.1396; 09 l.889 and l.1329 vs 05-storage l.715 (`TestNoNewLegacyMigrations` pins `LegacyMigration` at
`LegacyCap`: "every later books step is a `Migration` or a `DataMigration`") and 09 l.245.

**Evidence:** A new `LegacyMigration` at S7 fails `TestNoNewLegacyMigrations`. §5S.3.4 is the claims
section, not the migrations section.

**Fix:** "books ships one versioned `Migration` (05-storage §5S.3.5; `Additive`, over books' own
extension family, raises no level)" in all five places.

**Trade:** None.

### SIMP5-12 — MINOR — SIMP4-4 residue in the failure table

**Where:** 05-storage l.1490: "Foreign open detected … every level's served run restarts". The per-level
served runs are deferred-build (§5S.3.14, l.1368).

**Fix:** "open (logged; the previous boot's duties count as none, so every duty consumer falls back,
`TestServedDutiesFallBack`)".

**Trade:** None.

### SIMP5-13 — MINOR — The deferred cutover contract disagrees about when the `LegacyStores` field goes

**Where:** 06 §6.11.6 l.1733–1737 (the cutover "removes the family's `LegacyStores` field in the same PR")
vs 05-storage §5S.3.13 l.1319 (the cutover "**reads** the legacy data through the app's legacy store") and
l.1324 ("After it … the app stops supplying the legacy store. A build that deletes the legacy store
declares `UpgradeFloor{…}`").

**Evidence:** The cutover build needs the field in order to fold. 05-storage removes it in a later build
behind a floor.

**Fix:** 06 l.1736: "… and a later build removes the family's `LegacyStores` field behind the cutover's
`UpgradeFloor` (05-storage §5S.3.13 'After it')".

**Trade:** None.

### SIMP5-14 — MINOR — (Outside this lens) `log` exports four package-level slices, against R5

**Where:** 05-logging l.239 `var DefaultPathRules = []PathRule{…}`, l.241 `var DefaultRedactKeys`, l.251
`var DefaultRedactQuery`, l.258 `var URLKeys`; 03 R5 ("no exported package-level variables of map, slice,
func or pointer type"), checked by `core/archtest.TestNoGlobals`. Separately, the `DefaultPathRules`
comment (l.233–238) still says the `/invite/{token}` link "is answered by a redirect to `/invite#token=`
during the window". That contradicts FE4-2: the path form is served from S4 to S9 (05-authn l.965–972).
07 l.444 also names the owner of the rule as "05-http-api's".

**Fix:**

- Make the four values functions (`func DefaultPathRules() []PathRule`, and so on), as R4 does for
  `jsonschemax.Base()`, and update 05-http-api's references.
- Reword the comment to "the legacy `/invite/{token}` page (S4–S9, FE4-2)".
- 07 l.444: change "05-http-api's" to "05-logging's".

**Trade:** None.
