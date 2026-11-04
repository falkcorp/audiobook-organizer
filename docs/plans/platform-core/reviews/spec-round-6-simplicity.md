<!-- file: docs/plans/platform-core/reviews/spec-round-6-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 11d62210-8ef8-459e-b177-5c722837d7ef -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6: simplicity and scope (SIMP6)

Lens: over-engineering, YAGNI, duplicated mechanisms, scope creep. This round has four jobs:

1. Regression check of every round-5 SIMP finding (MAJORs in full, MINORs briefly).
2. Check that the store-interface design (R4-1) and the optional-capability model added by SIMP5-4 are
   **uniform**, as 03 §3.3 now claims.
3. Find remnants of removed machinery (R3-1's dual format, round 3's grants, the served runs, round 5's
   `LegacyStores.Dirty`, `OpenEnv.Stage` and the phantom result journal).
4. Check the deferred-build boundary (03 §3.4): nothing built in S0–S10 may lack a consumer or have only a
   later one.

R3-1, R4-1 and R5-1 to R5-7 are not attacked. The findings concern how the round-5 revision wrote down
SIMP5-1 and SIMP5-4, and one mechanism that has no consumer.

Revision reviewed: 03 v1.8.1, with 05-storage, 05-config, 05-authn, 05-rbac, 05-tasks, 05-library,
05-metadata, 05-notify, 06, 09, 12 and 14 as of the round-5 integration pass. Line numbers refer to that
revision. Code anchors are at `748fd787a`.

**Counts:** 0 BLOCKER, 3 MAJOR, 8 MINOR.

## Regression check (round-5 SIMP findings)

| Round 5 | Status | Notes |
|---|---|---|
| SIMP5-1 two selection models (MAJOR) | **Fixed** | 03 l.325–331 and 05-storage l.1257 define exclusive and routed stores and the `legacy+native` ledger value. `storage.Covering` is defined (05-storage l.1338–1345), and 05-metadata, 05-notify and 05-library route through it. Residues: library is "routed" but has no native `RootSource`, so the whole interface is legacy-only (SIMP6-3), and the default for a store without `Covers` is stated two ways (SIMP6-5). |
| SIMP5-2 two dirty-set carriers (MAJOR) | **Fixed** | `LegacyStores.Dirty` is gone from 06 l.360–403, 03 l.370–372 and 09 l.366. `index.Spec.Dirty` is the only door (05-storage l.1133–1136). |
| SIMP5-3 phantom result journal (MAJOR) | **Fixed** | Option (a) was applied in 04 l.105 and l.254–257, 05-ai l.619–623, 06 l.388, 09 l.364 and 03 l.365. |
| SIMP5-4 legacy members in frozen interfaces (MAJOR) | **Fixed in substance** | `auth.Store`, `rbac.Store`, `config.OverrideStore`, `tasks.RunStore`, `InstanceStore`, `GateStore` and `RuleStore` now hold only the native contract, and the capabilities are listed in 05-storage l.1258. Four residues remain. (1) The claim that a capability's removal is "never a v1 break" is wrong, and the cutover also removes `LegacyStores` fields (SIMP6-2). (2) The proposed shared `storage.ExtConfirmer` was adopted by three owners, while auth, rbac and config each have their own confirm shape (SIMP6-4). (3) The capability conformance suites follow three patterns (SIMP6-6). (4) Legacy-only data fields are grouped one way in `integrations` and another way elsewhere (SIMP6-7). |
| SIMP5-5 stage bridges in `OverrideStore` (MINOR) | Fixed | `OpenEnv.Stage` is deleted (05-config l.436–437), and `Changed` is `config.ExternalWriter`, deleted in the S3 PR (l.405–411). One name drift remains in 04 (SIMP6-9). |
| SIMP5-6 deferred-members grammar (MAJOR) | **Fixed** | 03 l.405–416 states one grammar. Every table now has package-qualified first columns. Built-member restrictions moved to prose (05-rbac §3.6, 05-updater, 05-backup). Residue: placeholder cells and a duplicate row (SIMP6-8). |
| SIMP5-7 `Online` data migrations (MAJOR) | **Fixed** | 05-storage §5S.3.14 l.1433 defers `datamigrate.Online`, `DataMigration.Writes`, `storage.ReadAdapter` and `Spec.ReadAdapters`. 06 l.1340 lands only `BootBlocking` at S5. 05-tasks l.1021–1057 defers the dispatch exemption and the tests. The `ReadAdapter` comment (05-storage l.526) is now marked deferred. |
| SIMP5-8 list restated six times | Fixed | 03 l.354–355 is the one list. 04 l.56, 09 l.352–354, 01 l.106 and 14 K2 cite it. `secrets.LegacyStore` is in 03's config row. |
| SIMP5-9 test names | Fixed | Naming rule in 05-storage l.1252. `TestNativeBackend_RoundTrip` and `taskstest.StoreSuite` survive only in history bullets. |
| SIMP5-10 `app.Options` exception | Fixed | 03 l.312–314. |
| SIMP5-11 "books legacy step" at S7 | Fixed | 05-metadata l.852 says "one versioned `Migration`". The remaining hits are history bullets. |
| SIMP5-12 per-level failure row | Fixed | 05-storage l.1576. |
| SIMP5-13 cutover removes the field | Fixed | 06 l.1876–1878 ("a **later** build removes the family's `LegacyStores` field"). This wording exposes SIMP6-2. |
| SIMP5-14 `log` globals | Fixed | 05-logging l.246 and the following lines: functions returning fresh copies, plus `TestDefaultLists_FreshCopies`. |

