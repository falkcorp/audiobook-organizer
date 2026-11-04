<!-- file: docs/plans/platform-core/reviews/spec-round-7-simplicity.md -->
<!-- version: 1.0.0 -->
<!-- guid: c1a9d6f5-ac14-4237-9f0f-13561e265881 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7: simplicity and scope (SIMP7)

Lens: over-engineering, YAGNI, duplicated mechanisms, scope creep. This round has four jobs:

1. Regression check of every round-6 SIMP finding (MAJORs in full, MINORs briefly).
2. The `legacy` package boundary introduced by R6-3: what is in it, what cannot move, and whether the
   mechanism that keeps owners from importing it (unexported mirrors, `export_test.go`, kernel detection)
   works as written.
3. Duplicated mechanisms introduced or left by the revision.
4. The deferred-build boundary (03 §3.4): nothing built in S0–S10 may lack a consumer.

R6-3 (one `legacy` package outside the API promise) and the round-6 lead decision (`Manifest.LegacyStores`
stays a v1 field typed `legacy.Stores`) are not attacked. The findings concern how they were written down.
Nothing below asks for removed machinery back.

Revision reviewed: 03 v1.9.1, 05-storage v1.7.1, and the other files as of commit `15c1d5f6b`. Line numbers
refer to that revision. Code anchors are at `748fd787a`.

**Counts:** 0 BLOCKER, 3 MAJOR, 8 MINOR.

## Regression check (round-6 SIMP findings)

| Round 6 | Status | Notes |
|---|---|---|
| SIMP6-1 config basis without a consumer (MAJOR) | **Fixed** | 05-config §1.12.5 (l.1581–1605) lists `config.Basis`, `BasisClass`, `BasisDerived`, `BasisWidening`, `Store.Confirm`, `AttachReport.Suspended` and `legacy.ConfigSuspender` as deferred-build members. 05-config l.1246–1248 says no S0–S10 key declares a basis. 05-storage l.1355 drops the capability from its list. 09 row 13 (l.161) no longer cites config. 03 l.454–455 and README l.62 add it to the deferred list, but cite the wrong section (SIMP7-9). The same class of problem recurs for library roots (SIMP7-2). |
| SIMP6-2 cutover removes v1 identifiers (MAJOR) | **Fixed in substance** (R6-3) | 12 SC1 (l.76–84), the apidiff filter (l.118), the fixture test (l.128), the gorelease override (l.134–136) and §12.4.2 (l.363–366) exclude `legacy/`. 05-storage l.1331, 06 l.2007–2015 and 14 K23 say "deleted in a minor". Stale "deprecated"/"removed" wording: none left outside history bullets. Three residues: (1) one SDK field exposes a capability's mirror type, so changing or removing that capability is still an SDK change (SIMP7-4). (2) The list of what stays in SDK packages exists in three different versions (SIMP7-3). (3) Two books-only storage identifiers carry a removal reason that contradicts the policy (SIMP7-7). |
| SIMP6-3 `library.RootSource` frozen with no native implementation (MAJOR) | **Fixed** (option (b), R6-3) | `legacy.RootSource` and `legacy.LibraryCoverID` (05-library l.256–301), `RootSpec.Legacy *RootLegacyState` (l.125–139), and 03's library row model "legacy-only, beside the native config entries" (l.418). `librarytest` survives only in history bullets. Residue: the input through which library receives the source is unspecified (SIMP7-4). |
| SIMP6-4 four confirm shapes | Fixed | One `legacy.ExtConfirmer` (05-storage l.1331, l.1449–1458). `auth.KeyConfirmer` and `rbac.Reconfirmer` survive only in "deleted" notes (05-authn l.654, 05-rbac l.465). Residue: 05-storage l.1331 lists "a rule's cover ID" as a confirm ID, but notify keeps no Widening record (SIMP7-11). |
| SIMP6-5 `Covers` default / test names | Fixed | 03 l.337–347, 05-storage l.1330. Tests renamed (05-metadata l.906, l.1273; 05-notify l.329). One helper name, `<Pkg>CoverID`. |
| SIMP6-6 three conformance patterns | Fixed | One pattern in 03 l.375–377 and 05-storage l.1331. `indextest.DirtySetSuite` is defined (05-storage l.1202). Residue: 04's generated objection text claims legacy cases for `aitest`/`logtest` that do not exist (SIMP7-11). |
| SIMP6-7 legacy state grouping | Fixed | `Role.Legacy *RoleLegacyState` (05-rbac l.282–297), `InstanceConfigLegacyState`/`StoredInstanceLegacyState` (05-metadata l.211–225, l.428–442), `RootSpec.Legacy` (05-library l.129). `RunQuery`/`ListQuery` are defined (05-tasks l.832–840). Naming residue: SIMP7-8. |
| SIMP6-8 placeholder cells / duplicate row | Fixed | 03 l.462–473 allows exactly `—`. `DownloadWatch` is listed only in 06 l.1291 (05-download l.70 cites it). |
| SIMP6-9 04 capability names | Fixed | 04 l.777. |
| SIMP6-10 batch-rule exceptions | Fixed | 06 l.1403–1408 names `providerhttp.GateStore`. |
| SIMP6-11 users "re-confirmed" | Fixed | 03 l.399–402. |

