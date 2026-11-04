<!-- file: docs/plans/platform-core/raw/03-layering-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: 54977ca8-da36-4f09-9ae4-44a249ab4e6d -->
<!-- last-edited: 2026-11-03 -->

# 03 - Layering guard (`internal/arch/layering_test.go`) verification

All anchors are at commit 748fd787a, file `internal/arch/layering_test.go` (597 lines, header version 1.4.14, `layering_test.go:2`) unless another path is given. `go test ./internal/arch/` passes at this commit (run by this scout, about 10 s).

## 1. What the test is

`TestLayering` (`:383`) loads the module import graph with one `go list` call, then enforces: package-count floor, one positive-control edge, `layerOf` completeness, `layerOf` equals the computed layer, no wrong-way edge unless listed in `allowed`, every `allowed` edge still exists and is still wrong-way. The doc it points to, `docs/architecture/layering.md`, exists (referenced at `:403`).

## 2. Verified claims

| claim | verified value | anchor |
|---|---|---|
| Package-count floor "claimed >= 150" | `const minModulePackages = 150`; enforced as `len(graph) < minModulePackages` then `t.Fatalf("the loader is broken, not the code: ...")`. Actual package count at this commit: **213**, so headroom is 63 packages (about 30%). | `:38`, `:387-390` |
| internal/server -> internal/database control edge | `var positiveControlEdge = edge{"internal/server", "internal/database"}`; test fails with "edge ... not found" if `graph["internal/server"]` lacks `internal/database`. The edge exists today (verified in `go list` data). | `:40`, `:391-394` |
| Loader | `exec.Command("go", "list", "-json=ImportPath,Imports", "./...")` with `cmd.Dir = root`; decodes only `ImportPath` and `Imports` (non-test imports, default build tags, no `-tags` flag); keeps only import paths with prefix `modulePath` (via `rel`). | `:339-381`, `:341-342`, `:357`, `:364-373` |
| `modulePath` | `const modulePath = "github.com/falkcorp/audiobook-organizer"` (equals root `go.mod:1`) | `:33`; `rel()` at `:326-336` |
| `repoRoot` | walks up from the working directory to the first directory containing `go.mod` | `:306-322` (check at `:313`, failure at `:318`) |
| layerOf completeness requirement | (i) every package in the graph must have a `layerOf` key ("package %q has no entry in layerOf"); (ii) every `layerOf` key must be a package in the graph ("layerOf lists %q, which is not a package in the module: remove the entry"), so deleted or moved packages must leave the map. `layerOf` has **213 entries**, equal to the 213 packages (verified: set difference empty both ways). Map spans `:59-286`. | `:399-411`, `:59-286` |
| layerOf must equal computed layer | message "layerOf[%q] is %d but the classification rule computes %d ... A layer change needs an entry in layerOverride" | `:413-430`, message `:424` |
| Wrong-way edge rule | `to > from` and edge not in `allowed` gives "layering violation: ..." | `:432-452`, message `:445` |
| `allowed` is a ratchet | listed edge that no longer exists is an error; listed edge that is legal (`to <= from`) is an error | `:453-466`, messages `:456`, `:463` |
| Build tags considered | **none**: default tags only (no `-tags` in the `go list` call at `:341`); test files excluded (`Imports`, not `TestImports`). Edges that exist only under tags are invisible. Verified by this scout: with `-tags bench` the graph gains `cmd -> internal/dedup` and `cmd -> internal/ai` (both legal, layer 6 importing lower); `native_taglib`, `pprof`, `integration`, `embed_frontend` add no in-module edges. No package exists only under a tag (213 under every tag set tried). | `:341`; tag files listed in raw/01 |

### Layer table (from the `layerOf` header, `:46-48`)

0 leaf, 1 config, 2 storage, 3 domain, 4 jobs, 5 transport, 6 entry. Counts from `layerOf`: layer 0 = 63 packages, 1 = 1, 2 = 3, 3 = 78, 4 = 20, 5 = 30, 6 = 18.

### Classification rule (`computeLayers`, `:561-596`)

Order: `layerOverride` (`:541-546`), then `namedLayer` (`:516-537`), then `familyLayer` (`:549-559`: `internal/plugins/**` = 4, `internal/server/**` = 5, `cmd`, `cmd/*`, `tools/cmd/*` = 6), then no in-module imports gives 0 (`:578`), else max(layer of in-module imports) floored at 3 (`:586`). Most "domain" packages get layer 3 simply by importing anything; `layerOf` is derived, not free-form.

### `layerOverride` (every entry, `:541-546`)

| package | layer | reason in test | anchor |
|---|---|---|---|
| `internal/matcher` | 0 | imports only `internal/personname` (layer 0); namedLayer says domain (3) | `:542-543` |
| `internal/fingerprint` | 0 | imports only `internal/audioutil` (no module imports); namedLayer says domain (3) | `:544-545` |