**Remnant scan.** Outside history bullets I searched for: Teach, mirror, adopt, hand-over, `migrated_to`,
`GrantReadWrite`, `legacystore`, `legacyhist`, legacy-format adapter, per-level, `RequiresServed`, "served
run", `LegacyStores.Dirty`, `OpenEnv.Stage`, `Legacy.Rows`, `LegacyGateStore`, `Accepts(spec`, result
journal and `storage_grants`. Every hit is one of three things: a "does not exist" statement, a
deferred-build table row, or an unrelated use of the word (HEAD's own books legacy steps 1..`LegacyCap`;
`TestLegacyHistory_*` is a books test name). **No remnant of removed machinery is left in normative
text.**

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP6-1 | MAJOR | 05-config l.162–171, l.325–328, l.420–427, l.533, l.792, l.833, l.1087, l.1186–1196, l.1362, l.1374–1375; 09 §9.2.1 row 13 (l.158) | The config "basis" extension-record mechanism (`config.Basis`, `BasisClass`, `config.Suspender`, `Store.Confirm`, `AttachReport.Suspended`, `core.settings.confirm`, health `config_extension_suspended`) is built at S3, but **no key in any spec file declares a basis**. It has no consumer. |
| SIMP6-2 | MAJOR | 05-storage l.1258 ("a v0/additive removal, never a v1 break"); 05-config l.394; 06 l.1876–1878; 14 K23 l.44; 03 l.300, l.338–339 vs 12 §12.4.2 | The cutover is designed to **remove** optional capability interfaces, their conformance suites and `module.LegacyStores` fields. Under 12 §12.4.2, removing an exported identifier after v1.0.0 needs a major, and Go puts a major ≥ 2 on a new module path. The freeze cost SIMP5-4 asked to surface is still misstated. |
| SIMP6-3 | MAJOR | 03 l.333–339, l.364; 05-library l.177–209; 05-storage l.1252, l.1257–1258; 06 l.398 | `library.RootSource` has no native implementation, so the whole frozen SDK interface (with `Covers` as a member, `librarytest.RootSourceSuite` and `LegacyStores.Roots`) exists only for an app with pre-core data. This contradicts 03's "an SDK store interface carries only the native contract". |
| SIMP6-4 | MINOR | 05-storage l.1329–1336 vs 05-authn l.591–599, 05-rbac l.428–433, 05-config l.420–427 | SIMP5-4 residue. One operation, `Extension.Confirm`, is reached through four capability shapes. `storage.ExtConfirmer`'s doc ("A store without it has nothing to confirm") is false for auth, rbac and config. |
| SIMP6-5 | MINOR | 03 l.336–337 vs 05-storage l.1257, l.1338–1345; 05-metadata l.378–381, l.828, l.956–959, l.1173–1174; 05-notify l.189–192, l.320–321 | The `Covers` default is stated two ways (03: the *native half* lacks it; owners: a *legacy* store without it covers everything), the tests are misnamed `…NativeStoreCoversAll`, and the record-ID helpers have two names. |
| SIMP6-6 | MINOR | 05-storage l.1258 ("a separate suite per capability"); 05-authn l.1426; 05-rbac l.1353; 05-config l.1087; 05-metadata l.837; 05-tasks l.1228; 03 l.300 | Capability conformance follows three patterns (a separate suite, cases inside the store suite, the implementor's own test). `storage.Covering`/`ExtConfirmer` have no suite. `storage/index/indextest` is named only in 03. |
| SIMP6-7 | MINOR | 05-rbac l.270–274; 05-library l.218–223; 05-metadata l.398–437; 05-tasks l.1139 | Legacy-only data in frozen native types is grouped in `integrations.StoredInstance.Legacy *LegacyRecord` (nil for native), but sits directly on `rbac.Role.Suspended` and `library.SourcedRoot`. `RunStore.List` documents a `Legacy` marker that no type has, over an undefined `RunQuery`. |
| SIMP6-8 | MINOR | 05-backup l.781–784; 05-notify l.50; 05-download l.70 vs 06 l.1295; 03 l.411 vs 06 l.1284–1287, 05-updater l.329 | The deferred-members tables put `—` or an empty cell in the first column, which 03's generator self-test refuses. `module.Registrar.DownloadWatch` is listed under two headings. 03 says the unchecked column is "second", while 06 and 05-updater use the last column. |
| SIMP6-9 | MINOR | 04 l.760 vs 05-config l.405–434, 06 l.1336–1338 | 04 names the S2 capability for `Changed` as `CommitObserver` (it is `ExternalWriter`), puts `CommitObserver` at S2 (it is S3), and omits `Suspender.Confirm`. |
| SIMP6-10 | MINOR | 06 l.372–378 vs 05-storage l.1251, 05-metadata l.976–982 | 06's `LegacyStores` comment lists two exceptions to the batch rule. 05-storage and 05-metadata have a third: `providerhttp.GateStore`. |
| SIMP6-11 | MINOR | 03 l.347–348 vs 05-authn l.594–596 | 03 says API keys **and users** are re-confirmed through "the audited re-enable". 05-authn says the user record has no Widening field, is never suspended and needs no confirmation. |

---

## Findings

### SIMP6-1 — MAJOR — The config "basis" extension-record mechanism is built at S3 with no consumer

**Where:**

- 05-config §1.3.1 l.162–171: `func Basis(p Path, class BasisClass, legacyKeys ...string) SectionOption`,
  `BasisDerived`, `BasisWidening`.
- l.325–328: `func (s *Store) Confirm(ctx, p Path, a Actor) error`, audited `core.settings.confirm`.
- l.420–427: the optional `config.Suspender` with `Suspended()` and `Confirm(ctx, b, p)`, "From S3".
- l.533: `AttachReport.Suspended`.
- l.792 and l.833: health token `config_extension_suspended` and its failure row.
- l.1087: the `Suspender` case of `configtest.LegacyOverrideStoreSuite`.
- §1.12.2 l.1186–1196: per-entry extension records of the blob row.
- Rollback rows l.1362 and l.1374–1375.
- 09 §9.2.1 row 13 (l.158): "a config value derived from a legacy field … stays suspended … `Store.Confirm` |
  S3 | 'Confirm suspended settings after an upgrade that follows a rollback.'"
- 05-storage l.1258 lists `config.Suspender` as a capability that books implements.

**Evidence:**

- `grep -n "Basis("` over `docs/specs/platform-core/` finds only the declaration at 05-config l.164.
  `grep -i basis` finds only the mechanism's own text and 09's row 13.
- No owner file declares a key with `config.Basis`.
  - 05-config itself says "most keys declare none" (l.1189).
  - Its one example, "a remote-listener permission derived from an auth-off blob" (l.1193), names no key.
  - The only candidate in the spec, `core.auth.disabled_allow_remote` (05-authn l.1322), is a plain bool
    with an `auth.Options` default (l.279). It declares no basis.
- Every native-only config key is therefore a **standalone** record (l.1186–1188), which is never
  suspended.
- The mechanism has no consumer in S0–S10 or in the proof-depth apps: TV/Movies have no legacy store
  (10 l.154).
- It is not small. It adds:
  - an exported option and enum;
  - a `Store` method;
  - an optional capability;
  - an `AttachReport` field;
  - an audit action;
  - a health token;
  - per-entry `storage.Extension` use over the blob;
  - a suite case;
  - two rollback-table rows;
  - a release-note line in 09 for a consequence that cannot occur.
- 03 §3.4 (l.401–404) forbids exactly this: "a built package carries no member whose only consumer is
  deferred". Here no consumer exists at all.

This is the same class as SIMP5-7 (`Online` migrations kept for a consumer that was removed). R4-1 needed
extension records for roles, keys, roots and instances, and config inherited the general machinery
without a key that uses it.

**Fix:**

1. Add a heading `#### 1.12.3 Deferred-build members` to 05-config, with this first column:
   `config.Basis`, `config.BasisClass`, `config.BasisDerived`, `config.BasisWidening`,
   `config.Suspender`, `config.Store.Confirm`, `config.AttachReport.Suspended`. Its "Also deferred (not
   checked)" column holds: audit action `core.settings.confirm`; health token
   `config_extension_suspended`; per-entry extension records of the settings blob; the `Suspender` case
   of `configtest.LegacyOverrideStoreSuite`. Land with: "the first config key whose meaning depends on
   a legacy blob field". The contract text of l.1186–1196 stays, at proof depth.