**Remnant scan.** Outside history bullets I searched for every pre-R6-3 name: `storage.Covering`,
`storage.ExtConfirmer`, `storage.Extension`, `storage.ExtRecord`, `storage.LegacyHandles`,
`auth.LegacySignIns`, `auth.Reconciler`, `auth.KeyConfirmer`, `rbac.LegacyAtomWriter`, `rbac.Reconfirmer`,
`config.LegacyOpener`, `config.OpenEnv`, `config.ExternalWriter`, `config.LegacySecrets`,
`config.CommitObserver`, `config.Suspender`, `secrets.LegacyStore`, `tasks.Recoverer`,
`tasks.LegacyStoresFunc`, `library.RootSource`, `library.RouteKey`, `library.SourcedRoot`, `librarytest`,
`integrations.CoverID`, `providerhttp.CoverID`, `integrations.LegacyRecord`, `LegacySignInsSuite`,
`configtest.LegacyOverrideStoreSuite`, `integrationstest.LegacyInstanceStoreSuite` and the type
`module.LegacyStores`. Every hit is one of three things: a "formerly"/"deleted" note, a row of 05-storage's
former-name column, or a history bullet. **The rename landed.**

## Summary table

| ID | Sev | Where | One line |
|---|---|---|---|
| SIMP7-1 | MAJOR | 03 l.371–373; 05-config l.494–495, l.1801–1802; 05-metadata l.76–79, l.603–604, l.907, l.1275; 05-notify l.194, l.331; 05-library l.260–262, l.729; 05-tasks l.1299–1301; 06 l.1456–1458 | R6-3's only guard against a silently skipped capability (the mirror ⇄ `legacy` assertion) is specified as living in `export_test.go`. In the owner's own package that file cannot import `legacy`: `legacy` imports the owner, and Go refuses the cycle in tests. Only 05-authn and 05-rbac place it in the external test package. |
| SIMP7-2 | MAJOR | 05-library l.118, l.133–139, l.273–274, l.290–295, l.594–597, l.727; 03 l.399–400; 05-storage l.1331 | Root extension suspension and confirmation have no S0–S10 consumer. The only Widening field is `ReadShares`, and ABO has one module, so no share can name another module. This is SIMP6-1's class again. |
| SIMP7-3 | MINOR | 03 l.377–390; 05-storage l.1331 ("Two things therefore stay"), l.1355–1361; 14 K23 l.44; 05-metadata l.79–81 | The list of legacy-serving identifiers that stay in SDK packages exists in three versions: 03 has four kinds, 05-storage "two things" plus a "kept outside" list, K23 four kinds. None is complete. |
| SIMP7-4 | MAJOR | 05-config l.1784, l.1798–1802, l.1492; 05-library l.258–259; 03 l.383–386 | `secrets.Options.LegacyStore` is an exported SDK field typed by secrets' unexported mirror, so the method set of `legacy.SecretsStore` is frozen through it, and removing it at the settings cutover is an SDK break. Library's input for the root source is unspecified ("an opaque value"). |
| SIMP7-5 | MINOR | 03 l.368–369; 05-storage l.1331; 06 l.1453–1456; 05-authn l.658–659; 05-rbac l.467–468; 05-tasks l.1296–1297; 06 l.1766 | Capabilities are detected twice: by the kernel at P4c and by the owner. The kernel's result ("records what it found") has no consumer except for the two capabilities it drives. 05-storage's rule that capability signatures name only owner-layer types contradicts those two. |
| SIMP7-6 | MINOR | 05-storage l.1329 (last sentence), l.1748; 05-metadata l.737–738 vs 05-authn l.1568, 05-rbac l.1333, 05-metadata l.836, 05-library l.596 | Two health signals for one condition. Each owner has its own suspended token, and a generic `storage_extension_suspended` "per store, with counts" has no producer: `legacy.Extension` moved to L5, storage (L1) never sees its reads, and 05-health does not list the token. |
| SIMP7-7 | MINOR | 05-storage l.566, l.569, l.707–716, l.1360–1361, l.1982; 09 l.254–278 | `storage.LegacyMigration` and `Spec.Legacy` are books-only, legacy-only identifiers in frozen `storage`. The stated reason they stay outside `legacy` ("go with their own floor") implies a removal that would be a v1 break. After the S5 floor release they have no consumer and nothing deletes them before the split. |
| SIMP7-8 | MINOR | 03 l.359–364; 05-storage l.1341–1353; 05-library l.129, l.136, l.728; 05-tasks l.1310 | The naming rule ("prefix where ambiguous") is applied inconsistently: `AuthReconciler` vs `AtomWriter`, `TaskRecoverer` vs `legacytest.TasksRunStoreSuite`, `RootLegacyState` vs the `<Type>LegacyState` rule, `TestNativeRootStore_RoundTrip` with no root store. |
| SIMP7-9 | MINOR | 03 l.455, l.929; README l.62 vs 05-config l.1498, l.1581 | 03 and README cite the config basis deferral as 05-config §1.12.3 (the key map). The heading is §1.12.5. |
| SIMP7-10 | MINOR | 03 l.281, l.416 vs 05-tasks l.48, l.1109, l.1252–1256; 06 l.1424; 09 l.378; 04 l.1328 | 03's "one list" omits `tasks.StandDownStore` and stand-down markers from the `tasks` row. Every other file has them (MOD6-4). |
| SIMP7-11 | MINOR | 04 l.1609 (Objections item 1); 05-storage l.1331 | Generated and stale text. 04 says the legacy cases of `aitest` and `logtest` are in `legacytest` (no such suites exist), and it carries a garbled fragment "`app.BinaryID` delegates to it (06))". 05-storage names "a rule's cover ID" as a confirm ID, but notify has no `ExtConfirmer`. |