Both exist because `internal/database` (layer 2) imports them (`internal/database/pebble_store.go:31,33`); without the override that would be a wrong-way edge.

### `allowed` (every entry, `:290-303`; all 12 verified to exist and to violate layer order at this commit)

| # | importer (layer) -> imported (layer) | reason given | anchor |
|---|---|---|---|
| 1 | `internal/config` (1) -> `internal/aidispatch` (3) | config reaches a package that floors at domain; 07-M1 makes config a leaf | `:291` |
| 2 | `internal/config` (1) -> `internal/auth` (3) | config imports auth (which imports database); 07-M1 | `:292` |
| 3 | `internal/config` (1) -> `internal/backup` (3) | config reaches a package that floors at domain; 07-M1 | `:293` |
| 4 | `internal/config` (1) -> `internal/database` (2) | config reads the store directly; 07-M1 | `:294` |
| 5 | `internal/config` (1) -> `internal/dedup/unified` (3) | config reaches a package that floors at domain; 07-M1 | `:295` |
| 6 | `internal/dedup` (3) -> `internal/operations/registry` (4) | domain package registers operations itself; move registration into plugins/dedup | `:296` |
| 7 | `internal/metadata` (3) -> `internal/operations/registry` (4) | provider clients import the operations registry; 07-M4 | `:297` |
| 8 | `internal/plugins/itunes` (4) -> `internal/itunes/service` (5) | itunes/service imports realtime (transport), so it computes to layer 5 | `:298` |
| 9 | `internal/plugins/maintenance` (4) -> `internal/itunes/service` (5) | same | `:299` |
| 10 | `internal/reconcile` (3) -> `internal/operations/registry` (4) | domain package registers operations itself | `:300` |
| 11 | `internal/reconcile` (3) -> `pkg/plugin/sdk` (4) | pkg/plugin/sdk imports the operations registry, so it computes to layer 4 | `:301` |
| 12 | `internal/repairs` (3) -> `internal/operations/registry` (4) | domain package imports the operations registry | `:302` |

Platform-relevant reading: 5 of 12 entries are `config` reaching up (the `07-M1` leaf-config work is unfinished at this commit: `config` still imports database, auth, backup, aidispatch, dedup/unified); 5 entries are domain packages importing the operations registry or the SDK that sits on it (rows 6, 7, 10, 11, 12), so the task/job system is not yet below the domain layer; 2 are plugins reaching `itunes/service`.

### `namedLayer` (`:516-537`)

Mirrors the layer table of `docs/architecture/layering.md`. Notable entries: layer 0 includes `internal/models`, `logger`, `logging`, `metrics`, `cache`, `util`, `pathutil`, `httputil` (`:518-521`); layer 1 `internal/config` (`:523`); layer 2 `database`, `openlibrary`, `search` (`:525`); layer 4 `operations/registry`, `scheduler`, `maintenance/jobs` (`:532`); layer 5 `realtime`, `syncapi` (`:533`; `internal/syncapi` itself is not a package, only `syncapi/conformance` and `syncapi/progress` exist and compute to 0).

## 3. What breaks if packages move to a nested Go module (different module path)

Assume packages move to a directory with its own `go.mod` and module path P (a platform-core module), and the audiobook app imports them via P (with `replace` or a published version). Effects, in the order the test hits them:

1. **Moved packages vanish from the graph.** `go list ./...` run from the root (`:341-342`) does not descend into a directory that has its own `go.mod` (standard Go tooling behavior). Even if it did, `rel()` (`:326-336`) returns false for any import path not under `modulePath`, so the package is skipped at `:364-367` and dependency edges to it are dropped at `:369-373`.
2. **`layerOf lists "X", which is not a package in the module: remove the entry`** fires once per moved package (`:406-411`). The test hard-fails until every moved key is deleted from `layerOf`, and the moved packages are then **no longer layer-checked at all**.
3. **Package-count floor.** Floor is 150 (`:38`, `:387-390`) against 213 today: moving more than 63 packages makes the loader "broken" and the whole test fails with `t.Fatalf`. Moving only the 59 packages classified generic in raw/01 leaves 154 (margin 4); moving generic plus mixed (109) leaves 104 and fails.
4. **Positive control.** `internal/server -> internal/database` (`:40`, `:391-394`) fails immediately if `database` moves; the control edge must be re-pointed at an edge that stays inside the root module.
5. **Computed-layer drift.** Unnamed packages take max(layer of in-module imports) floored at 3 (`:578-586`), so dropping edges to moved packages changes importers' computed layers. A package that is layer 4 only because it imports `internal/operations/registry` would compute to 3 if registry moved, giving `layerOf[...] is 4 but the classification rule computes 3` (`:424`). Packages that import nothing in-module compute 0 (`:578`).
6. **`allowed` ratchet.** Each `allowed` entry whose importer or imported package moved no longer exists in the graph, giving "allowed lists A -> B, but that import no longer exists: remove this entry" (`:453-458`). Rows 1-5 (config -> database/auth/backup/aidispatch/dedup-unified) and rows 6, 7, 10, 11, 12 (-> registry / sdk) would trip if config, registry or sdk moved.
7. **No cross-module direction check.** The wrong-way rule (`:432-452`) only compares `layerOf` values of two keys that are both in the graph (`:438-441`, the `!okFrom || !okTo` skip). Edges from the app into the platform module are not validated, and neither is the forbidden reverse direction (platform importing app). The guard must load both modules and key `layerOf` on full import path or (module, relative path).
8. **`repoRoot` and `modulePath` are single-module.** `repoRoot` stops at the first `go.mod` found walking up (`:313`); if the test file moved into the nested module it would pick the nested `go.mod` while `modulePath` (`:33`) still names the app module, so `rel()` would return false for every package, `len(graph)` would be 0 and the floor check would fail.
9. **Workspace constraint from CLAUDE.md.** A two-module checkout would normally use `go.work`, but `CLAUDE.md:112-118` documents that `go work init` breaks `go build ./...` here (ambiguous `genproto` imports). The nested module would therefore be wired with `replace` directives; the test never sees through a `replace`, only through import-path prefixes.
10. **Other hard-coded module-path users** (not in `layering_test.go`, but they fail in the same move): `tools/cmd/oplint/analyzer.go:45` (`modulePath`), `tools/cmd/sdkguard/main.go:66` (`.../internal/` prefix) and its allow-list `:77-80`, `tools/cmd/oplint/analyzer_test.go:22,34-35`, and the roughly 12 fixture packages under `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...`.