2. 05-config §1.12.2: "Every native-only key is a standalone record in `m/books/ext/config/`; no key
   of S0–S10 declares a basis (deferred, §1.12.3)." Delete the Widening row l.833 and the Widening
   clauses of l.1362 and l.1374–1375, or mark them "(with the deferred basis)".
3. 05-storage l.1258: drop `config.Suspender` from the capability list. 03 §3.4 l.395–400: add "the
   config basis mechanism (05-config §1.12.3)" to the in-package deferred list.
4. 09 §9.2.1 row 13: delete "a config value derived from a legacy field" from the examples. Keep the
   row for roles and instances.

**Trade:** None for ABO. A future key that needs a basis brings the mechanism additively. That is a v0
change before the split and a minor after it, because it adds an option, a method and a capability.

### SIMP6-2 — MAJOR — The cutover removes v1 SDK identifiers; the spec calls that "never a v1 break"

**Where:**

- 05-storage l.1258: optional capabilities "each go with that family's cutover (a v0/additive removal,
  never a v1 break of the store interface)".
- 05-config l.394: "Each is removed with the settings cutover".
- 05-tasks l.1177–1178: `Recoverer` "goes with the legacy store at that family's cutover".
- 06 l.1876–1878: "a later build removes the family's `LegacyStores` field (and that store's
  legacy-only capabilities)".