---

## Findings

### SIMP7-1 — MAJOR — The mirror assertion cannot live in the owner's `export_test.go`

**Where:**

- 03 l.371–373: "an `export_test.go` assertion in the owner checks, in both directions, that its unexported
  interface and the `legacy` one are mutually assignable".
- 05-config l.494–495: "An `export_test.go` assertion in each consumer". l.1801–1802: secrets'
  "export_test.go asserts that legacyStore and legacy.SecretsStore are mutually assignable".
- 05-metadata l.76–79 ("an `export_test.go` assertion in each package"), l.603–604 ("integrations'
  export_test.go asserts the two agree"), l.907 (`TestLegacyMirrorsMatch` "(`export_test.go`: …)"), l.1275
  (`TestGateLegacyMirrorsMatch` "(`export_test.go`: …)").
- 05-notify l.194 ("notify's export_test.go checks against legacy.Covering"), l.331
  (`TestRulesLegacyMirrorsMatch` "(`export_test.go`: …)").
- 05-library l.260–262: "an `export_test.go` assertion in `library` checks … that `legacy.LibraryCoverID`
  equals library's own route key".
- 05-tasks l.1299–1301: "an export_test.go assertion in tasks" (the mirror is in `internal/executor`, per the
  same comment).
- 06 l.1456–1458.
- These two get it right: 05-authn l.661 ("checks, **from auth's external test package**") and 05-rbac l.471.

**Evidence:**

- By Go convention, `export_test.go` is a file in the package under test (`package notify`). That is the only
  place an unexported mirror is visible, and 04 uses it that way (l.1271–1280: `export_test.go` re-exports
  internals for `store_test`).
- `legacy` imports every owner it names. 03 l.367 says "it imports the L0–L4 packages whose types its
  signatures name", and 05-metadata l.598 says "imports integrations and providerhttp".
- An in-package test file of `notify` that names `legacy.Covering` therefore imports a package that imports
  `notify`. `go test` refuses this with "import cycle not allowed in test".
- Three spec'd tests (`TestLegacyMirrorsMatch`, `TestGateLegacyMirrorsMatch`, `TestRulesLegacyMirrorsMatch`)
  are placed in `export_test.go` explicitly. 05-library also calls `legacy.LibraryCoverID` from there.
- The assertion is R6-3's only protection against drift. 05-metadata l.79–80: "so a renamed or retyped member
  fails that package's tests **instead of silently disabling routing**". 05-tasks l.1301–1302: "a failed assertion
  is silent".
- As written, the protection does not compile. An implementer who resolves the cycle by deleting the `legacy`
  reference keeps a test that checks nothing.

**Fix:** State the pattern once, in 03 §3.3 "Legacy-only identifiers", and have every owner cite it. Replace
03 l.371–373 with:

> "An owner's `export_test.go` (in the owner's package) exports each mirror as a test-only alias (`type
> CoveringMirror = covering`) and each unexported record-ID helper as a test-only variable. A second file,
> `legacy_mirror_test.go`, in the owner's **external** test package (`package <owner>_test`, which may
> import `legacy`), asserts `var _ legacy.X = (<owner>.XMirror)(nil)` and `var _ <owner>.XMirror =
> (legacy.X)(nil)` and compares the helpers on a table. For the executor this is
> `internal/executor`'s external test package."

Then:

- 05-config l.494, l.1801; 05-metadata l.76, l.603, l.907, l.1275; 05-notify l.194, l.331; 05-library l.260;
  05-tasks l.1299; 06 l.1456: replace "export_test.go assertion" with "the mirror test (03 §3.3)".
- Move the three named tests to `legacy_mirror_test.go`.
- 05-tasks: name the package as `internal/executor`.

**Trade:** None. One extra small file per owner.

### SIMP7-2 — MAJOR — Root suspension and confirmation are built at S7 with no consumer

**Where:**

- 05-library l.118: `ReadShares []storage.Owner`.
- l.133–139: `RootLegacyState`, "ReadShares Widening … `Suspended bool // ReadShares not applied until
  re-confirmed (legacy.ExtConfirmer); reported in health`".
- l.273–274: `List` "a suspended root's Legacy.Suspended true; library withholds its ReadShares".
- l.290–295: books' source implements `legacy.ExtConfirmer` with `id = string(RootID)`, and
  `legacytest.RootSourceSuite` runs the `ExtConfirmer` case.
- l.594–597: health `library_root_extension_suspended` and the route
  `/api/core/v1/library/roots/{id}/confirm`.
- l.727: `TestRootExtensionStaleAfterPrevWrite` ("`ReadShares` suspended until confirmed").
- 03 l.399–400: "for integration instances and import roots, their `confirm` routes". 05-storage l.1331:
  "a root ID".

**Evidence:**

- The root record's **only** Widening field is `ReadShares` (05-library l.133–135). Sentinel is Derived;
  Watch, Quarantine, AppDir and MinFree are Independent. Under DATA5-4 (05-storage l.1329), "only a record
  whose type has at least one Widening field is ever suspended".
- `ReadShares` names other modules that may read a root (l.580: "Read: owner + `ReadShares`"). Core holders
  of `Manager` read without shares (l.580).
- ABO has one module during S0–S10. 03 R3 (l.512): "for ABO there is one module, so the rule is vacuous until
  a second exists".
- So a books import root's `ReadShares` can name no second module, and suspending it withholds nothing.
  `examples/hello` cannot consume it either: `legacy` is off-limits to hello (R2), and config-entry roots
  carry no extension record.
- At HEAD the legacy row has no share concept: `ImportPath{ID, Path, Name, Enabled, CreatedAt, LastScan,
  BookCount}` (`internal/database/store.go:596-604` @748fd787a).
- The mechanism is not small. It adds:
  - an exported field (`RootLegacyState.Suspended`);
  - a capability implementation and its suite case;
  - an HTTP route with a permission, interactivity and audit;
  - a health token;
  - half of a test.
- 03 §3.4 (l.456–457) forbids "a member whose only consumer is deferred", and here the consumer (a second
  module sharing a legacy-served root) is not even planned. 09 row 13 (l.161) already lists only roles and
  instances as suspendable. 03 l.399–400 adds import roots.

**Fix:**

1. 05-library §5L.3.1: "A legacy-served root takes no `ReadShares`: `RootSource.Create`/`Update` refuse a
   non-empty list with `ErrLegacyRootShares` (the legacy row cannot express it, and ABO has one module).
   The root's extension record therefore has no Widening field and is never suspended (DATA5-4)."
   `RootLegacyState` keeps only `Ext`.
2. Add a `Deferred-build members` table to 05-library. First column: `library.RootLegacyState.Suspended`.
   "Also deferred (not checked)": books' root `ExtConfirmer`; the route
   `/api/core/v1/library/roots/{id}/confirm`; the health token `library_root_extension_suspended`; the
   `ExtConfirmer` case of `legacytest.RootSourceSuite`; the `ReadShares` half of
   `TestRootExtensionStaleAfterPrevWrite`. Land with: "the first app with two modules that shares a
   legacy-served root".
3. 03 l.399–400: "for integration instances, their `confirm` route". 05-storage l.1331: drop "a root ID".
4. 05-library l.594–597: delete the suspension sentence (keep the Derived sentinel reset).

**Trade:** None for ABO. A future multi-module app with pre-core roots adds the field back additively.

### SIMP7-3 — MINOR — Three versions of the "stays in the SDK" list, none complete

**Where:**

- 03 l.377–390: "Four kinds of legacy-only identifier cannot move". They are: record-type state; value types
  and owner format readers (`secrets.LegacyCopies`, `LegacyCheck`, `ReadLegacyKey`); "the owner entry points
  … — `integrations.Registry.Confirm` … and … `providerhttp.Options.Legacy`"; and `Manifest.LegacyStores`.
- 05-storage l.1331: "**Two things** therefore stay in owner packages … record type … and the
  `module.Manifest.LegacyStores` field". l.1355–1361 adds a "Kept outside `legacy`" list (`LegacyRange`,
  `Claim`, `ErrLegacyStoreMissing`, `ExtState`, `LegacyGuardProjection`, `LegacyMigration`).
- 14 K23 l.44: four kinds, the same two entry-point examples.
- 03 l.364–365 says "05-storage §5S.3.11 'Legacy capabilities' owns the full list", but 05-storage's list
  is the shortest of the three.

**Evidence:** These legacy-serving SDK members are missing from all three lists:

- `secrets.Options.LegacyStore` (05-config l.1784), plus `secrets.SourceLegacy`, `secrets.LegacyForm` and
  `Value.LegacyForm` (l.1754–1775);
- `auth.Service.ConfirmAPIKey` (05-authn l.344);
- `rbac.Service.ReconfirmRole` (05-rbac l.348);
- the confirm routes;
- library's input for the root source (SIMP7-4).

A reader can no longer tell what the cutover leaves behind, which is the cost SIMP6-2 asked to surface.

**Fix:**

- 05-storage §5S.3.11: replace "Two things therefore stay" and the "Kept outside `legacy`" sentence with one
  table, "Legacy-serving identifiers that stay in SDK packages", with columns *identifier*, *why it cannot
  move* and *after the cutover*. The reasons are: named by a lower-layer type; owner's own format;
  permanent guard; owner entry point. The after-cutover values are: inert nil/zero, answers 409, or stays
  live.
- Rows: every item above plus 03's and K23's.
- 03 l.377–390 and K23 then say "the kinds are listed in 05-storage §5S.3.11 (one list)" and keep at most
  one example each.

**Trade:** None.

### SIMP7-4 — MAJOR — One SDK field exposes a mirror, and one input is unspecified

**Where:**

- 05-config l.1784: `LegacyStore legacyStore // … a legacy.SecretsStore … set by the kernel` in the exported
  struct `secrets.Options`.
- l.1798–1802: `legacyStore` is "secrets' unexported mirror of legacy.SecretsStore", kept identical by
  the mirror test.
- 05-config l.1492: the settings cutover removes "the store's capabilities from package `legacy` (R6-3; not
  a breaking change)".