## 4. What breaks if packages move out of `internal/` (same module)

Example: `internal/logger` -> `platform/logger` (same `go.mod`, new import path). Nothing in the test checks the `internal/` prefix semantically, but the **string keys** are all `internal/...`-prefixed, so:

1. `package "platform/logger" has no entry in layerOf` and `layerOf lists "internal/logger", which is not a package in the module` (`:401-411`). `layerOf` keys must be renamed (the table already holds non-`internal/` keys such as `pkg/ops` at `:211` and `pkg/plugin/sdk` at `:233`, so that form is supported).
2. `namedLayer` (`:516-537`) and `layerOverride` (`:541-546`) are keyed by the old paths; the renamed package silently loses its named or override layer and falls to the import-derived rule (`:578-586`). Examples: `internal/config` (named 1, `:523`) would compute as 3 after a rename because it imports other packages, giving `layerOf[...] is 1 but the classification rule computes 3` (`:424`); `internal/database` (named 2, `:525`) would compute 3; the `internal/matcher` and `internal/fingerprint` overrides (`:543`, `:545`) stop applying, which makes `database` import layer-3 packages (a violation not covered by `allowed`).
3. `familyLayer` (`:549-559`) is prefix-based on `internal/plugins/`, `internal/server/`, `cmd/`, `tools/cmd/`; a package moved out of those families loses the 4, 5 or 6 assignment.
4. `allowed` (`:290-303`) and `positiveControlEdge` (`:40`) use the old strings and fail the "no longer exists" check (`:453-458`) or the positive control (`:391-394`).
5. The `.`, `cmd` and `tools/cmd` keys are unaffected.
6. Go's `internal/` visibility rule is not exercised by this test. Moving packages out of `internal/` makes them importable by external modules (relevant to the platform goal) and the layering test would not notice. `pkg/` is already used for that purpose today (`pkg/ops`, `pkg/plugin/sdk`), but those packages import `internal/database` and `internal/auth` (see raw/02 section (e)).
7. Sibling guards in `internal/arch/` also hard-code `internal/...` file paths and package keys and would need updating in the same move: `internal/arch/opstest_guard_test.go:28,36`, `internal/arch/journal_direct_writers_test.go:36,41,47,50`, `internal/arch/hashcost_guard_test.go:28`, `internal/arch/filtered_getter_callers_test.go:46-57`, `internal/arch/resume_restart_guard_test.go:221-235`.

## 5. Minimal guard changes implied (inference, not implemented)

- Replace `modulePath` (`:33`) with a list of module prefixes (app plus platform) and make `rel()` return module and relative path; run `go list` in each module root, or `go list -deps` from the app root, so cross-module edges are visible.
- Key `layerOf`, `namedLayer`, `layerOverride` and `allowed` on full import paths (or add a module qualifier); derive `familyLayer` from a table instead of hard-coded `internal/` prefixes.
- Re-base `minModulePackages` (`:38`) per module and re-point `positiveControlEdge` (`:40`) at one edge per module plus one cross-module edge.
- Decide the platform module's layer numbers explicitly (it would occupy layers 0-4 below the app's layer-3 domain packages) and add the rule "platform module must not import the app module".
- Add build-tag sweeps (`-tags bench`, `native_taglib`, `embed_frontend`) if edges under those tags should be guarded; today they are not (`:341`).