- 03 l.338–339: "each optional interface is deprecated with its family's cutover".
- 03 l.300: the `<pkg>test` packages, including capability suites, are "part of the SDK and frozen with
  it".
- 14 K23 l.44: "each can be deprecated with its family's cutover instead of staying frozen in v1".

Against these, 12 §12.4.2 l.342–358:

- "Major: any incompatible SDK change — and in Go a major ≥ 2 changes the module path".
- "A deprecated Go identifier … stays for at least two minor releases and 90 days. **Removal therefore
  only happens in a major.**"

**Evidence:**

- The cutover is "a later, per-family, one-way step, decided on measured data" (02 N15, 06 l.1872). No
  stage schedules one. The split happens when SC1–SC10 hold after S10 (12 l.45), and the first tag is
  v1.0.0 (12 l.344–347). So a cutover will almost certainly run against a published v1 module.
- Each of the following is an exported identifier of a v1 package:
  - `auth.LegacySignIns`, `auth.Reconciler`, `auth.KeyConfirmer`;
  - `rbac.LegacyAtomWriter`, `rbac.Reconfirmer`;
  - `config.LegacyOpener`, `config.LegacySecrets`, `config.Suspender`, `config.CommitObserver`;
  - `tasks.Recoverer`;
  - `storage.Covering`, `storage.ExtConfirmer`;
  - `integrations.LegacyRecord`, `StoredInstance.Legacy`;
  - every `module.LegacyStores` field and the struct itself;
  - `authtest.LegacySignInsSuite`, `configtest.LegacyOverrideStoreSuite`,
    `integrationstest.LegacyInstanceStoreSuite`.