- 05-library l.258–259: "The kernel hands the built source to library's constructor **as an opaque
  value**". No library constructor or options type is specified anywhere in 05-library.

**Evidence:**

- Go lets another package set a field whose type is unexported. So `secrets.Options.LegacyStore` is an SDK
  member, and its type is the mirror.
- The mirror test forces `legacyStore` to track `legacy.SecretsStore` exactly. So any change to
  `SecretsStore`'s method set, which R6-3 says is free in a minor, changes the type of an exported field of
  the frozen `secrets` package.
- At the settings cutover, either the field stays (and with it the mirror, so `SecretsStore`'s contract is
  frozen in v1) or it is deleted (an SDK break under 12 §12.4.2).
- 05-config l.1492's "not a breaking change" holds for `legacy` only.
- This is SIMP6-2's problem recurring for one capability. The other owners (auth, rbac, tasks,
  integrations, notify, providerhttp) use their mirror only in an internal type assertion, so nothing is
  exposed.
- For library, "opaque value" leaves the exported entry point undefined. The kernel cannot name library's
  unexported mirror, so the parameter must be `any`, the mirror itself (exposed as above), or an exported
  interface, and the last is the frozen `library.RootSource` that SIMP6-3 (b) removed.

**Fix:** One rule for an owner that receives a legacy-only value with no SDK interface (secrets, library),
stated in 03 §3.3 and applied in both files.