- `apidiff -incompatible` reports the removal of any of them. That is the check SC1 uses (12 l.112–115)
  and that `gorelease` uses after the split.
- 05-storage and 06 tell the cutover PR to remove them. Following those files would ship a breaking
  change in a minor (against 12 §12.4.2) or force `mediacore/v2`, which changes every app import.
- 03 and 14 say "deprecated", which is compatible with 12 only if nothing is ever removed in v1. Neither
  file says so, and K23's "instead of staying frozen in v1" is false. A deprecated type stays frozen
  until v2.

SIMP5-4 made a real gain: native implementations no longer stub anything. The cost it was meant to
surface ("removing them is a v1 major change", round-5 SIMP5-4 evidence) has been moved, not removed.

**Fix:** State one policy and apply it in every place listed above.

1. 05-storage l.1258, replacing the parenthesis: "At a family's cutover its capabilities, its
   `LegacyStores` field and its capability suites are marked `// Deprecated:` (12 §12.4.2), and the
   kernel refuses a non-nil deprecated field once the `backends` ledger records `native`
   (`ErrLegacyStoreRetired`). They are removed only in a major. Unlike members of the store interface,
   they impose nothing on any implementation in the meantime."
2. 05-config l.394, 05-tasks l.1177–1178 and 06 l.1876–1878: "deprecated" instead of "removed". 06's
   bullet: "a later build stops supplying the field (sets it nil); the field is deprecated, not
   deleted, in v1".
3. 14 K23: replace "instead of staying frozen in v1" with "they stay in v1 as deprecated, inert
   identifiers until a major; no implementation stubs them".