- **(a) Recommended.** The field is typed `any`: `secrets.Options.LegacyStore any` and
  `library.Options.LegacySource any`. The owner asserts its mirror at construction and returns
  `ErrLegacyStoreType` (boot fails) on a mismatch, never a silent skip. The kernel always passes a value
  built by a typed `legacy.Stores` constructor, and the mirror test (SIMP7-1) catches drift in CI. The
  `legacy` method set then changes freely, and the field stays in v1, inert (nil) after the cutover.
- **(b)** Keep the mirror-typed field, and state in 05-config §2.3, 05-library and K23 that
  `legacy.SecretsStore`'s method set is frozen with `secrets.Options` until a major.

Either way:

- list the field in SIMP7-3's table;
- fix 05-config l.1492 to "removing the store's capabilities from package `legacy`, not
  `secrets.Options.LegacyStore`";
- specify library's constructor (`library.New(opts Options)`) in 05-library §5L.3.

**Trade:** (a) costs one `any` field per owner (two) and a boot-time type check. (b) is text only, but it
gives up R6-3's freedom for the secrets capability.

### SIMP7-5 — MINOR — Capabilities are detected twice, and the kernel's record has no consumer

**Where:**

- 03 l.368–369: "The kernel detects each capability by type assertion once at P4c (06) and hands the store to
  its owner". The owner then asserts its own mirror (l.369–371).
- 06 l.1453–1456: "the kernel detects each capability at P4c … **and records what it found**
  (`TestLegacyStores`)".
- 05-tasks l.1296–1297, 05-authn l.658–659 and 05-rbac l.467–468 repeat it.
- 06 l.1766: `TestLegacyStores` checks members, backends, handles and disabled modules, and nothing about
  capabilities.
- 05-storage l.1331: "so a capability's signatures name only types of its owner's layer or lower".

**Evidence:**

- The kernel needs the assertion only for the two capabilities it **drives**, `ConfigOpener.Open` and
  `ConfigSecrets.Secrets()` (05-config l.491–493).
- For every other capability the owner re-detects it on the value it is handed. The kernel's record is read
  by no test, route, log line or health token in any file.
- Separately, 05-storage's absolute rule is false for exactly those two driven capabilities:
  `ConfigOpener.Open` takes `legacy.ConfigOpenEnv`, and `ConfigSecrets.Secrets()` returns
  `legacy.SecretsStore` (05-config l.530–535). 05-config states the exception, and 05-storage does not.

**Fix:**

- 03 l.368, 05-storage l.1331, 06 l.1453–1456 and the owners: "The kernel asserts only the capabilities it
  drives (`legacy.ConfigOpener`, `legacy.ConfigSecrets`); every other capability is detected once, by its
  owner, through its mirror." Delete "records what it found".
- 05-storage l.1331: "a capability's signatures name only types of its owner's layer or lower, except the
  two the kernel drives, whose signatures may name `legacy` types (05-config §1.3.3)".

**Trade:** None.

### SIMP7-6 — MINOR — `storage_extension_suspended` duplicates the owners' tokens and has no producer

**Where:**

- 05-storage l.1329 (last sentence): "Health reports suspended records per store
  (`storage_extension_suspended`, with counts)". l.1748: the same token.
- 05-metadata l.737–738: "counts in 05-storage's `storage_extension_suspended`".
- Per-owner tokens: `auth_extension_suspended` (05-authn l.1568), `rbac_role_suspended` (05-rbac l.1333),
  `integration_local_suspended` (05-metadata l.836), `library_root_extension_suspended` (05-library l.596).

**Evidence:**