4. Optional (the owner's choice, recorded as a 14 open question): put every legacy-only identifier
   (capabilities, `LegacyStores`, `LegacyHandles`, `LegacyRecord`, the capability suites) in one
   package, `core/legacy`. 12's SC1 filter and 12 §12.4.2 would then exclude that package from the API
   promise, as they exclude `kernel/`. Cutover removals would be free, at the price of a new top-level
   package that apps import.

**Trade:** None for option 1–3 beyond text. Option 4 trades one more package for free removal.

### SIMP6-3 — MAJOR — `library.RootSource` is a frozen SDK interface that exists only for legacy data

**Where:**

- 03 l.333–339: "An SDK store interface carries only the native contract … `Covers` … are optional
  interfaces … Native implementations therefore carry no stubs".
- 03 l.364: library row, model "routed", native column "the `core.library.roots` and
  `core.library.mappings` config entries (no `c/library/` root records)".
- 05-library l.177–209: "RootSource has NO native implementation, so the whole interface exists only
  for an app with pre-core roots"; `Covers` kept as a member.
- 05-storage l.1252: "The one exception is `library.RootSource`, which has no native implementation".
- 05-storage l.1257: "library's `RootSource` carries `Covers` on the interface".
- 05-storage l.1258: "`library.RootSource` keeps `Covers` as a member".
- 06 l.398: `Roots func(storage.LegacyHandles) (library.RootSource, error)`.
- 03 l.300: `library/librarytest` with `librarytest.RootSourceSuite`.

**Evidence:**

- SIMP5-1 offered two fixes for library: state that the native side is config entries, or "give library
  a real `NativeRootSource` … so that 05-storage's 'one native implementation per interface' holds.
  Pick one." The first was picked. As a result, every member of `RootSource`, and the interface itself,
  is what 03 l.333–339 calls a legacy-only capability. Yet it is frozen in v1 as a store interface.
- Its conformance suite has no core implementation to run against. Only ABO runs
  `librarytest.RootSourceSuite`, so core CI cannot catch a suite regression.
- 03's capability rule is broken in its own list: l.336 names `Covers` as optional, while 05-storage
  makes library the one exception.
- 10 l.154 already shows that TV/Movies never set `LegacyStores`. For every app except ABO, `RootSource`,
  `RouteKey`, `SourcedRoot` (`Ext`, `Suspended`), `librarytest` and `LegacyStores.Roots` are dead SDK.
- The model is not uniform. Three routed stores have a native half that implements the interface and a
  legacy half that adds `storage.Covering`. The fourth has a native half that is not an implementation
  at all, so `List` merging, routing and the `backends` value `legacy+native` are special-cased in
  05-library l.570–582.

**Fix (pick one, and say which in 03 l.364):**

- **(a) Uniform (recommended).** Add `library.NativeRootSource` over the `core.library.roots` /
  `core.library.mappings` entries. Its `List` reads them. Its `Create`, `Update` and `Delete` stage
  through `config.OverrideStore.Stage` into the caller's batch. Move `Covers` off `RootSource` onto
  `storage.Covering`, like the other routed stores. `librarytest.RootSourceSuite` then runs in core
  against the native implementation. Then:
  - 05-storage l.1252: delete "The one exception…".
  - 05-storage l.1257–1258: delete library's special cases.
  - 05-library l.177–200: "routed; native half `NativeRootSource` over config entries".
  - 05-library l.570–582: routing as in 05-metadata M1.1.
  - 03 l.364: native column `NativeRootSource` (config entries).
- **(b) Classify it as legacy.** Keep the shape, but list `library.RootSource`, `library.RouteKey`,
  `library.SourcedRoot`, `module.LegacyStores.Roots` and `library/librarytest` in 05-storage l.1258 as
  one legacy-only capability set, subject to SIMP6-2's deprecation policy. Change 03 l.333 to "carries
  only the native contract, **except** `library.RootSource`, which is legacy-only as a whole (no native
  root record exists; 05-library)", and change 03 l.364's Model to "legacy-only, beside config entries".

**Trade:** (a) adds one small native type (four methods over existing config entries) and removes every
special case. (b) changes text only but keeps a v1 interface that no app except ABO can use.

### SIMP6-4 — MINOR — SIMP5-4 residue: one confirm operation, four capability shapes

**Where:**

- 05-storage l.1329–1336: `storage.ExtConfirmer` `Confirm(ctx, b, id string)`, "id is the record ID
  the owner spec defines (**kind-qualified where one store keeps several record kinds**). A store
  without it has nothing to confirm."
- Used by integrations, library and notify (05-metadata l.383, 05-library l.186, 05-notify l.189).
- Against these:
  - `auth.KeyConfirmer.ConfirmAPIKey(b, id)` (05-authn l.597–599);
  - `rbac.Reconfirmer.Reconfirm(b, r *Role, accept bool)` (05-rbac l.431–433);
  - `config.Suspender.Confirm(ctx, b, p Path)` (05-config l.424–427).

**Evidence:**

- All four wrap `storage.Extension.Confirm` (05-storage l.1321–1323).
- The kind-qualified ID clause was written for a store like auth, which keeps several record kinds.
  Auth does not use it.
- Taken literally, ExtConfirmer's doc says that the auth, rbac and config legacy stores, which lack
  `ExtConfirmer`, "have nothing to confirm". Each of them does.
- `accept` is on the store for rbac, but on the service for auth (`Service.ConfirmAPIKey(ctx, actor, id,
  accept)`, 05-authn l.339). The same decision is made at two levels.

**Fix:** Use `storage.ExtConfirmer` everywhere:

- auth: `Confirm(ctx, b, "apikey/"+id)`, deleting `auth.KeyConfirmer`;
- rbac: `Confirm(ctx, b, "role/"+name)`, with "discard" done by the service as an ordinary `PutRole`
  of the legacy-derived set, deleting `rbac.Reconfirmer`;
- config: `Confirm(ctx, b, string(path))`, keeping only `Suspended()` on `config.Suspender` (or nothing,
  if SIMP6-1 defers it).

Update the 05-storage l.1258 list, 05-authn l.1220, l.1387 and 05-rbac l.848, l.1353. If a per-owner
signature is kept on purpose, change ExtConfirmer's doc to "a store without **any** confirm capability
(this one or its owner's) has nothing to confirm" and list the owner-specific ones as its variants.

**Trade:** None.

### SIMP6-5 — MINOR — The `Covers` default is stated two ways, and its tests and ID helpers are misnamed

**Where:**

- 03 l.336–337: "(a routed store's **native half** implements no `Covers`: absence means 'covers
  everything')".
- 05-storage l.1257: "a **legacy** store without it covers every record". 05-storage l.1343–1344: "A
  legacy store without it covers every record; the native half is never asked".
- 05-metadata l.380–381 and l.958–959: the same as 05-storage.
- Test names `TestRegistry_NativeStoreCoversAll` (05-metadata l.828) and `TestRules_NativeStoreCoversAll`
  (05-notify l.321), described as "a store without `storage.Covering` covers every record".
  `TestGate_StoreWithoutCoveringCoversAll` (05-metadata l.1173) is named correctly.
- ID helpers: `integrations.CoverID`, `providerhttp.CoverID`, `library.RouteKey`.

**Evidence:**

- 03 is normative, and its reading (absence on the native half) describes a router that would consult
  the native half. Every owner says the native half is never consulted, and that absence on the store in
  the *legacy* slot sends every record there.
- Under that owner rule, a legacy store without `Covering` makes the "routed" store exclusive-legacy:
  the native half serves nothing. Yet the `backends` ledger records `legacy+native` (05-storage l.1257).
- The test names say "native store".

**Fix:**

- 03 l.336–337: "(only a routed store's legacy half implements `Covers`; a legacy half without it covers
  every record, and the native half is never asked)".
- 05-storage l.1257: "a legacy half without `Covering` covers every record, and the ledger then records
  `legacy`".
- Rename the tests `TestRegistry_LegacyWithoutCoveringCoversAll` and
  `TestRules_LegacyWithoutCoveringCoversAll`.
- Call the record-ID helper `CoverID` in all three packages (`library.CoverID(role, id)`), or `RouteKey`
  in all three.

**Trade:** None.

### SIMP6-6 — MINOR — Capability conformance follows three patterns

**Where:**

- 05-storage l.1258: "Conformance suites split the same way: `ptest.ISuite` for the interface, a
  separate suite per capability".
- Owners:
  - auth: `authtest.LegacySignInsSuite` (05-authn l.1426), but nothing for `Reconciler` or
    `KeyConfirmer`;
  - rbac: "the optional `LegacyAtomWriter`/`Reconfirmer` cases" inside `rbactest.StoreSuite`
    (05-rbac l.1353);
  - config: one umbrella `configtest.LegacyOverrideStoreSuite` for five capabilities (05-config l.1087);
  - integrations: `integrationstest.LegacyInstanceStoreSuite` (05-metadata l.837);
  - tasks: "`Recoverer` is tested by its implementor" (05-tasks l.1228).
- `storage.Covering` and `storage.ExtConfirmer` have no suite package.
- 03 l.300 lists `storage/index/indextest`, which no owner file defines (05-storage §5S.3.8 names no
  `DirtySetSuite`).

**Evidence:** Under SIMP6-2, every exported capability suite is frozen v1 surface. Five different
choices, each made locally, are therefore costly.

**Fix:**

- 05-storage l.1258 picks one pattern. The minimal one: "**one** legacy suite per store interface,
  `ptest.Legacy<I>Suite`, which runs a case for each capability the implementation has (the config
  model); `storage/storagetest.CoveringCases` and `ExtConfirmerCases` are helpers it calls".
- Rename `authtest.LegacySignInsSuite` to `authtest.LegacyStoreSuite` (adding `Reconciler` and the
  confirm case).
- Move rbac's optional cases into `rbactest.LegacyStoreSuite`.
- Either add `taskstest.LegacyStoresSuite` with a `Recoverer` case, or state in 05-storage that `tasks`
  is the stated exception.
- Define `indextest.DirtySetSuite` in 05-storage §5S.3.8, or drop `indextest` from 03 l.300.

**Trade:** None.

### SIMP6-7 — MINOR — Legacy-only data fields in native types are grouped in one owner only

**Where:**

- 05-metadata l.398–437: `StoredInstance.Legacy *LegacyRecord`, "nil for every native record, so the
  native contract carries no legacy field (SIMP5-4)", which holds `Ext`, `Suspended`, `Moved`,
  `PendingLocal`.
- Against it:
  - `rbac.Role.Suspended bool` (05-rbac l.270–274), directly on a type every native caller sees;
  - `library.SourcedRoot{Ext, Suspended}` (05-library l.218–223), which is defensible only under
    SIMP6-3 (b);
  - 05-tasks l.1139: `List(ctx, q RunQuery)` "… **Legacy** marks rows an older run system wrote". No
    `Legacy` field exists in `RunView` (l.836–853) or `RunRecord` (l.1094 ff.), and `RunQuery` is used
    in two frozen signatures (l.813, l.1139) but defined nowhere.
  - 05-tasks l.1228–1230 says "`tasks.Stores` carries no other legacy-only member".

**Fix:**

- Choose integrations' pattern as the rule in 05-storage l.1258: "legacy-only state on a record type is
  one pointer field, `Legacy *<Pkg>LegacyState`, nil from a native store".
- Apply it as `rbac.Role.Legacy *RoleLegacyState{Suspended}`, or keep `Suspended` and state why a
  computed read-time flag is exempt.
- 05-tasks: define `RunQuery`, and either add the marker it documents (as `RunRecord.Legacy
  *RunLegacyState`, nil from `NativeStores`) or delete "Legacy marks rows an older run system wrote".

**Trade:** None.

### SIMP6-8 — MINOR — Placeholder cells and a duplicate row in the generated deferred list

**Where:**

- 03 l.405–416: the first column "holds only package-qualified Go identifiers", and "a generator
  self-test fails on a first-column entry that does not parse as a package-qualified identifier".
- 05-backup l.781–784: four rows whose first column is `—`.
- 05-notify l.50: a row whose first column is empty (the `core.inbox` adapter).
- `module.Registrar.DownloadWatch` is a first-column entry in both 05-download l.70 and 06 l.1295.
- 03 l.411 says the unchecked column is "a second column", while 06 l.1284–1287 and 05-updater
  l.326–329 put it last.

**Evidence:**

- As written, the self-test fails on 05-backup and 05-notify on the S2 PR that adds it.
- If the self-test skips such cells, the grammar in 03 is wrong.
- The duplicate entry needs two deletions in the PR that builds the watch. This is fail-safe, but noisy.

**Fix:**

- 03 l.414–416: "… fails on a first-column entry that is neither a package-qualified identifier nor
  exactly `—` (a row with nothing to check)". Fill 05-notify l.50's empty cell with `—`.
- Keep `module.Registrar.DownloadWatch` only in 06 §6.4.9, and have 05-download cite it.
- 03 l.411: "a column headed exactly `Also deferred (not checked)`, in any position".

**Trade:** None.

### SIMP6-9 — MINOR — 04 misnames the config capabilities and their stages

**Where:** 04 l.760: "the optional capabilities `LegacyOpener` (`Open`) and `CommitObserver` (`Changed`)
at S2, `Suspender` (`Suspended`) at S3".

**Evidence:**

- 05-config l.405–411 says `Changed` belongs to `config.ExternalWriter` (S2 to the S3 PR). l.429–434 says
  `CommitObserver` is `Committed`, from S3. l.424–427 gives `Suspender` both `Suspended` and `Confirm`.
- 06 l.1336–1338 agrees with 05-config.

**Fix:** In 04 l.760, write "`LegacyOpener` (`Open`) and `ExternalWriter` (`Changed`, deleted in the S3
PR) at S2; `LegacySecrets`, `Suspender` (`Suspended`, `Confirm`) and `CommitObserver` (`Committed`) at
S3". Better, cite 06 §6.4.10 instead of restating it. Adjust this if SIMP6-1 defers `Suspender`.

**Trade:** None.

### SIMP6-10 — MINOR — 06 omits the `GateStore` exception to the batch rule

**Where:** 06 l.372–378: "with two stated exceptions … log.JournalStore … and the maintenance methods
Recover and Prune". Against it, 05-storage l.1251 lists `providerhttp.GateStore` as a third exception
("commits its own batch under the Registry's mutex"), and 05-metadata l.976–982 says the same.

**Fix:** In 06 l.372–378, write "… three stated exceptions …, and `providerhttp.GateStore`, whose writes
commit their own batches under the Registry's mutex (05-metadata M2)". Better, cite 05-storage
"Store interfaces" for the exception list instead of restating it.

**Trade:** None.

### SIMP6-11 — MINOR — 03 says users are re-confirmed; 05-authn says users are never suspended

**Where:** 03 l.347–348: "through a confirmation path its store exposes (for API keys **and users**, the
audited re-enable)". 05-authn l.594–596: "The user record declares no Widening field, so it is never
suspended and needs no confirmation (§11)."

**Fix:** In 03 l.347–348, write "(for API keys, `auth.Service.ConfirmAPIKey`; for roles,
`ReconfirmRole`; for integration instances and import roots, their `confirm` routes)".

**Trade:** None.