- Since R6-3, `Extension[T]` is `legacy.Extension` (05-storage l.1343, l.1420–1446), at L5, called by app
  code (books' stores).
- Its methods take no reporter. `storage` (L1) never observes a `Get` that returns `ExtStale` and cannot
  import `legacy`.
- No file defines who counts or publishes the generic token, and 05-health does not list it. So the generic
  token has no producer, and the condition it describes is already reported by four owner tokens.

**Fix:** Delete `storage_extension_suspended`:

- 05-storage l.1329: replace the last sentence with "Each owner reports its suspended records under its own
  health token (05-authn, 05-rbac, 05-metadata; 05-library's goes with SIMP7-2)".
- 05-storage l.1748: name "the owner's token".
- 05-metadata l.738: delete the parenthesis.

**Trade:** None.

### SIMP7-7 — MINOR — `storage.LegacyMigration` and `Spec.Legacy`: a contradictory reason, then dead SDK

**Where:**

- 05-storage l.566 (`Spec.Legacy []LegacyMigration // books only`), l.569 (`LegacyGuard
  *LegacyGuardProjection // books only`), l.707–716.
- l.1360–1361: "Kept outside `legacy` … `storage.LegacyGuardProjection` and `storage.LegacyMigration`
  (books' pre-guard projection and HEAD-style steps, **which go with their own floor**)".
- l.1982: the legacy steps "are deleted only in the floor release".
- 09 l.254–278: the floor release is T8, at S5.

**Evidence:**

- Both are identifiers that exist only for an app's pre-core data, so R6-3's rule would put them in
  `legacy`. The real reason they cannot move is that `storage.Spec` (L1) names them. "Go with their own
  floor" instead implies a removal, which after v1 is a break (12 §12.4.2).
- The two identifiers also differ:
  - `LegacyGuardProjection` is a permanent guard. Pre-guard builds exist forever, so it never goes.
  - `LegacyMigration` and `Spec.Legacy` lose their last consumer at the S5 floor release, because books
    deletes every step 1..`LegacyCap`, and the floor refuses any store below it. Yet nothing schedules
    deleting the type and the field before the split. They would be frozen in v1 with no consumer, along
    with `TestNoNewLegacyMigrations` and the runner's implicit "legacy phase first" edge (l.752).

**Fix:**

- 05-storage l.1360–1361: "`storage.LegacyGuardProjection` and `Spec.LegacyGuard` stay permanently
  (`storage.Spec` names them; pre-guard builds never go away). `storage.LegacyMigration`, `Spec.Legacy`, the
  runner's legacy phase and `TestNoNewLegacyMigrations` are deleted by the floor release's T8 PR (S5, v0,
  before the split); `UpgradeFloor` and the version-source adapter that reads books' applied version stay."
- Add that deletion to 09's T8 row (l.1061) and 04 §8.1's S5 row.
- List `LegacyGuardProjection` in SIMP7-3's table.

**Trade:** None. The deletion is free at v0.

### SIMP7-8 — MINOR — The naming rule is applied inconsistently

**Where:** 03 l.359–364 ("drops any `Legacy` prefix and takes its former package's name **where it would
otherwise be ambiguous**"). 05-storage l.1341–1353.

**Evidence:**

- `legacy.AuthReconciler` and `AuthSignIns` are prefixed, while `legacy.AtomWriter` (rbac) and
  `legacy.RootSource` (library) are not. Yet the same file has `legacy.LibraryCoverID`.
- `legacy.TaskRecoverer` uses a singular prefix, while the suite is `legacytest.TasksRunStoreSuite` and the
  package is `tasks`. 03's history bullet l.945 records a flip from `TasksRecoverer`.
- `RootLegacyState` (05-library l.129, l.136) breaks 03's `<Type>LegacyState` rule (l.379): the type is
  `RootSpec`. The siblings follow the rule (`InstanceConfigLegacyState`, `StoredInstanceLegacyState`,
  `RoleLegacyState`).
- `TestNativeRootStore_RoundTrip` (05-library l.728, 05-storage l.1325) names a store that SIMP6-3 (b) says
  does not exist.
- "Where ambiguous" is a judgement, so each owner judged differently.

**Fix:**

- 03 l.359–364: "an owner-specific identifier takes its former package's name in Go initialism form (`Auth`,
  `RBAC`, `Tasks`, `Config`, `Secrets`, `Library`, `Integrations`, `ProviderHTTP`); a cross-owner one
  (`Stores`, `Handles`, `Extension`, `ExtRecord`, `ExtConfirmer`, `Covering`) takes none".
- Renames: `legacy.AtomWriter` → `legacy.RBACAtomWriter`; `legacy.RootSource` → `legacy.LibraryRootSource`;
  `legacy.TaskRecoverer` → `legacy.TasksRecoverer`; `RootLegacyState` → `RootSpecLegacyState`;
  `TestNativeRootStore_RoundTrip` → `TestConfigEntryRoots_RoundTrip`.
- Alternatively, keep the names and replace "where ambiguous" with "as listed in 05-storage's table".

**Trade:** Renames only, in text that is not yet code.

### SIMP7-9 — MINOR — Wrong section cited for the config basis deferral

**Where:** 03 l.455 and l.929 ("05-config §1.12.3"); README l.62 ("[05-config] §1.12.3"). The heading is
05-config l.1581 `#### 1.12.5 Deferred-build members`. §1.12.3 is the key map (l.1498), and 05-config itself
cites §1.12.5 (l.1248).

**Fix:** Change §1.12.3 to §1.12.5 in 03 l.455 and README l.62. The history bullet at 03 l.929 may stay.

### SIMP7-10 — MINOR — 03's one list omits `tasks.StandDownStore`

**Where:**

- 03 l.416 (tasks row): "`tasks.Stores` (`RunStore`, `ClockStore`, `PauseStore`)"; persists "runs, queues,
  checkpoints, schedule clocks, the pause marker".
- 03 l.281 (`tasks` package row): "schedule clocks and the pause marker".
- The other files have it: 05-tasks l.48, l.1109 (`StandDown StandDownStore // MOD6-4`), l.1252–1256,
  l.1454; 06 l.1424 ("scan stand-down marker"); 09 l.378 (`StandDownStore`); 04 l.1328.

**Evidence:** 03 calls the table "the **one list** of store interfaces: other files cite … instead of
restating it" (l.408). MOD6-4 added the fourth interface everywhere except in the list.

**Fix:**

- 03 l.416: "`tasks.Stores` (`RunStore`, `ClockStore`, `PauseStore`, `StandDownStore`)". Add "stand-down
  markers" to "What it persists".
- In the ABO column add "and the scan stand-down marker `registry.scan_standdown` (MOD6-4)".
- 03 l.281: add "stand-down markers".

### SIMP7-11 — MINOR — Generated and stale text residues

**Where and evidence:**

- 04 l.1609 (Objections item 1), generated text:
  - It says `integrations/ai/aitest` and `log/logtest` are "native contract only, the legacy cases are in
    `legacy/legacytest`". But `ai.JobStore` and `log.JournalStore` have no capability, so 05-storage's suite
    list (l.1353) has no AI or log legacy suite. That matches its own rule: "each store interface whose
    legacy implementation can have a capability has one legacy suite".
  - The same item has a broken entry, "`auth/oidc` (…); `app.BinaryID` delegates to it (06))". The
    `buildid` entry's head was dropped, and the tail was left behind.
- 05-storage l.1331: "with the owner's kind-qualified record ID (`apikey/<id>`, `role/<name>`, an instance's
  **or rule's** cover ID, a root ID)". Notify's legacy rule store keeps no extension record (09 l.384:
  "What books' extension family holds: —"), and 05-notify defines no confirm path.

**Fix:**

- Make 04's generator emit "the legacy cases are in `legacy/legacytest`" only for interfaces that 05-storage
  l.1353 lists, and fix the `buildid` entry, or drop it, since 03 l.274 now lists `buildid`.
- 05-storage l.1331: delete "or rule's" (and "a root ID" per SIMP7-2).

---

## Notes (no finding)

- **Deferred-build boundary, rest of S0–S10.** I checked the S7 members of `legacy`:
  - `legacy.LocalCandidate` (consumer: books' S7 migration, 05-metadata l.945);
  - `legacy.IntegrationsCoverID` and `legacy.ProviderHTTPCoverID` (books' `Covers`);
  - `legacy.Stores.NotifyRules` (the one legacy-backed webhook rule, 09 l.384);
  - `legacytest.CoveringCases`, `ExtConfirmerCases` and `ExtensionSuite`.

  Each has an S0–S10 consumer. The only member without one is SIMP7-2.
- **`legacy` lands at S2 for one identifier** (`ConfigExternalWriter`, deleted in the S3 PR; 06 l.1338,
  05-storage l.1347). This is acceptable: the package is needed from S2b anyway, and at v0 the churn is free.
- **Layering.** `legacy` (L5) imports `config`, `secrets`, `storage`, `auth`, `rbac`, `tasks`, `log`,
  `library`, `integrations`, `integrations/ai`, `integrations/notify` and `providerhttp`, all of them L0–L4
  (03 R4 l.513). `module` (L7) imports it for `Manifest.LegacyStores`. No owner imports it. The layer
  claims hold apart from SIMP7-1's test files.
