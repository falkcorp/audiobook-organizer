<!-- file: docs/specs/platform-core/04-package-mapping.md -->
<!-- version: 1.13.0 -->
<!-- guid: a52b0af6-8e36-41c8-a78d-94859f6cac27 -->
<!-- last-edited: 2026-11-05 -->

# 04 — Package mapping

Every one of the **213** packages of the root module, mapped to exactly one target bucket, with
the reason and the import-graph evidence that forces the decision. This file applies the
promotion rule of [03-architecture](03-architecture.md) §3.4 and uses its names, package tree
and rules R1–R8. Decisions are fixed by [02-decision-record](02-decision-record.md) (D1–D9,
N1–N15; decision 13 as extended by round-3 resolution R3-1 and refined by round-4 resolution R4-1).

Evidence commit: `748fd787a` (the spec branch changes no code: `git diff 748fd787a HEAD` touches only
`docs/` and one `todo.d/` fragment). Raw inputs: [raw/01](../../plans/platform-core/raw/01-package-inventory.md)
(authoritative package list), [raw/02](../../plans/platform-core/raw/02-import-graph-hotspots.md),
[raw/03](../../plans/platform-core/raw/03-layering-guard.md),
[raw/04](../../plans/platform-core/raw/04-database-store.md),
[raw/05](../../plans/platform-core/raw/05-storage.md),
[raw/06](../../plans/platform-core/raw/06-tasks-ops.md),
[raw/07](../../plans/platform-core/raw/07-plugin-registry-sdk.md),
[raw/09](../../plans/platform-core/raw/09-http-api.md),
[raw/10](../../plans/platform-core/raw/10-auth-rbac.md),
[raw/11](../../plans/platform-core/raw/11-integrations.md),
[raw/12](../../plans/platform-core/raw/12-cross-cutting.md).

## 1. Summary

| bucket | packages | non-test LOC | test LOC |
|---|---|---|---|
| Core public (SDK) path | 13 | 14430 | 13072 |
| Core internal path | 5 | 22989 | 33616 |
| Core `shared/` path | 0 | 0 | 0 |
| Merge into another package | 20 | 8749 | 11201 |
| ABO app path (stays in the root module) | 164 | 623802 | 731725 |
| Delete | 11 | 34861 | 3115 |
| **total** | **213** | **704831** | **792729** |

Of the 20 merge rows, 19 merge into a core package and 1 into an ABO package. Estimated non-test LOC that ends up in core (whole core-bucket and core-merge packages minus the files their split lists keep in ABO or delete, plus the files ABO packages' split lists send to core), counted per file and split as §10 splits it (MOD8-7): **18261** LOC of moved files, which land verbatim apart from §2.2's mechanical rewrites, plus **43177** LOC of ABO source in re-implemented parts (§8.6), which core rewrites and which is shown for scale only, of 704831 (61438 together, 8.7%). File-level detail for every split is in the row's *Split* cell.

| bucket | generic | mixed | audiobook |
|---|---|---|---|
| Core public (SDK) path | 10 | 3 | 0 |
| Core internal path | 2 | 3 | 0 |
| Core `shared/` path | 0 | 0 | 0 |
| Merge into another package | 18 | 2 | 0 |
| ABO app path (stays in the root module) | 26 | 36 | 102 |
| Delete | 3 | 6 | 2 |

Headline decisions:

1. **`internal/database` is split, not moved, and core never reads its rows.** Of
   `database.Store`'s 445 methods, **108** sit behind core contracts (tasks 39, auth 33, storage 11,
   AI jobs 10, rbac 6, library 6, log 3) and **337** stay with the books module (314 domain + 23 HEAD
   ops-v1). Only the 11 storage-engine methods move into core `storage`. The other **97** are HEAD's code over
   the families that core services take over. Core never calls them: each core service runs its own native
   store over `c/<subsystem>/` and exports an importer (the one list is 03 §3.3 "Store interfaces and
   importers"; this file does not restate it, SIMP5-8). In R2, books' cutover steps M2–M6 decode HEAD's rows
   with that code, hand the records to the importers and delete the rows (03 §3.8), so core holds no audiobook
   key name or row shape (03 R1(c)). The steps live in the new ABO package `internal/books/cutover`, which
   imports `internal/config`, the residue and `internal/operations/state` and is imported only by the
   composition root (03 §3.5.1; MOD5-2, R5-2). HEAD's 15 run-row store files (7,592 LOC) and legacy step 66's
   body move into it at F5, the tasks flip, which deletes HEAD's engine: from then on they are the reader of
   step M4 and nothing else (05-tasks 5T.8.1; R5-2, MOD5-1; §8.6 gate E). The identity store's 3,053 LOC
   (`pebble_store_auth.go` and four siblings), HEAD's cipher and setting rows and the AI-job, throttle,
   import-root and dirty-set files stay in the residue, which the steps import. HEAD's run-state vocabulary
   `internal/operations/state` stays in ABO beside them, because step M4 decodes HEAD's stored states with it
   (R6-2, MOD6-1). R3 deletes all of this code with `internal/books/cutover` (the HEAD-format readers, 03
   §3.8). The 4 `SettingsStore` methods stay with books, because `setting:` is books' runtime-marker store
   (cross-cutting resolution 7). The residue is renamed `internal/books/store` in release R1 (§4).
2. **No package qualifies for `mediacore/shared/` yet.** The promotion rule's second door (two real
   consumers) is closed until a second app exists; §9 lists the candidates and their triggers.
3. **`pkg/` stops exposing `database.Book`.** `pkg/ops` becomes the public `mediacore/tasks` with
   its Book-typed `Writer` moved to the books module; `pkg/plugin/sdk` is deleted (§6).
4. **`internal/models` holds nothing for core**: its live dedup-score types merge into
   `internal/dedup/unified` (§5).
5. **Four registries are deleted** (`internal/serviceregistry`, `internal/plugin`,
   `internal/maintenance`'s job registry, `internal/opscatalog`) in favour of explicit
   `module.Registrar` composition (N4, R5).
6. **Gin-based packages are re-implemented, not moved** (`internal/httputil`, `internal/realtime`,
   `internal/server/middleware`, `internal/server/handlers/opsv3`): core is `net/http` (N7). So is every
   core-bound file whose ABO imports cannot change by import path alone or by one of §2.2's mechanical rewrites
   (R7-8, R8-6; a planning classification, not a CI rule, A-4). The task engine `internal/operations/registry`,
   `childop`, `opsmetrics`, `opsv3` and `pkg/ops/opstest` use HEAD's run-state vocabulary and v2/v3 row types,
   so core's executor and its helpers are written fresh, each with a §8.6 row and a generated count. The ABO
   files keep running HEAD's engine until F5, the tasks flip, which registers every converted definition and
   deletes them in the same PR (05-tasks 5T.8). ABO's
   gin engine is mounted **permanently** through `httpapi.MountLegacy` (cross-cutting resolution 2),
   so the gin helpers the legacy mount needs (`internal/httputil`'s `parse.go`/`respond.go`,
   `internal/server`'s `gone.go`) stay in ABO for good. Gin copies of concerns core takes over are
   deleted in the PR that takes them over: the perimeter in release R1, auth at F4, the task API at F5, the SPA
   at F9.
8. **No ABO import is ever rewired onto `mediacore/internal/...` or `mediacore/kernel`** (03 §3.4
   visibility rule, R2; MOD-2). The internal/public column is derived from the import graph: a
   package that ABO code still uses goes to a public path (`compactprogress` → `storage`, `oauth` →
   `auth/oidc`, `lifecycle` → `lifecycle`) or stays in ABO until a named release or flip rewires its last ABO user to
   a public surface (§8.3 lists every such destination with every ABO user that `go list -deps -test -json`
   finds at the evidence commit and its kind of use, non-test or test-only, MOD2-1, R3-3; in CI, Go's `internal/`
   rule makes a user left unrewired a build failure of the PR that moves the destination, for every (`GOOS`, tag set) of
   03 §3.5.1 item 4's list, because a required PR job also runs `go vet` with `GOOS=darwin` and with `-tags integration`
   over both modules, A-4, R9-10, MOD9-3: `internal/startup`
   therefore stays in ABO until F8, because `cmd/root.go:274` is a
   non-test user and its replacement `Registrar.StartupSteps` lands at F8, MOD3-1).
9. **Moves are keyed by [09](09-abo-on-core.md) §9.4's releases and [03](03-architecture.md) §3.8's flips** (R0,
   release R1, R2's foundation PRs F2 and flips F3–F9, R3), not by a separate numbering (§8; MOD-6). 09 references these rows for its per-release
   move lists instead of restating them.
7. **Three packages are dead code** with zero importers in test and non-test code
   (`internal/bookfiles`, `internal/writeback`, and `internal/operations/freshness`); the first two
   are deleted, the third merges into `tasks` because a consumer is already documented.
10. **No row copies, mirrors or keeps a HEAD-format store, and no core code touches a books family (R3-1,
   R4-1; D10).** Core services that persist state ABO already keeps (identity, roles, config overrides and
   secret rows, runs and clocks, system log rows, import roots, AI batch jobs, provider holds and quotas) run
   their native stores from the flip that wires them. HEAD's rows reach them once, through the importers that
   books' cutover steps call in R2 (03 §3.8 M2–M6). The full-text index is rebuilt instead, and the provider
   holds are reset (D12 (b)). The decoders are the files listed as "stays" in the `internal/database` row, the
   run-row files that move into `internal/books/cutover`, and HEAD's marker formats of `internal/scheduler` and
   `internal/operations/registry`, which the tasks step reads. `internal/metadata/dailyquota` stays in ABO, because it hard-codes `provider_daily_budget:`
   (`internal/ai/resultjournal` stays too, as plain books code: no core store reads it, COR5-10).
   There is **no storage grant**: books keeps a claim on every family it still holds, and M2–M6 delete the
   families they move (03 §3.5 R8). The generator verified at the evidence commit that no ABO key name lands in
   core (§2.1 (h)); in CI, 03's `TestCoreHoldsNoAppKeyNames` is the check.

## 2. Method and rules applied

### 2.1 Package set and cross-check

**This file is informative (A-4).** It is the planning map: the per-package rows with their destinations, stages
and LOC (§3, §8), and the re-implementation rows of §8.6. No CI job reads it, runs its generator or regenerates
its tables, and no PR is required to keep it current: feature PRs never edit it, and a move or flip PR may correct a
row but is not gated on one (09 §9.2.10; A-11). The checks that bind during the extraction are 03's and 09's: Go's
`internal/` rule, in CI for every (`GOOS`, tag set) of 03 §3.5.1
item 4's list (a later release's import problem fails that PR) — a required PR job runs `go vet` with `GOOS=darwin` (goreleaser's set: `CGO_ENABLED=0`, `embed_frontend`)
and with `-tags integration` over both modules, which no PR job compiled before (R9-10, MOD9-3); `TestCoreImportsOnlyCore`,
which reads that list's tag union;
`archtest.TestNoAppRequire`; the ABO layering guard; the `SDKOnly`/kernel guard; and R1(c)'s key-name check
`TestCoreHoldsNoAppKeyNames` (03 §3.5.1, R1). There is no stage marker and no release-gating trailer (03 §3.8).
Every count in this file was measured at the evidence commit and is informative: a PR that needs a count generates
it at its own base commit (ADV-11).

- `go list ./...` with default tags at the evidence commit returns **213** packages; the set equals
  raw/01's inventory and the 213 keys of `layerOf` (`internal/arch/layering_test.go:59-286`). No
  package exists only under a build tag (raw/01 "Build-tag-only packages").
- LOC, layer and class come from raw/01 and were recomputed from `go list -json` at the same
  commit: every value matches. **Importer counts are generated at the evidence commit** (MOD2-1, MOD4-16): the
  `in/out` column, the "in N, out M" prefix of every evidence cell, every "N importers" phrase, §8.2 and §8.3 all
  come from one parse of `go list -deps -test -json ./...`, with the `ForTest` variants (`p [q.test]`) and the
  synthetic `.test` mains dropped **by this counting parse only** (MOD8-10: the type-checking helper below keeps
  them). `in` is the number of root-module packages that import the package from any file, test files included
  (so it can exceed raw/01's non-test in-degree); `out` is its non-test in-module imports.
- One generator builds the tables from one mapping file plus that `go list` output and a per-file parse of the
  same packages (imports, top-level declarations, struct fields, methods, unresolved identifiers). It is a
  spec-authoring tool kept **outside the repository**: it is not committed, and no CI job or `make` target runs
  it (A-4). It models every **stage point** of 09 §9.4 in 03 §3.8's order: R0, release R1, R2's foundation PRs F2 and
  flips F3–F9 (each a point of its own, because each foundation or flip PR leaves a `main` that boots and passes e2e)
  and R3. A row dated with
  a flip is due at that flip's PR; 05-tasks' conversion groups T2–T7 land dark before F5 and are not points of
  their own, because they move no file.
  Its type-checking helper (stdlib `go/types` over `go list -export` data, outside the repository too) runs gate
  E's typed scans (§8.6) and (h)'s constant folding. **It keeps the `ForTest` entries** (MOD8-10): each
  `p [q.test]` variant with its `Export` file, because a test build recompiles its in-module dependencies
  (`internal/database`'s test build has 9 variants, among them
  `internal/{merge,undo,testutil/rapidgen} [internal/database.test]`, which its `XTestGoFiles` import and which
  import `internal/database`); when it type-checks a test variant it resolves every import through that variant's
  `ImportMap`, so an external test and the package under test see one `database` package, never two. Its controls
  run on every generation (a §8.5 logger cut classifies as a move; a `database.OperationV2Row` use without a §8.6
  row is reported; a rule outside §2.2's list is reported, MOD8-1; a split that separates build-tag twins is
  reported, MOD8-3). At the evidence commit it **verified** that:
  (a) the mapping covers exactly the 213 packages `go list ./...` returns; (b) every file named in a
  *Split* cell is a non-test file of that package — a default-tag file or a build-tagged one — named once, and
  *Split* cells name the **tag-union** file set (R8-6, MOD8-3): **build-tag twins travel together**, i.e. two
  non-test files of a package that declare the same top-level name (necessarily tag-exclusive) sit in the same
  part, in the tag union or not; a named build-tagged file lands with its part (checks (c)–(e) and (h) see it
  there), while an unnamed one stays where the bucket says (in CI a darwin twin left behind fails the darwin vet job of
  the PR that moves its partner, R9-10, MOD9-3). 8 build-tagged files are named at the
  evidence commit: `reflink_darwin.go` and `reflink_unsupported.go` with `reflink.go`, `static_embed.go` with
  `static_nonembed.go`, the darwin and windows `memory_*`/`uptime_*` twins with `memory.go`/`uptime.go` and
  `persistence_owner_other.go` with `persistence_owner_unix.go`;
  (c) **MOD-2 at every stage point** (R2-5, R3-3): for each stage point in order, no file still
  in ABO at that point imports a path that is non-importable by then (`mediacore/internal/...`,
  `mediacore/kernel`), and every ABO user found is listed in §8.3 with the **kind** of use found and how it is
  rewired. The kind is read from the non-test imports of every root package, `cmd/*` and the root `main` included,
  separately from its test imports. Which files exist is decided by **03 §3.5.1 item 4's one tag-set list**
  (MOD5-16, MOD7-14; this file does not restate it): `go list` runs once per (`GOOS`, tag set) listed there,
  `GOOS=windows` for `./cmd/mtls-bridge` only, and the union of their file lists is the file set, so a tagged file
  counts only if some real build compiles it (18 files at the evidence commit are out); gate E's test half reads the
  same union (§8.6). It sees `cmd`'s `dedup_bench*.go` and `internal/server/bench.go`, which import
  `internal/database` (renamed in release R1), `internal/config`, `internal/ai` and `internal/httputil`, and the
  `integration`-tagged `internal/server/transcode_integration_test.go`, a test-only user of
  `internal/operations/registry` counted by the F5 gate. A user any of whose non-test files (build-tagged ones
  included) uses the moved code is **non-test**, otherwise **test-only** (MOD3-1: `cmd` had been declared
  test-only for `internal/startup`, but `cmd/root.go:274` builds the `serve` step runner from it). The deletion of
  `internal/plugin` at F7 is listed the same way (§8.3, third table);
  (d) **rule R1 at every stage point**: no file that has landed in core by a stage point imports ABO code at that point
  unless its row states how the import is cut (§8.5, or a re-implemented part, §8.6);
  (e) **splits type-check** (MOD2-3) over the tag-union file set (MOD8-3): a part that moves verbatim shares no
  unexported name, no exported identifier declared in a part that stays (such as `internal/logger`'s `Logger`,
  MOD4-6), no struct field or method access and no method on a foreign type with another part of its package;
  parts that are re-implemented are listed with the generated count of staying-side references the move PR
  rewrites (§8.6);
  (f) every core path named in §8 exists in 03 §3.3 or in a §3 row, matched exactly or as a sub-path written
  relative to its parent row, never through its first segment alone (MOD2-2, MOD3-9); the one exception is an
  internal path (`mediacore/internal/...`), which matches 03 §3.3's open-ended `internal/...` row when this file
  states its stage and purpose (§8.1; MOD4-16); every public core path this file uses that 03 §3.3 does not list
  is named under "Objections to 03";
  (g) the per-file parse covers every package `go list ./...` returns;
  (h) **no ABO key name or op ID lands in core** (R4-1, 03 R1(c); MOD5-3, MOD5-4) — the planning twin of 03's
  `TestCoreHoldsNoAppKeyNames`, which is the CI check: no non-test file whose destination is a core package holds
  a string literal (comments ignored) that is (1) a key of a books family — the prefix set is the claim
  generator's **output** for owner books, read through the exported `KeyFamilies()` (`keyfamilies.go:330`) at the
  file's current path (`internal/books/store` from release R1), with the reserved `c/` and `m/` excluded, and a literal
  matches only when the character after the prefix is not a space, so core's `"library: …"` errors (Go's
  `pkg: message` form) pass while `"library:" + id` does not; (2) an ABO setting or preference key,
  constant-folded at every `GetSetting`/`SetSetting`/`DeleteSetting`/`…UserPreference…` call site; or (3) one of
  the 269 op IDs of `internal/server/testdata/op_ids.golden`; or (4) a literal of 03 R1(c)'s reviewed list of ABO
  row-type and param-key literals that no family prefix carries (`legacy_op_id`; MOD7-2). The **match rule** is
  03 R1(c)'s, its only text (MOD6-7, MOD7-7), applied as written there, folded-prefix clause included; under it a
  **struct tag** is matched per key, each `key:"value"` pair's value up to its first `,` (MOD8-4), so the
  generator tokenises Go source and never reads a raw string as quoted literals (`maintenance.go:285`'s
  `` `json:"legacy_op_id"` `` is a listed-literal hit for that reason). A call site whose key folds to **no**
  constant (`SetSetting(req.Key, …)`, a loop over a table) in a core-bound file is listed with its key source (a
  table folded through its composite literal, a books-supplied value); 37 call sites at the evidence commit pass a key that folds to no constant; none of them is in a file that lands in core, so the source list is empty. Core-owned kinds never spell an ABO
  op ID: books' kind-rename map maps a former op ID onto a core kind itself (03 R1(c); 05-tasks 5T.8.2; MOD6-6). The generator sees
  every file whose destination is core, moved or re-implemented (§8.6; MOD8-4), named build-tag twins included;
  new core code (05-health's `core.diagnostics.export`) is caught by `TestCoreHoldsNoAppKeyNames` in the PR that
  writes it. Each hit is listed with the cut part that takes it out of core (§8.6 "Key names and op IDs"), or is on
  the reviewed allowlist (one entry: `invite:global`, an in-memory limiter bucket name). This is why the HEAD-format
  reader files, `internal/ai/resultjournal` (`aijournal:`), `internal/metadata/dailyquota`
  (`provider_daily_budget:`) and `internal/server`'s `apikey_expiry_stamp.go` stay in ABO. Each table row counts
  its hits by kind — family keys, setting and preference keys, listed literals, op IDs — and every hit that is not
  an op ID is named in the row's cut text, so a setting key cannot hide behind a row written for op IDs (MOD7-3);
  (i) **moves and re-implementations are classified** (R7-8, R8-6, MOD7-1, MOD8-1): a file is planned as a move
  when every ABO import it has changes by its import path alone or by one of §2.2's mechanical rewrites; each §8.5
  row's rule classifies as one of them (20 rows do). A file that lands in core and references an ABO
  row-format vocabulary that has no import-path-only replacement — HEAD's run-state package
  `internal/operations/state`, and in `internal/database` every top-level name of the run-row part that moves into
  `internal/books/cutover` (HEAD's v2/v3 run-row types) plus the v1 `Operation`, `OperationLog`, `OperationStore`, `UpdateOperationStatus` and the
  books `ActivityEntry` — is planned as part of a re-implementation with a §8.6 row and a generated count. The
  classification sizes each move PR's work; it is not a CI rule (A-4).
  The tables below are its output. Gate rows name each user by its path **at the gate's stage point** (release R1
  renames `internal/database` to `internal/books/store`, F5 `internal/operations/opswriter` to
  `internal/books/writer`; MOD3-11). The alphabetical index in §11 lists every package once.

### 2.2 Buckets

The bucket records **what happens to the package's current import path**:

| bucket | meaning |
|---|---|
| core public | the path leaves the root module; the bulk becomes one public (SDK) core package |
| core internal | the path leaves; the bulk becomes one `mediacore/internal/...` package |
| core `shared/` | the path leaves; it becomes a `mediacore/shared/...` package |
| merge | the path leaves; its content is absorbed into a package whose main identity is something else (core or ABO) |
| ABO app path | the path stays in the root module, unchanged or renamed as stated; parts may still be extracted to core, listed in *Split* |
| delete | the path leaves and nothing replaces it as a package (retired mechanism or dead code) |

A *Split* cell lists every file that leaves a package, by destination, with its LOC. Files not
named stay where the bucket says; in the `internal/database` row, parts labelled **stays:** name the
files that remain in the residue as HEAD-format readers of the R2 cutover steps until R3 (or as books' own
code), so it is visible which step reads which file. A split part is either a **move** (the files change package verbatim, apart from import paths and the mechanical
rewrites below, and type-check on both sides, §2.1 (e)) or a **re-implementation** (the content is rewritten
against core contracts in the move PR, the ABO files are deleted then — for the task engine and the helpers
its v2 callers use (`childop`, `opsmetrics`), by the tasks flip F5 (§8.3; R8-6, MOD8-2) — and the staying side's
references are rewritten in the same PR; §8.6 lists every such
part). The generator planned a file as a move when every ABO import it has changes **by its import path alone or
by one of these mechanical rewrites**, the generic cuts of §8.5 — (1) the logger cut: `internal/logger` -> the destination's `*slog.Logger` (child of `log`) passed in, no package-level logger (R5); (2) the store-interface cut: `internal/database` -> the destination's own native store interface or `coretest`/`storage` fixture; (3) the config cut: `internal/config` -> a typed config section handle (05-config); (4) the HTTP-helper cut: `internal/httputil` -> `httpapi` helpers (N7); (5) the metrics cut: `internal/metrics`/`internal/telemetry` -> the App's metrics registry, attribute keys and scope through `metrics.Scope` (MOD4-4); (6) the injected-value cut: a value an ABO package computed (the watched roots, an owner check) passed in by books as a value or func; anything else — in particular any
use of an ABO row format or of HEAD's run vocabulary (§2.1 (i)) — made its part a re-implementation. The
classification sizes the work of each move PR; it is **not a CI rule** (A-4), and 12's history split finds moved
files by git rename detection, so no list of moved paths is kept. **Every commit of a move PR builds** (MOD4-14;
09 §9.4 R1's CI classification): the repo merges by rebase, so a rename-only commit that cannot compile would land on
`main` and break `git bisect` and auto-revert. `scripts/ci/check_commits_build.py` in `extraction-gates` enforces it
for every PR whose diff renames a file into `core/`: it runs `go build ./... && go -C core build ./...` at each commit of
the PR and reports any moved file whose first-commit similarity is below git's rename threshold, so a PR that squashes
`go fix` into the move commit cannot drop a file's history silently (MOD9-5; 09 §9.4 R1's CI classification). Each move PR applies in the same PR the `go fix` modernizers core's
`fix-check` requires in the moved files (`go fix -diff ./...` exits 1 at HEAD with pending fixes in 245 root files,
e.g. 3 in `internal/telemetry`, 2 in `internal/diag/flight`; 03 §3.2 item 4: the root has no `go fix` gate), the rule-R1
cuts and the R5 rewrites (MOD3-6). *Split* cells are not an input of core's history path file: 12 §12.8.1 derives
it from real history by git rename detection, so a re-implemented part keeps only its post-arrival history
(MOD2-12). Where a single file must itself be divided (for example
`internal/database/pebble_store.go`, `internal/config/config.go`), the cell says so and §4 / §7
give the cut line. A *mixed* package (raw/01 class) with no *Split* cell stays wholly in ABO: its
mixed-ness is a dependency on books types (`database.Book*` etc.), and those types stay in ABO too.

### 2.3 Promotion rule, as applied

03 §3.4's rule is non-circular: a package is **built** in core only if ABO consumes it in one of 09's
releases R0–R3 or a built core package needs it. A package (or part) of the root module therefore goes to
core only if one of these holds:

| test | source | used for |
|---|---|---|
| P1 — it implements a built core package of 03 §3.3 (Build column "ABO" or "core") | 03 §3.4 (a) | storage, tasks, events, log, metrics, config, secrets, httpapi, realtime, auth, rbac, audit, library, integrations (ai, metadata, download/deluge, notify mail+webhook, providerhttp), backup, updater, health, ui, kernel, archtest, coretest |
| P2 — it has two real consumers across apps | 03 §3.4 | none today (one app) |
| P3 — a built core package needs it | 03 §3.4 (b) | e.g. email normalization (`internal/util/email.go`) is needed by core auth; Range serving is needed by core httpapi |

No row maps to a **deferred-build** package (`acquire`, `arrfacade`, `integrations/indexer`, the
download grab/monitor pipeline, non-Deluge download adapters): ABO has no code for them, so they
appear in no release here.

Packages that are domain-free but fail all three stay in ABO: `internal/cache`, `internal/flight`,
`internal/keyedmutex`, `internal/errhandling`, `internal/tools`, `internal/querygrammar`,
`internal/mtls`, `internal/cfaccesspolicy`, `internal/cfservicetoken`, `internal/wirecontract`,
`internal/applycap`. (`internal/keyedmutex` was moved into core only by build experiment E2 in 03
§3.2.1, which tests the module mechanism; 03 §3.2.1 records that it is not a placement decision. Core gets its
own `core/internal/keyedmutex`, an **L0** leaf that imports only the standard library, at F3, §8.1 and §9; ABO keeps its package.)
`internal/appdirs` and `internal/telemetry/contract` stay for a different reason: they import or walk
ABO code (MOD-21, MOD-6). `internal/logger/logtest` stays because it captures by swapping
`slog.Default`, which R5 forbids in core; `coretest` captures through an injected handler (MOD3-3).
`internal/metadata/dailyquota` is a generic mechanism but hard-codes ABO's `provider_daily_budget:`, so it
stays in ABO; core's native `GateStore` over `c/providerhttp/` re-implements quota accounting at F7, and M6
deletes HEAD's budget rows, because the holds are reset (D12 (b); R4-1, §2.1 (h)). `internal/ai/resultjournal` hard-codes
`aijournal:` and stays too, but as plain books code over books' own claim, written only by books' apply
handlers: no core service persists through it and 05-ai defines no result-journal interface (COR5-10,
SIMP5-3). `internal/telemetry` keeps its path as books telemetry: its attribute
vocabulary, view table, scope helper and version global are ABO names (MOD4-4).

Where raw/01's class was wrong for this decision, the row says so: `internal/server/absauth` is
classed audiobook-specific but holds the web login's password hashing, throttle and auth audit
type (used by `internal/server/handlers/auth.go`, `user.go`, `internal/server/bootstrap.go`); `internal/serverdecode`
is classed generic but is the in-process *audio* decoding switch.

### 2.4 New root-module packages created by splits

Five new paths — `internal/books`, `internal/books/contract`, `internal/books/cutover`, `internal/cfaccess` and
`internal/server/legacyauth` (the bridge of 05-http-api §8.3) — are 03 §3.5.1's count. The renames below keep the
count, and so does a rename of `internal/operations/state` under `internal/books/` if one is made (R6-2: 213 − 49
+ 5 = **169**; §8.4 note 6). Each enters `layerOf` in the PR that creates it (MOD5-9). Its imports and importers
are declared in the generator's input, whose cycle model (§8.6 gate E) found no cycle against HEAD's package graph
at the evidence commit. The guard assertion of 03 §3.5.1 item 1 reads **non-test** imports (MOD5-11). The G6/G7
test `TestSecondModuleNoAudiobookEdit` lives in `internal/books`, and so does `TestLegacyStepPreconditionsResolve`:
it needs the residue's legacy step list and the step bodies that move into `internal/books/cutover` together, and
only the composition root imports both without a cycle (MOD6-2).

| new path | created in | holds |
|---|---|---|
| `internal/books` | R1 | the **composition root only** (R4-5, MOD4-2): `books.Module()` (the one constructor name in 03, 04, 06 and 09; MOD5-11), the `Registrar` calls, the schedule declarations (from `internal/scheduler/tasks.go`, `extra_ops.go`, `full_sweep.go`, with the extra-op wiring `internal/server` does today, §8.6 gate E), `Manifest.Cutover` (the ordered cutover steps of `internal/books/cutover`, 06 §6.4.2), the funcs that wire the bodies of legacy steps 62, 65 and 66 from `internal/books/cutover` into the residue's legacy step list (R5-2) and `TestLegacyStepPreconditionsResolve`, which checks that wiring (MOD6-2). It imports `module`/`app` and every books package it registers; its computed layer is 6 and only `.` and `cmd` import it, so no allowance is needed (03 §3.5.1 item 1) |
| `internal/books/contract` (name open) | F5 | the shared books helpers that plugins and handlers import, below the composition root (layer 3; it imports neither `module`/`app` nor anything that imports `internal/books`, so a plugin that imports a helper cannot close a cycle). Created at **F5**, the flip that moves content into it (MOD5-9): the books typed `Writer` contract (from `pkg/ops/writer.go`), `PageBooks` (from `pkg/plugin/sdk/iterate.go`), selection helpers (from `internal/operations/selection.go`, `types.go`), HEAD's v1 checkpoint helpers (`OperationState`, `SaveCheckpoint`/`LoadCheckpoint`/`ClearState` from `internal/operations/state.go`, until their callers' conversion PRs replace them with `rc.Checkpoint`; R7-8, MOD7-2), the task-kind tombstones the kind-rename map consumes (from `internal/opscatalog/tombstones.go`), and task `Resource` declarations and subject resolvers (from `internal/operations/registry/deps.go`). Nothing needs it before F5: plugins import `pkg/plugin/sdk` until then, which closes no cycle |
| `internal/books/cutover` (name open) | F3 | books' **R2 migration steps** (03 §3.8 M2–M6, with the level step M7 and the compaction M8): one `BootBlocking` step per subsystem that decodes HEAD's rows, calls core's importer and deletes the rows in one batch per chunk (09 §9.2.3). Created in the config flip F3 with M2, M7 and M8; the frozen copy of HEAD's `Config` struct, codec and cipher that M2 reads (05-config §1.12.2) is files of this package, not a sub-package, so the count stays 03's; M3 joins at F4, M4 and M5 at F5, M6 at F7. At F5, the tasks flip, HEAD's 15 run-row store files from `internal/database` and `pebble_store_plan_legacy.go` (legacy step 66's body) move into it unchanged in format, with the bodies of legacy steps 62 and 65 (from `migrations.go`) and the every-boot ops reconcile of `InitializeStore` (from `store.go`), which M4 runs once; so do HEAD's clock-key codec (from `internal/scheduler/interval_clock.go`), the maintenance-window clock `maintenance_window_last_run` with its `YYYY-MM-DD` date codec (`internal/scheduler/maintenance.go:53,71`; R7-8, MOD7-3), the pause marker (from `internal/operations/registry/pause_gate.go`) and the stand-down marker (`registry.scan_standdown` and its JSON shape, from `internal/operations/registry/scan_standdown.go`; MOD5-4, MOD6-4), which M4 decodes into `tasks.Importer`. It imports HEAD's run-state vocabulary from `internal/operations/state`, which **stays in ABO** (R6-2, MOD6-1), `internal/config` and the residue. **The import runs cutover → store, never the reverse** (R5-2, MOD5-1), with an ABO guard assertion: the residue keeps only exported helpers the cutover code calls (`RecoverPebbleClosed`, `PrefixEnd`, `PrefixUpperBound`, `OpChangeKey`, a pure ops-family key predicate for its raw-write guard), the `opsV2Timeline*` and `ops*` fields of `PebbleStore` move onto the cutover reader's type, and every capability assertion on a moved method becomes a direct call through a func the composition root wires, so no legacy step is skipped or recorded as applied without running (§8.6 gate E). It cannot live in the residue: `internal/config` imports the residue from four non-test files (`LoadConfigFromDatabase(store database.SettingsStore)`, `persistence.go:915`), so that placement would close `internal/config` → residue → `internal/config` (gate E's negative control). Only `internal/books` imports it. **Its tests** (MOD6-2): the 19 ops test files of `internal/database` move into it at F5 (the `race`-tagged timeline twin included, MOD7-8) as in-package tests, build their store with the package's own test constructor over core's in-memory storage and the residue's exported open, and use `internal/books/store/storetest` only from an external test package (storetest imports the cutover package, so an in-package test importing it is an import cycle); gate E's test half lists every test file's disposition (§8.6). R3 deletes the package with the HEAD-format readers and books' legacy steps 1..66 (03 §3.8) |
| `internal/books/store` (+`storetest`, `aggtest`) | R1 (rename) | rename of the `internal/database` residue (§4): books' own store, and until R3 the HEAD-format readers that the cutover steps import |
| `internal/books/writer` | F5 (rename) | rename of the `internal/operations/opswriter` residue |
| `internal/telemetrypolicy` | R1 (rename) | rename of the test-only `internal/telemetry/contract` suite, plus `internal/telemetry/ratchet_test.go` (both walk or import ABO code; MOD-6) |
| `internal/cfaccess` | F4 | the Cloudflare Access identity adapter and owner proof, consolidated from `internal/oauth/cfaccess.go`, `internal/server/middleware/{cfaccess,cfbounce,owner_guard}.go` and `internal/auth/owner.go`, with the `CFAssertionVerifier` interface from `internal/server/middleware/absauth.go` (G11: deployment-specific, behind core auth's authenticator extension point). `internal/server`'s CF Access route files (`cf_recovery.go`, `guest_service_tokens.go`, `invite_guest_access.go`, `owner_routes.go`) **stay** in `internal/server`: they import `internal/server/handlers`, `internal/config` and the middleware while `internal/server` imports the adapter, so consolidating them would close `internal/server` → `internal/cfaccess` → `internal/server`; `internal/config`'s update service takes the owner-proof check and the access-email reader as one injected interface (gate E, round 5; the cut is required by the ABO layering guard, MOD8-6), and so does `internal/plugins/maintenance` (layer 4, `layering_test.go:227`), whose `repairs_ops.go:319` owner check `internal/books` injects as a func (the MOD4-18 rule; MOD7-4). **`layerOf` = 5** (transport): `owner_guard.go` imports `internal/httputil`, which computes to 5 from release R1 (§8.4 note 3), so every importer must be at layer 5 or above, which the generator checks against gate E's importer table (§8.6) |
| `internal/server/legacyauth` | F4 | the read-only **bridge** that replaces the gin auth chain inside the legacy mount (05-http-api §8.3; 03 §3.8 F4): `legacyauth.FromCorePrincipal` reads the `auth.Principal` that core's stage 12 binds and fills the legacy context values, and `legacyauth.Can(ctx, rbac.LegacyCheck(atom, readSide))` replaces each raw in-handler `auth.Can`; the bridge never authenticates, and its adapters still enforce through `rbac.Require`. **New ABO code, no moved file**: `internal/server/middleware/auth.go`'s `RequireAuth` chain is replaced by it (§4 `internal/server/middleware` row). It is a package of its own because the handler packages that keep an in-handler `auth.Can` after the moves (`internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/audiobooks`) cannot import `internal/server`, which also imports it (the generator's input declares these four importers; the sites were counted at the evidence commit over the files that stay in ABO). Created in the F4 flip PR, which deletes the gin auth chain and where it enters `layerOf`. It imports only core `auth` and `rbac`, gin and books' atom → scope table, no other ABO package, so its computed layer is the floor (0) and every ABO layer may import it (03 §3.5.1; gate E's importer table, §8.6) |

`internal/books/rbacreach` (05-rbac §6's frozen reach snapshot) is a data directory read by a test, not a
package; if 05-rbac makes it a package it is a sixth new path and the counts below change (MOD5-17).

## 3. Mapping tables

Columns: current package (anchor target of §11), `layerOf` layer, non-test LOC, raw/01 class
(generic / mixed / audiobook), non-test in-degree / out-degree, target, reason, evidence. Evidence
for rows without hand-written text is computed from the import graph (audiobook-class imports
first, then importers).

### 3.1 Core public (SDK) path (13 packages, 14430 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-aidispatch"></a>`internal/aidispatch` | 3 | 1802 | mixed | 7/2 | mediacore/integrations/ai (routing, endpoint health, inflight, attribution) | Generic capability->endpoint router with cooldowns; capability IDs become per-module registrations. `metrics.go`, which holds nothing but HEAD's five `ai_dispatch.*` instruments, does **not** move to core: it goes to books' AI recorder in `internal/ai`, which owns every `ai_dispatch.*` family through `ai.WithObserver`, and the F7 PR deletes `dispatch.go`'s instrument calls (`requests.Add` … `failover.Add`), so core records only `<ns>_ai_*` (R9-3, COR9-14, MOD10-10; 05-ai §8). | in 7, out 2. imports `logger`, `telemetry`; `Call[T]` `dispatch.go:347`; process-global `defaultHealth` `health.go:46` (R5); 10 audiobook capability IDs registered at import `capabilities.go:67-95,176,185`.<br>**Split:** **mediacore/integrations/ai (no HEAD `ai_dispatch.*` instrument: the F7 PR deletes `dispatch.go`'s instrument calls, MOD10-10)** ← `attribution.go`, `classify.go`, `dispatch.go`, `errors.go`, `health.go`, `inflight.go`, `locality.go` (1422 LOC); **internal/ai (ABO capability IDs, registration mechanism -> core; `metrics.go`, the five `ai_dispatch.*` instruments, -> books' AI recorder, MOD10-10)** ← `capabilities.go`, `metrics.go` (380 LOC) |
| <a id="p-internal-auth"></a>`internal/auth` | 3 | 610 | generic | 20/1 | mediacore/auth (identity context, credential methods); permissions + seed roles -> mediacore/rbac | Small, dependency-light identity core (raw/10 'positive building blocks'). The closed permission list and seed roles become `rbac` registrations (G1-G3). Owner proof is Cloudflare-Access-specific (G11) and stays in ABO. | in 20, out 1. imports `internal/database` only for `User`/`Role` and two seed-store interfaces (`seed.go:20-30`); closed list `permissions.go:91-129`; 13 atoms `permissions.go:24-85`; seed overwrite `seed.go:86-129`; owner proof `owner.go`; 20 importers.<br>**Split:** **mediacore/auth** ← `context.go`, `user_active.go` (202 LOC); **mediacore/rbac** ← `permissions.go`, `seed.go` (300 LOC); **internal/cfaccess (ABO, owner proof)** ← `owner.go` (108 LOC) |
| <a id="p-internal-backup"></a>`internal/backup` | 3 | 1582 | generic | 6/3 | mediacore/backup | Consistent checkpoint-based archive of the DB directory with codecs, retention and verification; no book logic. Must grow to every core store and module-declared artifacts (03 §3.3). | in 6, out 3. imports `diskstats`, `logger`, `safepath`; `Checkpointable` `backup.go:28-33`; dead SQLite branches `backup.go:959,1028,1103` (delete); restore does not reopen the live store `internal/server/handlers/system/handler.go:823-905` (raw/05 §5). |
| <a id="p-internal-lifecycle"></a>`internal/lifecycle` | 0 | 35 | generic | 3/0 | mediacore/lifecycle (`ErrShutdown` cancellation cause) | Shutdown-vs-cancel sentinel consumed by the executor and scheduler. 03 §3.3 defines the public L0 package `lifecycle` (shutdown types used by kernel and tasks), and `internal/aiscan` stays in ABO, so the target is public (MOD-6, MOD-2). | in 3, out 0. `ErrShutdown`/`IsShutdown` `shutdown.go:33`; importers `aiscan`, `registry`, `scheduler`. |
| <a id="p-internal-logging"></a>`internal/logging` | 0 | 529 | generic | 13/0 | mediacore/log | slog-native context helpers, op context, sanitizers and path redaction: the shape 03 §3.3 prescribes for `log`. | in 13, out 0. no in-module imports (raw/02 a3: zero closure); `structured.go:47-68`, `opcontext.go:15`, `sanitize.go:28,38`, `redact_path.go:107` (invite-token redaction; invites are core auth). |
| <a id="p-internal-mail"></a>`internal/mail` | 0 | 364 | generic | 3/0 | mediacore/integrations/notify/mail | SMTP `Sender`; fully generic (raw/11 §6). | in 3, out 0. no in-module imports; `Sender` `mail.go:60-62`; `SMTPSender` `mail.go:85`. |
| <a id="p-internal-metadata-providerhttp"></a>`internal/metadata/providerhttp` | 3 | 417 | generic | 4/1 | mediacore/integrations/providerhttp | Per-provider throttled HTTP clients; must become instance-scoped (03 §3.3). | in 4, out 1. imports `logger`; package-global limits/clients `providerhttp.go:40,60-81` (raw/12 §9). |
| <a id="p-internal-oauth"></a>`internal/oauth` | 3 | 827 | generic | 4/3 | mediacore/auth/oidc (OIDC/OAuth providers, public API; 05-authn §11) | GitHub OAuth2, Google OIDC, signed PKCE state and user linking are generic SSO (03 §3.3 `auth`: OIDC/OAuth). ABO's ABS OpenID flow (`internal/server/handlers/abs/openid.go`, `internal/server/absauth`) stays in ABO and uses `Config`, `New`, `IdentityClaims` and `ParseAllowedEmails`, so the providers go to the public `auth` package, not `internal/` (MOD-2). The Cloudflare Access JWT verifier is deployment-specific (G11). | in 4, out 3. `Provider` `providers.go:23-27` (2 impls `:37,:119`); `resolve.go:32,65`; `state.go:55-132`; CF verifier `cfaccess.go:68-97`; imports `database`, `logger`, `util` (email normalization).<br>**Split:** **internal/cfaccess (ABO)** ← `cfaccess.go` (132 LOC) |
| <a id="p-internal-plugins-webhook"></a>`internal/plugins/webhook` | 4 | 162 | generic | 1/1 | mediacore/integrations/notify/webhook | Generic HMAC webhook notifier; becomes a notify adapter subscribed to core events. | in 1, out 1. imports only `internal/plugin` (`plugin.go:33`); brand header names `X-Audiobook-Event`/`-Signature-256` `plugin.go:124,126` -> app-supplied prefix. |
| <a id="p-internal-realtime"></a>`internal/realtime` | 5 | 426 | generic | 2/0 | mediacore/realtime (+ mediacore/internal/sse) | SSE hub with client subscriptions; core makes it resumable and permission-filtered (03 §3.3), off gin. The books-changed coalescer generalizes to an entity-change coalescer keyed by `entity.Ref` (03 §3.3). | in 2, out 0. no in-module imports; gin `HandleSSE` (raw/12 §5); `EventBooksChanged` `book_changes.go:18`; importers `itunes/service`, `server`.<br>**Split:** **mediacore/realtime** ← `events.go`, `book_changes.go` (426 LOC) |
| <a id="p-internal-updater"></a>`internal/updater` | 3 | 547 | generic | 1/2 | mediacore/updater | Self-update mechanism; the release source must be a parameter. Only `updater.go` moves; `scheduler.go` (`Scheduler`, `tick` and the auto-apply loop) is deleted with `SchedulerStarterAdapter` and the `updatescheduler` service, replaced by the `core.updater.check` task (05-updater 5U.12; A-6, MOD9-9). | in 1, out 2. repo slug hard-coded `updater.go:36,68-76`; imports `config`, `serviceregistry` (`register.go:60,76` -> delete).<br>**Split:** **mediacore/updater** ← `updater.go` (335 LOC); **delete (replaced by the `core.updater.check` task, 05-updater 5U.12; MOD9-9)** ← `scheduler.go` (118 LOC); **delete (serviceregistry)** ← `register.go` (94 LOC) |
| <a id="p-pkg-ops"></a>`pkg/ops` | 3 | 3073 | mixed | 8/2 | mediacore/tasks | The v3 authoring model (Task/Batch/Fixer/Pipeline, Schedule, Effects, CancelContract, RC) is domain-neutral and becomes the public `tasks` API (N9). Its Book-typed `Writer` is the only audiobook edge and leaves. | in 8, out 2. imports `internal/auth` (`approval.go:11`, `definition.go:20,93`) and `internal/database` (`writer.go:11`; `Writer` methods `ModifyBook`/`ModifyBookFile`/`RepointBookFile`/`ModifyBookAuthors` `writer.go:46-90`); `SubjectType` book/book_file `writer.go:112-121`; `LaneMac` `lanes.go:15`; omitted `Concurrency` defaults to `CPU()` `batch.go:123` (R7 tightens this to no zero value).<br>**Split:** **internal/books/contract (typed books Writer contract; `tasks.Writer` becomes generic over `Resource`)** ← `writer.go` (148 LOC) |
| <a id="p-pkg-ops-opstest"></a>`pkg/ops/opstest` | 4 | 4056 | mixed | 0/6 | mediacore/tasks/taskstest | Conformance suite, fault injection and fake world for task definitions; every app needs it. | in 0, out 6. imports `opswriter`, `registry`, `state`, `pkg/ops`, `database`, `logger`; Book fakes `fakes.go:74,83,93` (raw/02); no importer in `go list -deps -test` (`tools/cmd/oplint/analyzer_test.go:35` names it only as an `analysistest` fixture path string).<br>**Split:** **internal/books/store/storetest (Book-typed fakes)** ← `fakes.go` (613 LOC) |

### 3.2 Core internal path (5 packages, 22989 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-operations-registry"></a>`internal/operations/registry` | 4 | 15137 | mixed | 39/14 | mediacore/internal/executor (re-implemented engine, written fresh against `tasks.RunStore`; public surface mediacore/tasks; the ABO path is deleted by the tasks flip F5) | The production v2 executor (dispatcher gates, workers, watchdog, resume, cancel, pause gate, RunItems) becomes core's executor. 31 root packages call its API (`RunItems`, `Reporter`, `OperationDef`, `EnqueueOption`, `Registry`), so core's **engine** is a re-implementation written dark at F2, the foundation PRs (05-tasks 5T.8.2's Core group) against `tasks.RunStore`/`RunState` — the engine's 30 non-test files use HEAD's v2/v3 run-row types in 14 of them and hold 39 code references (comments excluded) to HEAD's run-state package `internal/operations/state` in 5 files (generated), which R6-2 keeps in ABO, so no import-path change compiles (R7-8, MOD7-1; §8.6). HEAD's engine keeps running while 05-tasks' groups T2–T7 convert every definition; F5, the tasks flip, registers them all and deletes this path in the same PR (MOD-2 gate), with the v1 status bridge (`legacy_op_status.go`, `legacy_backfill.go`), which has nothing left to bridge once no v1 writer exists. The pause gate's mechanism moves with the engine, while its persisted marker (`registry.operations_paused` and its JSON shape, `pause_gate.go:52-60`) and the scan stand-down's marker (the JSON setting row `registry.scan_standdown`, `scan_standdown.go:27`, written `:571`, cleared `:583`, read back `:596`) are HEAD formats that cutover step M4 decodes into `tasks.Importer` (MOD5-4, MOD6-4); the `library.scan` kind name is books data (check (h)). Audiobook residue: book-field `Requires` predicates, the `Resource` constant list, `GetBookByID` dep adapter. The activity mirror, permanent books behaviour, moves in the flip PR to `internal/activity` (MOD8-8). | in 39, out 14. imports `auth`, `database`, `logger`, `metrics`, `opsmetrics`, `sdnotify`, `serviceregistry`, `pkg/ops`; `database.Book` predicates `deps.go:53,78,84-125`; `Resource` consts `books/book_files/authors/series/catalog/...` `types.go:384-398`; v1 subject `book` `types.go:417-420`; `New(store, slog.Default(), 8, hub)` `register.go:98`; 39 importers; RunItems `Concurrency` clamp `run_items.go:155-157`.<br>**Split:** **internal/books/contract (subject resolvers, Resource declarations, dep adapter)** ← `deps.go` (311 LOC); **delete (serviceregistry wrapper)** ← `register.go` (131 LOC); **delete at F5 (HEAD's v1 status bridge, which keeps v1 `operation:` rows in step and carries the `legacy_op_id` params key: nothing is left to bridge once no v1 writer exists; 05-tasks 5T.8.1)** ← `legacy_op_status.go`, `legacy_backfill.go` (486 LOC); **internal/activity (books subscriber on `core.task.log-appended.v1`, moved in the F5 flip PR that deletes the registry path; the mirror part of `reporter_db.go` (`:37,866`) is rewritten onto it by the engine's §8.6 row; MOD8-8)** ← `activity_mirror.go` (198 LOC) |
| <a id="p-internal-opsmetrics"></a>`internal/opsmetrics` | 3 | 525 | generic | 5/2 | mediacore/internal/executor (task-run instruments, re-implemented; the ABO path is deleted by the tasks flip F5) | OTel instruments for task runs with a closed def-id label set: an executor concern. Core's one successor is the executor's task-run instruments in `mediacore/internal/executor` (05-tasks 5T.11; R8-6, MOD8-2), never public `tasks`, so ABO's closed source names (`opsmetrics.go:367-371`) never enter the SDK. The metric names may change (D10): core records the task-run families under its own names in the namespace the app passes (`app.Options.MetricNamespace`; 05-metrics), and ABO's dashboards and rules switch in the flip PR that renames them (03 §3.8). The ABO file stays at its path until F5, which deletes it with its other users (the engine files `registry/{registry,worker,resume,reporter_db}.go` and `scheduler/v3_schedule.go`), while books' own gauges (`repairs/guards.go`, `plugins/maintenance/retired_versions_fixer.go`, today `Default().RegisterAbandoned`) move **together, in one PR**, to one app-registered observable on books' `metrics.Scope` that carries HEAD's `source` values (`SourceRepairsResolver`, `SourceRetiredVersionsStat`), so `ops_abandoned_calls`, the one family core does not emit, changes owner exactly once and never has two (`ErrFamilyTaken`; 05-metrics §13; R10-3, MOD10-7) (§8.3 gate). | in 5, out 2. imports `operations/state`, `telemetry`; instrument names `audiobook_organizer.ops.*` `opsmetrics.go:12-15,114`; closed source names `opsmetrics.go:367-371`; `RegisterDefID` `defids.go:24`. |
| <a id="p-internal-scheduler"></a>`internal/scheduler` | 4 | 4249 | mixed | 5/16 | mediacore/internal/scheduler (re-implemented core scheduler, public `tasks.Schedule`; the ABO path keeps ticking until the tasks flip F5, which deletes it) | Core's scheduler is a **re-implementation** (§8.6), not a move or a copy (MOD5-4): the ABO formats in the files it is built from stay books — the clock key prefix `scheduled_task_last_tick.` and its RFC3339 codec (`interval_clock.go:50-60`) are HEAD's clock format, which cutover step M4 reads once, so the first R2 boot fires no catch-up storm (05-tasks 5T.9), and the map of 38 task names to ABO def IDs (`taskV2DefIDs`, `maintenance.go:154-175`) is books data the cutover reads (check (h), `KEYCUT`). One schedule mechanism for core (durable interval clock, daily-at, cron, maintenance window). Until F5 ABO's scheduler keeps ticking the 38 `TaskDefinition`s; conversion group T7 turns each into the `Schedule` of its books or core kind, and F5 registers them and deletes the ABO path, so no kind ever has two schedulers (05-tasks 5T.8.1, `TestOneSchedulerPerKind`; DATA4-5). The 38 ABO `TaskDefinition`s and the metadata-upgrade extra op are books declarations; `OperationDef.Schedule` cron strings become live `tasks.Schedule` values (raw/06 §7.5). | in 5, out 16. imports `dedup` (`tasks.go:21`), `scanner` (`tasks.go:24`), `metabatch`/`metafetch` (`extra_ops.go:25-26`) -> 80 transitive deps, 39 audiobook (raw/02 finding 6); durable clock `interval_clock.go:60,146`; v3 loop `v3_schedule.go:193,622`.<br>**Split:** **mediacore/internal/scheduler** ← `cron.go`, `daily_at.go`, `interval_clock.go`, `maintenance.go`, `scheduler.go`, `v3_schedule.go` (2268 LOC); **internal/books (schedule declarations)** ← `tasks.go`, `extra_ops.go`, `full_sweep.go` (1981 LOC) |
| <a id="p-internal-sdnotify"></a>`internal/sdnotify` | 0 | 149 | generic | 5/0 | mediacore/internal/sdnotify (behind `lifecycle` readiness and start-timeout hooks) | systemd notify protocol used by the kernel lifecycle and long migrations. It moves in release R1 and **all five** ABO importers are rewired in that stage (MOD2-1, §8.3): `.` (`--print-capabilities` fast exit, `main.go:53`) → `app`; `internal/server` (READY/STATUS/STOPPING, `server_lifecycle.go:340-343,552`) → the kernel owns readiness; `internal/database` (`startup_notify.go:24`) → the stateless `app.ExtendStart`, injected by `serve` (in release R1 the store opens before `app.New`, 06 §6.4.5); `internal/search` (`register.go:55`) → `module.Deps.ExtendStart` (05-health §5H.3.4); `internal/operations/registry` (`subprocess.go:70`, child-env scrub) → the public constant `lifecycle.NotifySocketEnv`. | in 5, out 0. stdlib only (`sdnotify.go`); importers `.`, `database`, `registry`, `search`, `server`. |
| <a id="p-internal-server-handlers-opsv3"></a>`internal/server/handlers/opsv3` | 5 | 2929 | mixed | 1/9 | mediacore/internal/taskhttp (native `/api/core/v1/tasks`; HEAD's `/api/v3/ops/*` deleted by the tasks flip F5) | The cleanest existing API (own package, injected authz, resumable SSE): becomes core's task API (raw/09 §8.4). The repairs-specific access rule becomes a books authz hook. | in 1, out 9. imports `repairs` (`access.go:15`), `database.WorkerProgress` (`view.go:32,69,102`), gin; SSE resume `events.go:490-640`; 12 routes (raw/09 §2.3). |

### 3.3 Core `shared/` path (0 packages, 0 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|

### 3.4 Merge into another package (20 packages, 8749 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-ai-aijobs"></a>`internal/ai/aijobs` | 3 | 575 | generic | 3/2 | mediacore/integrations/ai (durable provider batch jobs) | Provider batch-API job lifecycle is a generic AI capability; its callback registry becomes module registration (R5). | in 3, out 2. imports `database` (AIJobsStore), `logger`; `Register` `aijobs.go:89`; `BatchClient` `aijobs.go:31-36`. |
| <a id="p-internal-boundedcall"></a>`internal/boundedcall` | 0 | 120 | generic | 2/0 | mediacore/library (time-bounded uninterruptible calls on stuck mounts) | Guarding blocking syscalls on stuck mounts is part of safe filesystem access. | in 2, out 0. stdlib; `boundedcall.go:120`; importers `plugins/maintenance`, `repairs`. |
| <a id="p-internal-compactprogress"></a>`internal/compactprogress` | 3 | 359 | generic | 3/2 | mediacore/storage (compaction progress reporting; public `storage.Admin` statistics) | Reports Pebble compaction progress. `internal/metafetch`, `internal/plugins/maintenance` and `internal/server` stay in ABO and use `Stats`, `Step` and `RunStep`, so it goes to the public `storage` package, not `internal/pebble` (MOD-2). | in 3, out 2. imports `database` (`stats.go:22`, CompactionStats) + `logger`; importers `metafetch`, `plugins/maintenance`, `server`. |
| <a id="p-internal-diag-flight"></a>`internal/diag/flight` | 3 | 659 | generic | 2/1 | mediacore/health (flight recorder in the diagnostics bundle) | Runtime trace flight recorder written on incidents: health diagnostics. It imports ABO's `internal/logger` (`flight.go:108`, `var flightLog = logger.New("flight-recorder")`), which stays in ABO, so the move **rewrites** it to take a `*slog.Logger` (a `log` child logger) through its constructor, with no package-level logger (R1, R5; MOD2-11). | in 2, out 1. imports `logger` only (`flight.go:108`); `flight.go:659`; importers `server`, `handlers`. |
| <a id="p-internal-diskstats"></a>`internal/diskstats` | 0 | 24 | generic | 2/0 | mediacore/library (free-space checks) | Free-space check; used by core backup and library. | in 2, out 0. stdlib; `diskstats_unix.go`; importers `backup`, `server`. |
| <a id="p-internal-libroot"></a>`internal/libroot` | 3 | 215 | generic | 2/1 | mediacore/library | Library-root trust checks (mounted, owned) every library-wide op makes: `library` Root semantics. | in 2, out 1. `libroot.go:189`, `owner_unix.go:26`; imports `util`; importers `maintenance/jobs`, `plugins/maintenance`. |
| <a id="p-internal-mail-mailtest"></a>`internal/mail/mailtest` | 3 | 48 | generic | 2/1 | mediacore/coretest (recording notifier) | Recording fake sender for tests. | in 2, out 1. imports `mail`; 2 test importer files. |
| <a id="p-internal-models"></a>`internal/models` | 0 | 337 | mixed | 5/0 | internal/dedup/unified (dedup score types); `Audiobook` -> internal/itunes | Only the dedup-score type family is live; it is book-pair dedup evidence. Book DTOs are dead except `Audiobook` (one user). Nothing belongs in core (raw/02 §b). | in 5, out 0. 5 importers: `internal/database` (`embedding_store.go:22`), `internal/dedup/unified` (`score.go:23,106,127,135,144` aliases), `internal/itunes` (`import.go:361,394`), `internal/plugins/dedup` (`calibrate_composite.go:180,778`); generic DTOs `audiobook.go:109-144` have 0 users.<br>**Split:** **internal/dedup/unified** ← `dedup_score.go` (188 LOC); **internal/itunes (`Audiobook` only; other types deleted)** ← `audiobook.go` (149 LOC) |
| <a id="p-internal-operations"></a>`internal/operations` | 3 | 379 | generic | 15/1 | mediacore/tasks (`progress.go`, `trigger_source.go` only); v1 checkpoint helpers and selection -> internal/books/contract; `BulkMetadataFetchParams` -> internal/server | Shared v1-era op types. Trigger source and `ProgressReporter` are task concerns and go to `tasks`. The v1 checkpoint row shape `OperationState` with `SaveCheckpoint`/`LoadCheckpoint`/`ClearState` (`state.go:14-100`, 11 books call sites, e.g. `itunes/service/importer.go:626`, `dedup/drain_stale.go:324`) is a books legacy format (05-tasks: the v1 checkpoint family is books'), so it goes to `internal/books/contract` until its callers' conversion PRs (05-tasks T2/T5/T7) replace it with `rc.Checkpoint`, never to the frozen public `tasks` (R6-3, R7-8, MOD7-2); `BulkMetadataFetchParams{PreferAudible}` (`state.go:27-30`) is used only by `internal/server/metadata_ops.go:196,796,885` and goes there. `SelectionSpec`/`ResolveBookIDs` select books. | in 15, out 1. `ProgressReporter` `progress.go:24`; `ResolveBookIDs` `selection.go:16`; trigger source `trigger_source.go:25,31`; v1 checkpoint helpers `state.go:14-100`; `BulkMetadataFetchParams` `state.go:27-30`; 15 importers.<br>**Split:** **mediacore/tasks** ← `progress.go`, `trigger_source.go` (210 LOC); **internal/books/contract (v1 checkpoint helpers `OperationState`, `SaveCheckpoint`/`LoadCheckpoint`/`ClearState`, until their callers use `rc.Checkpoint`, 05-tasks T2/T5/T7; **file divided**: `BulkMetadataFetchParams` (`state.go:27-30`) moves to `internal/server`, its only user; R7-8, MOD7-2)** ← `state.go` (96 LOC); **internal/books/contract (selection helpers)** ← `selection.go`, `types.go` (73 LOC) |
| <a id="p-internal-operations-childop"></a>`internal/operations/childop` | 3 | 158 | generic | 4/2 | mediacore/tasks (re-implemented as `rc.Await`; the ABO path is deleted by the tasks flip F5) | Parent/child run liveness is executor behaviour: core writes `rc.Await` fresh (05-tasks 5T.8.1). The ABO `Follow` is called by v2 `OperationDef` bodies until their conversion group (05-tasks T2–T7: `WaitForOp`, `server_maintenance_deps.go:1185-1198`, which switches on v2 `row.Status`; `plugins/dedup/run_all.go:532`; `label_refinement.go`; `plugins/maintenance/optimize.go`) and by the engine's own `v3_adapter.go`, so the path stays in ABO until F5, which deletes it with the engine (§8.3 deletion gate; R8-6, MOD8-2). | in 4, out 2. imports `database` (v2 run reader) + `state`; `Follow` `follow.go:46,101`. |
| <a id="p-internal-operations-freshness"></a>`internal/operations/freshness` | 0 | 50 | generic | 0/0 | mediacore/tasks (per-item freshness stamps) | Domain-free per-item stamp primitive. Zero importers today, but a consumer is documented (`internal/plugins/maintenance/chapters_backfill.go:40-52`: wire freshness before scheduling that op); belongs with tasks rather than being deleted and re-invented. | in 0, out 0. no importer, no in-module import; `freshness.go:31`. |
| <a id="p-internal-operations-opmode"></a>`internal/operations/opmode` | 0 | 97 | generic | 10/0 | mediacore/tasks (preview/live mode resolution) | Every writer defaults to preview: a tasks contract. | in 10, out 0. no in-module imports; `dryrun.go:8-36,66`; 10 importers. |
| <a id="p-internal-security-pathvalidation"></a>`internal/security/pathvalidation` | 0 | 255 | generic | 7/0 | mediacore/library (path confinement; superseded by os.Root where possible) | Path traversal validation is library confinement. | in 7, out 0. stdlib; `pathvalidation.go:255`; 7 importers. |
| <a id="p-internal-security-safehttp"></a>`internal/security/safehttp` | 0 | 267 | generic | 2/0 | mediacore/integrations/providerhttp (SSRF-safe client for provider-supplied URLs) | Outbound-HTTP safety for third-party URLs belongs to the adapter HTTP layer. | in 2, out 0. stdlib; `safehttp.go:267`; importers `covers`, `metadata`. |
| <a id="p-internal-security-safepath"></a>`internal/security/safepath` | 0 | 51 | generic | 6/0 | mediacore/library | Path-confinement helper used by backup and file ops. | in 6, out 0. stdlib; `safepath.go:51`; 6 importers incl. `backup`. |
| <a id="p-internal-server-middleware"></a>`internal/server/middleware` | 5 | 3247 | mixed | 10/11 | mediacore/auth + mediacore/httpapi (generic middleware); ABS files -> internal/server/absauth; Cloudflare files -> internal/cfaccess | `RequireAuth`, permission checks, credential/owner guards, rate limiting, body limits and log/trace redaction are the core request chain (raw/09 §8.2), re-implemented on net/http. The gin copies of the perimeter concerns are deleted in release R1, when core's perimeter middleware wraps the legacy mount (the Basic gate at F3, with its password moving to `secrets`); the gin auth chain is deleted at F4, when the `legacyauth` bridge (the new ABO package `internal/server/legacyauth`, 03 §3.5.1) reads the core principal inside gin (cross-cutting resolution 2). ABS and Cloudflare Access binders are app/deployment specific. | in 10, out 11. imports `server/absauth` (`absauth.go:19`), `fingerprint/workerapi` (`request_size.go:13`); `RequireAuth` `auth.go:152-262`; role-name admin check `auth.go:522-537` (G4); worker confinement `auth.go:271-306`; 10 importers.<br>**Split:** **mediacore/auth (authn/authz middleware)** ← `auth.go`, `credential_guard.go`, `actor.go` (684 LOC); **mediacore/httpapi** ← `ratelimit.go`, `request_size.go`, `log_redact.go`, `recovery_redact.go`, `trace_redact.go` (873 LOC); **mediacore/httpapi (the Basic gate, perimeter stage 8; at F3, the config flip, because it needs the Basic password, a secret core reads only from F3 on; 05-http-api §8.1)** ← `basicauth.go` (119 LOC); **internal/server/absauth (ABO)** ← `absauth.go`, `absauthprobe.go` (948 LOC); **internal/cfaccess (ABO)** ← `cfaccess.go`, `cfbounce.go`, `owner_guard.go` (623 LOC) |
| <a id="p-internal-startup"></a>`internal/startup` | 0 | 211 | generic | 3/0 | mediacore/kernel (startup step classes) + mediacore/health (degraded tokens), at F8 | The reviewed Fatal/Degraded step table is the kernel supervisor's contract; modules declare their own steps through `Registrar.StartupSteps` (typed `health.StartupStep`), which lands at **F8** with the rest of `health` (06). Until then the path stays in ABO (03 §3.4 visibility rule; MOD3-1, R3-3): `cmd/root.go:274` builds `serve`'s step runner from it and runs seven books steps through it (`root.go:281-385`: `initializeStore`, `InitOTEL`, `EnsureSecureStateDir`, `guardAgainstKeyRegeneration`, `initEncryption`, `loadConfigFromDB`, `config.Validate`), and `internal/server` runs its own start steps through it (`server.go:257,493`, `server_lifecycle.go:157,226-247`). The release-R1 kernel supervisor uses its own fixed core step list. At F8 every user is rewired onto `Registrar.StartupSteps` (§8.3) and the path is deleted. | in 3, out 0. stdlib-only leaf; `steps.go:35-118`, exported slice `var Table` `steps.go:91`; importers `cmd` (**non-test** `root.go:27,274`; test `commands_test.go:25`), `internal/server` (non-test `server.go:86`, `server_lifecycle.go:42`; test `startup_steps_test.go:15`), `internal/server/handlers/system` (test only, `handler_health_test.go:17`). |
| <a id="p-internal-walkresume"></a>`internal/walkresume` | 0 | 242 | generic | 4/0 | mediacore/tasks (yield/resume helpers) | Exists only because layer-3 packages could not import the registry (`walkresume.go:5-17`); with tasks below every ABO layer the seam is unnecessary and its `ErrYield`/checkpoint cadence become tasks helpers. | in 4, out 0. no in-module imports; importers `itunes`, `plugins/maintenance`, `remux`, `server`; `CheckpointEvery = 10` (raw/06 §1.7). |
| <a id="p-internal-watcher"></a>`internal/watcher` | 3 | 457 | generic | 1/3 | mediacore/library (root watching) | fsnotify supervision of media roots is a library concern; the extension filter becomes a module-supplied predicate. | in 1, out 3. imports `appdirs`, `config`, `pathutil`; `config.SupportedExtensionSet()` `watcher.go:243` (raw/02 a2); `fsnotify` dependency. |
| <a id="p-tools-cmd-oplint"></a>`tools/cmd/oplint` | 6 | 998 | generic | 0/0 | mediacore/archtest/tasklint | Lints task bundles (unbundled-def, writer-bypass, store-in-deps, missing-help, sequential-empty-reason, deletes-owner-review); these rules govern every app's `tasks` definitions, so they are a `tasks` platform concern delivered through `archtest` (apps run it from their own tests). | in 0, out 0. analyzer `tools/cmd/oplint/analyzer.go`; hard-codes `modulePath` (`analyzer.go:45`) and ~12 fixtures under `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` (raw/02 §e) -> becomes a parameter; test imports `pkg/ops/opstest` (`analyzer_test.go:35`). |

### 3.5 ABO app path (stays in the root module) (164 packages, 623802 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-root"></a>`.` | 6 | 86 | mixed | 0/6 | . (unchanged) | The ABO `main`; becomes the composition root that calls `app.New(ctx, opts, books.Module())` (03 §3.7). | in 0, out 6. imports `internal/database` (`main.go:13`), `internal/server` (`main.go:16`), registry, sdnotify, telemetry. Its edge `. -> cmd` (`main.go:12`) is the root-module in-module positive control (03 §3.5.1). |
| <a id="p-cmd"></a>`cmd` | 6 | 1876 | mixed | 1/14 | cmd (unchanged; no file moves) | Cobra CLI of ABO; the CLI and cobra stay in ABO (03 §3.5.1, 06, 13 §13.5.1). No file of `cmd` moves (MOD2-4): `serve`'s store/OTel/slog bootstrap in `root.go` is **replaced** by `app.New`/`app.InstallProcessDefaults` calls in release R1 and its flag wiring moves onto `app.Options`; `rootCmd`, `SetVersion`/`Execute` (`root.go:92,462-467`, called by `main.go:23,31`) and `guardAgainstKeyRegeneration` (`root.go:760`) stay. `cmd/` is not in core's history path file (12 §12.8.1). | in 1, out 14. imports `internal/fingerprint` (`cmd/fp_worker.go:24`), uses `database.BookCore/Author/Series` (`cmd/diagnostics.go:116`); `cmd/root.go:280` opens the store, `:718` calls `slog.SetDefault` (R5: replaced by `app.InstallProcessDefaults`). Build-tagged files that stay with it: `dedup_bench.go`, `dedup_bench_batch.go`, `dedup_bench_crossval.go`, `dedup_bench_pass2.go`, `dedup_bench_prompts.go`, `dedup_bench_runner.go`, `dedup_bench_status.go`, `dedup_bench_summary.go`, `dedup_bench_types.go` (`//go:build bench`, `rootCmd.AddCommand` at `dedup_bench.go:63`), `umask_windows.go`. |
| <a id="p-cmd-itl-audit-encoding"></a>`cmd/itl-audit-encoding` | 6 | 90 | audiobook | 0/1 | cmd/itl-audit-encoding (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 1. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-itl-check"></a>`cmd/itl-check` | 6 | 89 | audiobook | 0/1 | cmd/itl-check (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 1. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-itl-diff"></a>`cmd/itl-diff` | 6 | 319 | audiobook | 0/1 | cmd/itl-diff (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 1. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-mtls-bridge"></a>`cmd/mtls-bridge` | 6 | 369 | generic | 0/1 | cmd/mtls-bridge (unchanged) | Dev tool bridging an MCP stdio protocol between hosts; not a platform concern (03 §3.3 list) and one consumer. | in 0, out 1. only importer of `internal/mtls` (`cmd/mtls-bridge/main.go:12`; raw/09 §7: not used by the HTTP server). |
| <a id="p-cmd-pebble-inject-skip"></a>`cmd/pebble-inject-skip` | 6 | 65 | generic | 0/1 | cmd/pebble-inject-skip (unchanged) | Operator repair tool for ABO's Pebble store, deliberately bypassing the format guard. | in 0, out 1. opens Pebble directly (`cmd/pebble-inject-skip/main.go:35`, raw/05 §1 row 8); writes ABO key families. |
| <a id="p-cmd-pid-census"></a>`cmd/pid-census` | 6 | 226 | mixed | 0/2 | cmd/pid-census (unchanged) | iTunes maintenance CLI; audiobook-only. | in 0, out 2. imports `internal/itunes` only (raw/01). |
| <a id="p-cmd-verify-suite"></a>`cmd/verify-suite` | 6 | 57 | generic | 0/1 | cmd/verify-suite (unchanged) | ABO dev tool; one consumer; not a platform concern. | in 0, out 1. no importer; one in-module import (raw/01). |
| <a id="p-internal-acoustid"></a>`internal/acoustid` | 0 | 224 | audiobook | 2/0 | internal/acoustid (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is a thin client for the acoustid.org /v2/lookup API. We only need the smallest slice of the response —". | in 2, out 0; importers: `internal/plugins/acoustid`, `internal/reconcile` |
| <a id="p-internal-activity"></a>`internal/activity` | 3 | 2526 | mixed | 14/5 | internal/activity (unchanged path; book changelog + per-user audit entries) | The books activity feed (tiers, batching, digests, changelog) stays in the books module (03 §3.3 `log` row; 05-logging §5): core `log` gets a new system-log journal and core `audit` gets new rows only. The slog line parsing in `writer.go:149-215` is deleted by 05-logging stage 1. | in 14, out 5. imports `undo` (`changelog.go:17`), uses `database.MetadataChangeRecord/BookPathChange` (`changelog.go:27-28`); serviceregistry registrations `register.go:37-120`; 14 importers.<br>**Split:** **delete (serviceregistry)** ← `register.go` (129 LOC); **internal/activity (ABO)** ← `api.go`, `batcher.go`, `service.go`, `writer.go`, `changelog.go`, `user_data.go` (2397 LOC) |
| <a id="p-internal-ai"></a>`internal/ai` | 3 | 6108 | mixed | 10/9 | internal/ai (unchanged path; audiobook AI features) | The OpenAI-compatible chat/batch/embedding clients, retry and pool routing are a provider adapter (`integrations/ai/openai`). `ai_metrics.go` and `telemetry.go` are **divided** (MOD10-10; 05-ai §8): their recorder half (`aiInstrumentsFor`, `modelLabelSet`, `callMeta.normalized`, `classifyCall`, the exported `RecordParseResult` books' parser calls, `attemptMiddleware`, `classifyAttempt`, `observe`'s counting rules, `recordReplyFailure`; `ai_metrics.go:144-462`, `telemetry.go:125-391`) stays here as **books' AI recorder**, with `ai_metrics_models.go` (its model-label seeding from ABO config) and `internal/aidispatch/metrics.go`, and only the client-side tracing goes to the adapter. Books' AI recorder, through `ai.WithObserver` (`Observer.Attempt` and the per-HTTP-attempt `Observer.Transport`), is the one owner of every `ai.*` and `ai_dispatch.*` family, and core records only `<ns>_ai_*` (R9-3, COR9-14; 05-ai §8); prompts and features (dedup review, author dedup, cover text, metadata scoring) are books. | in 10, out 9. imports `covertext` (`cover_text.go:20`), uses `database.DedupCandidate` (`dedup_review.go:80`); concrete `*OpenAIParser` `openai_parser.go:261,358`; `aijobs.Register` from `init()` `author_dedup_job.go:132`, `dedup_review.go:242`; 10 importers.<br>**Split:** **mediacore/integrations/ai/openai (adapter; client-side tracing from the divided `telemetry.go`, no HEAD `ai.*` instrument, MOD10-10)** ← `embedding_batch.go`, `embedding_client.go`, `openai_batch.go`, `openai_batch_find.go`, `retry.go`, `pool_routing.go`, `aijobs_adapter.go` (1899 LOC); **delete (serviceregistry)** ← `register.go` (174 LOC); **internal/ai (ABO; `openai_parser.go` splits: client -> adapter, Parse* methods -> ABO; books' AI recorder: `ai_metrics.go` and `telemetry.go` are divided, their recorder half staying here and their client-side tracing going to the adapter, and `ai_metrics_models.go` seeds the recorder's model labels, MOD10-10)** ← `openai_parser.go`, `author_dedup_job.go`, `cover_text.go`, `dedup_review.go`, `embedding_scorer.go`, `llm_scorer.go`, `metadata_llm_review.go`, `metadata_scorer.go`, `ai_metrics.go`, `ai_metrics_models.go`, `telemetry.go` (4035 LOC) |
| <a id="p-internal-ai-mocks"></a>`internal/ai/mocks` | 3 | 166 | generic | 2/1 | internal/ai/mocks (unchanged) | Mock of `MetadataCandidateScorer`, which stays in ABO. | in 2, out 1. `mock_metadata_candidate_scorer.go`; 3 test importer files. |
| <a id="p-internal-ai-resultjournal"></a>`internal/ai/resultjournal` | 3 | 263 | generic | 5/2 | internal/ai/resultjournal (unchanged path; plain books code over books' `aijournal:` claim; no core store interface, COR5-10) | Durable cache of finished AI results, used only by books packages (`internal/transcribe`, `internal/scanner`, `internal/plugins/maintenance`, `internal/reconcile`). It hard-codes ABO's family `aijournal:` (`journal.go:51`) and no core service persists through it, so it stays in ABO as plain books code over books' own claim, written only by books' apply handlers; 05-ai defines no result-journal interface (COR5-10, SIMP5-3). | in 5, out 2. imports `database` (RawKV), `logger`; `KVStore` `journal.go:81`; family `aijournal:` (raw/05 §3). |
| <a id="p-internal-aiscan"></a>`internal/aiscan` | 3 | 2915 | mixed | 2/5 | internal/aiscan (unchanged) | AI author-scan pipeline over books and dedup; book metadata cross-validation. | in 2, out 5; imports audiobook pkgs: `internal/dedup`; importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-appdirs"></a>`internal/appdirs` | 3 | 88 | generic | 8/3 | internal/appdirs (unchanged path; adapter from ABO config to `library.AppDirs`) | Core `library` takes an `AppDirs` value (the type moves with `internal/pathutil/hidden.go`); `FromConfig` takes `*config.Config` and `Current` reads the global `config.AppConfig` (`appdirs.go:48-73`), which R1 and R5 keep out of core, so the package stays in ABO as the adapter (MOD-21). | in 8, out 3. imports `backup`, `config`, `pathutil` (`appdirs.go:55-66`); 8 importers. |
| <a id="p-internal-applycap"></a>`internal/applycap` | 0 | 401 | generic | 9/0 | internal/applycap (unchanged) | Ceiling on how many BOOKS one bulk apply may touch; the concept is ABO's bulk metadata apply. | in 9, out 0. stdlib; `applycap.go:401`; 9 ABO importers. |
| <a id="p-internal-applygate"></a>`internal/applygate` | 3 | 2970 | audiobook | 10/8 | internal/applygate (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the certainty gate every BULK metadata apply consults before it writes a candidate onto a book: the cached". | in 10, out 8; imports audiobook pkgs: `internal/authorjunk`, `internal/authorname`, `internal/franchise`, `internal/metadata` (+2); importers: `internal/catalog`, `internal/itunes/service`, `internal/maintenance/jobs` (+7) |
| <a id="p-internal-arch"></a>`internal/arch` | 0 | 0 | generic | 0/0 | internal/arch (unchanged path; ABO layering guard) | 03 §3.5.1: the classification engine moves to `archtest` as a library; the ABO guard keeps its rule with the ratio floor, the `. -> internal/server` control edge and layer -1 for core imports. | in 0, out 0. 0 non-test LOC; `const minModulePackages = 150` `layering_test.go:38`; control edge `:40`; `layerOf` 213 keys `:59-286`; sibling guards hard-code `internal/...` (raw/03 §4.7).<br>**Split:** **mediacore/archtest (engine, from layering_test.go)** |
| <a id="p-internal-audio"></a>`internal/audio` | 3 | 107 | audiobook | 1/1 | internal/audio (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 1; imports audiobook pkgs: `internal/transcode`; importers: `internal/server` |
| <a id="p-internal-audiobooks"></a>`internal/audiobooks` | 3 | 20631 | audiobook | 15/29 | internal/audiobooks (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides the core business logic for managing audiobooks, including CRUD operations, metadata management". | in 15, out 29; imports audiobook pkgs: `internal/authorcredit`, `internal/dedup`, `internal/itunesguard`, `internal/mediainfo` (+11); importers: `internal/batch`, `internal/decisions`, `internal/maintenance/jobs` (+12) |
| <a id="p-internal-audioext"></a>`internal/audioext` | 0 | 160 | audiobook | 5/0 | internal/audioext (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "owns the ONE list of file extensions that decide whether a file on disk is part of this library". | in 5, out 0; importers: `internal/bookfiles`, `internal/config`, `internal/deluge` (+2) |
| <a id="p-internal-audioutil"></a>`internal/audioutil` | 0 | 384 | audiobook | 8/0 | internal/audioutil (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds small, dependency-free helpers shared by the audio processing packages (internal/mediainfo". | in 8, out 0; importers: `internal/fingerprint`, `internal/mediainfo`, `internal/plugins/acoustid` (+5) |
| <a id="p-internal-authorcredit"></a>`internal/authorcredit` | 3 | 1317 | audiobook | 10/4 | internal/authorcredit (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "turns one author credit string ("J.N. Chaney, Jonathan P. Brazee") into the author rows a book should be". | in 10, out 4; imports audiobook pkgs: `internal/authority`, `internal/personname`; importers: `internal/audiobooks`, `internal/authority/authoritybuild`, `internal/importer` (+7) |
| <a id="p-internal-authority"></a>`internal/authority` | 3 | 810 | audiobook | 8/2 | internal/authority (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "keeps the authority lists: a rebuildable reference index of known people (authors, narrators, cast members)". | in 8, out 2; imports audiobook pkgs: `internal/personname`; importers: `internal/authorcredit`, `internal/authority/authoritybuild`, `internal/catalog` (+5) |
| <a id="p-internal-authority-authoritybuild"></a>`internal/authority/authoritybuild` | 4 | 1233 | audiobook | 1/5 | internal/authority/authoritybuild (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 5; imports audiobook pkgs: `internal/authorcredit`, `internal/authority`, `internal/catalog`, `internal/metadata`; importers: `internal/plugins/maintenance` |
| <a id="p-internal-authorjunk"></a>`internal/authorjunk` | 3 | 1351 | audiobook | 7/3 | internal/authorjunk (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "answers "is this AUTHOR ROW a person, or something the importer filed in the author field that is not a". | in 7, out 3; imports audiobook pkgs: `internal/authorname`, `internal/personname`; importers: `internal/applygate`, `internal/catalog`, `internal/metabatch` (+4) |
| <a id="p-internal-authormerge"></a>`internal/authormerge` | 3 | 342 | audiobook | 2/2 | internal/authormerge (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the journaled write path every author merge shares: moving one book's credits off the merged author, and". | in 2, out 2; imports audiobook pkgs: `internal/undo`; importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-authorname"></a>`internal/authorname` | 0 | 757 | audiobook | 12/1 | internal/authorname (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the one author name the system writes to mean "we could not resolve an author". It exists as its own". | in 12, out 1; imports audiobook pkgs: `internal/personname`; importers: `internal/applygate`, `internal/authorjunk`, `internal/dedup` (+9) |
| <a id="p-internal-batch"></a>`internal/batch` | 3 | 1169 | audiobook | 5/6 | internal/batch (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 5, out 6; imports audiobook pkgs: `internal/merge`, `internal/versionprimary`; importers: `internal/audiobooks`, `internal/metafetch`, `internal/server` (+2) |
| <a id="p-internal-boilerplate"></a>`internal/boilerplate` | 3 | 95 | audiobook | 3/1 | internal/boilerplate (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the compiled-in list of publisher intro/outro "titles" that are not real books, plus a pure matcher". | in 3, out 1; imports: `internal/util`; importers: `internal/dedup`, `internal/dedup/dataset`, `internal/plugins/maintenance` |
| <a id="p-internal-bookfileaudio"></a>`internal/bookfileaudio` | 3 | 209 | audiobook | 7/3 | internal/bookfileaudio (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "fills a book_file row's audio facts (duration, codec) at the moment the row is created, so no writer stores a". | in 7, out 3; imports audiobook pkgs: `internal/mediainfo`; importers: `internal/importer`, `internal/maintenance/jobs`, `internal/merge` (+4) |
| <a id="p-internal-cache"></a>`internal/cache` | 0 | 294 | generic | 10/1 | internal/cache (unchanged) | Generic in-memory cache with generation counter, but every consumer is ABO and no core contract needs it (03 §3.4 rule). | in 10, out 1. imports `metrics`; importers `ai`, `audiobooks`, `database`, `server`, handlers (9). |
| <a id="p-internal-catalog"></a>`internal/catalog` | 4 | 3071 | audiobook | 3/9 | internal/catalog (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the author catalog (design .claude/notes/catalog-wanted-requests-design-2026-10-01.md, phase P1): the". | in 3, out 9; imports audiobook pkgs: `internal/applygate`, `internal/authority`, `internal/authorjunk`, `internal/metadata` (+2); importers: `internal/authority/authoritybuild`, `internal/server`, `internal/server/handlers/metadata` |
| <a id="p-internal-cfaccesspolicy"></a>`internal/cfaccesspolicy` | 0 | 622 | generic | 1/0 | internal/cfaccesspolicy (unchanged) | Edits a Cloudflare Access policy to admit invited guests: deployment-specific edge auth (G11), one consumer. | in 1, out 0. `client.go:83-325`; 1 importer. |
| <a id="p-internal-cfservicetoken"></a>`internal/cfservicetoken` | 0 | 437 | generic | 1/0 | internal/cfservicetoken (unchanged) | Mints per-guest Cloudflare service tokens; deployment-specific (G11). | in 1, out 0. `client.go:53,170-191`; name prefix `aorg-guest-`; 1 importer. |
| <a id="p-internal-chaptershape"></a>`internal/chaptershape` | 0 | 97 | audiobook | 4/0 | internal/chaptershape (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "recognises the "one chapter per folder" layout: <Book>/<Book> - 1/file <Book>/<Book> - 2/file ... The scanner". | in 4, out 0; importers: `internal/database`, `internal/organizer`, `internal/plugins/maintenance` (+1) |
| <a id="p-internal-config"></a>`internal/config` | 1 | 10506 | mixed | 52/10 | internal/config (unchanged path; books typed config section `modules.books.*` and the `config.AppConfig` façade over core handles, kept: SIMP-3) | Generic loader machinery (layering, `Mutate`/`Snapshot`/`Generation`, viper decode, unknown/removed-key detection, protected fields, secure state dir, secret-row persistence) goes to core `config`/`secrets`; the 864-line struct's audiobook keys, blob migrations and ABO sub-configs stay. Its five wrong-way edges disappear because core `config` imports nothing above storage (R4). | in 52, out 10. imports `audioext` (`config.go:23`), `dedup/unified` (`dedup_score_config.go:11`, `update_service.go:22`), `database`, `auth`, `backup`, `aidispatch` (allowed wrong-way edges 1-5, `internal/arch/layering_test.go:291-295`); global `var AppConfig` (`config.go:1939`), viper (`config.go:2590`) violate R5; 52 importers.<br>**Split:** **mediacore/config (loader, layering, generation, decode, key audits; `state_dir.go` keeps HEAD's fallback next to the database, R3-4)** ← `config_deepcopy.go`, `viper_decode.go`, `unknown_keys.go`, `removed_keys.go`, `protected_fields.go`, `blob_default_audit.go`, `state_dir.go`, `persistence_owner_unix.go`, `persistence_owner_other.go` (1575 LOC); **mediacore/integrations/ai (endpoint config section)** ← `ai_endpoints.go` (554 LOC); **mediacore/auth (bootstrap-key TTL, invite + SMTP sections)** ← `bootstrap_key_ttl.go`, `invite_config.go` (324 LOC); **mediacore/log (retention section)** ← `log_retention.go` (104 LOC); **delete (serviceregistry accessor)** ← `register.go` (51 LOC); **internal/config (ABO; config.go/persistence.go/update_service.go are split by function: loader+secret rows -> core, struct+blob migrations -> ABO)** ← `abs_config.go`, `chapter_consolidation.go`, `dedup_score_config.go`, `itunes_libraries.go`, `naming_patterns.go`, `config.go`, `persistence.go`, `update_service.go` (7911 LOC) |
| <a id="p-internal-covers"></a>`internal/covers` | 3 | 166 | audiobook | 3/2 | internal/covers (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 2; imports: `internal/security/safehttp`, `internal/security/safepath`; importers: `internal/plugins/maintenance`, `internal/server`, `internal/server/handlers/abs` |
| <a id="p-internal-covertext"></a>`internal/covertext` | 0 | 226 | audiobook | 3/0 | internal/covertext (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "stores the text a vision model read off a cover image". | in 3, out 0; importers: `internal/ai`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-database"></a>`internal/database` | 2 | 103432 | mixed | 110/16 | internal/books/store (renamed in release R1; residue = books module store, plus until R3 the HEAD-format readers of the R2 cutover steps) | Keystone split. The 445-method `Store` dissolves: 108 methods sit behind core contracts and 337 stay as the books module's own store (314 domain + 23 HEAD ops-v1). Of the 108, only the 11 storage-engine methods move into core `storage`. The other **97** (tasks 39, auth 33, AI jobs 10, rbac 6, library 6, log 3) are HEAD's code over the families that core services take over: core never calls them. Each core service runs its own native store over `c/<subsystem>/` and exports an importer (03 §3.3 "Store interfaces and importers"), and in R2 books' cutover steps M2–M6 decode HEAD's rows with this code, hand the records to the importers and delete the rows (03 §3.8; 09 §9.2.3), so core holds no audiobook key name or row shape (R4-1, 03 R1(c)). The steps live in `internal/books/cutover`, which imports this package and is never imported by it (R5-2, MOD5-1; 03 §3.5.1). HEAD's 15 run-row store files and `pebble_store_plan_legacy.go` (legacy step 66's body) move into it, unchanged in format, at F5, the tasks flip that deletes HEAD's engine, with the bodies of legacy steps 62 and 65 and the ops part of `InitializeStore`; the legacy step list reaches 62/65/66 through funcs the composition root wires, and the residue keeps only exported helpers the moved files call (§4.5, §8.6, gate E). The identity store, HEAD's cipher and setting rows, and the AI-job, throttle, import-root and dirty-set files stay here as the readers of steps M2, M3 and M6. R3 deletes all of this code with the cutover package and books' legacy steps 1..66 (the HEAD-format readers, 03 §3.8). Books' guard projection onto HEAD's format stamp (`storage.LegacyGuardProjection`, which M7 raises past `LegacyMaxFormat`), the version-source adapter over `preference:` (05-storage §5S.3.7, §5S.3.5) and the exact census over books families stay for good. The residue is renamed because, once core owns `storage`, a root-module package named `database` would read as the platform DB. See §4. | in 110, out 16. 110 importers (61 audiobook, 37 mixed, 6 generic importers, raw/02 finding 1); `type Book struct` `internal/database/store.go:202`; `Store` composes six composites `store.go:48-55`; imports 7 audiobook packages (`pebble_store.go:31,33,35`, `pebble_store_authors.go:21`, `metadata_field_locks.go:15`, `book_own_folder.go:15`, `pebble_store_bookmarks.go:39`) and `models` (`embedding_store.go:22`); ABO key literals in the files that stay as HEAD-format readers: `pebble_store_auth.go` (`apikey:`, `idx:apikey:hash:`), `oauth_identity.go` (`oauthid:`), `pebble_store_aijobs.go` (`aijob:`), `pebble_store_importpaths.go` (`import_path:`), `provider_throttle.go` (`provider_throttle:`), `pebble_store_search_dirty.go:39` (`idx:sidx:dirty:`), `settings.go` (`setting:`), `storage_format.go`/`migration_bookkeeping.go` (`preference:`), `census_exact.go:31` (`system:census:exact`).<br>**Split:** **mediacore/storage + mediacore/internal/pebble** ← `census.go`, `census_families.go`, `compaction_stats.go`, `database.go`, `interface.go`, `partial_read.go`, `pebble_gate.go`, `pebble_metrics_export.go`, `raw_batch.go`, `raw_snapshot.go`, `startup_notify.go`, `store_from_store.go` (2292 LOC); **stays until R3: HEAD's identity store over the identity rows (users, API keys incl. `abk_` token hashing, invites, sessions, OAuth ids, `role:` rows), the reader of cutover step M3 (R4-1; 05-authn §11, 05-rbac §6)** ← `apikey_token.go`, `iface_auth.go`, `iface_oauth.go`, `invite_id.go`, `oauth_identity.go`, `pebble_store_auth.go`, `pebble_store_invite_delivery.go`, `pebble_store_verified_email_backfill.go` (3190 LOC); **internal/books/cutover (at F5, the reader of cutover step M4: HEAD's v2/v3 run-row store, unchanged in format, plus legacy step 66's body, importing HEAD's run-state vocabulary from `internal/operations/state`, which stays in ABO (R6-2, MOD6-1); it imports the residue and the residue never imports it, R5-2, MOD5-1, gate E; 05-tasks 5T.8.1); deleted in R3 with the HEAD-format readers** ← `iface_op_rows.go`, `iface_ops_v2.go`, `iface_ops_v3.go`, `op_class_lists.go`, `ops_v2_recent.go`, `pebble_store_op_rows.go`, `pebble_store_ops_v2.go`, `pebble_store_ops_v2_status_lookup.go`, `pebble_store_ops_v2_timeline.go`, `pebble_store_ops_v2_wait.go`, `pebble_store_ops_v3.go`, `pebble_store_ops_v3_lastrun.go`, `pebble_store_ops_v3_migrate.go`, `pebble_store_ops_v3_reconcile.go`, `pebble_store_ops_v3_rowmarks.go`, `pebble_store_plan_legacy.go` (7760 LOC); **stays: HEAD's cipher and the setting rows: books' `setting:` store, and the four secret rows HEAD stores encrypted (OpenAI key, Google Books key, Hardcover token, Basic password), which ABO's config code uses until F3 and step M2 then re-encrypts into `c/secrets/` with a frozen copy of the cipher (05-config §1.12.2)** ← `settings.go` (466 LOC); **stays until R3: HEAD's AI job store, the reader of cutover step M6 (05-ai)** ← `ai_jobs_types.go`, `pebble_store_aijobs.go` (292 LOC); **stays until R3: HEAD's provider throttle over `provider_throttle:`, in use until F7; step M6 deletes its rows, because the holds are reset (D12 (b); 05-metadata M2)** ← `provider_throttle.go` (74 LOC); **stays until R3: HEAD's import-root store over `import_path:`, which also keeps books' memdb table current (DATA4-4); step M6 writes the roots as `core.library.roots` entries, IDs kept (05-library)** ← `pebble_store_importpaths.go` (261 LOC); **stays until R3: HEAD's search dirty set over `idx:sidx:dirty:` and its change observer, in use until F7; books' index is rebuilt under `storage/index` and step M6 deletes the dirty-set rows (03 §3.3; 05-storage §5S.3.8)** ← `change_observer.go`, `dirty_set.go`, `pebble_store_search_dirty.go` (401 LOC); **stays: books' guard projection onto HEAD's format stamp, version-source adapter and exact census over books families (05-storage §5S.3.5, §5S.3.7)** ← `census_exact.go`, `migration_bookkeeping.go`, `storage_format.go`, `storage_format_repair.go` (1816 LOC); **delete (decorator/SQLite leftovers)** ← `audiobooks.go`, `diagnostics_types.go`, `store_capability.go`, `storecap.go`, `web.go` (249 LOC) |
| <a id="p-internal-database-aggtest"></a>`internal/database/aggtest` | 0 | 146 | generic | 4/0 | internal/books/store/aggtest (renamed) | Counts `RecomputeBookAggregates` runs for tests; books-only. | in 4, out 0. test-only helper; 9 test importer files; `RecomputeBookAggregates` is books (`BookAggregateWriter`, raw/04 §8). |
| <a id="p-internal-database-dbtest"></a>`internal/database/dbtest` | 3 | 449 | mixed | 11/1 | internal/books/store/storetest (renamed) | Books store test fixtures and invariants. The in-memory engine it relies on becomes core `coretest.NewStorage(t)` (vfs.NewMem), which this package then wraps. | in 11, out 1. uses `database.Book` (`internal/database/dbtest/invariants.go:84`); in-memory store `NewPebbleStoreInMemory` (`internal/database/pebble_store.go:578`); 30 test importer files.<br>**Split:** **mediacore/coretest (in-memory storage engine)** ← `newstore.go` (99 LOC) |
| <a id="p-internal-decisions"></a>`internal/decisions` | 3 | 2751 | mixed | 3/3 | internal/decisions (unchanged) | Retired-version decisions over `Book`/`BookVersion`/`BookFile`. | in 3, out 3; imports: `internal/database`, `internal/keyedmutex`, `internal/logger`; importers: `internal/audiobooks`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-dedup"></a>`internal/dedup` | 3 | 15806 | audiobook | 19/21 | internal/dedup (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "exact-rule provenance and the placeholder-title guard". | in 19, out 21; imports audiobook pkgs: `internal/authorname`, `internal/boilerplate`, `internal/dedup/dataset`, `internal/dedup/unified` (+6); importers: `internal/aiscan`, `internal/audiobooks`, `internal/decisions` (+16) |
| <a id="p-internal-dedup-dataset"></a>`internal/dedup/dataset` | 3 | 821 | audiobook | 3/4 | internal/dedup/dataset (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "builds labeled dedup examples and runs deterministic catchers over them". | in 3, out 4; imports audiobook pkgs: `internal/boilerplate`, `internal/fingerprint`; importers: `internal/dedup`, `internal/plugins/dedup`, `internal/server/handlers/dedup` |
| <a id="p-internal-dedup-unified"></a>`internal/dedup/unified` | 3 | 686 | audiobook | 10/1 | internal/dedup/unified (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides the scoring core for the unified deduplication pipeline (SPEC 1, fable5)". | in 10, out 1; imports: `internal/models`; importers: `internal/config`, `internal/database`, `internal/decisions` (+7) |
| <a id="p-internal-deluge"></a>`internal/deluge` | 3 | 2264 | mixed | 4/10 | internal/deluge (unchanged path; library import, discovery, protected paths) | The Deluge Web JSON-RPC client is the first `download` adapter; discovery-by-label, import, mark-in-place and protected paths are audiobook integration logic. | in 4, out 10. concrete client `client.go:39,136,259-483` (raw/11 §4); `discovery.go:36` imports `database`; imports `fingerprint`, `tagger`, `audioext`.<br>**Split:** **mediacore/integrations/download/deluge** ← `client.go` (506 LOC) |
| <a id="p-internal-diagnosis"></a>`internal/diagnosis` | 0 | 295 | audiobook | 1/0 | internal/diagnosis (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "probes audio files with available system tools (ffprobe, mediainfo, file) to produce structured diagnostic". | in 1, out 0; importers: `internal/remux` |
| <a id="p-internal-diagnostics"></a>`internal/diagnostics` | 3 | 799 | mixed | 3/2 | internal/diagnostics (unchanged) | Diagnostic export of books and iTunes albums (SlimBook). | in 3, out 2; imports audiobook pkgs: `internal/itunes`; importers: `internal/server`, `internal/server/handlers`, `internal/server/handlers/opaccess` |
| <a id="p-internal-errhandling"></a>`internal/errhandling` | 0 | 120 | generic | 1/0 | internal/errhandling (unchanged) | Silent-failure sweep primitive used only by the ABO server. | in 1, out 0. stdlib; 1 importer (`internal/server`). |
| <a id="p-internal-filehash"></a>`internal/filehash` | 0 | 186 | audiobook | 14/0 | internal/filehash (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "owns the ONE hash algorithm that identifies an audio file in this database". | in 14, out 0; importers: `internal/fileops`, `internal/importer`, `internal/itunes/service` (+11) |
| <a id="p-internal-fileops"></a>`internal/fileops` | 3 | 1660 | mixed | 16/6 | internal/fileops (unchanged path; tag writing + book-file hash recording only) | Byte copy, reflink, safe move/copy, browse allow-list and the canonical whole-file hash are generic library operations; tag writing and book_file hash recording are books. | in 16, out 6. imports `filehash` (`book_file_hash_recorder.go:12`, `write_tags_safe.go:21`), `database.BookFile*` (`book_file_hash_recorder.go:20-40`); 16 importers.<br>**Split:** **mediacore/library** ← `copy.go`, `hash.go`, `reflink.go`, `reflink_linux.go`, `safe_operations.go`, `service.go`, `reflink_darwin.go`, `reflink_unsupported.go` (1122 LOC); **delete (serviceregistry)** ← `register.go` (22 LOC); **internal/fileops (ABO)** ← `book_file_hash_recorder.go`, `write_tags_safe.go` (568 LOC) |
| <a id="p-internal-fingerprint"></a>`internal/fingerprint` | 0 | 2866 | audiobook | 13/1 | internal/fingerprint (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "generates AcoustID-compatible acoustic fingerprints for audio files". | in 13, out 1; imports audiobook pkgs: `internal/audioutil`; importers: `cmd`, `internal/database`, `internal/dedup` (+10) |
| <a id="p-internal-fingerprint-workerapi"></a>`internal/fingerprint/workerapi` | 0 | 314 | audiobook | 4/0 | internal/fingerprint/workerapi (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the wire format of the remote fingerprint worker API (/api/v1/fingerprint/worker/*), shared by the server". | in 4, out 0; importers: `internal/fingerprint/workerclient`, `internal/plugins/acoustid`, `internal/server/handlers/fpworker` (+1) |
| <a id="p-internal-fingerprint-workerclient"></a>`internal/fingerprint/workerclient` | 3 | 1968 | audiobook | 1/4 | internal/fingerprint/workerclient (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the fp-worker client: a process on another host (an Apple Silicon Mac in practice) that leases". | in 1, out 4; imports audiobook pkgs: `internal/fingerprint`, `internal/fingerprint/workerapi`; importers: `cmd` |
| <a id="p-internal-flight"></a>`internal/flight` | 0 | 128 | generic | 3/0 | internal/flight (unchanged) | Collapse-concurrent-requests helper; consumers are ABO only. | in 3, out 0. stdlib; importers `audiobooks`, `server`, `handlers/audiobooks`. |
| <a id="p-internal-foldercover"></a>`internal/foldercover` | 3 | 345 | audiobook | 2/3 | internal/foldercover (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "decides whether a book should take its cover from an image in its own folder, and applies that decision". | in 2, out 3; imports audiobook pkgs: `internal/audioext`, `internal/metadata`; importers: `internal/plugins/maintenance`, `internal/scanner` |
| <a id="p-internal-foldernames"></a>`internal/foldernames` | 3 | 344 | audiobook | 3/4 | internal/foldernames (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "builds the person and series evidence the folder parse (metadata.ExtractMetadataFromFolderWith) decides an". | in 3, out 4; imports audiobook pkgs: `internal/authority`, `internal/metadata`, `internal/personname`; importers: `internal/metafetch`, `internal/plugins/maintenance`, `internal/scanner` |
| <a id="p-internal-franchise"></a>`internal/franchise` | 0 | 812 | audiobook | 5/0 | internal/franchise (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the one matcher for the libraries the owner curates by hand: Doctor Who, Big Finish and Torchwood". | in 5, out 0; importers: `internal/applygate`, `internal/maintenance/jobs`, `internal/metadata` (+2) |
| <a id="p-internal-httputil"></a>`internal/httputil` | 0 | 604 | mixed | 19/2 | internal/httputil (unchanged path; gin helpers of the permanent legacy mount); envelope types and Range serving -> mediacore/httpapi | Response envelope and request parsing are the core HTTP framework, but the gin helpers cannot move (core is `net/http`, N7) and ABO's gin engine is mounted permanently (`httpapi.MountLegacy`, cross-cutting resolution 2), so `parse.go` and `respond.go` stay; core re-implements them on net/http. `RespondWithApplyCapExceeded` uses ABO's `internal/applycap` (`respond.go:16,78`) and stays with them (MOD-21). The gin-free envelope types and Range serving move. | in 19, out 2. imports `gin` (third-party), `applycap`, `logging`; `.m4b/.m4a` content-type switch `rangeserve.go:34`; `RespondWithError` envelope (raw/09 §5); 19 importers, 18 in server.<br>**Split:** **mediacore/httpapi (envelope types)** ← `types.go` (106 LOC); **mediacore/httpapi (Range serving; media types come from module registration)** ← `rangeserve.go` (222 LOC); **internal/httputil (ABO, gin helpers)** ← `parse.go`, `respond.go` (276 LOC) |
| <a id="p-internal-identification"></a>`internal/identification` | 3 | 347 | audiobook | 1/1 | internal/identification (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the body of the identification.advance driver (spec 02 section 3.2 "The driver", section 4 PR 14): it". | in 1, out 1; imports: `internal/database`; importers: `internal/server` |
| <a id="p-internal-importer"></a>`internal/importer` | 4 | 789 | audiobook | 3/15 | internal/importer (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 15; imports audiobook pkgs: `internal/authorcredit`, `internal/bookfileaudio`, `internal/dedup`, `internal/filehash` (+4); importers: `internal/server`, `internal/server/handlers`, `internal/server/handlers/mocks` |
| <a id="p-internal-itunes"></a>`internal/itunes` | 3 | 8822 | audiobook | 12/6 | internal/itunes (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides functionality for importing audiobooks from iTunes Library.xml files". | in 12, out 6; imports: `internal/config`, `internal/database`, `internal/logger`, `internal/models` (+2); importers: `cmd/itl-audit-encoding`, `cmd/itl-check`, `cmd/itl-diff` (+9) |
| <a id="p-internal-itunes-service"></a>`internal/itunes/service` | 5 | 5587 | audiobook | 7/21 | internal/itunes/service (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "contains the iTunes integration: import pipeline, playlist import, and ITL download". | in 7, out 21; imports audiobook pkgs: `internal/applygate`, `internal/authorcredit`, `internal/dedup`, `internal/filehash` (+8); importers: `internal/plugins/itunes`, `internal/plugins/maintenance`, `internal/server` (+4) |
| <a id="p-internal-itunesguard"></a>`internal/itunesguard` | 3 | 136 | audiobook | 3/3 | internal/itunesguard (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the one iTunes-ownership predicate for writes to a book's version-group primary flag". | in 3, out 3; imports audiobook pkgs: `internal/repairs`, `internal/versionprimary`; importers: `internal/audiobooks`, `internal/plugins/maintenance`, `internal/server/handlers/dedup` |
| <a id="p-internal-keyedmutex"></a>`internal/keyedmutex` | 0 | 55 | generic | 3/0 | internal/keyedmutex (unchanged) | Per-key mutex; consumers are ABO only (moved into core only in build experiment E2, which is not a placement decision). | in 3, out 0. stdlib; importers `decisions`, `deluge` (`import.go:24,67`), `versions`. |
| <a id="p-internal-linkintegrity"></a>`internal/linkintegrity` | 0 | 489 | audiobook | 1/0 | internal/linkintegrity (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the shared vocabulary for the "First Aid" library validation + repair pass: the shapes a broken". | in 1, out 0; importers: `internal/plugins/maintenance` |
| <a id="p-internal-logger"></a>`internal/logger` | 0 | 442 | mixed | 73/0 | internal/logger (unchanged path; books façade over mediacore/log, kept; deleting it is an optional ratchet, SIMP-3) | The printf `Logger` interface bundles progress, change recording and cancellation (task concerns) and leaks `BookID`; core exposes `log/slog` only, so the interface is not promoted. R5 binds core only (SIMP-3): the façade survives as books code over core handles, and the 342-file migration to slog is an optional ratchet, not an extraction stage. | in 73, out 0. `BookID` field `internal/logger/logger.go:45`; interface `logger.go:54-98`; writes through `slog.Default()` (`standard.go`); 73 importers, 342 files import it (raw/12 §3). **No file leaves** (MOD4-6): unexported `sanitizeLogLine` (`sanitize.go:44`) is called by the staying `logger.go:106` and `standard.go:50`; `attrs.go:29,42` and `retention.go:49` take `Logger`/`LevelLogger`, declared in `logger.go:54,93`; `retention.go` prunes ABO's `operationlog:` and `syslog:` families and is deleted in the F5 flip that starts `logjournal` and the tasks run-log pruning (05-logging §11); `Sanitize`/`SanitizeErr` go with 05-logging stage 2. |
| <a id="p-internal-logger-logtest"></a>`internal/logger/logtest` | 0 | 135 | generic | 11/0 | internal/logger/logtest (unchanged; stays in ABO) | It captures by swapping `slog.Default` (`logtest.go:79-90`, restored in `t.Cleanup`), which is what ABO's logger facade writes to (SIMP-3). R5 forbids `slog.SetDefault` in core, so the helper stays with ABO's tests, and `coretest` captures logs through an injected handler instead (03 R5; MOD3-3). | in 11, out 0. 24 test importer files; no in-module imports. |
| <a id="p-internal-maintenance-jobs"></a>`internal/maintenance/jobs` | 4 | 12366 | audiobook | 3/25 | internal/maintenance/jobs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "registers all built-in maintenance jobs with the maintenance registry". | in 3, out 25; imports audiobook pkgs: `internal/applygate`, `internal/authorcredit`, `internal/authorname`, `internal/bookfileaudio` (+10); importers: `internal/audiobooks`, `internal/server`, `internal/server/handlers/abs` |
| <a id="p-internal-masterlist"></a>`internal/masterlist` | 0 | 1309 | audiobook | 2/0 | internal/masterlist (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the input side of the master-list import: the canonical row an uploaded list is stored as, the validation". | in 2, out 0; importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-matcher"></a>`internal/matcher` | 0 | 545 | audiobook | 4/1 | internal/matcher (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 4, out 1; imports audiobook pkgs: `internal/personname`; importers: `internal/database`, `internal/dedup`, `internal/plugins/maintenance` (+1) |
| <a id="p-internal-mediainfo"></a>`internal/mediainfo` | 3 | 313 | audiobook | 6/1 | internal/mediainfo (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 6, out 1; imports audiobook pkgs: `internal/audioutil`; importers: `cmd`, `internal/audiobooks`, `internal/bookfileaudio` (+3) |
| <a id="p-internal-merge"></a>`internal/merge` | 3 | 13113 | audiobook | 28/10 | internal/merge (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 28, out 10; imports audiobook pkgs: `internal/bookfileaudio`, `internal/metastate`, `internal/personname`, `internal/syncapi/progress` (+1); importers: `internal/audiobooks`, `internal/batch`, `internal/database` (+25) |
| <a id="p-internal-metabatch"></a>`internal/metabatch` | 4 | 3528 | audiobook | 5/13 | internal/metabatch (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 5, out 13; imports audiobook pkgs: `internal/applygate`, `internal/authority`, `internal/authorjunk`, `internal/authorname` (+2); importers: `internal/plugins/maintenance`, `internal/scheduler`, `internal/server` (+2) |
| <a id="p-internal-metadata"></a>`internal/metadata` | 3 | 12071 | audiobook | 24/18 | internal/metadata (unchanged path; book metadata sources and parsing) | Six audiobook providers and the `BookMetadata` result type stay. The throttle registry and circuit breaker are independent of `BookMetadata` (raw/11 §3) and move to core; core `integrations/metadata` defines a generic `Provider[Q, R]` that ABO's `MetadataSource` implements. | in 24, out 18. `MetadataSource` `source.go:52-56`; `defaultThrottles` global `throttle_registry.go:69`; `CircuitBreaker` `circuitbreaker.go:32,138`; imports `operations/registry` (allowed wrong-way edge 7, `layering_test.go:297`); 24 importers.<br>**Split:** **mediacore/integrations/providerhttp (throttle registry, circuit breaker)** ← `throttle.go`, `throttle_registry.go`, `circuitbreaker.go` (997 LOC) |
| <a id="p-internal-metadata-dailyquota"></a>`internal/metadata/dailyquota` | 0 | 348 | generic | 4/0 | internal/metadata/dailyquota (unchanged path; books code over `provider_daily_budget:`, in use until F7) | Per-provider daily budget. Instance-scoped quota accounting is re-implemented in core `providerhttp` over its native store (05-metadata M2) at F7; this package hard-codes ABO's family `provider_daily_budget:` (`dailyquota.go:85`), so it stays in ABO, and step M6 deletes HEAD's budget rows, because the holds are reset (D12 (b)). Its `For(provider)` global (`dailyquota.go:333`) is ABO code (R5 binds core only). | in 4, out 0. no in-module imports; `For(provider)` global `dailyquota.go:333`; `Install` from `init()` in `internal/metadata/google_quota.go:61`. |
| <a id="p-internal-metadata-mocks"></a>`internal/metadata/mocks` | 3 | 456 | audiobook | 1/1 | internal/metadata/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 1; imports audiobook pkgs: `internal/metadata`; importers: `internal/server/handlers/duplicates` |
| <a id="p-internal-metafetch"></a>`internal/metafetch` | 3 | 21306 | audiobook | 17/31 | internal/metafetch (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 17, out 31; imports audiobook pkgs: `internal/authorcredit`, `internal/authority`, `internal/authorjunk`, `internal/authorname` (+12); importers: `internal/applygate`, `internal/audiobooks`, `internal/catalog` (+14) |
| <a id="p-internal-metastate"></a>`internal/metastate` | 0 | 73 | audiobook | 8/0 | internal/metastate (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "owns the on-disk representation of per-book metadata state: the user-preference KEY a book's state is stored". | in 8, out 0; importers: `internal/audiobooks`, `internal/database`, `internal/merge` (+5) |
| <a id="p-internal-metrics"></a>`internal/metrics` | 0 | 840 | mixed | 14/1 | internal/metrics (unchanged path; books metrics on the App registry; names unchanged, `audiobook_organizer_*`, SIMP-13) | The Pebble collector moves to core `storage` in release R1 with HEAD's family names (05-metrics §13 table). The five `operations_*` families are deleted with HEAD's engine in the tasks flip F5, where the executor's `ops_*` families carry their information, with the dashboards and rules in the same PR (05-metrics §13; R14). Book, search, catalog and ABS series stay books' and register through `Provider.Registerer()` from the release R1 PR that makes the App's registry the served one, so no package-level collector on the default registry remains (R5; 05-metrics §13 item 2). | in 14, out 1. namespace `audiobook_organizer` `metrics.go:19`; `books_total` `metrics.go:51-53`; `prometheus.MustRegister` default registry `metrics.go:300` (raw/12 §4); 14 importers.<br>**Split:** **mediacore/internal/pebble (Pebble collector; ABO's Open Library store reaches it through `storage.OpenAuxiliary`)** ← `pebble_collector.go` (251 LOC); **internal/metrics (ABO; `metrics.go`'s `operations_*` families deleted at F5 with HEAD's engine)** ← `metrics.go`, `pipeline_metrics.go` (589 LOC) |
| <a id="p-internal-mtls"></a>`internal/mtls` | 0 | 693 | generic | 1/0 | internal/mtls (unchanged) | CA/PSK + client-cert TLS for the MCP bridge tool; not part of HTTP auth. | in 1, out 0. only importer `cmd/mtls-bridge` (`internal/mtls/transport.go:14`, raw/10 §2.1). |
| <a id="p-internal-openlibrary"></a>`internal/openlibrary` | 2 | 843 | audiobook | 4/2 | internal/openlibrary (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 4, out 2; imports: `internal/database`, `internal/metrics`; importers: `internal/metadata`, `internal/metafetch`, `internal/server` (+1) |
| <a id="p-internal-operations-opswriter"></a>`internal/operations/opswriter` | 3 | 1930 | mixed | 6/3 | internal/books/writer (renamed; implements the books typed Writer) | Fencing is an executor mechanism and goes to core as public `tasks` fencing (`ErrFenced`, `WithFence`, `FenceFromContext`; `fence.go` is self-contained). Write-ahead intents and the evidence journal stay with the Writer: `writer.go` uses their unexported `intents` (`writer.go:123,149`) and `journalMemo` (`:133`) and `intent.go:15`/`evidence_journal.go:14` are typed through `database` (MOD2-3), so they are books code. | in 6, out 3. uses `database.MetadataChangeRecord/BookAuthor/BookFile/Book` (36 lines, `evidence.go:58,337,356`, raw/02 a1); imports `pkg/ops`; 6 importers (`registry`, `plugins/dedup`, `repairs`, `opstest`).<br>**Split:** **mediacore/tasks (run fencing: `ErrFenced`, `WithFence`, `FenceFromContext`)** ← `fence.go` (120 LOC); **internal/books/writer** ← `writer.go`, `evidence.go`, `history.go`, `intent.go`, `evidence_journal.go` (1810 LOC) |
| <a id="p-internal-operations-state"></a>`internal/operations/state` | 0 | 578 | generic | 11/0 | internal/operations/state (unchanged path until R3, which deletes it together with `tools/cmd/opsgen` and `web/src/generated/ops.ts`, R15; HEAD's run-state package: the v2 table, V3 constants, transition machine, `Classify*`, `FromV2`/`ToV2`; R6-2) | HEAD's run-status vocabulary and transition machine are the **row format of the ops families**, so they stay in ABO with the code that reads those rows (R6-2, MOD6-1; 03 §3.5.1): until F5 HEAD's engine and run-row store use it, and from F5 the run-row files in `internal/books/cutover` import it from six files (`iface_ops_v3.go`, `pebble_store_ops_v2.go`, `pebble_store_ops_v2_wait.go`, `pebble_store_ops_v3.go`, `pebble_store_ops_v3_lastrun.go`, `pebble_store_ops_v3_migrate.go`) so that step M4 decodes HEAD's stored states (the V3 strings, `V3Dropped` is `"dropped"`, and the v2 status table with `FromV2`). Public `tasks.RunState` has twelve values that are not HEAD's ten (`interrupted_dropped` for `dropped`; `waiting_deps` and `interrupted_quiesced` as states), and core's machine in `mediacore/internal/executor` is not importable from ABO (03 R2), so core's executor state machine is a **re-implementation** over `tasks.RunState` (§8.6), and the `state.V3` → `RunState` mapping is step M4's (05-tasks 5T.8.3). `tools/cmd/opsgen` keeps reading this package until R3. The package may be renamed under `internal/books/` (it is then a rename, which keeps the count) but is never moved to core. R3 deletes it, after `internal/books/cutover` has gone with the HEAD-format readers, together with `tools/cmd/opsgen` and `web/src/generated/ops.ts` in one PR (03 §3.8 R3; R15, XFILE-2). | in 11, out 0. no in-module imports; `state.go:29,150-183`, `v3.go:20-31,187,289,344`; 11 importers incl. `tools/cmd/opsgen` (`main.go:6,28`) and the six run-row source files; `state.V3` used 18 times there, `ClassifyV3`, `Next` ×3, `FromV2`, `ToV2`. |
| <a id="p-internal-organizer"></a>`internal/organizer` | 3 | 10070 | audiobook | 10/22 | internal/organizer (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 10, out 22; imports audiobook pkgs: `internal/authorname`, `internal/bookfileaudio`, `internal/chaptershape`, `internal/filehash` (+7); importers: `internal/audiobooks`, `internal/itunes/service`, `internal/metafetch` (+7) |
| <a id="p-internal-pathutil"></a>`internal/pathutil` | 0 | 1040 | mixed | 25/0 | internal/pathutil (unchanged path; iTunes/versions-dir/protected-folder rules only) | Named roots, root splitting (remote-worker wire format), separator handling and hidden-dir sweep rules are generic path mapping; iTunes and book-versions conventions are ABO. | in 25, out 0. `OpenLibraryDumpDir` `hidden.go:80`; iTunes special case `protected.go:403`; 25 importers.<br>**Split:** **mediacore/library (path mapping)** ← `pathvars.go`, `root.go`, `prefix.go`, `rel.go`, `hidden.go` (514 LOC); **internal/pathutil (ABO)** ← `itunes.go`, `protected.go`, `versions_dir.go` (526 LOC) |
| <a id="p-internal-personname"></a>`internal/personname` | 0 | 1844 | audiobook | 17/0 | internal/personname (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "answers "does this string look like a human author's name, rather than a title or a structural marker?" --". | in 17, out 0; importers: `internal/authorcredit`, `internal/authority`, `internal/authorjunk` (+14) |
| <a id="p-internal-playlist"></a>`internal/playlist` | 3 | 791 | audiobook | 2/2 | internal/playlist (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 2, out 2; imports: `internal/database`, `internal/search`; importers: `cmd`, `internal/server/handlers` |
| <a id="p-internal-plugins-acoustid"></a>`internal/plugins/acoustid` | 4 | 5718 | audiobook | 2/13 | internal/plugins/acoustid (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the UOS plugin for AcoustID fingerprinting operations". | in 2, out 13; imports audiobook pkgs: `internal/acoustid`, `internal/audioutil`, `internal/dedup`, `internal/fingerprint` (+1); importers: `internal/opscatalog`, `internal/server` |
| <a id="p-internal-plugins-dedup"></a>`internal/plugins/dedup` | 4 | 9279 | audiobook | 2/21 | internal/plugins/dedup (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "op dedup.breakdown-backfill". | in 2, out 21; imports audiobook pkgs: `internal/dedup`, `internal/dedup/dataset`, `internal/dedup/unified`, `internal/fingerprint` (+2); importers: `internal/opscatalog`, `internal/server` |
| <a id="p-internal-plugins-deluge"></a>`internal/plugins/deluge` | 4 | 2433 | mixed | 2/11 | internal/plugins/deluge (unchanged) | Audiobook import centralization over Deluge; becomes books tasks on the core download contract. | in 2, out 11; imports audiobook pkgs: `internal/filehash`, `internal/tagger`; importers: `internal/opscatalog`, `internal/server` |
| <a id="p-internal-plugins-itunes"></a>`internal/plugins/itunes` | 4 | 355 | audiobook | 1/6 | internal/plugins/itunes (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the UOS plugin for iTunes/Music library operations". | in 1, out 6; imports audiobook pkgs: `internal/itunes/service`; importers: `internal/opscatalog` |
| <a id="p-internal-plugins-maintenance"></a>`internal/plugins/maintenance` | 4 | 101319 | audiobook | 2/71 | internal/plugins/maintenance (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "op maintenance.build-folder-book-files". | in 2, out 71; imports audiobook pkgs: `internal/applygate`, `internal/audiobooks`, `internal/audioutil`, `internal/authorcredit` (+41); importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-plugins-metafetch"></a>`internal/plugins/metafetch` | 4 | 3942 | audiobook | 1/11 | internal/plugins/metafetch (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "op metafetch.calibrate-scoring (INIT-3-T1)". | in 1, out 11; imports audiobook pkgs: `internal/applygate`, `internal/metadata`, `internal/metafetch`, `internal/repairs`; importers: `internal/opscatalog` |
| <a id="p-internal-policy"></a>`internal/policy` | 0 | 80 | audiobook | 4/0 | internal/policy (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "evaluates per-book processing policy tags". | in 4, out 0; importers: `internal/metafetch`, `internal/organizer`, `internal/server` (+1) |
| <a id="p-internal-quarantine"></a>`internal/quarantine` | 3 | 1003 | audiobook | 2/7 | internal/quarantine (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 2, out 7; imports audiobook pkgs: `internal/authorname`, `internal/filehash`; importers: `internal/scanner`, `internal/server` |
| <a id="p-internal-querygrammar"></a>`internal/querygrammar` | 0 | 896 | mixed | 5/0 | internal/querygrammar (unchanged; `shared/` candidate) | The Library search-bar value syntax; one app consumes it. Promote to `mediacore/shared/querygrammar` when a second app adopts the same search bar (03 §3.4). | in 5, out 0. no in-module imports; `BookID`/`Title` in the budget error `budget.go:70`; importers `audiobooks`, `search`, `server`, `handlers`, `handlers/audiobooks`. |
| <a id="p-internal-readstatus"></a>`internal/readstatus` | 3 | 322 | audiobook | 3/1 | internal/readstatus (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 1; imports: `internal/database`; importers: `internal/merge`, `internal/server/handlers`, `internal/server/handlers/abs` |
| <a id="p-internal-reconcile"></a>`internal/reconcile` | 3 | 3893 | audiobook | 2/17 | internal/reconcile (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 2, out 17; imports audiobook pkgs: `internal/acoustid`, `internal/fingerprint`, `internal/merge`, `internal/scanner` (+2); importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-remux"></a>`internal/remux` | 3 | 671 | audiobook | 3/8 | internal/remux (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 8; imports audiobook pkgs: `internal/diagnosis`; importers: `cmd/pebble-inject-skip`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-repairs"></a>`internal/repairs` | 3 | 5148 | audiobook | 13/14 | internal/repairs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the shared framework behind the Repairs lane of /review: one contract every library fixer implements, and". | in 13, out 14; imports audiobook pkgs: `internal/applygate`, `internal/franchise`, `internal/merge`, `internal/undo`; importers: `internal/audiobooks`, `internal/itunesguard`, `internal/plugins/maintenance` (+10) |
| <a id="p-internal-scanlock"></a>`internal/scanlock` | 0 | 359 | audiobook | 5/0 | internal/scanlock (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the per-BOOK lock that lets a library scan and a metadata apply run at the same time without either one". | in 5, out 0; importers: `internal/organizer`, `internal/scanner`, `internal/server` (+2) |
| <a id="p-internal-scanner"></a>`internal/scanner` | 3 | 14775 | audiobook | 11/27 | internal/scanner (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides file scanning and processing utilities for the audiobook organizer". | in 11, out 27; imports audiobook pkgs: `internal/audioutil`, `internal/authorcredit`, `internal/authorname`, `internal/bookfileaudio` (+12); importers: `cmd`, `internal/audiobooks`, `internal/importer` (+8) |
| <a id="p-internal-search"></a>`internal/search` | 2 | 4425 | mixed | 6/6 | internal/search (unchanged path; books index documents, field routing, per-user terms) | `storage/index` is a platform contract (03 §3.3), so the bleve engine lifecycle (open, mapping-version rebuild, markers, degraded open) and a query AST + translator go to core; the book document mapping, field routing and per-user terms stay. | in 6, out 6. uses `database.UserBookState/Author/Series/Book` (`index_builder.go:28-29,84`, 29 lines); `bleve_index.go` has 92 book/author/series mentions; degraded open `register.go:46-60`; mapping rebuild `bleve_index.go:186-215`; `database.SetCollectionPerViewerClassifier` init hook `field_routing.go:159`.<br>**Split:** **mediacore/internal/bleveindex (engine lifecycle cut out of bleve_index.go)**; **mediacore/storage/index (query AST, regex terms)** ← `query_ast.go`, `regex_terms.go` (316 LOC); **delete (serviceregistry)** ← `register.go` (115 LOC); **internal/search (ABO; `bleve_translator.go` translates the books grammar and declares `PerUserFilter`/`TranslateAt`, used by `internal/audiobooks` and `internal/playlist`, so it stays)** ← `bleve_translator.go`, `bleve_index.go`, `document.go`, `field_routing.go`, `field_term.go`, `index_builder.go`, `per_user_match.go`, `per_user_mixed.go`, `per_user_shape.go`, `per_user_values.go`, `query_parser.go`, `relative_date.go`, `tag_terms.go` (3994 LOC) |
| <a id="p-internal-searchcache"></a>`internal/searchcache` | 3 | 1157 | audiobook | 4/2 | internal/searchcache (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "holds the shared search result cache: the full ranked list of matching book IDs per (query, filter set)". | in 4, out 2; imports: `internal/logger`, `internal/metrics`; importers: `internal/audiobooks`, `internal/server`, `internal/server/handlers/abs` (+1) |
| <a id="p-internal-seqnum"></a>`internal/seqnum` | 0 | 270 | audiobook | 3/0 | internal/seqnum (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "extracts a volume / sequence number ("Big Cats 3", "Vol". | in 3, out 0; importers: `internal/applygate`, `internal/catalog`, `internal/metafetch` |
| <a id="p-internal-server"></a>`internal/server` | 5 | 46789 | mixed | 2/122 | internal/server (unchanged path; books HTTP adapter, mounted permanently through `httpapi.MountLegacy`, N7) | The gin composition layer of ABO, mounted permanently behind core's perimeter (cross-cutting resolution 2). The core concerns inside it are re-implemented in core on net/http and leave in the PR that makes them native: perimeter in release R1, auth at F4, task admin at F5, SPA serving at F9; books route groups stay unless rewritten for their own reasons. `csp.go` stays in ABO (FE3-10): `contentSecurityPolicy()` hashes ABO's embedded `index.html` and re-reads its Access team host (`csp.go:34-46,95-106,129-138`), so it is the legacy mount's `DefaultCSP` until F9 and then shrinks to ABO's `ConnectSrcFrom` in `app.Options.UIOptions` (FE3-4); the shell's own policy builder is written in `ui` at F9. The build-tagged `static_embed.go` (`embed_frontend`, in the tag union; it calls `spaWillNotServe`, `spa_fallback.go:119`) is named in the F9 `ui` part with its twin `static_nonembed.go` (MOD8-3): `ui.Handler` replaces both and they leave together. Two files stay because they touch ABO families (R4-1): `apikey_expiry_stamp.go` (HEAD's one-shot done flag `system:migration:apikey_expiry_stamp_v1_done`, `:23`) runs over HEAD's keys until F4, when step M3 imports the keys with their expiry (DATA4-12), and `provider_throttle_wire.go` wires HEAD's throttle until F7, when core's native `providerhttp.GateStore` takes over. | in 2, out 122. out 122, imports 56 audiobook packages (raw/02 d); 10 `serviceregistry.Register` from `init()` `registry_wire.go:97-343`; ~48 `addOpRegistrar` inits; `NewServer` ~612 lines `server.go:653`; plugin mount `plugins_init.go:42`.<br>**Split:** **mediacore/auth (bootstrap, temp login, invite accept, reset, login/invite/bootstrap rate limits, credential/admin route helpers, API-key expiry)** ← `bootstrap.go`, `bootstrap_rate_limit.go`, `auth_temp_login.go`, `auth_accept_invite.go`, `auth_reset_password.go`, `login_ceiling.go`, `invite_rate_limit.go`, `credential_routes.go`, `wire_admin_role.go`, `toplevel_auth_chain.go`, `apikey_expiry_sweep.go`, `wire_auth_routes.go`, `wire_oauth.go` (3184 LOC); **mediacore/httpapi (perimeter: peer allow-list, security headers/CORS)** ← `peer_allowlist.go`, `server_middleware.go` (535 LOC); **delete at F9 (gin `/api/v1/csp-report`; the core route `/ui/csp-report` is re-implemented in mediacore/internal/uihttp at F4, 07 §7.6.2; FE3-10)** ← `csp_report.go` (369 LOC); **mediacore/ui (SPA fallback, static serving with its `embed_frontend` twin `static_embed.go`; at F9, FE-1, MOD8-3)** ← `spa_fallback.go`, `static_nonembed.go`, `static_embed.go` (397 LOC); **mediacore/health (flight recorder wiring)** ← `flight_recorder.go` (55 LOC); **mediacore/updater (HTTP)** ← `update_handlers.go` (64 LOC); **mediacore/storage/index (dirty-set reconciler, change observer)** ← `search_reconciler.go`, `search_observer.go`, `index_queue.go` (997 LOC); **delete (init registrars)** ← `op_registrars.go` (30 LOC); **delete at F7 with `internal/plugin` (plugin mount)** ← `plugins_init.go` (47 LOC); **internal/server (ABO; gin 410 stubs of the permanent legacy mount, core `httpapi.Gone` is a port)** ← `gone.go` (36 LOC) |
| <a id="p-internal-server-absauth"></a>`internal/server/absauth` | 5 | 1536 | audiobook | 4/1 | internal/server/absauth (unchanged path; ABS JWT/refresh only) | Not purely ABS: web login uses its argon2id hashing, failed-login throttle and saturation limiter, and its audit event type. Those move to core; the ABS token surface stays. | in 4, out 1. `absauth.HashPassword` used by `internal/server/handlers/user.go`, `auth.go`, `internal/server/bootstrap.go`, `auth_accept_invite.go`, `login_ceiling.go` (grep at 748fd787a); `absauth.Audit` 46 uses; argon2id `password.go:131-190` (raw/10 §7).<br>**Split:** **mediacore/auth (password hashing, login throttle, saturation, LRU)** ← `password.go`, `throttle.go`, `saturation.go`, `lru.go` (853 LOC); **mediacore/audit (auth audit events)** ← `audit.go` (137 LOC); **internal/server/absauth (ABO)** ← `config.go`, `token.go`, `refresh.go` (546 LOC) |
| <a id="p-internal-server-handlers"></a>`internal/server/handlers` | 5 | 20579 | mixed | 4/47 | internal/server/handlers (unchanged path; books handlers) | Books HTTP handlers; the auth/user/API-key/invite handlers become core `auth` routes at F4 and scheduler admin a core `tasks` route at F5 (their only ABO caller, `internal/server`'s wiring, is deleted in the same PR; MOD-2 gate); the activity routes serve the books feed and stay; plugin handlers die with `internal/plugin`. | in 4, out 47. uses `database.Book/Collection/BookFile` (`ai.go:44`; 249 lines, raw/02 a1); imports `plugins/maintenance` (`activity_compact.go:22`), `server/absauth`; 47 module imports.<br>**Split:** **mediacore/internal/authhttp** ← `auth.go`, `apikeys.go`, `oauth_login.go`, `user.go`, `user_credentials.go`, `web_session_revoke.go`, `invite_delivery.go` (2535 LOC); **mediacore/internal/taskhttp (scheduler admin)** ← `scheduler_admin.go` (543 LOC); **mediacore/health (trace download)** ← `diagnostics_traces.go` (89 LOC); **delete (legacy plugin handlers)** ← `plugins.go` (188 LOC) |
| <a id="p-internal-server-handlers-abs"></a>`internal/server/handlers/abs` | 5 | 16070 | audiobook | 2/15 | internal/server/handlers/abs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "implements the Audiobookshelf-compatible auth surface (design spec Phase 1): GET /ping, GET /status, POST". | in 2, out 15; imports audiobook pkgs: `internal/audioutil`, `internal/covers`, `internal/metadata`, `internal/searchcache` (+2); importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-server-handlers-admindebug"></a>`internal/server/handlers/admindebug` | 5 | 1030 | mixed | 1/5 | internal/server/handlers/admindebug (unchanged) | Debug-edit of Book/BookFile rows with undo images (`admin_debug_edit:`). | in 1, out 5; imports audiobook pkgs: `internal/merge`; importers: `internal/server` |
| <a id="p-internal-server-handlers-aibackends"></a>`internal/server/handlers/aibackends` | 5 | 420 | generic | 1/4 | internal/server/handlers/aibackends (unchanged) | Toggles ABO's local-Ollama vs remote backend mode (managed tool lifecycle); one consumer. | in 1, out 4. imports `aidispatch`, `config`, `httputil`, `tools`; `aibackends.go:420`. |
| <a id="p-internal-server-handlers-audiobooks"></a>`internal/server/handlers/audiobooks` | 5 | 4056 | audiobook | 1/27 | internal/server/handlers/audiobooks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 27; imports audiobook pkgs: `internal/audiobooks`, `internal/batch`, `internal/merge`, `internal/metadata` (+6); importers: `internal/server` |
| <a id="p-internal-server-handlers-audiobooks-mocks"></a>`internal/server/handlers/audiobooks/mocks` | 5 | 3473 | audiobook | 1/5 | internal/server/handlers/audiobooks/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 5; imports audiobook pkgs: `internal/audiobooks`, `internal/batch`, `internal/metafetch`; importers: `internal/server/handlers/audiobooks` |
| <a id="p-internal-server-handlers-dedup"></a>`internal/server/handlers/dedup` | 5 | 4407 | audiobook | 1/13 | internal/server/handlers/dedup (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 13; imports audiobook pkgs: `internal/dedup`, `internal/dedup/dataset`, `internal/itunesguard`, `internal/merge` (+1); importers: `internal/server` |
| <a id="p-internal-server-handlers-dedup-mocks"></a>`internal/server/handlers/dedup/mocks` | 5 | 1600 | audiobook | 1/4 | internal/server/handlers/dedup/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 4; imports audiobook pkgs: `internal/dedup`, `internal/merge`; importers: `internal/server/handlers/dedup` |
| <a id="p-internal-server-handlers-duplicates"></a>`internal/server/handlers/duplicates` | 5 | 771 | audiobook | 1/9 | internal/server/handlers/duplicates (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 9; imports audiobook pkgs: `internal/dedup`, `internal/merge`, `internal/metadata`, `internal/undo`; importers: `internal/server` |
| <a id="p-internal-server-handlers-duplicates-mocks"></a>`internal/server/handlers/duplicates/mocks` | 5 | 681 | audiobook | 1/4 | internal/server/handlers/duplicates/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 4; imports audiobook pkgs: `internal/merge`, `internal/metadata`; importers: `internal/server/handlers/duplicates` |
| <a id="p-internal-server-handlers-entities"></a>`internal/server/handlers/entities` | 5 | 2187 | audiobook | 1/9 | internal/server/handlers/entities (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 9; imports audiobook pkgs: `internal/audiobooks`, `internal/dedup`, `internal/undo`, `internal/work`; importers: `internal/server` |
| <a id="p-internal-server-handlers-entities-mocks"></a>`internal/server/handlers/entities/mocks` | 5 | 2602 | audiobook | 1/4 | internal/server/handlers/entities/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 4; imports audiobook pkgs: `internal/audiobooks`, `internal/work`; importers: `internal/server/handlers/entities` |
| <a id="p-internal-server-handlers-fpworker"></a>`internal/server/handlers/fpworker` | 5 | 228 | audiobook | 2/2 | internal/server/handlers/fpworker (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "serves the remote fingerprint worker API (/api/v1/fingerprint/worker/*): fp-worker processes on other hosts". | in 2, out 2; imports audiobook pkgs: `internal/fingerprint/workerapi`; importers: `internal/fingerprint/workerclient`, `internal/server` |
| <a id="p-internal-server-handlers-metadata"></a>`internal/server/handlers/metadata` | 5 | 2466 | audiobook | 1/17 | internal/server/handlers/metadata (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 17; imports audiobook pkgs: `internal/applygate`, `internal/catalog`, `internal/merge`, `internal/metabatch` (+4); importers: `internal/server` |
| <a id="p-internal-server-handlers-metadata-mocks"></a>`internal/server/handlers/metadata/mocks` | 5 | 2901 | audiobook | 1/3 | internal/server/handlers/metadata/mocks (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 1, out 3; imports audiobook pkgs: `internal/metafetch`; importers: `internal/server/handlers/metadata` |
| <a id="p-internal-server-handlers-mocks"></a>`internal/server/handlers/mocks` | 5 | 9993 | mixed | 2/12 | internal/server/handlers/mocks (unchanged) | Mocks of books handler dependencies (importer, iTunes, AI pipeline). | in 2, out 12; imports audiobook pkgs: `internal/dedup`, `internal/importer`, `internal/itunes/service`, `internal/merge` (+2); importers: `internal/server`, `internal/server/handlers` |
| <a id="p-internal-server-handlers-opaccess"></a>`internal/server/handlers/opaccess` | 5 | 885 | mixed | 7/10 | internal/server/handlers/opaccess (unchanged) | Access rules for repair plans and per-user result views; tied to repairs, undo, merge, usermerge. | in 7, out 10. imports `repairs` (`repairs.go:24`), `undo` (`:26`), `merge`, `usermerge`, `diagnostics`; 7 importers. |
| <a id="p-internal-server-handlers-operations"></a>`internal/server/handlers/operations` | 5 | 1052 | mixed | 1/14 | internal/server/handlers/operations (unchanged path; books' domain routes under `/api/v1/operations`) | HEAD's task routes in this package — the run views with the conflict set `GET /api/v1/operations/{id}/{activity,changes,result,results}` and `GET /api/v1/operations/v2/{id}`, and the pause routes — stay books' gin routes until F5, the tasks flip, which deletes them together with `/api/v3/ops/*`, so a conflict set never spans two owners (03 R6; 05-http-api §4.8), and moves the Activity page and the first-party scripts onto core's task routes (`/api/core/v1/tasks/...`, 05-tasks 5T.3.10; 03 §3.8 F5). Books' domain routes under `/api/v1/operations` stay books' gin routes and call core's tasks client (`Deps.Tasks`) from F5 (05-tasks 5T.3.10); `/maintenance-window/*` also stays books' (FIN12-4). | in 1, out 14. imports `audiobooks` (`handler.go:44`), `undo` (`:53`); uses `database.BookAuthor/BookNarrator/Author` (`handler.go:276-313`); `pause.go`.<br>**Split:** **mediacore/internal/taskhttp (pause/resume)** ← `pause.go` (170 LOC) |
| <a id="p-internal-server-handlers-operations-mocks"></a>`internal/server/handlers/operations/mocks` | 5 | 2022 | mixed | 1/3 | internal/server/handlers/operations/mocks (unchanged) | Mocks for the books operations handler (Author/Narrator/Book types). | in 1, out 3; imports: `internal/database`, `internal/operations/registry`, `internal/scheduler`; importers: `internal/server/handlers/operations` |
| <a id="p-internal-server-handlers-repairs"></a>`internal/server/handlers/repairs` | 5 | 710 | audiobook | 1/9 | internal/server/handlers/repairs (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "serves the Repairs lane of /review (/api/v1/repairs/*): list the fixers, start a plan, page a stored plan's". | in 1, out 9; imports audiobook pkgs: `internal/repairs`; importers: `internal/server` |
| <a id="p-internal-server-handlers-review"></a>`internal/server/handlers/review` | 5 | 1390 | audiobook | 2/6 | internal/server/handlers/review (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "hosts the universal review-queue HTTP handlers (PR-A1)". | in 2, out 6; imports audiobook pkgs: `internal/itunes/service`, `internal/merge`; importers: `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-server-handlers-system"></a>`internal/server/handlers/system` | 5 | 1402 | mixed | 1/14 | internal/server/handlers/system (unchanged path) | System handler mixes backup/restore (core `backup` routes), system status/logs (core `health`/`log`) and ABO dedup/metafetch/organizer actions. | in 1, out 14. imports `dedup` (`handler.go:44`), `metafetch` (`:47`), `backup`; uses `database.BookCore/Author` (`interfaces.go:39`); backup routes `handler.go:769-776,862`. |
| <a id="p-internal-server-handlers-system-mocks"></a>`internal/server/handlers/system/mocks` | 5 | 1866 | mixed | 1/3 | internal/server/handlers/system/mocks (unchanged) | Mocks for the system handler's books store (BookCore/Author). | in 1, out 3; imports: `internal/config`, `internal/database`, `internal/sysinfo`; importers: `internal/server/handlers/system` |
| <a id="p-internal-server-handlers-tools"></a>`internal/server/handlers/tools` | 5 | 92 | generic | 1/1 | internal/server/handlers/tools (unchanged) | HTTP for ABO-managed external tools (Ollama, fpcalc). | in 1, out 1. imports `internal/tools` only. |
| <a id="p-internal-serverdecode"></a>`internal/serverdecode` | 0 | 45 | generic | 2/0 | internal/serverdecode (unchanged) | Switch for in-process audio decoding; audio-specific despite the generic class in raw/01. | in 2, out 0. stdlib; `internal/serverdecode` 45 LOC; importers ABO. |
| <a id="p-internal-sweep"></a>`internal/sweep` | 3 | 373 | mixed | 4/4 | internal/sweep (unchanged) | Tombstone/temp sweeps over `Book`/`BookCore` rows. | in 4, out 4; imports: `internal/activity`, `internal/database`, `internal/logger`, `internal/pathutil`; importers: `internal/audiobooks`, `internal/plugins/maintenance`, `internal/server` (+1) |
| <a id="p-internal-syncapi-conformance"></a>`internal/syncapi/conformance` | 0 | 333 | audiobook | 1/0 | internal/syncapi/conformance (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "diffs this server's Audiobookshelf-compatible API responses against golden fixtures captured from a real". | in 1, out 0; importers: `internal/server/handlers/abs` |
| <a id="p-internal-syncapi-progress"></a>`internal/syncapi/progress` | 0 | 405 | audiobook | 5/0 | internal/syncapi/progress (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "implements the ABS-sync progress conflict-resolution policy from docs/specs/2026-07-29-abs-sync-api-design.md". | in 5, out 0; importers: `internal/audiobooks`, `internal/database`, `internal/maintenance/jobs` (+2) |
| <a id="p-internal-sysinfo"></a>`internal/sysinfo` | 3 | 694 | mixed | 3/3 | internal/sysinfo (unchanged path; library dashboard) | Host memory/uptime and service status are health diagnostics; dashboard counts (books, playlists) are books. | in 3, out 3. uses `database.Playlist` (`dashboard_service.go:23`), `Books` count `:38,68`; imports `config`, `database`, `serviceregistry`.<br>**Split:** **mediacore/health (host stats and service status, re-implemented; the darwin and windows twins travel with `memory.go`/`uptime.go`, MOD8-3; **file divided** (MOD8-5): `service.go`'s host and runtime status goes to `health`, while `RecentOperations` (`service.go:77-99`, v2 rows -> v1 `database.Operation`), `CollectSystemStatus`' library and import fields (`config.AppConfig.RootDir` `:207`, `database.ImportPath`, `DashboardStats`) and the v1 `OperationLog` helpers (`:287-340`) stay in `internal/sysinfo` as books' dashboard code, re-pointed at `tasks` by its conversion PR (05-tasks T2–T7))** ← `memory.go`, `memory_linux.go`, `memory_darwin.go`, `memory_windows.go`, `uptime.go`, `uptime_linux.go`, `uptime_darwin.go`, `uptime_windows.go`, `service.go` (665 LOC); **delete (serviceregistry)** ← `register.go` (21 LOC); **internal/sysinfo (ABO)** ← `dashboard_service.go` (133 LOC) |
| <a id="p-internal-tagger"></a>`internal/tagger` | 3 | 744 | audiobook | 12/4 | internal/tagger (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 12, out 4; imports: `internal/config`, `internal/database`, `internal/fileops`, `internal/logger`; importers: `cmd`, `internal/audiobooks`, `internal/deluge` (+9) |
| <a id="p-internal-telemetry"></a>`internal/telemetry` | 0 | 1363 | generic | 10/0 | internal/telemetry (unchanged path; books telemetry: ABO's attribute vocabulary, view table, scope helper and version, permanently; provider construction -> mediacore/metrics in release R1) | Provider construction, the endpoint validator and the resource are platform metrics (03 §3.3) and are **re-implemented** in core `metrics` in release R1 as per-`App` values (R5; §8.6, MOD3-3, 05-metrics §13 item 1). The rest is ABO vocabulary and stays here permanently as books telemetry (MOD4-4): `attr.go` (about 30 attribute keys such as `def_id`, `plugin`, `fixer`, `op_id`, `attr.go:31-89`), `views.go` (the ABO histogram bucket table), `meter.go` (`Meter(name)` with `ScopePrefix` `meter.go:17`, `MeterFrom`, the `SetVersion`/`Version` global `meter.go:58-72`) and `config.go` (`LoadConfig`, `WithMetricsOTLP`, `WithEnvironment`, `ParseMetricsInterval`); they feed `Scope.View`/`Allowed` and `Options.ServiceVersion` at registration, so core holds no ABO name. The process-global installs (`otel.SetTracerProvider` `telemetry.go:287`, `otel.SetMeterProvider` `:461`, `otel.SetErrorHandler` `errorhandler.go:121`, `otel.SetLogger` via `sdklog.go:39`, the default-registry Prometheus exporter) stay in this façade until the composition-root PR of release R1, where `app.InstallProcessDefaults` takes over the meter, tracer and propagator installs as well as the logger and error handler, and the same PR makes the App's private registry the served one: the client_golang families register through `Provider.Registerer()` and ABO's legacy gin `/metrics` route serves `Provider.Handler()` until F8, when the kernel mounts `/metrics` and the gin route is removed, with no metric rename (05-metrics §6, §13; MOD4-3, R14). Its repo-walking test `ratchet_test.go` (`TestRatchet_ScansRealTree`, `ratchet_test.go:310`) stays in ABO, in `internal/telemetrypolicy` (MOD-6). | in 10, out 0. no in-module imports; `otel.SetTracerProvider`/`SetMeterProvider` `telemetry.go:287,461` and `instanceID` `resource.go:31` are process globals (raw/12 §9); scope prefix brand `meter.go:17`; ABO packages that create instruments through the globals: `internal/ai/telemetry.go`, `internal/aidispatch/metrics.go`, `internal/database/memdb_metrics.go`, `internal/dedup/engine.go`, `internal/operations/registry/worker.go`, `internal/opsmetrics/opsmetrics.go` (round-3 X22); identifiers each importer uses: `Meter`, `MeterFrom`, `Views`, `HistogramBuckets`, the attribute keys, `SetVersion`/`Version`, `LoadConfig`/`WithMetricsOTLP`/`WithEnvironment`/`ParseMetricsInterval`/`InitOTEL` (round-4 Y3).<br>**Split:** **mediacore/metrics (provider construction, endpoint validation, resource; re-implemented in release R1)** ← `telemetry.go`, `endpoint.go`, `resource.go` (710 LOC); **delete in release R1 (process-global error handler and SDK logger installs; `app.InstallProcessDefaults` takes them over, MOD4-3)** ← `errorhandler.go`, `sdklog.go` (266 LOC) |
| <a id="p-internal-telemetry-contract"></a>`internal/telemetry/contract` | 0 | 0 | generic | 0/0 | internal/telemetrypolicy (renamed; stays in the root module) | Test-only suite that pins ABO's series names and walks the ABO tree; moving it into core reproduces the E2 hazard (core `go mod tidy` pulls the app module, 03 §3.2.1), so it stays in ABO (MOD-6). | in 0, out 0. 0 non-test LOC, 1531 test LOC (raw/01); `series_contract_test.go:51-56` imports `internal/ai`, `aidispatch`, `database`, `metrics`, `opsmetrics`; `declared_families_test.go` walks `internal/`, `pkg/` and `cmd/`. |
| <a id="p-internal-testutil"></a>`internal/testutil` | 4 | 415 | mixed | 2/6 | internal/testutil (unchanged) | Integration fixtures importing metadata, scanner, itunes. | in 2, out 6; imports audiobook pkgs: `internal/itunes`, `internal/metadata`, `internal/scanner`; importers: `internal/itunes/service`, `internal/server` |
| <a id="p-internal-testutil-rapidgen"></a>`internal/testutil/rapidgen` | 3 | 243 | mixed | 4/1 | internal/testutil/rapidgen (unchanged) | Property-test generators for Book/Author/BookVersion. | in 4, out 1; imports: `internal/database`; importers: `internal/audiobooks`, `internal/database`, `internal/playlist` (+1) |
| <a id="p-internal-titleutil"></a>`internal/titleutil` | 0 | 172 | audiobook | 4/0 | internal/titleutil (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "provides shared helpers for normalising book titles". | in 4, out 0; importers: `internal/database`, `internal/itunes/service`, `internal/metadata` (+1) |
| <a id="p-internal-tools"></a>`internal/tools` | 0 | 704 | mixed | 7/0 | internal/tools (unchanged) | Managed external binaries (Ollama daemon, fpcalc) and a book-ID embed queue; ABO pipeline tooling, one consumer. | in 7, out 0. embed queue keyed by `book_id` `embed_queue.go:66`; `config_types.go` names the audiobook pipeline; 7 importers. |
| <a id="p-internal-trackseq"></a>`internal/trackseq` | 0 | 164 | audiobook | 1/0 | internal/trackseq (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "answers one question: what sequence number does this audiobook filename carry? It exists because that". | in 1, out 0; importers: `internal/scanner` |
| <a id="p-internal-transcode"></a>`internal/transcode` | 3 | 489 | audiobook | 3/4 | internal/transcode (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 4; imports audiobook pkgs: `internal/audioutil`; importers: `internal/audio`, `internal/plugins/maintenance`, `internal/server` |
| <a id="p-internal-transcribe"></a>`internal/transcribe` | 3 | 8306 | audiobook | 2/4 | internal/transcribe (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "extracts and transcribes the opening seconds of an audio file". | in 2, out 4; imports: `internal/ai/resultjournal`, `internal/aidispatch`, `internal/config`, `internal/logger`; importers: `internal/plugins/maintenance`, `internal/reconcile` |
| <a id="p-internal-undo"></a>`internal/undo` | 3 | 7214 | audiobook | 18/2 | internal/undo (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 18, out 2; imports audiobook pkgs: `internal/merge`; importers: `internal/activity`, `internal/audiobooks`, `internal/authormerge` (+15) |
| <a id="p-internal-usermerge"></a>`internal/usermerge` | 3 | 2561 | audiobook | 4/5 | internal/usermerge (unchanged) | Merges one account's per-user books data into another; the per-user data is books-owned. | in 4, out 5. audiobook-specific per raw/01 (`apply.go:17`); `UserMerger` capability `internal/database/user_merge.go:384`. |
| <a id="p-internal-util"></a>`internal/util` | 0 | 1478 | mixed | 23/0 | internal/util (unchanged path; audiobook string helpers) | Mostly audiobook string rules (credits, narrator, Bible title sort, transcript match). Email normalization is needed by core auth; SafeJoin and file modes by core library. | in 23, out 0. `NarratorCreditVerdict` `narrator_credit.go:15`; `title_sort.go` numbered Bible books; `NormalizeEmail` used by `oauth`; 23 importers.<br>**Split:** **mediacore/auth (email normalization)** ← `email.go` (55 LOC); **mediacore/library (SafeJoin, permission modes)** ← `path.go`, `perms.go` (57 LOC) |
| <a id="p-internal-versionprimary"></a>`internal/versionprimary` | 3 | 1619 | audiobook | 16/4 | internal/versionprimary (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "is the ONE rule for which member of a version group is its primary". | in 16, out 4; imports audiobook pkgs: `internal/audioutil`; importers: `internal/audiobooks`, `internal/batch`, `internal/dedup` (+13) |
| <a id="p-internal-versionprimary-vptest"></a>`internal/versionprimary/vptest` | 3 | 202 | audiobook | 15/1 | internal/versionprimary/vptest (unchanged) | Audiobook domain; app bucket (03 §3.4). raw/01: "seeds version groups on a real PebbleStore for the tests of every path that hands a group's primary flag on". | in 15, out 1; imports: `internal/database`; importers: `internal/audiobooks`, `internal/batch`, `internal/dedup` (+12) |
| <a id="p-internal-versions"></a>`internal/versions` | 3 | 6194 | audiobook | 4/8 | internal/versions (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 4, out 8; imports audiobook pkgs: `internal/filehash`, `internal/merge`; importers: `internal/audiobooks`, `internal/importer`, `internal/plugins/maintenance` (+1) |
| <a id="p-internal-wirecontract"></a>`internal/wirecontract` | 0 | 0 | generic | 0/0 | internal/wirecontract (unchanged) | Golden JSON contract tests for ABO's tygo types; core gets its own contract tests from its OpenAPI document. | in 0, out 0. 0 non-test LOC, 262 test LOC; goldens under `web/src/types/contract/testdata/` (raw/09 §5). |
| <a id="p-internal-work"></a>`internal/work` | 3 | 102 | audiobook | 3/2 | internal/work (unchanged) | Audiobook domain; app bucket (03 §3.4). | in 3, out 2; imports: `internal/database`, `internal/serviceregistry`; importers: `internal/server`, `internal/server/handlers/entities`, `internal/server/handlers/entities/mocks` |
| <a id="p-tools-cmd-dedup-dataset-audit"></a>`tools/cmd/dedup-dataset-audit` | 6 | 270 | audiobook | 0/1 | tools/cmd/dedup-dataset-audit (unchanged) | Audiobook maintenance CLI. | in 0, out 1. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-itunes-group-preview"></a>`tools/cmd/itunes-group-preview` | 6 | 83 | audiobook | 0/2 | tools/cmd/itunes-group-preview (unchanged) | Audiobook maintenance CLI. | in 0, out 2. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-merge-split-books"></a>`tools/cmd/merge-split-books` | 6 | 264 | audiobook | 0/0 | tools/cmd/merge-split-books (unchanged) | Audiobook maintenance CLI. | in 0, out 0. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-opsgen"></a>`tools/cmd/opsgen` | 6 | 244 | generic | 0/1 | tools/cmd/opsgen (unchanged path until R3, which deletes it together with `web/src/generated/ops.ts` and its input `internal/operations/state`; R15, XFILE-2) | Generates TS status enums from `operations/state`; in core the task states are OpenAPI enums and `@mediacore/api-client` is generated from the core OpenAPI document (03 §3.3), so the generator is subsumed. `ops.ts` is imported by `web/src/services/api.ts:14-15,590` and `web/src/utils/operationPolling.ts:7`, which has 5 non-test importers (`OperationsIndicator`, `ITunesImport`, `ActivityLog`, `Library`, `useOperationsStore`; 07 §7.8). The Activity page stops reading `ops.ts` in the tasks flip F5, when it moves onto core's task routes (03 §3.8 F5). Until R3 the input package `internal/operations/state` stays in ABO (R6-2, MOD6-1): opsgen emits `OperationV2Status`/`STATUS_PROPS` from HEAD's v2 table and `RUN_STATE_PROPS` keyed on HEAD's V3 states (`dropped`, `ops.ts:336`), neither of which is derivable from `tasks.RunState`, so it is **not** repointed and its freshness gate stays byte-identical trivially. R3 deletes `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state` together, in one PR that first moves `operationPolling` and its remaining books importers onto the generated `TaskRunView` states and drops `ops.ts` from `services/api` (03 §3.8 R3; 07 §7.8; R15, XFILE-2). It is deleted, not moved, so 03 §3.5.1 counts it among the packages that stay. | in 0, out 1. writes `web/src/generated/ops.ts` from `internal/operations/state/{state,v3}.go` (`tools/cmd/opsgen/main.go:6,28`, raw/09 §5). |
| <a id="p-tools-cmd-orphan-nonprimary-census"></a>`tools/cmd/orphan-nonprimary-census` | 6 | 474 | audiobook | 0/0 | tools/cmd/orphan-nonprimary-census (unchanged) | Audiobook maintenance CLI. | in 0, out 0. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-reconcile-book-counts"></a>`tools/cmd/reconcile-book-counts` | 6 | 260 | audiobook | 0/2 | tools/cmd/reconcile-book-counts (unchanged) | Audiobook maintenance CLI. | in 0, out 2. audiobook-specific per raw/01; no importer. |
| <a id="p-tools-cmd-reconcile-paths"></a>`tools/cmd/reconcile-paths` | 6 | 657 | audiobook | 0/1 | tools/cmd/reconcile-paths (unchanged) | Audiobook maintenance CLI. | in 0, out 1. audiobook-specific per raw/01; no importer. |

### 3.6 Delete (11 packages, 34861 non-test LOC)

| package | layer | LOC | class | in/out | target | reason | evidence (import graph, file:line) |
|---|---|---|---|---|---|---|---|
| <a id="p-internal-bookfiles"></a>`internal/bookfiles` | 3 | 105 | audiobook | 0/2 | - | Dead: no importer in non-test or test code. `FirstAudioFile` helpers were split out only to keep the SDK dependency ratchet small (`first_audio_file.go:6-9`), a reason that disappears with `pkg/plugin/sdk`. | in 0, out 2. no importer (raw/01) and 0 test importer files (grep at 748fd787a); only reference is its `layerOf` key `internal/arch/layering_test.go:153`. |
| <a id="p-internal-database-mocks"></a>`internal/database/mocks` | 3 | 31462 | mixed | 23/1 | - (successor: storetest in-memory store + per-consumer narrow mocks) | Mockery mock of the full 445-method `Store`. After the split there is no full Store to mock; recreating a 333-method `books.Store` only to keep this mock would re-widen the coupling CLAUDE.md's worked example removed. Cost surfaced: 114 test files use it. | in 23, out 1. `internal/database/mocks/mock_store.go:4564`, 31,408 LOC (raw/04 §5); 114 test importer files (counted at 748fd787a). |
| <a id="p-internal-logger-mocks"></a>`internal/logger/mocks` | 0 | 300 | generic | 1/0 | - | Mocks `ActivityLogWriter`/`RetentionStore` of the retired printf logger; core regenerates mocks from its own interfaces. | in 1, out 0. `mock_activity_log_writer.go`, `mock_retention_store.go`; 2 test importer files. |
| <a id="p-internal-maintenance"></a>`internal/maintenance` | 4 | 630 | mixed | 4/3 | - (successor: mediacore/tasks definitions; temporary adapter in internal/books) | `MaintenanceJob` is the v2.5 job shape N9 retires; its global `Register` panics on duplicates and is populated from 34 `init()`s (R5). The 60-method `JobStore` straddles core and books (§4.3). | in 4, out 3. `MaintenanceJob` `job.go:474-499`; `JobStore` `job.go:442`; `registry.go:19-24` panics on duplicate; uses `database.Book/BookFile/BookAuthor` (`job.go:254-256`); 4 importers. |
| <a id="p-internal-opscatalog"></a>`internal/opscatalog` | 4 | 385 | mixed | 1/7 | - (successor: module.Registrar + kernel catalog validation) | The v3 bundle catalog is empty and hard-lists ABO plugin bundles; module registration replaces it. Its op-ID tombstones feed books' kind-rename map (05-tasks 5T.8.2). | in 1, out 7. `var All = []func(Deps) ops.Bundle{}` `catalog.go:56`; imports `plugins/acoustid/dedup/itunes/metafetch` `catalog.go:28-32`; 1 importer (`internal/server`).<br>**Split:** **internal/books/contract (op-ID tombstones for the kind-rename map)** ← `tombstones.go` (98 LOC) |
| <a id="p-internal-plugin"></a>`internal/plugin` | 3 | 477 | mixed | 10/2 | - (deleted at F7; successor: mediacore/integrations/notify through `module.Deps.Notify`, module.Registrar) | Legacy runtime plugin registry with one implementor (webhook), a goroutine-per-handler bus using the publisher ctx, an unauthenticated route mount, and three vestigial capability interfaces. **Deleted at F7** (SIMP3-10), in the flip that moves the webhook plugin to a notify instance: its event publishers then publish books' 12 legacy event types through `module.Deps.Notify` (05-notify §10), and its registry, route mount and handlers are deleted (§8.3 deletion gate). Its bus is not moved into core `events` at F6, because the webhook plugin consumes it until F7. No compatibility surface and no `pkg/plugin/sdk` import keep it alive (`go list`: `pkg/plugin/sdk` does not import it). | in 10, out 2. `globalRegistry` `registry.go:15,45`; bus `events.go:85-103` (not as designed, raw/07 §2); `EventBook*` + `Event.BookID` `events.go:19-37`; `DownloadClient` zero implementors `plugin.go:52-63`; mount bypasses auth `internal/server/plugins_init.go:42` (raw/10 G8). |
| <a id="p-internal-plugins"></a>`internal/plugins` | 4 | 0 | generic | 0/0 | - (successor: R7 `tasks.Parallelism` has no zero value) | Test-only lint that every `RunItems` call states non-zero `Concurrency`; R7 makes the property a type error. Kept until the last v2 `RunItems` call site is migrated, then deleted. | in 0, out 0. 0 non-test LOC; `internal/plugins/runitems_concurrency_lint_test.go:106-128`; ~170 call sites (raw/06 §2). |
| <a id="p-internal-serviceregistry"></a>`internal/serviceregistry` | 0 | 532 | mixed | 24/0 | - (successor: mediacore/kernel container, explicit construction) | Process-global factory map filled by 43 `init()` registrations; N4/R5 forbid it. Typed keys include audiobook names. | in 24, out 0. `var registered = map[string]ServiceDef{}` (no mutex) `registry.go:38`; `KeyAudiobook` `keys.go:14`; 24 importers; `IncludeGroup` init-by-import coupling (raw/07 §6.4). |
| <a id="p-internal-writeback"></a>`internal/writeback` | 3 | 143 | audiobook | 0/1 | - (concept superseded by mediacore/outbox) | Dead: a durable ITL write-back outbox that nothing imports (`outbox.go` header: backlog 4.3). The same need is met by core's same-batch `outbox` (03 §3.3) when the iTunes write-back is ported. | in 0, out 1. no importer and 0 test importer files; only reference `internal/arch/layering_test.go:210,530`. |
| <a id="p-pkg-plugin-sdk"></a>`pkg/plugin/sdk` | 4 | 519 | mixed | 10/2 | - (successor: mediacore/tasks + module.Registrar) | Type-alias facade over the v2 registry that hands `database.Book` out; N4 retires it. `PageBooks` keepalive paging is a books helper; `Progress` becomes a tasks helper. | in 10, out 2. aliases onto `internal/operations/registry` (`operation.go:11-18`, `reporter.go:12`, `events.go:11-15`); `iterate.go:12,26,46` uses `database.Book`; 10 importers (6 plugins + importer, reconcile, scheduler, server).<br>**Split:** **internal/books/contract (`PageBooks`)** ← `iterate.go` (100 LOC); **mediacore/tasks (progress helper)** ← `progress.go` (120 LOC) |
| <a id="p-tools-cmd-sdkguard"></a>`tools/cmd/sdkguard` | 6 | 308 | generic | 0/0 | - | Guards `pkg/plugin/sdk`'s internal-dependency allow-list; the SDK is retired (N4) and R1/R2 `archtest` guards replace the check for every core package. | in 0, out 0. allow-list `tools/cmd/sdkguard/main.go:77-80` (registry, auth, database, serviceregistry); hard-codes `internal/` prefix `:66` (raw/03 §3.10). |


## 4. The keystone: `internal/database`

### 4.1 What is being split

`internal/database` is 103,432 non-test LOC in 272 files (raw/01), imported by 104 of the 213
packages (raw/02 finding 1). `database.Store` (`internal/database/store.go:48-55`) is pure
composition of six unexported composites — `catalogStore` 132, `mediaStore` 75, `accountStore` 44,
`enrichmentStore` 60, `operationsStore` 92, `platformStore` 42 = 445 methods
(`store.go:58,70,99,111,123,134`; raw/04 §1). A further 115 exported capability interfaces live off
`Store` and are reached through `database.AsCapability[T]` (`store_capability.go:89`) or concrete
`*PebbleStore` assertions (raw/04 §6). The only production implementation is `*PebbleStore`
(`pebble_store.go:107`; 772 exported methods).

The package also imports seven audiobook packages (`fingerprint`, `matcher`, `titleutil`,
`personname`, `metastate`, `chaptershape`, `syncapi/progress`) and `models` (raw/02 finding 2).
None of those edges survives into core: every file that needs them stays in the books residue.

### 4.2 Method-level cut of `database.Store`

| composite (methods) | behind a core contract (moved, or HEAD's code that a cutover step reads with) | books' own store | leaf → target |
|---|---|---|---|
| `catalogStore` (132) | 0 | 132 | `BookStore` 63, `AuthorStore` 25, `SeriesStore` 11, `NarratorStore` 8, `WorkStore` 9, `BookVersionStore` 10, `CollectionStore` 6 → books |
| `mediaStore` (75) | 0 | 75 | `BookFileStore` 44, `BookSegmentStore` 6, `ChapterReader` 1, `PlaylistStore` 5, `UserPlaylistStore` 9, `PathHistoryStore` 2, `PlaybackStore` 8 → books |
| `accountStore` (44) | 39 | 5 | `UserReader`+`UserWriter` 7, `SessionStore` 6, `OAuthIdentityStore` 3, `APIKeyStore` 9, `InviteStore` 5, per-user preferences 3 (`SetUserPreferenceForUser`, `GetUserPreferenceForUser`, `GetAllPreferencesForUser`) → behind `auth.Store`: core's native store from F4, HEAD's code stays in the residue as the reader of cutover step M3 until R3; `RoleStore` 6 → behind `rbac.Store`, same; `DeleteExpiredABSSessions` 1 and the 4 global preference methods (`Get/Set/Delete/GetAllUserPreferences`, which also hold per-book metadata state, `internal/metastate`) → books |
| `enrichmentStore` (60) | 0 | 60 | `MetadataStore` 7, `MetadataCacheStore` 4, `RejectedMetadataStore` 3, `ExternalIDStore` 11, `TagStore` 26, `UserTagStore` 4, `HashBlocklistStore` 5 → books |
| `operationsStore` (92) | 54 | 38 | `OpsV2Store` 39 → behind `tasks.Stores`: ABO callers re-point to `tasks` in 05-tasks' groups T2–T7; at F5 the methods move, unchanged in format, into `internal/books/cutover` as the reader of step M4 (05-tasks 5T.8.1); its legacy-step methods (`SweepHollowOperationsV2`, `MigrateOpsV2ToV3`, `StripLegacyPlanRows`) move with it together with the step bodies that call them, and R3 deletes them with books' legacy steps (09 §9.2.2; R5-2); `SystemActivityStore` 3 → behind `log.JournalStore`: HEAD's reader of the `syslog:` rows for step M5 (05-logging §5); `AIJobsStore` 10 → behind `ai.JobStore`: HEAD's job store, read by step M6; `Optimize`+`CompactionStats` 2 → moved to `storage`; `OperationStore` v1 23 → HEAD's v1 store (its writers go with their conversion PRs, and its history reader serves step M4 until R3, 05-tasks 5T.8.1; core starts with v2/v3 run state only, N9); `OperationChangeStore` 4 (undo journal keyed by `BookID`, `store.go:634-656`) → books; `MaintenanceStore` scan-cache/dirty-folder 6, `ReviewStore` 5 → books |
| `platformStore` (42) | 15 | 27 | `LifecycleStore` 2, `RawKVStore` 7 → moved to `storage` (keyspace-scoped); `ImportPathStore` minus `CountBooksByPathPrefix` 6 → HEAD's root store, read by step M6, which writes the roots as `core.library.roots` config entries; `SettingsStore` 4 → books (`setting:` is books' runtime-marker store with 56 non-test `SetSetting` call sites; core `config`/`secrets` never read `config_blob` or the 4 secret rows: step M2 decodes them, 05-config §1.12.2; cross-cutting resolution 7); `CountBooksByPathPrefix` 1, `ITunesStateStore` 6, `StatsStore` 8, `UserPositionStore` 8 → books |
| **total 445** | **108** (11 moved to `storage`; 97 HEAD-format readers of the cutover steps, deleted in R3) | **337** | |

Core gets **no** wide composite in return: each core service exposes its own small Go API and one SDK
store interface (05-storage, 05-tasks, 05-authn, 05-rbac, 05-library, 05-config, 05-ai, 05-logging), and
the books residue does not re-create a 337-method `Store` either (§4.6). Each store interface has one
native core implementation over `c/<subsystem>/`, which every app runs, ABO's included, and an in-memory one in
`coretest/fakes`; core enforces its invariants above the interface and ships one conformance suite per interface
that both run (03 §3.3 "Store interfaces and importers").

### 4.3 How the narrowed consumer interfaces guide the cut

raw/04 §4 counted 591 interfaces outside `internal/database` whose method set is a subset of
`Store`. Re-running the scan at the evidence commit with each method tagged by the target in §4.2
(go/ast walk, embedded `database.*` interfaces resolved; helper outside the repository) finds 587
(the 4-interface difference comes from how unresolvable embeds are skipped) with median width 3.
Re-run for v1.1.0 with `SettingsStore` tagged books. Classified by target:

| class | interfaces | what it means for the cut |
|---|---|---|
| books-only | 465 | already the right shape; they re-point from `database` to `internal/books/store` by import rewrite only |
| core-only | 46 | consumers of identity, roles, run state; they re-point to the core service API in the flip of their subsystem (e.g. `AuthStore` `internal/server/handlers/auth.go:82`, `UserStore` `internal/server/handlers/user.go:42`, `UserStore` `internal/oauth/resolve.go:32`, `APIKeyHandlerStore` `internal/server/handlers/apikeys.go:114`, `authSessionStore` `internal/server/middleware/auth.go:38`) |
| raw-KV-only | 29 | books code using `SetRaw/GetRaw/ScanPrefix` over its own key families (e.g. 9 in `internal/merge`); they become methods of the books module's `storage.Keyspace` handle (R8), not a dependency on a core service |
| straddling | 47 | need **two** dependencies after the split (a books store interface plus a core service handle in `module.Deps`); these are the real rewiring work |

The straddling set is where the cut is visible. The widest:

| interface | declared at | methods | targets |
|---|---|---|---|
| `ServerOpsStore` | `internal/server/server_ops_store.go:42` | 102 | books 71, books v1 ops 11, core:auth 10, core:tasks 5, core:library 3, core:storage 2 |
| `OpsStore` | `internal/plugins/maintenance/deps.go:403` | 91 | books 79, core:storage 9, core:auth 2, core:library 1 |
| `JobStore` | `internal/maintenance/job.go:442` | 70 | books 52, books v1 ops 9, core:storage 7, core:auth 1, core:tasks 1 |
| `opRegistryStore` | `internal/operations/registry/register.go:51` | 40 | core:tasks 39, books 1 |
| `opsHousekeeping` | `internal/plugins/maintenance/deps.go:304` | 22 | books 11, core:storage 9, core:auth 2 |
| `OperationsStore` | `internal/server/handlers/operations/interfaces.go:74` | 21 | books 16, books v1 ops 3, core:tasks 2 |
| `SystemStore` | `internal/server/handlers/system/interfaces.go:108` | 21 | books 18, core:log 1, core:storage 1, core:tasks 1 |
| `serverOperationStore` | `internal/server/server_ops_store.go:77` | 17 | books v1 ops 11, core:tasks 5, books 1 |
| `IngestStore` | `internal/versions/store.go:96` | 15 | books 9, core:library 6 |
| `Store` | `internal/diagnostics/store.go:41` | 12 | books 10, core:log 1, core:tasks 1 |
| `jobOperationStore` | `internal/maintenance/job.go:417` | 12 | books v1 ops 9, books 2, core:tasks 1 |
| `UserStateRepairStore` | `internal/merge/repair_user_state.go:32` | 12 | books 8, core:storage 3, core:auth 1 |

Three conclusions drive the plan:

1. **The cut line is real, not invented.** 465 + 46 + 29 = 540 of 587 consumer interfaces fall
   wholly on one side. The cut follows the composites' existing seams except inside
   `accountStore` (ABS sessions, global preferences), `operationsStore` (v1, undo journal,
   scan cache, review) and `platformStore` (stats, iTunes, positions).
2. **`opRegistryStore` proves the executor's only books dependency is `GetBookByID`** (39 of its 40
   methods are `OpsV2Store`). That one method backs the book-field `Requires` predicates in
   `internal/operations/registry/deps.go:53,78,84-125`; it becomes a subject-resolver callback the
   books module registers, which is how `internal/executor` loses its import of books types.
3. **The three widest interfaces (`ServerOpsStore`, `OpsStore`, `JobStore`) are composition-root
   bags**, not consumers: each is split into its books part and explicit core handles. `JobStore`
   disappears with `internal/maintenance` (N9).

### 4.4 Capability interfaces off `Store` (raw/04 §6)

`OpsV3Store` and its parts (`iface_ops_v3.go:397-488`), `OpRowStore`, `OperationsV2SinceLister`
move with HEAD's run-row store into `internal/books/cutover` at F5 (their row types are named by about 30 root
packages today; ABO callers re-point to `tasks` through 05-tasks' groups T2–T7, and core `tasks` never reads the
families: step M4 decodes them into `tasks.Importer`). `ActivityStorer`/`ActivityReader`/`ActivityWriter`/`ActivityCounter`/`ActivityLifecycle`/
`ActivityRetention`/`ActivityPartialQuerier`/`ActivityFilterIndexBackfiller` stay with books: the
activity feed is the books module's (03 §3.3 `log` row, 05-logging §5). To core: `RawSnapshot`, `RawSnapshotter`,
`RawBatchApplier`, `PrefixPageScanner`, `PebbleMetricsSampler`, `DBCensusProvider`,
`DBCensusExactRunner` → `storage` (the exact census runner over books' families stays books';
`storage` takes it as a census source); `SearchIndexDirtyStore`, `ChangeObserver`, `ChangeObservable`
stay with HEAD's dirty set in the residue, in use until F7, when books' index is rebuilt under `storage/index` and
M6 deletes the dirty-set rows (03 §3.3); `InviteDeliveryStore`, `InviteEmailGrantStore`,
`InviteAcceptChecker`, `InviteIDLookup`, `UserCredentialWriter`, `UserPasswordRehasher`,
`StrictUserLister`, `AdminEmailSetter`, `UnverifiedEmailDisplacer`, `VerifiedEmailMarker`,
`VerifiedEmailIdentityLinker` stay in the residue with HEAD's identity store, whose methods they
describe and which step M3 reads; ABO's `internal/usermerge`, `internal/merge`, `internal/server/handlers` and
`internal/cfaccess` reach identity through core `auth` from F4 (so the revocation journal and the never-empty-scope
rule apply), and R3 deletes these interfaces with the HEAD-format readers (R4-1; MOD-2 does not apply, because
nothing of them moves to `mediacore/internal/...`). `StoreUnwrapper` and `AsCapability` are deleted: with no decorators and no
wide interface, capability-by-assertion has nothing left to discover (`store_capability.go:60-100`;
raw/04 §5 "Decorators: none at HEAD"). `GuestServiceTokenStore` stays in the books residue (it is ABO deployment state) and is consumed by `internal/cfaccess`; every
other capability interface (Book*, Author*, Series*, Retired*, Sync*, Bookmark*, Review*, Narrator*,
Collection*, FileProvenance*, VectorANN*, …) stays with books.

### 4.5 File-level split

| destination | files | non-test LOC |
|---|---|---|
| `mediacore/storage` + `mediacore/internal/pebble` (engine, census mechanism, raw batches and snapshots; re-implemented per 05-storage) | 12 | 2,292 |
| delete (decorator and SQLite leftovers) | 5 | 249 |
| residue until R3, HEAD's identity store (identity rows, `abk_` token hashing, the auth capability interfaces; the identity store's 3,053 LOC of the round-3 `internal/authstore` part), the reader of step M3 | 8 | 3,190 |
| `internal/books/cutover` at F5, the reader of step M4 (HEAD's v2/v3 run-row store, its `*PebbleStore` methods re-homed on the reader's type, plus legacy step 66's body; cutover → store only; deleted in R3) | 16 | 7,760 |
| residue, HEAD's cipher and setting rows (`settings.go`; books' `setting:` store, and HEAD's cipher, whose frozen copy step M2 reads, 05-config §1.12.2) | 1 | 466 |
| residue until R3, HEAD's AI-job, provider-throttle, import-root and dirty-set code (read or deleted by step M6) | 7 | 1,028 |
| residue, books' guard projection onto HEAD's format stamp (`storage.LegacyGuardProjection`), version-source adapter and exact census (05-storage §5S.3.5, §5S.3.7) | 4 | 1,816 |
| residue, books' own store (incl. the books activity store and `keyfamilies.go`) | 219 | 86,631 |

The exact file lists are in the `internal/database` row's *Split* cell. Three files must be
divided internally, and `keyfamilies.go` and `settings.go` are kept whole:

| file | cut |
|---|---|
| `pebble_store.go` (6,767) | engine open/format check/close/checkpoint/ID counters/ULIDs (`:548-1060`: `NewPebbleStore`, `NewPebbleStoreInMemory`, `openPebbleChecked`, `initPebbleStore`, `Close`, `Checkpoint`, `DB`, `nextID`, `newULID`) → `internal/pebble`; the `PebbleStore` struct keeps only books fields and embeds a `storage.Keyspace` (its `ops*`/`opsV2Timeline*` fields, `:194`, move onto the cutover reader's type at F5); `migrateImportPathKeys` (`:1060`) becomes a books legacy migration step; the open path's `markOpsV2TimelineClosed` call (`:927`) moves into the reader's open path, and the raw-write guard `rejectOpv2OpRawKey` (called `:5975,6055,6158,6183` and in `dirty_set.go:46`) is replaced by a residue-owned pure predicate over the ops family prefixes (R5-2, MOD5-1) |
| `store.go` (1,897) | `User`, `Session`, `Role`, `APIKey`, `Invite` **stay**: their JSON tags are HEAD's row format, which step M3 decodes into core's own `auth`/`rbac` types (05-authn §3.1, 05-rbac §3.3; R4-1); `Setting` stays (books' `setting:` store, cross-cutting resolution 7); v2 op row types move with the run-row store; `InitializeStore` (`:1835`) → kernel's storage open, except its every-boot ops reconcile (`:1861-1871`, `ReconcileOpsV3WithV2`), which books' start hook calls until F5 and which step M4 then runs once (COR5-7; R5-2); `globalStore`/`GetGlobalStore` (`:1809-1813`, zero non-test callers per raw/07 §7.3) → deleted; `Book` (`:202`), `OperationChange` (`:634`) and the composites → books |
| `migrations.go` (1,425) | the `Migration` types (`:88-111`) and the runner (`RunMigrations` `:512`) → `storage` migrations; the version bookkeeping over `preference:db_version`/`migration_<n>` (`getCurrentVersion`/`setVersion` `:579-610`, `migration_bookkeeping.go`) stays books' as 05-storage's version-source adapter; the hand-ordered list (`:112-509`) and the 66 bodies (`:611` onward) → the books module's legacy migration step list, gated by the existing `db_version` row so none re-runs, up to `LegacyCap`, the highest HEAD-style step (66 at the evidence commit; 09 §9.2.2, DATA4-19), and step M1 of 03 §3.8; at F5 the bodies of steps 62 and 65 (`:1238-1250`, `:1340-1352`) move into `internal/books/cutover` with step 66's body (`pebble_store_plan_legacy.go`), the list keeps its order and calls the three through funcs the composition root wires, and their capability assertions (`:1239-1242`, `:1413-1416`, which log "nothing to do" and record the step as applied when the method is missing) become direct calls; `TestLegacyStepPreconditionsResolve` asserts that no legacy step has a "nothing to do" path for a production store type (R5-2, MOD5-1) |
| `keyfamilies.go` (408) | stays in the residue: the family list holds ABO key names and is the input of the books claim generator (05-storage §5S.9.1); the census mechanism goes to `storage` with `census*.go` |
| `settings.go` (466) | **not divided; stays whole** (R4-1, R4-6): HEAD's cipher (`EncryptionKeyPath`/`FindEncryptionKey`/`InitEncryption`/`EncryptValue`/`DecryptValue`, `settings.go:51-293`) is the row format of the four secret rows HEAD already stores encrypted (OpenAI key, Google Books key, Hardcover token, Basic password; `internal/config/persistence.go:1955-1958`), which ABO's config code uses until F3; step M2 decodes those rows with a frozen copy of the cipher in `internal/books/cutover` and re-encrypts them into `c/secrets/` (05-config §1.12.2); core `secrets` is a native keyring (05-config §2); `GetSetting`/`SetSetting`/`GetAllSettings`/`DeleteSetting` and `UnreadableSettingsError` (`:298-466`) stay too (cross-cutting resolution 7) |

Key families (`keyfamilies.go:37-345`, 258 registered): ownership and claim mode of every family
and sub-prefix are stated once, in [05-storage](05-storage.md) §5S.9.1 (DATA-2); this file does not
restate them. What follows for the file cut: books' own domain families stay in place under books' claims
(09 §9.2.3); `setting:`, `preference:` and `pref:` stay books families (cross-cutting resolution 7); the `act:`
activity families stay books; core holds **no grant** on any of them (R4-1). The identity, config-blob and secret,
ops, `syslog:`, import-root, AI-job, provider and dirty-set families are decoded by books' cutover steps M2–M6,
which hand their records to core's importers and delete them (03 §3.8). The two shared families that need a sub-key
split are `idx:` and `pref:`/`preference:`, whose moved sub-prefixes are listed in 05-storage §5S.9.1.

### 4.6 What replaces `Store` in the books module

- The composition root (`internal/books`) holds the concrete `*store.PebbleStore` and hands it to the
  cutover steps of `internal/books/cutover` (`Manifest.Cutover`); nothing needs a books-wide interface (CLAUDE.md "Fix It Right" worked example: remove the wide
  parameter, do not narrow it).
- Consumer interfaces stay where they are used (458 already books-only).
- **Cost surfaced:** `internal/database/mocks` (31,408 LOC mockery mock of `Store`, 114 test
  importer files) and the hand-written `mock_store.go` (4,189 LOC, `var _ Store` at
  `mock_store.go:31`) have no successor type. The end state is: tests use
  `internal/books/store/storetest` (real store on core's in-memory engine) or a mock of their own
  narrow interface. Converting 114 + ~22 test files is the largest single test cost in the
  extraction. An interim `store.Store` composite (books methods only) may be kept **inside
  `storetest`** so the mocks regenerate mechanically while the conversion runs; it is deleted at
  the end of F5, after the last part of `database.Store` has moved. The owner decides whether the conversion is done
  in release R1 to F5 or deferred; the interim composite is not an acceptable end state.

### 4.7 Imports that move with each side

Core-bound database files (the 12 storage files) import only `internal/logger` (→ `log`),
`internal/metrics`/`telemetry` (→ `metrics`) and `internal/sdnotify`; `internal/operations/state`
(which stays in ABO, R6-2) and `internal/cache` are imported only by files that stay in the residue or move into `internal/books/cutover`. The seven audiobook imports and `models` are used only by residue files
(`pebble_store_lsh.go:38`, `pebble_store.go:31-35`, `pebble_store_authors.go:21`,
`metadata_field_locks.go:15`, `book_own_folder.go:15`, `pebble_store_bookmarks.go:39`,
`embedding_store.go:22`), so R1 (`archtest.TestCoreImportsOnlyCore`) holds for the core side by
construction.

## 5. `internal/models`

Two files, 337 LOC (raw/02 §b). Only the dedup-score family is live (`Signal`, `SignalKind`,
`UnifiedDedupScore`, provenance helpers; `dedup_score.go:14-188`), used by `internal/database`
(`embedding_store.go:124,178,861,867,1651-1771`), `internal/dedup/unified` (aliases
`score.go:23,106,127,135,144`) and `internal/plugins/dedup` (`calibrate_composite.go:180,200,754,778`).
It is book-pair dedup evidence: it merges into `internal/dedup/unified`, which already re-exports it
as aliases, and the aliases become the definitions. `Audiobook` (`audiobook.go:24`) moves into
`internal/itunes`, its only user (`import.go:361,394`). `Author`, `Series`, the four request/response
DTOs and the generic `FileSystemItem`/`BrowseRequest`/`ExclusionRequest`/`SystemStatus`/`LogEntry`
types have zero users and are deleted. Nothing enters core.

## 6. The `pkg/` packages

`pkg/ops`, `pkg/ops/opstest` and `pkg/plugin/sdk` are the only packages that were meant to be a
public boundary, and all three expose `database.Book` (raw/02 finding 4).

| package | decision | how `database.Book` leaves |
|---|---|---|
| `pkg/ops` | **core public `mediacore/tasks`**. The Task/Batch/Fixer/Pipeline/Schedule/Effects/CancelContract/RC model is domain-neutral (raw/06 §7.2) and is the authoring model N9 adopts. | `Writer` (`pkg/ops/writer.go:46-90`: `ModifyBook`, `ModifyBookFile`, `RepointBookFile`, `ModifyBookAuthors`) moves to `internal/books/contract` as the books typed writer; core `tasks.Writer` is generic over `tasks.Resource{Module, Kind, ID}` (G8) with typed per-module extensions registered by the module. `SubjectType` book/book_file (`writer.go:112-121`) becomes module-declared resource kinds. `LaneMac` (`lanes.go:15`) becomes a module-declared named lane. `Common.Permission auth.Permission` (`definition.go:93`) becomes `rbac.Permission`. The omitted-`Concurrency`-means-`CPU()` default (`batch.go:123`) is replaced by R7's required `tasks.Parallelism`. |
| `pkg/ops/opstest` | **core public `mediacore/tasks/taskstest`** (conformance suite, fault injection, fake world) | Book fakes (`fakes.go:74,83,93`) move to `internal/books/store/storetest`. |
| `pkg/plugin/sdk` | **delete** (N4) | `PageBooks` (`iterate.go:41`, Book-typed `BookStore` `:25`) moves to `internal/books/contract` (a plugin importing it from the composition root would close a cycle, R4-5); `Progress` becomes a `tasks` helper; the type aliases onto the v2 registry disappear with the registry's public surface. Its 10 importers (6 plugins, `importer`, `reconcile`, `scheduler`, `server`; raw/02 §e) re-point to `mediacore/tasks` + `module.Registrar`. |

Consequences: `tools/cmd/sdkguard` (the allow-list ratchet over `pkg/plugin/sdk`,
`tools/cmd/sdkguard/main.go:77-80`) is deleted — R1/R2 `archtest` checks cover every core package,
a strictly stronger guarantee. `tools/cmd/oplint` survives as `mediacore/archtest/tasklint`; its
hard-coded module path (`analyzer.go:45`) and the ~12 fixture packages under
`tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` are re-rooted on the
core module path. After this, no exported core identifier mentions a books type, which
`archtest.TestCoreImportsOnlyCore` enforces mechanically (R1).

## 7. Other splits that shape the plan

- **`internal/config`** keeps its path as the books typed config section, and its
  `config.AppConfig` façade survives over core handles (SIMP-3: R5 binds core only; moving the
  634 call sites is an optional ratchet, not part of any release). Core `config` takes the
  loader (layer order flags → env → file → DB overrides, `Mutate`/`Snapshot`/`Generation`,
  `viper_decode.go`, unknown/removed-key audits, protected fields, secure state dir); core `secrets` is
  a native keyring (05-config §2). Core `config` is created dark at F2, the foundation PRs, with every core section
  registered (05-config §1.12.1; 03 §3.8); its loader and `secrets` land in R2's config flip F3, which wires them, makes this package the
  `Facade` over core handles (05-config §1.12.1 stage 1; MOD6-11) and carries cutover step M2: HEAD's blob, its
  plaintext secret fields, the four rows HEAD already stores encrypted and the gap-fill `config.yaml` keys are
  read once by a frozen copy of HEAD's codec and cipher in `internal/books/cutover`, written under canonical keys
  through the rename table into `c/config/` and `c/secrets/`, and deleted (05-config §1.12.2; 03 §3.8 M2; the
  blob's integration-instance fields wait for M6). Release R1 has only 05-config's stage 0, the 8 bypassing
  `config.AppConfig` writes through `Mutate`, which is ABO code. `config.go` is cut by function: the `Config`
  struct's platform fields (database path, backup, OAuth, otel, auto-update) become core sections, audiobook
  fields stay. `viper`, `configFromViper`, `LoadConfigFromDatabase`, `SaveConfigToDatabase`, `SaveConfigToFile`
  and `UpdateService` leave ABO's boot and write paths in the flip (05-config §1.12.1). The state directory keeps
  HEAD's fallback next to the database (`state_dir.go:118-155`; R3-4).
  This removes all five wrong-way `allowed` edges from config (`layering_test.go:291-295`) because
  core `config` sits below everything that used to reach up (R4).
- **`internal/operations/registry` → `mediacore/internal/executor`, a re-implementation (§8.6; R7-8, MOD7-1).**
  Core's executor is written fresh, dark, at F2 against `tasks.RunStore`/`RunState`: of the engine's
  30 non-test files, 14 use HEAD's v2/v3 row types, and 5 hold 39 code references (comments excluded) to HEAD's
  run-state package `internal/operations/state` (generated, MOD8-7), which stays in ABO (R6-2), so no
  import-path change compiles. 31 root packages still call the v2 API; 05-tasks' conversion groups T2–T7 make
  every definition native while HEAD's engine keeps running, and F5, the tasks flip, registers them all and
  deletes the ABO files in the same PR (05-tasks 5T.8), with the v1 status bridge (`legacy_op_status.go`,
  `legacy_backfill.go`), which has nothing left to bridge once no v1 writer exists. The activity mirror
  (`activity_mirror.go`, books' `ActivityEntry`), permanent books behaviour, moves in the same PR to
  `internal/activity` as a books subscriber on `core.task.log-appended.v1` (MOD8-8). `internal/operations/state.go`'s
  v1 checkpoint helpers go to `internal/books/contract` until their callers use `rc.Checkpoint`, and
  `BulkMetadataFetchParams` goes to `internal/server`. `childop` (→ `tasks`' `rc.Await`), `opsmetrics` (→ the
  executor's instruments in `internal/executor`, its one core successor), `opsv3` (→ `internal/taskhttp`) and
  `pkg/ops/opstest` (→ `taskstest`) are re-implementations for the same reason, and F5 deletes their ABO files
  too. `ops.abandoned_calls`, the one `opsmetrics` family core does not emit, becomes a books observable on a books
  `metrics.Scope` in the flip PR, which repoints both of its callers (`repairs/guards.go`,
  `retired_versions_fixer.go`), so one build never holds two owners of it (05-metrics §13; R10-3, MOD10-7). Core's
  executor is one package, `mediacore/internal/executor`: 05-tasks' state machine and writer gate are files of it,
  and `opswriter`'s `fence.go` goes to public `tasks` (MOD7-13). Residue: `deps.go` (book-field predicates) and
  the `Resource` constant list (`types.go:384-398`) move to `internal/books/contract`; `register.go`
  (serviceregistry wrapper) is deleted. The pause gate's mechanism moves with the engine, while its persisted
  marker (`registry.operations_paused` and its JSON shape, `pause_gate.go:52-60`) and the scan stand-down's marker
  (`registry.scan_standdown`, written `scan_standdown.go:571`, cleared `:583`, read back `:596`) are HEAD formats
  that cutover step M4 decodes into `tasks.Importer`; the `library.scan` kind name is books data (MOD5-4, MOD6-4;
  §2.1 (h)).
- **`internal/scheduler` → `mediacore/internal/scheduler`, a re-implementation (§8.6; MOD5-4).** Core's
  scheduler is built dark against `tasks.Schedule`, never as a copy of the ABO files: the clock key prefix
  `scheduled_task_last_tick.` and its RFC3339 codec (`interval_clock.go:50-60`) and the maintenance window's
  `maintenance_window_last_run` marker with HEAD's date codec (`maintenance.go:53,71`; R7-8, MOD7-3) are HEAD
  formats that step M4 reads once, so the first R2 boot fires no catch-up storm (05-tasks 5T.9), and
  `taskV2DefIDs` (38 task names to ABO def IDs, `maintenance.go:154-175`) is books data the cutover reads.
  `tasks.go` (1,682 LOC, 38 `TaskDefinition`s), `extra_ops.go` and `full_sweep.go` are books declarations:
  conversion group T7 turns each into the `Schedule` of a books `tasks.Definition`, so the three files dissolve
  into their definitions, and F5 deletes the ABO path, so no kind ever has two schedulers (05-tasks 5T.8.1,
  `TestOneSchedulerPerKind`; DATA4-5).
- **`internal/server`** stays the books HTTP adapter, mounted permanently through
  `httpapi.MountLegacy` (cross-cutting resolution 2; N7). Its core concerns are re-implemented in
  core and deleted here in the PR that makes them native: peer allow-list, security headers and HTTP metrics in
  release R1 (the core perimeter; `otelgin` leaves the mount, 09 §9.2.7); the Basic gate,
  `internal/server/middleware/basicauth.go`, at F3, because it needs the Basic password, which core reads only from
  the config flip on (05-http-api §8.1); bootstrap, temp login, invite accept, reset, login/invite/bootstrap rate
  limits and API-key expiry at F4; the search reconciler at F7; SPA fallback and static serving at F9 (FE-1).
  `csp.go` stays: it is the legacy mount's per-request CSP until F9 and then ABO's `ConnectSrcFrom`; the gin
  `/api/v1/csp-report` handler is deleted at F9, after core's `/ui/csp-report` (F4) (FE3-10, FE3-4). The gin 410
  stubs (`gone.go`) stay with the legacy mount. Books route groups stay on gin unless rewritten for their own
  reasons.

## 8. Move order

Moves are keyed by [09](09-abo-on-core.md) §9.4's releases and [03](03-architecture.md) §3.8's flips: **R0**
(hotfixes; no package moves), **release R1** (the core runtime, no user-data change), R2's foundation PRs **F2**, landed dark before F3 (round 13 R1), and its flips **F3–F9** in 03
§3.8's order (F9, the shell, may slip to R3) and **R3** (cleanup and the upgrade floor). Code lands **dark** on
`main` and each subsystem is wired in one flip PR (D11); 05-tasks' conversion groups T2–T7 land dark before F5 and
move no package of their own. R4's layers (03 §3.5) constrain **imports**, not landing order (MOD3-20): a core
package lands in the first release or flip that needs it, and may land before a lower-layer package only as long as
**everything it imports already exists** (COR2-9). Where a package's full contract would import something that
lands later, the package lands with a **minimum surface** that imports only what exists, and grows: the last column
of §8.1 says what exists at each point, and 06 marks each `Manifest`/`Deps` field with the release or flip that adds
it. Each core service that persists state lands, in the flip that wires it, with its SDK store interface, its
native implementation over `c/<subsystem>/` (the only one any app runs, ABO's included), its importer and its
conformance suite package (`<pkg>/<pkg>test`, MOD5-6; 03 §3.3 "Store interfaces and importers"). Books' cutover
step for its families lands in the same flip, in `internal/books/cutover`. No release grants a core service a
books family. Deferred-build packages (L5 `acquire`, `arrfacade`, `integrations/indexer`) land nowhere.

### 8.1 Releases, flips and the core packages they create

| release or flip (09 §9.4) | core packages that first exist | depends on | minimum surface at this point (what its consumers need; the rest arrives later) |
|---|---|---|---|
| R0 | none: the toolchain bump, the six early fixes and the updater hotfix change ABO code only (09 §9.4 R0) | — | — |
| R1 | `core/go.mod` (empty `core/go.sum` committed, MOD2-15), `archtest` (engine from `internal/arch`, with its self-test on a synthetic graph as the core guard's first control, 03 §3.5.1 item 2), `storage/keys` (pure tuple-key functions, so core's `c/storage/` bookkeeping is written in core's encoding, DATA2-6), `buildid` (the dependency-free L0 leaf `buildid.Binary(version)`: the version plus the SHA-256 of the running executable, which `--version-json` calls and `app.BinaryID` delegates to; FE6-3), `GO_MODULE_DIRS := . core`; the leaves `log`, `metrics` (provider construction only, re-implemented from `internal/telemetry`'s `telemetry.go`, `endpoint.go`, `resource.go`: the process-global installs stay in the ABO façade at `internal/telemetry` until the composition-root PR, and ABO's attribute vocabulary, views, scope helper and version global stay there permanently, MOD3-3, MOD4-4), `lifecycle`, `entity`, `version` (`"0.1.0"`), `coretest` (an injected-handler log capture first, never `slog.SetDefault`, 03 R5; the crash-matrix kit with the storage PRs; it grows with each flip), `library` (path-safety leaves), `health` (flight recorder, rewritten onto a `*slog.Logger`, MOD2-11); `module`, `kernel`, `app`, `httpapi` (listeners, the perimeter without the Basic gate, `MountLegacy` with `LegacyAuthGin` until F4) with `internal/httpmw` and `internal/openapi` (`httpapi` → `internal/openapi`, which takes its route description from a leaf type and never imports `httpapi`, MOD3-4), `internal/jsonschemax` (the one reflector), `internal/sdnotify`; the `rbac` **leaf types** as 05-rbac §3.1 lists them (`Permission`, `Requirement`, `Need`, `AllOf`, `AnyOf`, `Public`, `AnyAuthenticated`, `Delegated`, and `LegacyAtom(atom)`/`LegacyCheck(atom, readSide)` as values that only carry the atom, so the legacy manifest records `rbac.LegacyAtom(atom)` for every `s.perm` route; their resolution through books' atom → scope table lands at F4, COR6-1; plus the `Checker` interface and `RoleName` that `auth.Principal` and `auth.User` name) and the `auth` **leaf types** (`Principal`, `Method`, `MethodSpec`, `User`, `UserID`, `UserStatus`, `Assurance`) that `httpapi` and `Registrar.AuthMethod` name (COR3-3, SEC3-14, MOD4-11, COR4-11); `ui.ReservedSegments()` and the dependency-free leaf types `ui.ModuleManifest`, `UIIDs` and `Redirect`, in one file, so `module.ReservedIDs()` holds from release R1 (FE3-8) and `internal/modulehttp` has an L6 type for `modules[].ui` at F4 (FE4-6); `app.InstallProcessDefaults` (the logger, error handler, meter, tracer and propagator installs) and the App's private registry as the served one: ABO's client_golang families register through `Provider.Registerer()` and ABO's legacy gin `/metrics` route serves `Provider.Handler()` until F8 (05-metrics §6, §13 items 1–2; MOD4-3); the HTTP metric changes of core's perimeter, with their dashboards and rules (05-metrics §13; R14); `examples/hello` (module, app, one `httpapi` route, its keyspace and one migration, and the OpenAPI subcommand writing `core/openapi/core.json`; MOD2-10, FE2-9, MOD3-4); `storage`, `internal/pebble` and `storage/restorejournal` (L0 leaf: the record types of the one swap journal — restore, checkpoint, revert and reset kinds, `KindReset` with `ResetStore` and `ResetFactory` (R8-1, SIMP8-1) — that `core/internal/restoreswap` and storage's P2 read, and `backup` reads directly from F8, so no L1 reader imports L4 `backup/journal`; `core/internal/restoreswap` is L1 and imports `storage` and its L0 leaves, never the reverse: `storage.Admin` carries no staging or swap method, the kernel holds the swapper, stages through it and runs it at P0a, 03 R4, COR7-10, COR8-5; the slot's sentinel `storage.ErrSwapPending` is `storage`'s, COR8-6): core opens the one store, books reaches its families through strict claims on `storage.LegacyRange`, a restore stages through the one journaled swap, and the only reset path is the offline `storage reset`, staged as `KindReset` with the service stopped (HEAD's two gin reset routes are deleted; 05-storage §5S.3.15); npm `@mediacore/api-client` (runtime and generator) **only**, generated from `core/openapi/core.json` (07 §7.19 steps 1–2; step 3, the switch to the generated client flip by flip, is R2's; MOD3-4), with the Docker frontend stage that copies `core/web` (MOD3-13) | — | leaves first, and no core package imports ABO (`TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames` from the first core PR, MOD9-6); `httpapi` without storage-backed features, its `Deps.Auth`/`RBAC`/`Audit` arrive at F4 (COR3-3); `Deps.Storage` offered from the storage PR, with no core grant on any books family (R4-1); no core `config` yet: release R1 has 05-config's stage 0, which is ABO code, and listener and perimeter values reach the kernel as `app.Options`; the composition root `internal/books` is created and enters `layerOf` here (every new path enters `layerOf` in the PR that creates it: `internal/books/cutover` at F3, `internal/cfaccess` and `internal/server/legacyauth` at F4, `internal/books/contract` at F5; MOD5-9); the kernel supervisor runs its own fixed core step list, and `internal/startup` stays in ABO until F8 (MOD3-1); the kernel hands books `Deps.ExtendStart`, ABO code that runs before `app.New` calls the stateless `app.ExtendStart`, and `lifecycle` gains `NotifySocketEnv`, for the rewired `sdnotify` users (§8.3) |
| F2 | the foundation PRs, landed dark in R2 before F3, so that each later flip only switches ownership (03 §3.8 F2; 09 §9.4; round 13 R1): `storage/datamigrate` and its runner `internal/storagemigrate` (every `BootBlocking` step, so F3's M2 and F4's M3 run on it; 05-storage §5S.3.6); `tasks` with `Parallelism`, `RunItems`, `ForEach` and `ItemsError` (+`internal/executor`, `internal/scheduler`, `internal/taskhttp`, `tasks/taskstest`; core's executor, state machine, parent-follows-child wait (`rc.Await`), task-run instruments (in `internal/executor`, the one successor of `internal/opsmetrics`), task API and `taskstest` are written fresh over `tasks.RunState`, R6-2, R7-8, R8-6) with `tasks.Stores` (runs, clocks, pause and stand-down markers, MOD6-4), its native implementation over `c/tasks/` and `tasks.Importer` (05-tasks §5T.8.2's Core group), dark in ABO: no books kind is registered, ABO's kernel starts no executor or scheduler, mounts no `internal/taskhttp` route and creates no `<namespace>.ops` instrument before F5 (05-metrics §13 item 1); `audit` (`audit.Store`, built before P4b, and 05-rbac's normative action table, `core.storage.*` and `core.settings.update` included); the full `core:*` scope catalog in `rbac`, every scope any flip, seed role or atom-table row names (`TestCoreCatalog_FixedFromF2`); `config` with every core section registered on the kernel's core-section list, each with its owner spec's key table, dark until its owner adopts it (05-config §1.12.1); the **readiness core of `health`** (`ComponentID`, `Reporter`, the registry and the `Ready(id)` view dispatch waits on; COR4-8) with the registry's token route, so the tokens F3 raises surface (IMPL-12); the minimal owner registrations of the three late-owner kinds, each in its owner's package with the kind's final `TaskSpec` over a one-method interim backend interface (05-tasks §5T.8.2): `core.integrations.release-holds` in `integrations/providerhttp` (`providerhttp.InterimHolds`), `core.ai.batch-poll` in `integrations/ai` (`ai.InterimPoll`) and `core.diagnostics.export` in `health/diagnostics` (`diagnostics.InterimExport`); `examples/hello` + `hello.count` (its `RunItems` task, on `tasks.NativeStores` from the PR that lands it) | R1 (`tasks.Definition` carries an `rbac.Permission`, a release-R1 leaf type; `pkg/ops/definition.go:93`) | nothing switches: no route, schedule or stored row changes, and no ABO path moves or is deleted (the HEAD sources of these packages arrive at the flips §8.2 names); `config` holds only the section declarations until F3, `integrations/providerhttp` and `integrations/ai` only the kind's `TaskSpec` and the interim interface until F7, `health` only its readiness core until F8; `TestABOMetricsGatherNoDuplicate` runs on every PR from F2 |
| F3 | the rest of `config` (the loader for the core sections, `config.OverrideStore` with its native implementation over `c/config/`, `config.Importer` and `config/configtest`, MOD5-6; COR5-14), `secrets` (the native keyring, `secrets.Importer`), and ABO's `internal/config` becomes the `Facade` over core handles (MOD6-11); `internal/keyedmutex` (an **L0** leaf that imports only the standard library: core's own copy of ABO's `internal/keyedmutex` for `config`, `integrations/download` and `library`; ABO keeps its own, COR2-16; 03 §3.3 row and R4); the Basic gate joins the `httpapi` perimeter; books' `internal/books/cutover` with step M2 and the level and compaction steps M7 and M8, which run in every build from then on (03 §3.8) | F2 | — |
| F4 | the rest of `rbac` and `auth`, with `rbac.Store` and `auth.Store`, their native implementations over `c/rbac/` and `c/auth/` (hello and `coretest` use them from F4; SIMP4-3), `auth.Importer`, `rbac.Importer` and the suites `auth/authtest`, `rbac/rbactest` (MOD5-6), and `auth/oidc` and `auth/password`; HEAD's Start-time identity writers (`seedRolesAndTokens`: `internal/auth/seed.go`'s seed-role and `_system` writes, `internal/server/bootstrap.go`'s token, the expiry stamp, the read-only key) move in the flip PR, when P7 seeding and `auth.BootWrites` take over (COR6-3); the revocation-journal types `RevocationJournal`, `Revocation`, `RestoreHook` in core `backup/journal` (L4, lands at F4; `auth`, `rbac` and later `backup` import it — declaring them in `auth` would make `rbac` → `auth` → `rbac` a cycle, COR2-9), `audit` serving (its store and action table landed at F2), `events` (the bus; `auth`/`rbac` publish on it, COR2-9), `internal/authhttp`, `internal/uihttp`, `internal/modulehttp` (FE3-16) and the other `internal/<capability>http` packages F4 needs, among them `internal/storagehttp` (L6, imported only by `kernel`: the factory-reset route `POST /api/core/v1/system/factory-reset`, which receives the kernel's staging function as the storagehttp-local interface `storagehttp.Stager`, never `*storage.Admin` (COR8-2), `core:admin` + `Guards.Interactive` + `Guards.Owner`; the SPA's reset control returns on it; R7-2, R8-1, COR7-1, COR8-1); books' cutover step M3; `examples/hello` + its permission | F3 | `events` without `outbox` (F6); ABO's `internal/server/legacyauth` (the read-only bridge that replaces the gin auth chain, 05-http-api §8.3; its layer is the floor) is created and enters `layerOf` in the flip PR, which deletes the gin auth chain (§2.4; 03 §3.5.1) |
| F5 | `archtest/tasklint`; run fencing from `opswriter/fence.go`; the flip starts F2's executor and scheduler in ABO's kernel, registers every definition that 05-tasks' groups T2–T7 converted dark and the three late-owner kinds from F2's minimal owner registrations, and deletes HEAD's engine (`internal/operations/registry`, `internal/operations/childop`, `internal/opsmetrics`, `internal/server/handlers/opsv3`, `internal/scheduler`), HEAD's task routes (the books operations conflict set and `/api/v3/ops/*`) and the two operation streams; the Activity page moves onto core's task routes, polling `GET /api/core/v1/tasks/runs` until F6, and stops reading `ops.ts` (03 §3.8 F5, R6, R15; `tools/cmd/opsgen`, `ops.ts` and `internal/operations/state` stay until R3); HEAD's `operations_*`, `operation_*` and `op_*` families are deleted and the `ops_*` label changes made, with their dashboards, rules and rule tests (05-metrics §13; R14); books' cutover steps M4 and M5, whose run-row and marker readers move into `internal/books/cutover`; the books helper package `internal/books/contract` (MOD5-9); `health`'s components `core.storage` (reported by storage) and `books.memdb` (by books' warm-up) on F2's readiness core (COR4-8); `internal/logjournal` (imports `storage` and `tasks`, 05-logging §5) with `log.JournalStore`, its native implementation over `c/log/`, `log.Importer` and `log/logtest` (MOD5-6), with `internal/taskhttp`'s routes mounted; the first point at which every import of 06 §6.12 exists in ABO's App, so G6/G7 (`TestSecondModuleNoAudiobookEdit`) are proven from F5 (COR2-9) | F4 | the three late-owner kinds run over the interim backends that ABO's `main` passes in `app.Options.InterimBackends` (`providerhttp.InterimHolds`, `ai.InterimPoll`, `diagnostics.InterimExport`: books code over HEAD's state; 06 §6.4.10) until F7 (holds, poll) and F8 (export) |
| F6 | `outbox`, `realtime` (+`internal/sse`), `internal/eventhttp`; the SPA's realtime client (`eventSourceManager` rewritten onto `/api/core/v1/events`, R12), which the Activity page uses from then on; `/api/events` deleted | F5 | no `notify` yet (webhooks move at F7, SIMP2-4) |
| F7 | `integrations` root (the one registry for integration instances, with `integrations.InstanceStore` native over `c/integrations/instances/` and `integrations.Importer`; R3-1, SIMP4-12), the rest of `integrations/providerhttp` (with `providerhttp.GateStore`, native over `c/providerhttp/`; no importer: HEAD's holds are reset, D12 (b)) and of `integrations/ai` (+`openai`; with `ai.JobStore`, native over `c/ai/`, and `ai.Importer`; `internal/ai/resultjournal` is books code, no core store, COR5-10), `integrations/metadata`, `integrations/download` + `download/deluge`, `integrations/notify` + `notify/mail` + `notify/webhook` (with `notify.RuleStore` and `notify.Importer`; deliveries in core's native webhook format, 05-notify §3.1), `storage/index` + `internal/bleveindex` (books' index is rebuilt at a new path at the first R2 boot, with no dirty set and no importer, 03 §3.3); the rest of `library` (its roots are `core.library.roots`/`core.library.mappings` config entries; no store); the conformance suites `integrations/ai/aitest`, `integrations/providerhttp/providerhttptest`, `integrations/integrationstest` and `integrations/notify/notifytest` (MOD5-6); books' cutover step M6; `integrations/providerhttp` and `integrations/ai` extend the registrations of `core.integrations.release-holds` and `core.ai.batch-poll` with their native bodies and delete `providerhttp.InterimHolds`, `ai.InterimPoll`, books' two backends and their `app.Options.InterimBackends` fields (05-tasks §5T.8.2); HEAD's AI families renamed, with their dashboards and rules (05-metrics §13; R14); `internal/plugin` is deleted (SIMP3-10) | F6 | — |
| F8 | `backup` (core's layout: plaintext archives with provenance, catalogue, staged restore; encryption lands with the first app that can activate it, SIMP4-5), `updater` (moved with its R0 behaviour); the rest of `health`, with `Registrar.StartupSteps` (the `internal/startup` content lands here, MOD3-1); `health/diagnostics` extends the registration of `core.diagnostics.export` with 05-health's bundle and deletes `diagnostics.InterimExport`, books' backend and with it `app.Options.InterimBackends`; the kernel mounts `/metrics` from the App's Provider through `Router.Infra`, replacing the legacy gin route in the same PR, with no metric rename: the HTTP families changed in release R1, the ops families at F5 and the AI families at F7 (05-metrics §6, §13; MOD4-3, R14); HEAD's `/api/health` and `/api/v1/health` deleted | F7 | — |
| F9 | the rest of `ui`; npm `@mediacore/ui-kit` and `@mediacore/shell` (with `/vite` and `/testing`) and hello's web package, created in the shell-adoption PR series (07 §7.19 steps 4–5); `examples/hello` + `ui_embed.go` (UI half of G6); in R3 instead if the shell is not ready | F2–F8 | — |
| R3 | none: R3 only deletes (`internal/serviceregistry`, `pkg/plugin/sdk`, the no-op `--db-type` and SQLite flags, books' legacy steps 1..66 with the HEAD-format readers and `internal/books/cutover`, and together `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state`, R15); books' boot step `legacy-file-cleanup` deletes `.encryption_key` and HEAD's `library.bleve` directory, each deletion a crash-labelled row of 06 §6.5.1's writer table (R6); R3 sets `UpgradeFloor{InstallFirst: R2}` (03 §3.8) | R2 | — |

### 8.2 Root-module packages rewired onto each core package

The superseded "wave" table of v1.0.0–v1.1.0 is deleted (MOD2-2): §8.1 is the release table. This table and
§8.3 are informative, generated at the evidence commit (§2.1): no PR regenerates them, and a move PR finds the
importers it must rewire when it builds (A-4, A-11). Which
root-module packages must be rewired onto each core package (generated from `go list -deps -test
-json`: current importers, test files included, of a source package whose path leaves the root
module; for sources that stay in ABO after a split, the callers of the moved files; for the
`internal/database` parts, the packages that declare consumer interfaces over the moved methods,
§4.3). The count is the **root packages, or staying split parts, that still import the source at the
stage point where it lands**, decided per file: a package whose own move to core comes later is
counted, because it must be rewired at the source's point (MOD3-10; e.g. `internal/aidispatch`, F7,
and `internal/opsmetrics`, F5, for `metrics` in release R1; `internal/server/middleware`'s staying
`cfaccess.go` for `auth/oidc` at F4). §8.3 counts the users still in ABO at a gate's stage point:

| first stage | core package | sources at HEAD (stage each moves in, if later) | root-module packages that must be rewired onto it (current importers of a source that stay in ABO) |
|---|---|---|---|
| R1 | `mediacore/archtest` | `internal/arch` | -; plus callers of the moved files of `internal/arch` (those paths stay in ABO) |
| R1 | `mediacore/coretest` | `internal/database/dbtest`, `internal/mail/mailtest` (F7) | 2: `internal/server`, `internal/server/handlers`; plus callers of the moved files of `internal/database/dbtest` (those paths stay in ABO) |
| R1 | `mediacore/health` | `internal/diag/flight`, `internal/server` (F8), `internal/server/handlers` (F8), `internal/sysinfo` (F8) | 0; plus callers of the moved files of `internal/server`, `internal/server/handlers`, `internal/sysinfo` (those paths stay in ABO) |
| R1 | `mediacore/httpapi` | `internal/httputil`, `internal/server`, `internal/server/middleware` | 9: `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/fpworker`, `internal/server/handlers/opaccess`, `internal/server/handlers/opsv3`, `internal/server/handlers/repairs`, `internal/server/handlers/system`; plus callers of the moved files of `internal/httputil`, `internal/server` (those paths stay in ABO) |
| R1 | `mediacore/internal/pebble` | `internal/metrics` | -; plus callers of the moved files of `internal/metrics` (those paths stay in ABO) |
| R1 | `mediacore/internal/sdnotify` | `internal/sdnotify` | 4: `.`, `internal/operations/registry`, `internal/search`, `internal/server` |
| R1 | `mediacore/kernel` | `internal/startup` (F8) | 3: `cmd`, `internal/server`, `internal/server/handlers/system` |
| R1 | `mediacore/library` | `internal/boundedcall`, `internal/diskstats`, `internal/fileops` (F7), `internal/libroot`, `internal/pathutil` (F7), `internal/security/pathvalidation`, `internal/security/safepath`, `internal/util`, `internal/watcher` (F7) | 13: `internal/audiobooks`, `internal/backup`, `internal/covers`, `internal/deluge`, `internal/maintenance/jobs`, `internal/organizer`, `internal/plugins/maintenance`, `internal/reconcile`, `internal/repairs`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/audiobooks` (+1); plus callers of the moved files of `internal/fileops`, `internal/pathutil`, `internal/util` (those paths stay in ABO) |
| R1 | `mediacore/lifecycle` | `internal/lifecycle` | 3: `internal/aiscan`, `internal/operations/registry`, `internal/scheduler` |
| R1 | `mediacore/log` | `internal/config`, `internal/logging` | 13: `internal/dedup`, `internal/httputil`, `internal/importer`, `internal/metabatch`, `internal/metafetch`, `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/dedup`, `internal/server/handlers/operations` (+1); plus callers of the moved files of `internal/config` (those paths stay in ABO) |
| R1 | `mediacore/metrics` | `internal/telemetry` | -; plus callers of the moved files of `internal/telemetry` (those paths stay in ABO) |
| R1 | `mediacore/storage` | `internal/compactprogress`, `internal/database` | 3: `internal/metafetch`, `internal/plugins/maintenance`, `internal/server`; via `internal/database` (core:storage): 19 packages declare consumer interfaces over these methods: `internal/ai/resultjournal`, `internal/audiobooks`, `internal/authority`, `internal/covertext`, `internal/decisions`, `internal/maintenance/jobs`, `internal/merge`, `internal/metadata/dailyquota`, `internal/metafetch`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/repairs`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/admindebug`, `internal/server/handlers/system`, `internal/undo`, `internal/usermerge` |
| F2 | `mediacore/audit` | `internal/server/absauth` (F4) | -; plus callers of the moved files of `internal/server/absauth` (those paths stay in ABO) |
| F2 | `mediacore/config` | `internal/config` (F3) | -; plus callers of the moved files of `internal/config` (those paths stay in ABO) |
| F2 | `mediacore/integrations/ai` | `internal/ai/aijobs` (F7), `internal/aidispatch` (F7), `internal/config` (F7) | 7: `internal/ai`, `internal/dedup`, `internal/scanner`, `internal/server`, `internal/server/handlers/aibackends`, `internal/telemetry/contract`, `internal/transcribe`; plus callers of the moved files of `internal/config` (those paths stay in ABO) |
| F2 | `mediacore/integrations/providerhttp` | `internal/metadata` (F7), `internal/metadata/providerhttp` (F7), `internal/security/safehttp` (F7) | 4: `internal/catalog`, `internal/covers`, `internal/metafetch`, `internal/server`; plus callers of the moved files of `internal/metadata` (those paths stay in ABO) |
| F2 | `mediacore/internal/executor` | `internal/operations/registry` (F5), `internal/opsmetrics` (F5) | 37: `.`, `cmd`, `internal/catalog`, `internal/dedup`, `internal/itunes/service`, `internal/maintenance`, `internal/maintenance/jobs`, `internal/metabatch`, `internal/metadata`, `internal/metafetch`, `internal/opscatalog`, `internal/plugins/acoustid` (+25) |
| F2 | `mediacore/internal/scheduler` | `internal/scheduler` (F5) | 5: `internal/server`, `internal/server/handlers`, `internal/server/handlers/mocks`, `internal/server/handlers/operations`, `internal/server/handlers/operations/mocks` |
| F2 | `mediacore/internal/taskhttp` | `internal/server/handlers` (F5), `internal/server/handlers/operations` (F5), `internal/server/handlers/opsv3` (F5) | 1: `internal/server`; plus callers of the moved files of `internal/server/handlers`, `internal/server/handlers/operations` (those paths stay in ABO) |
| F2 | `mediacore/tasks` | `internal/operations` (F5), `internal/operations/childop` (F5), `internal/operations/freshness` (F5), `internal/operations/opmode` (F5), `internal/operations/opswriter` (F5), `internal/walkresume` (F5), `pkg/ops` (F5), `pkg/plugin/sdk` (F5) | 26: `internal/audiobooks`, `internal/dedup`, `internal/importer`, `internal/itunes`, `internal/itunes/service`, `internal/maintenance`, `internal/maintenance/jobs`, `internal/metabatch`, `internal/metafetch`, `internal/opscatalog`, `internal/organizer`, `internal/plugins/acoustid` (+14); plus callers of the moved files of `internal/operations/opswriter` (those paths stay in ABO) |
| F2 | `mediacore/tasks/taskstest` | `pkg/ops/opstest` (F5) | 0 |
| F4 | `mediacore/auth` | `internal/auth`, `internal/config`, `internal/server`, `internal/server/absauth`, `internal/server/middleware`, `internal/util` | 19: `internal/operations/registry`, `internal/opscatalog`, `internal/plugins/dedup`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/repairs`, `internal/scheduler`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/fpworker` (+7); plus callers of the moved files of `internal/config`, `internal/server`, `internal/server/absauth`, `internal/util` (those paths stay in ABO) |
| F4 | `mediacore/auth/oidc` | `internal/oauth` | 4: `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/middleware` |
| F4 | `mediacore/internal/authhttp` | `internal/server/handlers` | -; plus callers of the moved files of `internal/server/handlers` (those paths stay in ABO) |
| F4 | `mediacore/rbac` | `internal/auth` | 20: `internal/config`, `internal/operations/registry`, `internal/opscatalog`, `internal/plugins/dedup`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/repairs`, `internal/scheduler`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/audiobooks` (+8) |
| F5 | `mediacore/archtest/tasklint` | `tools/cmd/oplint` | 0 |
| F6 | `mediacore/realtime` | `internal/realtime` | 2: `internal/itunes/service`, `internal/server` |
| F7 | `mediacore/integrations/ai/openai` | `internal/ai` | -; plus callers of the moved files of `internal/ai` (those paths stay in ABO) |
| F7 | `mediacore/integrations/download/deluge` | `internal/deluge` | -; plus callers of the moved files of `internal/deluge` (those paths stay in ABO) |
| F7 | `mediacore/integrations/notify/mail` | `internal/mail` | 2: `internal/server`, `internal/server/handlers` |
| F7 | `mediacore/integrations/notify/webhook` | `internal/plugins/webhook` | 0 |
| F7 | `mediacore/internal/bleveindex` | `internal/search` | -; plus callers of the moved files of `internal/search` (those paths stay in ABO) |
| F7 | `mediacore/storage/index` | `internal/search`, `internal/server` | -; plus callers of the moved files of `internal/search`, `internal/server` (those paths stay in ABO) |
| F8 | `mediacore/backup` | `internal/backup` | 6: `internal/appdirs`, `internal/config`, `internal/decisions`, `internal/organizer`, `internal/server`, `internal/server/handlers/system` |
| F8 | `mediacore/updater` | `internal/server`, `internal/updater` | 0; plus callers of the moved files of `internal/server` (those paths stay in ABO) |
| F9 | `mediacore/ui` | `internal/server` | -; plus callers of the moved files of `internal/server` (those paths stay in ABO) |

Earliest stage after which each package that stays in ABO can be fully rewired (latest stage in which something it imports today moves to core): R0: 61 packages, R1: 25 packages, F4: 5 packages, F5: 6 packages, F7: 58 packages, F8: 7 packages, F9: 2 packages (total 164).

### 8.3 Non-importable destinations and their gates (MOD-2, R2-5)

Every destination under `mediacore/internal/` or `mediacore/kernel` that 04 maps something to, with
the ABO code that still uses it at the stage point where the ABO path stops being importable (for
split sources, references to identifiers declared in the moved files, the staying files of the same
package included), split into **non-test users** — read from the non-test imports of every root
package, `cmd/*` and the root `main` included, build-tagged non-test files (`IgnoredGoFiles`, §2.1 (c))
counted as non-test — and **test-only users** (R3-3). The generator evaluated every stage point in 03 §3.8's
order at the evidence commit (§2.1 (c)); in CI, Go's `internal/` rule makes
a user left unrewired a build failure of the PR that moves the destination (A-4). Round 4 removed the `internal/authstore`,
`internal/rbacstore` and `internal/executor/legacystore` destinations: HEAD's store code stays in ABO as the
cutover steps' readers (R4-1), so no ABO user is rewired onto them. Such a destination is legal only because every listed user is rewired, no later than
that point, to the named public surface; the second table says how, per user, and with which kind of
use it was declared. A user found by `go list` and not declared, a user declared with the other kind,
or a declared user that `go list` no longer finds is reported by the generator (it would have caught MOD2-1,
where three `sdnotify` users were missing, and MOD3-1, where `cmd`'s non-test use of
`internal/startup` was declared test-only). Users are named by their path at the gate's stage point
(MOD3-11).

| source | non-importable destination | non-test users (from the non-test imports of every root package, `cmd/*` and the root `main` included) | test-only users | public surface they are rewired to | path stops being importable at |
|---|---|---|---|---|---|
| `internal/metrics` | `mediacore/internal/pebble` | 4: `internal/metafetch`, `internal/metrics`, `internal/openlibrary`, `internal/server` | 1: `internal/telemetrypolicy` (was `internal/telemetry/contract`) | `storage` metrics for the main and auxiliary stores (`storage.OpenAuxiliary`) | R1 |
| `internal/sdnotify` | `mediacore/internal/sdnotify` | 4: `.`, `internal/operations/registry`, `internal/search`, `internal/server` | none | `module.Deps.ExtendStart` and, for code that runs before `app.New`, the stateless `app.ExtendStart` (05-health §5H.3.4, 06 §6.4.5); `lifecycle.NotifySocketEnv`; readiness owned by the kernel; fast-exit flags handled by `app` | R1 |
| `internal/server/handlers` | `mediacore/internal/authhttp` | 2: `internal/server`, `internal/server/handlers` | none | core-mounted `/api/core/v1/auth/*`; `auth` invite delivery API for `internal/cfaccess` | F4 |
| `internal/operations/registry` | `mediacore/internal/executor` | 36: `.`, `cmd`, `internal/catalog`, `internal/dedup`, `internal/itunes/service`, `internal/maintenance`, `internal/maintenance/jobs`, `internal/metabatch` (+28) — all of them, through 05-tasks' conversion groups T2–T7; the F5 flip ships only at a generated count of 0 | 1: `internal/metafetch` | `tasks` (`RunItems`, `Definition`, starter options, run context) once each caller's definition is native (05-tasks T2–T7); the flip deletes the path | F5 |
| `internal/opsmetrics` | `mediacore/internal/executor` | 2: `internal/plugins/maintenance`, `internal/repairs` | 1: `internal/telemetrypolicy` (was `internal/telemetry/contract`) | books' own gauges through public `metrics` (an app-registered observable on books' `metrics.Scope`), never `RegisterAbandoned` with ABO source names, both gauges in one PR (MOD10-7); the engine and scheduler users go with their paths at F5 | F5 |
| `internal/scheduler` | `mediacore/internal/scheduler` | 5: `internal/scheduler`, `internal/server`, `internal/server/handlers/mocks`, `internal/server/handlers/operations`, `internal/server/handlers/operations/mocks` | 1: `internal/server/handlers` | `tasks.Schedule` / `ScheduleFromConfig` and the core task API | F5 |
| `internal/server/handlers` | `mediacore/internal/taskhttp` | 1: `internal/server` | 1: `internal/server/handlers` | core-mounted `/api/core/v1/tasks/*` | F5 |
| `internal/server/handlers/operations` | `mediacore/internal/taskhttp` | none | none | - | F5 |
| `internal/server/handlers/opsv3` | `mediacore/internal/taskhttp` | 1: `internal/server` | none | core-mounted `/api/core/v1/tasks/*`; `/api/v3/ops/*` deleted in the same PR | F5 |
| `internal/startup` | `mediacore/kernel` | 2: `cmd`, `internal/server` | 1: `internal/server/handlers/system` | `Registrar.StartupSteps` (typed `health.StartupStep`, 06) and the `health` degraded-token constants | F8 |

| source → destination | ABO user (path at the gate's stage point) | use | rewired by (no later than the stage in the table above) |
|---|---|---|---|
| `internal/metrics` → `mediacore/internal/pebble` | `internal/metafetch` | non-test | the Open Library store opens through `storage.OpenAuxiliary`, which registers its metrics |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/metrics` | non-test | the collector registration in `metrics.go` is deleted (rewrite) |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/openlibrary` | non-test | same (`store.go`) |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/server` | non-test | `pebble_metrics_sources.go` deleted |
| `internal/metrics` → `mediacore/internal/pebble` | `internal/telemetrypolicy` (was `internal/telemetry/contract`) | test | the series test (`internal/telemetrypolicy`) reads the App registry |
| `internal/opsmetrics` → `mediacore/internal/executor` | `internal/plugins/maintenance` | non-test | `retired_versions_fixer.go` likewise (`retired_versions_stat`), in the same PR as `guards.go` (one owner change, MOD10-7) |
| `internal/opsmetrics` → `mediacore/internal/executor` | `internal/repairs` | non-test | `guards.go` registers books' gauges as an app-registered observable on books' `metrics.Scope` instead of `RegisterAbandoned` with ABO source names |
| `internal/opsmetrics` → `mediacore/internal/executor` | `internal/telemetrypolicy` (was `internal/telemetry/contract`) | test | `internal/telemetrypolicy`'s contract test seeds the executor's instruments through the public SDK path (`coretest.MetricsProvider`, 05-metrics §6) instead of importing the ABO instruments |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/scheduler` | non-test | books declarations re-expressed (REWRITE) |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server` | non-test | the kernel owns the scheduler; server wiring deleted |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers` | test | scheduler-admin test moves with `scheduler_admin.go` (F5) |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers/mocks` | non-test | regenerated against the core task API |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers/operations` | non-test | `TaskInfo` from the core task API |
| `internal/scheduler` → `mediacore/internal/scheduler` | `internal/server/handlers/operations/mocks` | non-test | regenerated |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `.` | non-test | `--print-capabilities` fast exit handled by `app` (`main.go:53`) |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/operations/registry` | non-test | `subprocess.go:70` and its test use `lifecycle.NotifySocketEnv` |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/search` | non-test | `IndexService` takes an injected `func(step string)`, wired by `books.Module()` from `Deps.ExtendStart` (05-health §5H.3.4), so a layer-2 package imports no `module` (MOD4-18); until F7 moves the index lifecycle to core |
| `internal/sdnotify` → `mediacore/internal/sdnotify` | `internal/server` | non-test | READY/STATUS/STOPPING move into the kernel lifecycle (`server_lifecycle.go:340-343,552` deleted) |
| `internal/server/handlers` → `mediacore/internal/authhttp` | `internal/server` | non-test | the gin wiring of these handlers is deleted in the same PR (core mounts the routes) |
| `internal/server/handlers` → `mediacore/internal/authhttp` | `internal/server/handlers` | non-test | tests of the moved handlers move with them; `user_merge.go` per RESIDUE |
| `internal/server/handlers` → `mediacore/internal/taskhttp` | `internal/server` | non-test | gin wiring deleted in the same PR |
| `internal/server/handlers` → `mediacore/internal/taskhttp` | `internal/server/handlers` | test | `scheduler_admin_test.go` moves with the file |
| `internal/server/handlers/opsv3` → `mediacore/internal/taskhttp` | `internal/server` | non-test | `wire_ops_v3_routes.go` and its test deleted |
| `internal/startup` → `mediacore/kernel` | `cmd` | non-test | `root.go:274` builds `serve`'s step runner with `startup.New()`; its seven steps leave `serve` before F8 (per-step table in §8.3: `InitOTEL`, `EnsureSecureStateDir` and `initializeStore` in release R1, `guardAgainstKeyRegeneration`, `initEncryption`, `loadConfigFromDB` and `config.Validate` at F3, when core `config` and `secrets` take over), so `cmd`'s last non-test use ends at F3 and none becomes a books startup step; `commands_test.go` reads the token constants from `health` |
| `internal/startup` → `mediacore/kernel` | `internal/server` | non-test | its start steps (`server.go:257,493`, `server_lifecycle.go:157,226-247`) become books startup steps; `startup_steps_test.go` follows the table |
| `internal/startup` → `mediacore/kernel` | `internal/server/handlers/system` | test | `handler_health_test.go` uses a `coretest` health double |

**`cmd`'s use of `internal/startup`, per step (MOD4-17).** Release R1 edits `cmd/root.go`'s step runner and
keeps the Fatal/Degraded class of the steps that remain; the store and config steps leave in release R1 and at F3, so
none of the seven becomes a books startup step. `internal/server`'s own start steps keep the path in ABO until F8.

| step (`cmd/root.go:281-385`) | leaves `serve` at | successor |
|---|---|---|
| `InitOTEL` | R1 | `app.InstallProcessDefaults` and the App metrics provider (MOD4-3) |
| `EnsureSecureStateDir` | R1 | the kernel-resolved state directory, which ABO's code takes through an `app` accessor (05-config §1.7, OPS4-8) |
| `initializeStore` | R1 | kernel boot phase P2 (one store handle, 06 §6.5.1) |
| `guardAgainstKeyRegeneration`, `initEncryption`, `loadConfigFromDB`, `config.Validate` | F3 | core `config` and `secrets` with the façade (05-config §1.12.1 stage 1); HEAD's stored config reaches core once, through step M2 (05-config §1.12.2); never a second Pebble open (COR4-1). Until F3 they run inside `serve` over the store the kernel opened |

**Deletion gates.** `internal/plugin` is deleted at **F7**, the flip that turns the webhook plugin into
a notify instance (SIMP3-10), and `internal/operations/childop`'s ABO path at **F5**, the tasks flip (R8-6,
MOD8-2: its v2 callers become `rc.Await` in their conversion group, 05-tasks T2–T7, and the engine's
`v3_adapter.go` goes with the registry path; `opsmetrics`, whose successor is non-importable, is a row of the gate
table above). At HEAD 20 non-test files in 10 packages import it (18 outside the
generated mocks; `go list -deps -test -json`); every user still in ABO at F7 is listed below
like a MOD-2 gate, and so is every ABO user of `childop` still in ABO at F5:

| deleted path | stage | non-test users | test-only users | rewired to | user | use | how |
|---|---|---|---|---|---|---|---|
| `internal/operations/childop` | F5 | 3: `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/server` | none | `tasks`' `rc.Await` once each caller's definition is native (05-tasks T2–T7); the engine's `v3_adapter.go` goes with the registry path at F5 | `internal/plugins/dedup` | non-test | `run_all.go:532` and `label_refinement.go` call `rc.Await` once their definitions are native (T2–T7) |
| | | | | | `internal/plugins/maintenance` | non-test | `optimize.go` likewise (T2–T7) |
| | | | | | `internal/server` | non-test | `WaitForOp` (`server_maintenance_deps.go:1185-1198`) becomes `rc.Await` in the batch that makes its callers native (05-tasks T2–T7) |
| `internal/plugin` | F7 | 9: `internal/dedup`, `internal/itunes/service`, `internal/quarantine`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/audiobooks`, `internal/server/handlers/dedup`, `internal/server/handlers/metadata` (+1) | none | `module.Deps.Notify` (books' 12 legacy event types as notifications, 05-notify §10) and `module.Registrar` | `internal/dedup` | non-test | `lifecycle.go` publishes through `Deps.Notify` |
| | | | | | `internal/itunes/service` | non-test | `importer.go`, `service.go` publish through `Deps.Notify` |
| | | | | | `internal/quarantine` | non-test | `register.go`, `service.go` publish through `Deps.Notify`; tests follow |
| | | | | | `internal/server` | non-test | the plugin registry wiring (`registry_wire.go`, `server.go`) and route mount (`plugins_init.go`) are deleted |
| | | | | | `internal/server/handlers` | non-test | `filesystem.go`, `organize.go`, `interfaces.go` publish through `Deps.Notify`; `plugins.go` is deleted |
| | | | | | `internal/server/handlers/audiobooks` | non-test | `handler.go`, `handler_crud.go` publish through `Deps.Notify`; tests follow |
| | | | | | `internal/server/handlers/dedup` | non-test | `handler.go` publishes through `Deps.Notify`; test follows |
| | | | | | `internal/server/handlers/metadata` | non-test | `book_scan_lock.go`, `handler.go` publish through `Deps.Notify`; test follows |
| | | | | | `internal/server/handlers/mocks` | non-test | `mock_event_publisher.go`, `mock_plugin_registrar.go` regenerated or deleted |

### 8.4 Ordering notes for the implementation plan

1. **`internal/database` cannot be split before `storage` exists (release R1).** The books residue is
   created in release R1 as `internal/books/store` on `storage.LegacyRange` with books' generated claim
   list (05-storage §5S.9.1), strict from the first release. Core holds no grant on a books family: each core
   service runs its native store from its flip on, and HEAD's rows of its families reach it once, through its
   importer, called by books' cutover step in R2 (03 §3.8 M2–M6; 09 §9.2.3), which deletes them in the same
   batch. Until its flip, HEAD's own code keeps using its families as at HEAD.
2. **`tasks` is the long pole of F5** (core `tasks` itself lands dark at F2). The registry's importers are counted in its §3.2 row and
   those still in ABO at F5 in §8.3 (all rewired through 05-tasks' conversion groups T2–T7, which land dark;
   the flip ships when none is left, which its build shows), and ~170 `RunItems` call sites must
   state `tasks.Parallelism` (R7); `internal/plugins`'s lint stays until the last call site moves,
   then is deleted.
3. **Wrong-way `allowed` edges are re-evaluated against the mapped layer of the core package, not
   dropped** (MOD2-7). The ABO guard maps core's R4 layers onto its own: core L0–L5 → −1 (platform,
   legal from every ABO layer); core L6 (`httpapi`, `realtime`, `ui`) → ABO layer 5 (transport);
   core L7–L9 (`module`, `app`, `examples/*`) → ABO layer 6 (entry), with no allowance: the
   composition root `internal/books` computes to 6 and only `.` and `cmd` import it, while the shared
   helpers live below it in `internal/books/contract` (layer 3, created at F5; §2.4, R4-5, MOD4-2, MOD5-9).
   `internal/books/cutover` takes the computed layer of its highest in-module import (`internal/config` and the
   residue), and only `internal/books` imports it (03 §3.5.1; MOD7-9). `internal/cfaccess`'s `layerOf` is 5 (§2.4;
   MOD7-4). A guard assertion, over **non-test** imports (MOD5-11), fails when any package other than `.`, `cmd`
   and `internal/books` imports `mediacore/module` or `mediacore/app`, and when the residue imports
   `internal/books/cutover` (R5-2): below layer 6 an ABO package receives what it needs (`ExtendStart`, loggers,
   scopes) as funcs or interfaces from `internal/books` (MOD4-18; e.g. `internal/search`'s `IndexService` takes
   an injected `func(step string)`, §8.3). `coretest`, `archtest` and `storage/*` → −1;
   `shared/...` → 3. `loadGraph` (`internal/arch/layering_test.go:339-381`) keeps imports with the
   `github.com/falkcorp/mediacore/` prefix instead of dropping them, and the layer the guard computes
   for an ABO package is the maximum of its in-module imports' layers **and** the mapped layers of its
   core imports (03 §3.5.1, MOD3-5). Consequence in release R1: `internal/httputil` (declared layer 0,
   `layering_test.go:82`) keeps `respond.go`, which builds the envelope types that move to `httpapi`,
   so its computed and declared layer become 5; that is legal, because all 19 of its importers are
   layer 5. `internal/database`'s `startup_notify.go` takes `app.ExtendStart` as an injected func
   instead of importing `app`, whose mapped layer 6 would be a wrong-way edge from the store (§8.3). So the five
   edges `dedup`, `metadata`, `reconcile`, `repairs` → registry and `reconcile` → sdk disappear at F5 honestly
   (they then point at `tasks`, L4 → −1); the two `plugins/* → itunes/service` edges **stay** after F6, because
   `itunes/service` still imports a transport package (core `realtime`, mapped to layer 5;
   `layering_test.go:298-299`); the five `config` edges disappear at F3 as the loader and persistence move
   (`config`, L3 → −1).
4. **The logger façade** (`internal/logger` over `mediacore/log`) lets release R1 land without touching
   342 files. It is kept: removing it is an optional ratchet (SIMP-3), as is the
   `config.AppConfig` façade's 634-site migration; neither is part of any release.
5. **No gin retirement.** ABO's gin engine stays under `httpapi.MountLegacy` permanently
   (cross-cutting resolution 2); the gin helpers it needs stay in ABO (§1 decision 6).
6. Package-count effect on the ABO layering guard: **49** package paths leave the root module (164
   stay; the renames in §2.4 keep their count) and **5** new paths appear (`internal/books`,
   `internal/books/contract`, `internal/books/cutover`, `internal/cfaccess`, `internal/server/legacyauth`), so
   **169** packages remain and the literal floor of 150 (`layering_test.go:38`) would still pass with a margin
   of 19 — still too thin to rely on; the ratio floor of 03 §3.5.1 is required. These are 03 §3.5.1's figures.
   (v1.0.0 counted 59 leaving and 156 remaining; the difference is `internal/httputil`,
   `internal/logger`, `internal/appdirs`, `internal/telemetry/contract`, from v1.3.0 `internal/logger/logtest`,
   from v1.4.0 `internal/telemetry`, `internal/ai/resultjournal` and `internal/metadata/dailyquota`, from v1.5.0
   `tools/cmd/opsgen` and from v1.6.0 `internal/operations/state`, which now stay until R3 deletes them, R15.)

### 8.5 Rule-R1 cuts at each move

A file that lands in core may not import ABO code (rule R1; `TestCoreImportsOnlyCore` from the first core PR,
beside `TestCoreHoldsNoAppKeyNames`, both in 09 §9.4 R1's CI table, MOD9-6). For every
file that lands in core at a stage point and imports an ABO package whose referenced declarations are
still in ABO at that point, the row below states how the import is cut in the move PR (generated per
point; parts that are re-implemented are covered by §8.6 instead: since round 7 that includes every file that
references HEAD's run-state vocabulary, so no `internal/operations/state` cut is left here, R7-8, MOD7-1). Every
row's rule is one of §2.2's mechanical rewrites (the generator classified each), so every file below is planned
as a **move** (R8-6, MOD8-1). The generic cuts apply to every
moving file: `internal/logger` → a `*slog.Logger` (child of `log`) passed in, no package-level logger
(R5); `internal/config` → a typed config section handle; `internal/database` → the destination's own
native store interface (§4.3–§4.4); `internal/httputil` → `httpapi` helpers;
`internal/metrics` → the App's metrics registry.

| stage | file(s) landing in core | ABO import cut at the move | how |
|---|---|---|---|
| R1 | `internal/compactprogress`: `step.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| R1 | `internal/database/dbtest`: `newstore.go` | `internal/database` | `coretest` store fixture over `storage` |
| R1 | `internal/diag/flight`: `flight.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F4 | `internal/auth`: `context.go`, `seed.go`, `user_active.go` | `internal/database` | the destination's own native store interface (§4.3-§4.4) |
| F4 | `internal/oauth`: `resolve.go` | `internal/database` | `auth.Store` (core's native store, F4) |
| F4 | `internal/oauth`: `resolve.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F4 | `internal/server/absauth`: `audit.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F5 | `internal/operations`: `progress.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F7 | `internal/ai/aijobs`: `aijobs.go` | `internal/database` | the destination's own native store interface (§4.3-§4.4) |
| F7 | `internal/ai/aijobs`: `aijobs.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F7 | `internal/aidispatch`: `dispatch.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F7 | `internal/aidispatch`: `dispatch.go` | `internal/telemetry` | `integrations/ai` metrics declare their own attribute keys and scope through `metrics.Scope`; ABO's attribute vocabulary stays in books telemetry (MOD4-4); they are core's `<ns>_ai_*` only: `metrics.go` stays books' (its AI recorder) and the moved `dispatch.go` loses its HEAD instrument calls, so HEAD's `ai_dispatch.*` families are books' AI recorder's (R9-3, COR9-14, MOD10-10) |
| F7 | `internal/fileops`: `service.go` | `internal/config` | a typed config section handle (`config`, 05-config) |
| F7 | `internal/fileops`: `service.go` | `internal/database` | `library` move journal over `storage` |
| F7 | `internal/fileops`: `safe_operations.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F7 | `internal/metadata/providerhttp`: `providerhttp.go` | `internal/metadata/dailyquota` | `providerhttp.GateStore` (its quota half), native over `c/providerhttp/`; books' `internal/metadata/dailyquota` stays ABO code over `provider_daily_budget:` until F7, and step M6 deletes HEAD's budget rows (D12 (b)) |
| F7 | `internal/watcher`: `watcher.go` | `internal/appdirs` | the watched roots passed in by books (`library` roots) |
| F7 | `internal/watcher`: `watcher.go` | `internal/config` | a typed config section handle (`config`, 05-config) |
| F8 | `internal/backup`: `backup.go` | `internal/logger` | a `*slog.Logger` (child of `log`) passed in; no package-level logger (R5) |
| F8 | `internal/server/handlers`: `diagnostics_traces.go` | `internal/httputil` | `httpapi` helpers (N7) |

### 8.6 Re-implemented split parts and rewritten staying files (MOD2-3)

Split parts that do not move verbatim: the move PR rewrites the content against core contracts,
deletes the ABO files and rewrites the staying side's references. The count is generated by the
split check of §2.1 (e) (unexported names, struct field and method accesses, foreign-type methods
and forbidden edges between the part and the rest of its package). `internal/telemetry`'s core part is
a re-implementation: provider construction is rewritten as per-`App` values while the installs stay
in the ABO façade until the composition-root PR of release R1 and the ABO vocabulary stays for good (MOD3-3,
MOD4-4). Core's executor state machine is a re-implementation over `tasks.RunState` (R6-2, MOD6-1): no file of
`internal/operations/state` moves, and HEAD's vocabulary stays in ABO for cutover step M4. **By §2.1 (i) (R7-8,
MOD7-1) so is every core-bound file that uses that vocabulary or HEAD's run-row types**: the registry engine
(written fresh as `internal/executor`, dark from F2), `childop`, `opsmetrics`, `opsv3`, `pkg/ops/opstest` and
`internal/sysinfo`'s core part, which the generator's vocabulary check found besides the four the review named
(its `RecentOperations` reads v2 run rows; `service.go` is **file divided**, MOD8-5). The registry,
`childop` and `opsmetrics` keep their ABO paths until F5, which deletes them (§8.3), and `internal/sysinfo`
keeps its path for good, so their counts are in-package only. For a re-implemented part whose ABO path leaves
in its own PR, the count adds the **importer references** (selector uses of the part's declarations from files
still in ABO at its point), which the move PR rewires. `internal/database` → `internal/books/cutover` is listed
because HEAD's run-row code keeps its format but its `*PebbleStore` methods move onto the cutover reader's type
(05-tasks 5T.8.1); its point is F5, and its direction is checked by gate E below. `internal/scheduler`'s core part
is a re-implementation too (MOD5-4): core's scheduler is built dark at F2 against `tasks.Schedule`, F5 deletes the ABO
files, and the ABO formats they hold are read once by step M4 (the clock-key codec) or stay books data
(`taskV2DefIDs`). `internal/logger` is no longer split (MOD4-6), and the round-3 re-implemented parts that sent
ABO stores into core (`internal/authstore`, `integrations/ai`, `providerhttp`, `library`, `secrets` and
`storage/index` from `internal/database`) are gone: those files stay in ABO as HEAD-format readers (R4-1). Every
other split part is a verbatim move and type-checks (its `go fix` hunks in the same PR, §2.2); the two MOD2-3
cases are resolved by changing the mapping:
`opswriter` sends only the self-contained `fence.go` to core (public `tasks` fencing) and keeps
`intent.go` and `evidence_journal.go` with the Writer, and `user_merge.go`'s methods move onto an
ABO-owned handler type before `user.go` leaves (last row).

| package | part | stage | staying-side references the move PR rewrites (generated) | how |
|---|---|---|---|---|
| `internal/ai` | `mediacore/integrations/ai/openai` | F7 | 40 | OpenAI client re-implemented as the `ai/openai` adapter over `providerhttp` (05-ai); ABO's `OpenAIParser` methods become calls on the adapter; `ai_metrics.go` and `telemetry.go` are divided: only their client-side tracing goes to the adapter, and their recorder half stays books' AI recorder in `internal/ai`, which records HEAD's `ai.*` instruments from `ai.WithObserver` (R9-3, COR9-14, MOD10-10; 05-ai §8) |
| `internal/aidispatch` | `internal/ai` | F7 | 13 | file divided: `Capability`/`Kind`/`Spec`/`Lookup` and the registration mechanism land in `integrations/ai`; the ten capability IDs stay books registrations; `metrics.go` moves whole into books' AI recorder (MOD10-10) |
| `internal/auth` | `internal/cfaccess` | F4 | 1 | owner proof consolidated into `internal/cfaccess` over the public `auth` context API |
| `internal/config` | `mediacore/auth` | F4 | 16 | invite/bootstrap settings become `core.auth.*` keys (05-authn) |
| `internal/config` | `mediacore/config` | F3 | 27 | core `config.Load` for the core sections lands at F3, the config flip, which makes ABO's `internal/config` the `Facade` over core handles (05-config §1.12.1 stage 1; MOD6-11); HEAD's stored config reaches core once, through cutover step M2 and the rename table (05-config §1.12.2) |
| `internal/config` | `mediacore/integrations/ai` | F7 | 9 | the AI endpoint settings become `integrations.ai.*` keys of core config; cutover step M6 imports the blob's instance fields into `integrations.InstanceStore` (05-metadata M1, 05-config §1.12.2) |
| `internal/config` | `mediacore/log` | R1 | 6 | log-retention keys become `core.log.*` (05-logging) |
| `internal/database` | `internal/books/cutover` | F5 | 174 non-test + 56 test-side (gate E's test half, 24 test files) | HEAD's run-row store code moves at F5 unchanged in format, but its `*PebbleStore` methods become methods of the cutover reader's type over books' `storage.LegacyRange` (05-tasks 5T.8.1); the residue's references are rewritten in the same PR |
| `internal/database` | `mediacore/storage` | R1 | 61 | storage engine, census mechanism and the per-owner guard re-implemented in core `storage` (05-storage; §4.5 cut lines); books' guard projection onto HEAD's format stamp, version-source adapter and exact census stay in the residue |
| `internal/metadata` | `mediacore/integrations/providerhttp` | F7 | 17 | provider error classification re-implemented in instance-scoped `providerhttp` |
| `internal/metrics` | `mediacore/internal/pebble` | R1 | 1 | Pebble collector registered by `storage` for the main and auxiliary stores; `metrics.go`'s registration line is deleted |
| `internal/operations/childop` | `mediacore/tasks` | F5 | 0 | **re-implemented** as `tasks`' parent-follows-child wait (`rc.Await`) over `tasks.RunStore` (R7-8, MOD7-1): `follow.go` reads `database.OperationV2Row` and `state` (`follow.go:46,101`); the ABO file **stays** until F5 deletes it (R8-6, MOD8-2), so the count is in-package only and its users are §8.3's deletion gate |
| `internal/operations/registry` | `internal/books/contract` | F5 | 1 | file divided: the requirement mechanism (`Requirement`, `AllSatisfied`, `DepStore`, cycle check) lands in the engine; book-field predicates and the `GetBookByID` adapter become books requirement declarations (05-tasks) |
| `internal/operations/registry` | `mediacore/internal/executor` | F5 | 15 | **re-implemented** (R7-8, MOD7-1): core's executor is written fresh against `tasks.RunStore`/`RunState` (05-tasks 5T.8.1); 14 of the engine's 30 files use HEAD's v2/v3 row types (`OperationV2Row`, `OpsV2Store`, `TransitionRequest`, `OpLogV2Row`, `Ledger`, …) and 5 hold 39 code references (comments excluded) to HEAD's run-state vocabulary (`state.StopQuiesce`, `InterruptedQuiesced`, `TimeoutPrefix`, `state.ToV2` at `resume.go:555`), none of which has an import-path-only replacement; the ABO files keep running HEAD's engine until F5, which deletes them (`v3_adapter.go` among them: core's executor runs `tasks.Definition`s natively, MOD8-7), so the count is in-package only |
| `internal/operations/state` | `mediacore/internal/executor` | F5 | 0 | core's executor state machine is **re-implemented** over public `tasks.RunState` (R6-2, MOD6-1): no file of `internal/operations/state` moves; HEAD's vocabulary stays in ABO for cutover step M4 (which maps `state.V3` → `RunState`) and `tools/cmd/opsgen` |
| `internal/opsmetrics` | `mediacore/internal/executor` | F5 | 0 | **re-implemented** as the executor's task-run instruments in `internal/executor` (R7-8, R8-6, MOD8-2): `opsmetrics.go` classifies HEAD's V3 states (6 `state` references); each family has one owner in every build (R9-3, MOD9-1; 05-tasks 5T.11); the ABO file **stays** until F5 deletes it, so the count is in-package only and its users are §8.3's gate |
| `internal/scheduler` | `internal/books` | F5 | 9 | books schedule declarations re-expressed as `tasks.Schedule` declarations (05-tasks group T7); no method on the core scheduler type |
| `internal/scheduler` | `mediacore/internal/scheduler` | F5 | 12 | re-implemented against `tasks.Schedule` (MOD5-4): tick, due check and cron logic are rewritten in core; the clock key prefix and codec are read once by cutover step M4, `taskV2DefIDs` is books data; the ABO path keeps ticking until F5, which deletes it |
| `internal/search` | `mediacore/storage/index` | F7 | 2 | regex term expansion takes books' indexed-field table as data (`storage/index` options) |
| `internal/server` | `mediacore/auth` | F4 | 122 | auth routes and guards re-implemented in core `auth`/`authhttp` (05-authn); gin handlers deleted at F4 |
| `internal/server` | `mediacore/health` | F8 | 12 | flight-recorder wiring re-implemented in `health` (05-health) |
| `internal/server` | `mediacore/httpapi` | R1 | 29 | perimeter re-implemented in core `httpapi` (05-http-api §8.1); gin middleware deleted in release R1 |
| `internal/server` | `mediacore/storage/index` | F7 | 50 | search index queue/reconciler re-implemented as `storage/index` (05-storage) |
| `internal/server` | `mediacore/ui` | F9 | 13 | SPA serving re-implemented by `ui.Handler` at F9 (07 §7.6.3); the shell's CSP builder is written in `ui` (07 §7.6.2) while `csp.go` stays ABO's (FE3-10) |
| `internal/server` | `mediacore/updater` | F8 | 9 | update handlers become core `/api/core/v1/update` (05-updater) |
| `internal/server/handlers` | `mediacore/internal/authhttp` | F4 | 8 | auth/user/key/invite handlers re-implemented as core routes (05-authn §4.13); gin routes deleted in the same PR |
| `internal/server/handlers` | `mediacore/internal/taskhttp` | F5 | 1 | scheduler admin re-implemented as core schedule routes (05-tasks §5T.3.10) |
| `internal/server/handlers/operations` | `mediacore/internal/taskhttp` | F5 | 7 | core's pause route `/api/core/v1/tasks/pause` is re-implemented in taskhttp (05-tasks 5T.3.10); HEAD's `/api/v1/operations*` pause routes are deleted by the F5 flip |
| `internal/server/handlers/opsv3` | `mediacore/internal/taskhttp` | F5 | 0 in-package + 10 importer references | **re-implemented** as core's task API in `internal/taskhttp` over `tasks` (R7-8, MOD7-1; §1 decision 6): the gin handlers read `database.RunRecord`/`RunQuery`/`ItemPage`/`WorkerProgress` and HEAD's V3 states; the F5 PR deletes `/api/v3/ops/*` and the gin files, and the first-party callers switch in the same PR (03 §3.8 F5) |
| `internal/server/middleware` | `mediacore/auth` | F4 | 5 | gin auth middleware replaced by the read-only `legacyauth` bridge, the new ABO package `internal/server/legacyauth` (F4; 03 §3.5.1), over core stages 12-13 (05-http-api §8.3) |
| `internal/server/middleware` | `mediacore/httpapi` | R1 | 1 | gin perimeter middleware (rate limit, body limit, log/trace/recovery redaction; the Basic gate at F3) re-implemented as `httpapi` perimeter stages on net/http (05-http-api §4.2); the Basic gate's ABS exemption (`ABSRequireAuth`) becomes per-route `OwnAuth` (SEC-14) |
| `internal/sysinfo` | `mediacore/health` | F8 | 0 | host stats and service status are **re-implemented** in `health` (05-health) (R7-8): `service.go`'s `RecentOperations` (`service.go:28,48,77-82`) converts HEAD's v2 run rows (`OperationV2Row`, `OperationsV2SinceLister`, `ListRecentOperationsV2`) into v1 `database.Operation` for the dashboard, which has no import-path-only replacement; that reader, the library and import fields and the v1 log helpers stay in `internal/sysinfo` (file divided, MOD8-5) as books' dashboard code until its conversion PR (05-tasks T2–T7) re-points it at `tasks`; the path stays, so the count is in-package only |
| `internal/telemetry` | `mediacore/metrics` | R1 | 3 | provider construction, the endpoint validator and the resource are re-implemented in `metrics` in release R1, which only returns providers; the process-global installs stay in the ABO façade at the `internal/telemetry` path until the composition-root PR of release R1, when `app.InstallProcessDefaults` takes them over and the install code is deleted; the attribute vocabulary, views, scope helper, version global and config readers stay as books telemetry permanently (R5, MOD3-3, MOD4-3, MOD4-4, 05-metrics §13 item 1) |
| `pkg/ops` | `internal/books/contract` | F5 | 5 | file divided: `tasks.Writer` becomes generic over `Resource` (with `Subject`, `SubjectType`, `Risk`); the Book-typed methods form the books `Writer` contract |
| `pkg/ops/opstest` | `internal/books/store/storetest` | F5 | 18 | Book-typed fakes re-implemented over the exported `taskstest` fake world |
| `pkg/ops/opstest` | `mediacore/tasks/taskstest` | F5 | 22 in-package + 0 importer references | **re-implemented** as `taskstest` over `tasks.RunState` and core's in-memory storage (R7-8, MOD7-1): `conformance.go` and `run.go` read HEAD's V3 states, and `faults.go`, `store.go` and `world.go` build HEAD's v2/v3 rows (`Ledger`, `KeyRange`, `Intent`, `OpDefinitionV2Row`) on a `*database.PebbleStore`, so no file of the package moves by an import-path change (the generator's VOCAB check, round 7) |
| `internal/server/handlers` | `user_merge.go` (staying file) | F4 | 18 | F4, before `user.go` moves: the nine merge methods move from `*UserHandler` onto an ABO-owned merge handler type in `internal/server/handlers` over `internal/usermerge`, with the merge fields (MOD2-3); `mergeActor`'s role-name check (`user_merge.go:62-75`) is not carried over: the handler checks `Can(core:admin)` with key scoping, so a key scoped to `users.manage` cannot merge accounts (SEC8-7) |

**Gate E — one direction, no cycles (R5-2; MOD5-1, MOD5-2, MOD6-2).** A generator check at the evidence commit; in CI
the ABO guard assertion that the residue never imports `internal/books/cutover` (§8.4 note 3) and each PR's build
are the checks. A typed scan of `internal/database` (go/types over `go list -export` data, the generator's helper,
§2.1; so method and field uses are resolved, not matched by name) lists every reference between the run-row part
that moves into `internal/books/cutover` and the residue. Rewriting the residue's references onto the cutover
package would give `store → cutover → store`; keeping the capability assertions instead would compile and
silently skip steps 62 and 66 (recorded as applied), fail step 65 and skip the ops reconcile. So every residue →
cutover reference, including every residue interface call whose only concrete implementation moves, is listed
with how the F5 PR cuts it, and every unexported residue name the moved files use is listed with how they reach
it; an unlisted reference or a stale entry fails the generator. (`errStoreClosing` is declared in
`pebble_gate.go`, which goes to core `storage` in release R1, so the moved files reach it through
`storage.ErrClosed`, not through the residue.)

| residue file | names it uses from the part (typed scan) | how the move PR cuts them |
|---|---|---|
| `ai_scan_store.go` | `recoverPebbleClosed` | `recoverPebbleClosed` moves from `pebble_store_ops_v2.go` into the residue and is exported for the cutover package |
| `census.go` | `recoverPebbleClosed` | as above (`census.go` itself goes to core in release R1, which takes its own copy) |
| `census_exact.go` | `recoverPebbleClosed` | as above |
| `dirty_set.go` | `rejectOpv2OpRawKey` | the same residue-owned ops-family key predicate |
| `iface_assert.go` | `OpsV2Store`, `OpsV3Store` | the compile-time assertions for the ops interfaces move to the cutover package, asserting the cutover reader's type |
| `junction_bookid.go` | `recoverPebbleClosed` | as above |
| `migrations.go` | `BytesAfter`, `BytesBefore`, `HollowOps`, `LegacyPlanStrip`, `MigrateOpsV2ToV3`, `OpsV3MigrationReport`, `Plans`, `Scanned` (+12) | the bodies of steps 62, 65 and 66 move to the cutover package; the step list keeps its order and calls them through funcs the composition root wires from the cutover package; the capability assertions on `SweepHollowOperationsV2`, `MigrateOpsV2ToV3` and `StripLegacyPlanRows` become direct calls, so no step can log "nothing to do" and be recorded as applied (`migrations.go:1239-1242,1413-1416`) |
| `mock_store.go` | `BatchBucketEntry`, `OpDefinitionV2Row`, `OpErrorV2Row`, `OpLogV2Row`, `OpStateV2Row`, `OpStrikeV2Row`, `OpSubject`, `OperationV2Row` | the hand-written `MockStore` of the `Store` composite has no successor (§4.6); its ops methods are deleted with the composite at the end of F5 (§4.6) |
| `pebble_activity_filter_index.go` | `recoverPebbleClosed` | as above |
| `pebble_activity_store.go` | `recoverPebbleClosed` | as above |
| `pebble_store.go` | `markOpsV2TimelineClosed`, `rejectOpv2OpRawKey` | the open path's `markOpsV2TimelineClosed` (`pebble_store.go:927`) runs in the cutover package's open/`Recover`; the raw-write guard becomes a residue-owned pure key predicate over the ops family prefixes, so no residue writer imports the cutover package |
| `pebble_store_book_hash_index.go` | `derefStr` | the residue keeps its own two-line `derefStr` |
| `store.go` | `OpsV2Store`, `OpsV3ReconcileReport`, `ReconcileOpsV3WithV2`, `opsV3Log` | `InitializeStore`'s every-boot ops reconcile (`store.go:1861-1871`) becomes the reconcile that step M4 runs once; until F5 books' start hook calls it through the same wired func (COR5-7); `OpsV2Store` leaves the `operationsStore` composite, which dissolves (§4.6) |
| `storecap.go` | `OpsV2Store` | deleted in release R1 (decorator leftovers) |

| unexported residue name the part uses | route |
|---|---|
| `closingContext` | exported by the residue as a method of its handle |
| `db` | the cutover reader's type holds books' `storage.LegacyRange` handle, not the residue's field |
| `opChangeKey` | exported by the residue (`OpChangeKey`); the undo journal stays books' own store |
| `opsLogSeq` | moves onto the cutover reader's type |
| `opsMu` | moves onto the cutover reader's type |
| `opsV2MirrorOff` | moves onto the cutover reader's type |
| `opsV2TimelineClosed` | moves onto the cutover reader's type |
| `opsV2TimelineTrustMu` | moves onto the cutover reader's type |
| `opsV2TimelineTrusted` | moves onto the cutover reader's type (`pebble_store.go:194`, written `pebble_store_ops_v2_timeline.go:379`) |
| `prefixEnd` | exported by the residue (`PrefixEnd`) |
| `prefixUpperBound` | exported by the residue (`PrefixUpperBound`) |

**Gate E's test half (MOD6-2).** 396 of `internal/database`'s 400 test files are in-package, so the split
breaks tests as well as code: once the cutover package imports the residue, an in-package residue test that
names a moved declaration is `import cycle not allowed in test`, and an ops test that moves with the run-row
files cannot reach the residue's unexported names or its test-only helpers. A second typed scan covers the test
files (`GoFiles` + `TestGoFiles` type-checked as the in-package test variant, `XTestGoFiles` against it, every
import resolved through the variant's `ImportMap` onto the `[internal/database.test]` variants of `merge`,
`undo`, `rapidgen` and the other recompiled dependencies, §2.1; MOD8-10), and every test file that uses a moved
declaration has one declared disposition: **moves** into `internal/books/cutover` (its residue names need a
route, its residue helpers a declared copy into the package's `testhelpers_test.go`), becomes the residue's
**external** test `store_test` (which may import the cutover package; unexported names through the residue's
`export_test.go`), or **stays** with the moved names it uses rewritten away. A staying test that uses a helper
declared in a moving test file needs a declared copy. An unlisted file, an undeclared name or a stale entry
fails, and the cross-split test references are counted in the cutover row above. The test half's file set is
§2.1 (c)'s tag union, so the `race`-tagged timeline twin, which a default-tag `go list` reports only as ignored,
has its own row (MOD7-8). R3 deletes these tests with the package.

| test file of `internal/database` | disposition | how |
|---|---|---|
| `iface_ops_v2_test.go` | moves to `internal/books/cutover` (in-package) | tests the ops lifecycle interface of `iface_ops_v2.go`, which moves |
| `migration065_test.go` | becomes `store_test` (external test of `internal/books/store`) | step 65 through the residue's migration runner: becomes `store_test` (external test of `internal/books/store`), which imports the cutover package and wires step 65's body as the composition root does; deleted in R3 with the code only step 65 calls |
| `migration066_test.go` | becomes `store_test` (external test of `internal/books/store`) | step 66 through the runner (atomicity via `pebbleDigest`): becomes `store_test`, as above; deleted in R3 with the code only step 66 calls |
| `op_class_lists_test.go` | moves to `internal/books/cutover` (in-package) | ops class lists |
| `ops_v2_recent_test.go` | moves to `internal/books/cutover` (in-package) | v2 recent-runs listing |
| `pebble_apply_raw_batch_test.go` | stays (in-package residue test) | asserts the raw-write guard: rewritten onto the residue-owned ops-family key predicate and its error (gate E's `rejectOpv2OpRawKey` cut) |
| `pebble_ops_v2_closed_test.go` | moves to `internal/books/cutover` (in-package) | closed-timeline reads |
| `pebble_store_op_rows_test.go` | moves to `internal/books/cutover` (in-package) | row pages |
| `pebble_store_opchange_index_test.go` | stays (in-package residue test) | an undo-journal index test that seeds two ops rows: it writes them as raw `opv2:op:` rows through the residue's raw-KV methods (as `rawSetOp` does) instead of calling the ops store |
| `pebble_store_ops_v2_hollow_test.go` | moves to `internal/books/cutover` (in-package) | step 62's body (`SweepHollowOperationsV2`); calls the moved step-62 body directly instead of the residue's `migration062Up` wrapper; deleted in R3 with the code only step 62 calls |
| `pebble_store_ops_v2_result_test.go` | moves to `internal/books/cutover` (in-package) | v2 result data |
| `pebble_store_ops_v2_resumable_test.go` | moves to `internal/books/cutover` (in-package) | resumable runs |
| `pebble_store_ops_v2_status_lookup_test.go` | moves to `internal/books/cutover` (in-package) | status lookup |
| `pebble_store_ops_v2_test.go` | moves to `internal/books/cutover` (in-package) | v2 run store |
| `pebble_store_ops_v2_timeline_norace_test.go` | moves to `internal/books/cutover` (in-package) | build-tag twin of the timeline test (declares `opsV2TimelineRaceBuild` under `!race`) |
| `pebble_store_ops_v2_timeline_race_test.go` | moves to `internal/books/cutover` (in-package) | build-tag twin of the timeline test (declares `opsV2TimelineRaceBuild` under `race`); in §2.1 (c)'s tag union, so it has its own row (MOD7-8) |
| `pebble_store_ops_v2_timeline_test.go` | moves to `internal/books/cutover` (in-package) | v2 timeline; with its build-tag twins `pebble_store_ops_v2_timeline_norace_test.go` and `pebble_store_ops_v2_timeline_race_test.go` (`opsV2TimelineRaceBuild`), which move with it |
| `pebble_store_ops_v2_wait_test.go` | moves to `internal/books/cutover` (in-package) | wait/dependency rows |
| `pebble_store_ops_v3_bench_test.go` | moves to `internal/books/cutover` (in-package) | v3 benchmarks |
| `pebble_store_ops_v3_lastrun_test.go` | moves to `internal/books/cutover` (in-package) | v3 last-run index |
| `pebble_store_ops_v3_reconcile_test.go` | moves to `internal/books/cutover` (in-package) | `ReconcileOpsV3WithV2`, which becomes the reconcile that step M4 runs once; reads the version through the residue's exported version-source adapter instead of `getCurrentVersion` |
| `pebble_store_ops_v3_rowmarks_test.go` | moves to `internal/books/cutover` (in-package) | v3 row marks |
| `pebble_store_ops_v3_test.go` | moves to `internal/books/cutover` (in-package) | v3 run store; its family assertions read the residue's exported `KeyFamilies()` instead of `keyFamilies`, `keyFamilyRanges` and `unregisteredFamily` |
| `store_capability_concrete_test.go` | stays (in-package residue test) | its `OpsV2Store` assertion moves to the cutover package with `iface_assert.go`'s ops assertions; the rest of the file stays |

| name | route |
|---|---|
| `getCurrentVersion` (residue, unexported; used by a moving test) | the residue's exported version-source adapter (05-storage §5S.3.5), which the moved files already need |
| `keyFamilies` (residue, unexported; used by a moving test) | the residue's exported `KeyFamilies()` (`keyfamilies.go:330`) |
| `keyFamilyRanges` (residue, unexported; used by a moving test) | derived from `KeyFamilies()` |
| `migration062Up` (residue, unexported; used by a moving test) | the test calls the moved step-62 body directly (the residue's wrapper only forwards to it) |
| `unregisteredFamily` (residue, unexported; used by a moving test) | the exported census label (`(unregistered)`) through `KeyFamilies()`'s result |
| `checkV3Consistency` (test helper) | copied into the residue's external-test helper file for the migration tests (declared in the moving v3 test) |
| `countPrefix` (test helper) | copied into the cutover package's `testhelpers_test.go` |
| `familyForKey` (test helper) | copied into the cutover package's `testhelpers_test.go`, over `KeyFamilies()` |
| `firstDiff` (test helper) | copied into the residue's `test_helpers_test.go` for `soft_deleted_count_test.go` (declared in the moving timeline test) |
| `idAt` (test helper) | copied into the cutover package's `testhelpers_test.go` (declared in `migration065_test.go`, which becomes `store_test`) |
| `mustRun` (test helper) | as `checkV3Consistency` |
| `newTimelineStore` (test helper) | copied into the residue's external-test helper file (`store_test`) for the migration tests |
| `opsV2TimelineRaceBuild` (test helper) | moves with its build-tag twins |
| `rawSetOp` (test helper) | as `newTimelineStore` |
| `setupPebbleTestDB` (test helper) | copied into the cutover package's `testhelpers_test.go` as a constructor over coretest's in-memory storage and the residue's exported open (never `storetest`) |
| `setupTestDB` (test helper) | as `setupPebbleTestDB` |
| `timelineKeys` (test helper) | as `newTimelineStore` |
| `v3Digest` (test helper) | as `checkV3Consistency` |
| `db` (for `store_test`) | `export_test.go`: `func (s *PebbleStore) DBForTest()` |
| `getCurrentVersion` (for `store_test`) | `export_test.go` |
| `migration065Down` (for `store_test`) | `export_test.go` |
| `migration065Up` (for `store_test`) | `export_test.go` |
| `migration066Up` (for `store_test`) | `export_test.go` |
| `migrations` (for `store_test`) | `export_test.go` (the legacy step list, read-only) |
| `opsV3Migrator` (for `store_test`) | `export_test.go` |
| `pebbleDigest` (for `store_test`) | `export_test.go` re-exports the in-package test helper |
| `setVersion` (for `store_test`) | `export_test.go` |
| `setupTestDB` (for `store_test`) | `export_test.go` re-exports the in-package test helper |

The cycle check then builds HEAD's non-test package graph with every file at its final ABO location
(split parts as their own nodes; core destinations dropped, because gate B keeps core free of ABO
imports), the declared imports of the new paths, and the cuts below; any strongly connected component of
more than one package fails. A negative control adds the edge `internal/database` → `internal/config`
(the config cutover step placed in the residue) and must produce a cycle, which it does. Nodes carry
their **final** names (a renamed package, such as `internal/database` → `internal/books/store`, is one node;
MOD6-9), and so do the new-path table below and its edges (MOD7-10). Every cut must be needed: restoring it alone must close a cycle, or the generator fails. One import
of v1.4's mapping closes a cycle and is cut in its move PR:

| import removed | why it would close a cycle, and how the move PR removes it |
|---|---|
| `internal/cfaccess` → `internal/server/absauth` | `cfaccess.go` and `cfbounce.go` name the `CFAssertionVerifier` interface declared in `internal/server/middleware/absauth.go:50-55`, which goes to `internal/server/absauth` while `absauth.go` uses the CF Access adapter; the F4 consolidation PR moves that interface (a slice of `*oauth.CFAccessVerifier`) into `internal/cfaccess`, so the import runs `internal/server/absauth` → `internal/cfaccess` only |

Three more imports are removed by their move PR for another reason and are **not** cycle cuts (round 6
checked each by restoring it: the graph stays acyclic; MOD6-8); `internal/config` → `internal/cfaccess` and
`internal/plugins/maintenance` → `internal/cfaccess` are required by the ABO layering guard (MOD7-4, MOD8-6):

| import removed | why (not a cycle cut; MOD6-8) |
|---|---|
| `internal/config` → `internal/cfaccess` | removed by the F4 PR; not a cycle cut (MOD6-8: none of `internal/cfaccess`'s imports reaches `internal/config`), but **required** by the ABO layering guard (MOD8-6, as MOD7-4): `internal/config` is layer 1 (`layering_test.go:126`) and `internal/cfaccess` is layer 5, so the edge would be wrong-way. `update_service.go` reads both `auth.AccessEmailFromContext` (`:822`, the refusal log line) and `auth.OwnerProofWhyNot` (`:987`), which move to `internal/cfaccess` with `internal/auth/owner.go`; the F4 PR gives the update service one small injected interface carrying both the owner-proof check and the access-email reader, wired by `internal/books` (the MOD4-18 rule) |
| `internal/plugins/maintenance` → `internal/cfaccess` | removed by the F4 consolidation PR, not a cycle cut (MOD7-4): `repairs_ops.go:319` calls `auth.IsOwnerEmail` (`internal/auth/owner.go:80`, which moves to `internal/cfaccess`), but `internal/cfaccess` gets `owner_guard.go`, which imports `internal/httputil`, so its computed layer is 5 (transport) while `internal/plugins/maintenance` is layer 4 (`layering_test.go:227`): the edge would be wrong-way and the ABO guard rejects it. `internal/books` injects the owner check into `internal/plugins/maintenance` as a func (the MOD4-18 rule, as for `internal/config`) |
| `internal/server` → `internal/books` | required by the composition-root guard assertion (only `.` and `cmd` import `internal/books`, 03 §3.5.1 item 1), not by the cycle check (round 6: gate E now checks that every EDGECUT entry is needed, and this one closes no cycle at HEAD's mapping): `server.go` wires the metadata-upgrade extra op through `scheduler.NewExtraOpsRegistrar`/`ExtraOpsDeps` (`extra_ops.go`, which becomes a books declaration in the composition root); the F5 PR that moves `extra_ops.go` moves that wiring into `internal/books` too |

The new paths as the cycle check sees them (ABO-side edges only; core imports are gate B's). The computed
importers of `internal/books/cutover` beyond the composition root are HEAD's users of the ops row types
(`OperationV2Row` and the other §4.4 types): 05-tasks' groups T2–T7 re-point every one of them at `tasks` before F5
moves the files, so from F5 only `internal/books` imports the package, and it imports none of them, so no cycle
forms either way. It imports `internal/operations/state` (R6-2). One naming rule for the whole gate (MOD7-10):
every name is the **final** one, as in the cycle check, and each edge carries the stage point from which it exists
— the later of the two ends' creation and the arrival of the moved files that make it (e.g. the composition root's
`internal/scanner` edge comes with the F5 schedule declarations) — so the generator can compare each stage point.
The five paths are 03 §3.5.1's count:

| new path | created at | ABO-side non-test imports (computed from its moved files, plus declared; final names, each with the stage point from which the edge exists) | ABO-side importers (computed; declared for paths with no HEAD files; final names and edge stage) |
|---|---|---|---|
| `internal/books` | R1 | `internal/books/contract` (F5), `internal/books/cutover` (F3), `internal/books/store` (R1), `internal/cfaccess` (F4), `internal/config` (F5), `internal/dedup` (F5), `internal/metabatch` (F5), `internal/metafetch` (F5), `internal/scanner` (F5) | `.` (R1), `cmd` (R1) |
| `internal/books/contract` | F5 | `internal/books/cutover` (F5), `internal/books/store` (F5) | `internal/books` (F5), `internal/books/store/storetest` (F5), `internal/books/writer` (F5), `internal/dedup` (F5), `internal/itunes/service` (F5), `internal/maintenance/jobs` (F5), `internal/metabatch` (F5), `internal/organizer` (F5), `internal/plugins/maintenance` (F5), `internal/server` (F5) (+1) |
| `internal/books/cutover` | F3 | `internal/books/store` (F3), `internal/config` (F3), `internal/logger` (F5), `internal/metrics` (F5), `internal/operations/state` (F5) | `internal/books` (F3), `internal/books/contract` (F5), `internal/books/store/storetest` (F5), `internal/books/writer` (F5), `internal/diagnostics` (F5), `internal/maintenance/jobs` (F5), `internal/metabatch` (F5), `internal/plugins/dedup` (F5), `internal/plugins/maintenance` (F5), `internal/plugins/metafetch` (F5) (+12) |
| `internal/cfaccess` | F4 | `internal/httputil` (F4), `internal/logger` (F4) | `internal/books` (F4), `internal/server` (F4), `internal/server/absauth` (F4), `internal/server/handlers/abs` (F4), `internal/server/handlers/repairs` (F4) |
| `internal/server/legacyauth` | F4 |  | `internal/server` (F4), `internal/server/handlers` (F4), `internal/server/handlers/abs` (F4), `internal/server/handlers/audiobooks` (F4) |

**Key names and op IDs (check (h), MOD5-3, MOD5-4, MOD6-10; in CI, 03's `TestCoreHoldsNoAppKeyNames`).** Files that land in core, or whose core part is
re-implemented (the two `internal/scheduler` rows and the registry engine's: those files never land, and their
rows describe the cut), and hold an ABO key name or op ID under §2.1 (h)'s rule — 10 rows — with the
hits counted by kind and the cut part that takes each one out of core (MOD7-3):

| file (core-bound or re-implemented) | ABO key names / op IDs found | where they go |
|---|---|---|
| `internal/auth/permissions.go` | 1 op IDs: `library.organize` | the 13 HEAD atom constants are ABO data: books' atom → scope table maps them onto native scopes (05-rbac §6); core `rbac` declares no ABO atom (the literal matches an op ID of the same name) |
| `internal/config/log_retention.go` | 1 setting/preference keys: `log_retention_zero_migrated` | `log_retention_zero_migrated`: the one-shot blob migration marker (`log_retention.go:71`) stays in `internal/config` with `migrateLogRetentionBlob` (books' blob codec); only the retention section and its resolver move to `log` |
| `internal/operations/registry/pause_gate.go` | 1 setting/preference keys: `registry.operations_paused` | `registry.operations_paused`: the marker key and its JSON struct are a HEAD format that cutover step M4 reads once; the gate moves to the executor (MOD5-4) |
| `internal/operations/registry/scan_standdown.go` | 1 setting/preference keys: `registry.scan_standdown`; 1 op IDs: `library.scan` | the stand-down marker (the `registry.scan_standdown` setting row and its JSON shape) is a HEAD format that cutover step M4 decodes into `tasks.Importer` (core's `StandDownStore` is native over `c/tasks/`), so HEAD's restart behaviour is kept; the `library.scan` kind is books data passed to the executor's stand-down mechanism (MOD5-4, MOD6-4) |
| `internal/scheduler/interval_clock.go` | 1 setting/preference keys: `scheduled_task_last_tick.` | `scheduled_task_last_tick.`: the clock key prefix and RFC3339 codec are HEAD's clock format, which cutover step M4 reads once; the tick logic is re-implemented in core (MOD5-4) |
| `internal/scheduler/maintenance.go` | 1 setting/preference keys: `maintenance_window_last_run`; 1 listed literals: `legacy_op_id`; 37 op IDs: `acoustid.backfill`, `acoustid.lookup-online`, `catalog.harvest-authors`, `collections.refresh-relative-dynamic`, `dedup.author-scan`, `dedup.label-refinement` (+31) | `taskV2DefIDs` (38 task names to ABO def IDs, `maintenance.go:154-175`) is books data the cutover reads; the setting key `maintenance_window_last_run` (`maintenance.go:53,71`, a `YYYY-MM-DD` string) is HEAD's clock of the maintenance-window kind, which cutover step M4 reads once with HEAD's date codec, so the first R2 boot never runs the window twice in a day (R7-8, MOD7-3); `MaintenanceWindowOpParams` (`:284-287`), whose struct tag `json:"legacy_op_id"` (`:285`) carries the v1 bridge param `legacy_op_id`, stays in ABO's `internal/scheduler` until F5 deletes it (MOD7-2, MOD8-4); the maintenance-window logic is re-implemented in core (MOD5-4) |
| `internal/server/bootstrap.go` | 4 setting/preference keys: `bootstrap_token_expires_at`, `bootstrap_token_hash`, `startup_readonly_key_expires_at`, `startup_readonly_key_id` | the setting rows `bootstrap_token_hash`, `bootstrap_token_expires_at`, `startup_readonly_key_id` and `startup_readonly_key_expires_at` are HEAD's bootstrap rows: from F4 core `auth`'s `BootWrites` keeps its own state in `c/auth/`, and step M3 deletes HEAD's rows (D12 (b)); core never reads them (R4-1) |
| `internal/server/invite_rate_limit.go` | 1 family keys: `invite:global` | allowlist: `invite:global` is an in-memory limiter bucket name (`invite_rate_limit.go:294-295,492`), not a store key (reviewed allowlist, MOD5-3) |
| `internal/server/server_middleware.go` | 1 setting/preference keys: `dedup_dismissed_groups` | `dedup_dismissed_groups`: `loadDismissedDedupGroups` (`server_middleware.go:231`) is books dedup code and stays in `internal/server`; only the perimeter middleware moves to `httpapi` |
| `internal/server/handlers/operations/pause.go` | 9 op IDs: `acoustid.backfill`, `acoustid.lsh-backfill`, `acoustid.window-backfill`, `dedup.full-scan`, `maintenance.author-path-link`, `maintenance.duration-backfill` (+3) | `pausableDefs` (`pause.go:68`) is books data: each books definition declares whether it is pausable, so the task API holds no ABO kind name |

## 9. `shared/` candidates (none promoted now)

| package | why it might be shared | trigger for promotion to `mediacore/shared/...` |
|---|---|---|
| `internal/titleutil` | title normalization | a TV/Movies release-name parser needs the same rules (03 §3.4 example) |
| `internal/personname` | "is this a human name" | TV/Movies cast/crew credit parsing |
| `internal/seqnum`, `internal/trackseq` | sequence/volume numbers in names | episode numbering reuses it rather than a dedicated S/E parser |
| `internal/querygrammar` | the search-bar value syntax | a second app adopts the same search bar |
| `internal/matcher` | fuzzy title matching | a second app's metadata matcher |
| `internal/cache`, `internal/flight`, `internal/keyedmutex` | domain-free primitives | a core package needs one (then core-internal, not shared, since they are not domain logic). **Fired for `keyedmutex` at F3** (§8.1): `core/internal/keyedmutex` is a core-internal **L0** leaf copy (standard library only) for `config`, `integrations/download` and `library` (05-config, 05-download, 05-library cite it; 03 §3.3 row); ABO keeps its own package (COR2-16) |

## 10. Largest contributions to core

Two tables (MOD8-7): files that **move** (verbatim apart from §2.2's mechanical rewrites) and parts that are
**re-implemented** (§8.6: rewritten in core, so no file lands; their ABO source LOC is shown for scale only).
§1's figure reports both, separately: 18261 moved and 43177 re-implemented. Partial-file cuts
(`internal/database/pebble_store.go`, `store.go`, `migrations.go`, `internal/config/config.go`, `persistence.go`,
`update_service.go`, `internal/ai/openai_parser.go`, `ai_metrics.go`, `telemetry.go`, `internal/aidispatch/capabilities.go`,
`internal/metrics/metrics.go`) are counted on the ABO side, so the moved figure is a lower bound.

**Moved files** (verbatim apart from §2.2's mechanical rewrites): **18261** LOC in 43 parts; the top 30:

| package (current) | core destination | LOC |
|---|---|---|
| `pkg/ops` | `mediacore/tasks` | 2925 |
| `internal/backup` | `mediacore/backup` | 1582 |
| `internal/aidispatch` | `mediacore/integrations/ai` | 1422 |
| `internal/fileops` | `mediacore/library` | 1122 |
| `tools/cmd/oplint` | `mediacore/archtest/tasklint` | 998 |
| `internal/server/absauth` | `mediacore/auth` | 853 |
| `internal/oauth` | `mediacore/auth/oidc` | 695 |
| `internal/diag/flight` | `mediacore/health` | 659 |
| `internal/ai/aijobs` | `mediacore/integrations/ai` | 575 |
| `internal/logging` | `mediacore/log` | 529 |
| `internal/pathutil` | `mediacore/library` | 514 |
| `internal/deluge` | `mediacore/integrations/download/deluge` | 506 |
| `internal/watcher` | `mediacore/library` | 457 |
| `internal/realtime` | `mediacore/realtime` | 426 |
| `internal/metadata/providerhttp` | `mediacore/integrations/providerhttp` | 417 |
| `internal/mail` | `mediacore/integrations/notify/mail` | 364 |
| `internal/compactprogress` | `mediacore/storage` | 359 |
| `internal/updater` | `mediacore/updater` | 335 |
| `internal/auth` | `mediacore/rbac` | 300 |
| `internal/security/safehttp` | `mediacore/integrations/providerhttp` | 267 |
| `internal/security/pathvalidation` | `mediacore/library` | 255 |
| `internal/walkresume` | `mediacore/tasks` | 242 |
| `internal/httputil` | `mediacore/httpapi` | 222 |
| `internal/libroot` | `mediacore/library` | 215 |
| `internal/startup` | `mediacore/kernel` | 211 |
| `internal/operations` | `mediacore/tasks` | 210 |
| `internal/auth` | `mediacore/auth` | 202 |
| `internal/plugins/webhook` | `mediacore/integrations/notify/webhook` | 162 |
| `internal/sdnotify` | `mediacore/internal/sdnotify` | 149 |
| `internal/server/absauth` | `mediacore/audit` | 137 |

**Re-implemented parts** (§8.6; rewritten in core, so the ABO source LOC is shown for scale only and no file lands): **43177** LOC in 29 parts:

| package (current) | core destination | LOC |
|---|---|---|
| `internal/operations/registry` | `mediacore/internal/executor` | 14011 |
| `pkg/ops/opstest` | `mediacore/tasks/taskstest` | 3443 |
| `internal/server` | `mediacore/auth` | 3184 |
| `internal/server/handlers/opsv3` | `mediacore/internal/taskhttp` | 2929 |
| `internal/server/handlers` | `mediacore/internal/authhttp` | 2535 |
| `internal/database` | `mediacore/storage` | 2292 |
| `internal/scheduler` | `mediacore/internal/scheduler` | 2268 |
| `internal/ai` | `mediacore/integrations/ai/openai` | 1899 |
| `internal/config` | `mediacore/config` | 1575 |
| `internal/metadata` | `mediacore/integrations/providerhttp` | 997 |
| `internal/server` | `mediacore/storage/index` | 997 |
| `internal/server/middleware` | `mediacore/httpapi` | 873 |
| `internal/telemetry` | `mediacore/metrics` | 710 |
| `internal/server/middleware` | `mediacore/auth` | 684 |
| `internal/sysinfo` | `mediacore/health` | 665 |
| `internal/config` | `mediacore/integrations/ai` | 554 |
| `internal/server/handlers` | `mediacore/internal/taskhttp` | 543 |
| `internal/server` | `mediacore/httpapi` | 535 |
| `internal/opsmetrics` | `mediacore/internal/executor` | 525 |
| `internal/server` | `mediacore/ui` | 397 |
| `internal/config` | `mediacore/auth` | 324 |
| `internal/search` | `mediacore/storage/index` | 316 |
| `internal/metrics` | `mediacore/internal/pebble` | 251 |
| `internal/server/handlers/operations` | `mediacore/internal/taskhttp` | 170 |
| `internal/operations/childop` | `mediacore/tasks` | 158 |
| `internal/server/middleware` | `mediacore/httpapi` | 119 |
| `internal/config` | `mediacore/log` | 104 |
| `internal/server` | `mediacore/updater` | 64 |
| `internal/server` | `mediacore/health` | 55 |

## 11. Alphabetical index

Every package of `go list ./...` exactly once, linking to its row in §3.

| package | bucket | target |
|---|---|---|
| [`.`](#p-root) | abo | . |
| [`cmd`](#p-cmd) | abo | cmd |
| [`cmd/itl-audit-encoding`](#p-cmd-itl-audit-encoding) | abo | cmd/itl-audit-encoding |
| [`cmd/itl-check`](#p-cmd-itl-check) | abo | cmd/itl-check |
| [`cmd/itl-diff`](#p-cmd-itl-diff) | abo | cmd/itl-diff |
| [`cmd/mtls-bridge`](#p-cmd-mtls-bridge) | abo | cmd/mtls-bridge |
| [`cmd/pebble-inject-skip`](#p-cmd-pebble-inject-skip) | abo | cmd/pebble-inject-skip |
| [`cmd/pid-census`](#p-cmd-pid-census) | abo | cmd/pid-census |
| [`cmd/verify-suite`](#p-cmd-verify-suite) | abo | cmd/verify-suite |
| [`internal/acoustid`](#p-internal-acoustid) | abo | internal/acoustid |
| [`internal/activity`](#p-internal-activity) | abo | internal/activity |
| [`internal/ai`](#p-internal-ai) | abo | internal/ai |
| [`internal/ai/aijobs`](#p-internal-ai-aijobs) | merge | mediacore/integrations/ai |
| [`internal/ai/mocks`](#p-internal-ai-mocks) | abo | internal/ai/mocks |
| [`internal/ai/resultjournal`](#p-internal-ai-resultjournal) | abo | internal/ai/resultjournal |
| [`internal/aidispatch`](#p-internal-aidispatch) | core-public | mediacore/integrations/ai |
| [`internal/aiscan`](#p-internal-aiscan) | abo | internal/aiscan |
| [`internal/appdirs`](#p-internal-appdirs) | abo | internal/appdirs |
| [`internal/applycap`](#p-internal-applycap) | abo | internal/applycap |
| [`internal/applygate`](#p-internal-applygate) | abo | internal/applygate |
| [`internal/arch`](#p-internal-arch) | abo | internal/arch |
| [`internal/audio`](#p-internal-audio) | abo | internal/audio |
| [`internal/audiobooks`](#p-internal-audiobooks) | abo | internal/audiobooks |
| [`internal/audioext`](#p-internal-audioext) | abo | internal/audioext |
| [`internal/audioutil`](#p-internal-audioutil) | abo | internal/audioutil |
| [`internal/auth`](#p-internal-auth) | core-public | mediacore/auth |
| [`internal/authorcredit`](#p-internal-authorcredit) | abo | internal/authorcredit |
| [`internal/authority`](#p-internal-authority) | abo | internal/authority |
| [`internal/authority/authoritybuild`](#p-internal-authority-authoritybuild) | abo | internal/authority/authoritybuild |
| [`internal/authorjunk`](#p-internal-authorjunk) | abo | internal/authorjunk |
| [`internal/authormerge`](#p-internal-authormerge) | abo | internal/authormerge |
| [`internal/authorname`](#p-internal-authorname) | abo | internal/authorname |
| [`internal/backup`](#p-internal-backup) | core-public | mediacore/backup |
| [`internal/batch`](#p-internal-batch) | abo | internal/batch |
| [`internal/boilerplate`](#p-internal-boilerplate) | abo | internal/boilerplate |
| [`internal/bookfileaudio`](#p-internal-bookfileaudio) | abo | internal/bookfileaudio |
| [`internal/bookfiles`](#p-internal-bookfiles) | delete | - |
| [`internal/boundedcall`](#p-internal-boundedcall) | merge | mediacore/library |
| [`internal/cache`](#p-internal-cache) | abo | internal/cache |
| [`internal/catalog`](#p-internal-catalog) | abo | internal/catalog |
| [`internal/cfaccesspolicy`](#p-internal-cfaccesspolicy) | abo | internal/cfaccesspolicy |
| [`internal/cfservicetoken`](#p-internal-cfservicetoken) | abo | internal/cfservicetoken |
| [`internal/chaptershape`](#p-internal-chaptershape) | abo | internal/chaptershape |
| [`internal/compactprogress`](#p-internal-compactprogress) | merge | mediacore/storage |
| [`internal/config`](#p-internal-config) | abo | internal/config |
| [`internal/covers`](#p-internal-covers) | abo | internal/covers |
| [`internal/covertext`](#p-internal-covertext) | abo | internal/covertext |
| [`internal/database`](#p-internal-database) | abo | internal/books/store |
| [`internal/database/aggtest`](#p-internal-database-aggtest) | abo | internal/books/store/aggtest |
| [`internal/database/dbtest`](#p-internal-database-dbtest) | abo | internal/books/store/storetest |
| [`internal/database/mocks`](#p-internal-database-mocks) | delete | - |
| [`internal/decisions`](#p-internal-decisions) | abo | internal/decisions |
| [`internal/dedup`](#p-internal-dedup) | abo | internal/dedup |
| [`internal/dedup/dataset`](#p-internal-dedup-dataset) | abo | internal/dedup/dataset |
| [`internal/dedup/unified`](#p-internal-dedup-unified) | abo | internal/dedup/unified |
| [`internal/deluge`](#p-internal-deluge) | abo | internal/deluge |
| [`internal/diag/flight`](#p-internal-diag-flight) | merge | mediacore/health |
| [`internal/diagnosis`](#p-internal-diagnosis) | abo | internal/diagnosis |
| [`internal/diagnostics`](#p-internal-diagnostics) | abo | internal/diagnostics |
| [`internal/diskstats`](#p-internal-diskstats) | merge | mediacore/library |
| [`internal/errhandling`](#p-internal-errhandling) | abo | internal/errhandling |
| [`internal/filehash`](#p-internal-filehash) | abo | internal/filehash |
| [`internal/fileops`](#p-internal-fileops) | abo | internal/fileops |
| [`internal/fingerprint`](#p-internal-fingerprint) | abo | internal/fingerprint |
| [`internal/fingerprint/workerapi`](#p-internal-fingerprint-workerapi) | abo | internal/fingerprint/workerapi |
| [`internal/fingerprint/workerclient`](#p-internal-fingerprint-workerclient) | abo | internal/fingerprint/workerclient |
| [`internal/flight`](#p-internal-flight) | abo | internal/flight |
| [`internal/foldercover`](#p-internal-foldercover) | abo | internal/foldercover |
| [`internal/foldernames`](#p-internal-foldernames) | abo | internal/foldernames |
| [`internal/franchise`](#p-internal-franchise) | abo | internal/franchise |
| [`internal/httputil`](#p-internal-httputil) | abo | internal/httputil |
| [`internal/identification`](#p-internal-identification) | abo | internal/identification |
| [`internal/importer`](#p-internal-importer) | abo | internal/importer |
| [`internal/itunes`](#p-internal-itunes) | abo | internal/itunes |
| [`internal/itunes/service`](#p-internal-itunes-service) | abo | internal/itunes/service |
| [`internal/itunesguard`](#p-internal-itunesguard) | abo | internal/itunesguard |
| [`internal/keyedmutex`](#p-internal-keyedmutex) | abo | internal/keyedmutex |
| [`internal/libroot`](#p-internal-libroot) | merge | mediacore/library |
| [`internal/lifecycle`](#p-internal-lifecycle) | core-public | mediacore/lifecycle |
| [`internal/linkintegrity`](#p-internal-linkintegrity) | abo | internal/linkintegrity |
| [`internal/logger`](#p-internal-logger) | abo | internal/logger |
| [`internal/logger/logtest`](#p-internal-logger-logtest) | abo | internal/logger/logtest |
| [`internal/logger/mocks`](#p-internal-logger-mocks) | delete | - |
| [`internal/logging`](#p-internal-logging) | core-public | mediacore/log |
| [`internal/mail`](#p-internal-mail) | core-public | mediacore/integrations/notify/mail |
| [`internal/mail/mailtest`](#p-internal-mail-mailtest) | merge | mediacore/coretest |
| [`internal/maintenance`](#p-internal-maintenance) | delete | - |
| [`internal/maintenance/jobs`](#p-internal-maintenance-jobs) | abo | internal/maintenance/jobs |
| [`internal/masterlist`](#p-internal-masterlist) | abo | internal/masterlist |
| [`internal/matcher`](#p-internal-matcher) | abo | internal/matcher |
| [`internal/mediainfo`](#p-internal-mediainfo) | abo | internal/mediainfo |
| [`internal/merge`](#p-internal-merge) | abo | internal/merge |
| [`internal/metabatch`](#p-internal-metabatch) | abo | internal/metabatch |
| [`internal/metadata`](#p-internal-metadata) | abo | internal/metadata |
| [`internal/metadata/dailyquota`](#p-internal-metadata-dailyquota) | abo | internal/metadata/dailyquota |
| [`internal/metadata/mocks`](#p-internal-metadata-mocks) | abo | internal/metadata/mocks |
| [`internal/metadata/providerhttp`](#p-internal-metadata-providerhttp) | core-public | mediacore/integrations/providerhttp |
| [`internal/metafetch`](#p-internal-metafetch) | abo | internal/metafetch |
| [`internal/metastate`](#p-internal-metastate) | abo | internal/metastate |
| [`internal/metrics`](#p-internal-metrics) | abo | internal/metrics |
| [`internal/models`](#p-internal-models) | merge | internal/dedup/unified |
| [`internal/mtls`](#p-internal-mtls) | abo | internal/mtls |
| [`internal/oauth`](#p-internal-oauth) | core-public | mediacore/auth/oidc |
| [`internal/openlibrary`](#p-internal-openlibrary) | abo | internal/openlibrary |
| [`internal/operations`](#p-internal-operations) | merge | mediacore/tasks |
| [`internal/operations/childop`](#p-internal-operations-childop) | merge | mediacore/tasks |
| [`internal/operations/freshness`](#p-internal-operations-freshness) | merge | mediacore/tasks |
| [`internal/operations/opmode`](#p-internal-operations-opmode) | merge | mediacore/tasks |
| [`internal/operations/opswriter`](#p-internal-operations-opswriter) | abo | internal/books/writer |
| [`internal/operations/registry`](#p-internal-operations-registry) | core-internal | mediacore/internal/executor |
| [`internal/operations/state`](#p-internal-operations-state) | abo | internal/operations/state |
| [`internal/opscatalog`](#p-internal-opscatalog) | delete | - |
| [`internal/opsmetrics`](#p-internal-opsmetrics) | core-internal | mediacore/internal/executor |
| [`internal/organizer`](#p-internal-organizer) | abo | internal/organizer |
| [`internal/pathutil`](#p-internal-pathutil) | abo | internal/pathutil |
| [`internal/personname`](#p-internal-personname) | abo | internal/personname |
| [`internal/playlist`](#p-internal-playlist) | abo | internal/playlist |
| [`internal/plugin`](#p-internal-plugin) | delete | - |
| [`internal/plugins`](#p-internal-plugins) | delete | - |
| [`internal/plugins/acoustid`](#p-internal-plugins-acoustid) | abo | internal/plugins/acoustid |
| [`internal/plugins/dedup`](#p-internal-plugins-dedup) | abo | internal/plugins/dedup |
| [`internal/plugins/deluge`](#p-internal-plugins-deluge) | abo | internal/plugins/deluge |
| [`internal/plugins/itunes`](#p-internal-plugins-itunes) | abo | internal/plugins/itunes |
| [`internal/plugins/maintenance`](#p-internal-plugins-maintenance) | abo | internal/plugins/maintenance |
| [`internal/plugins/metafetch`](#p-internal-plugins-metafetch) | abo | internal/plugins/metafetch |
| [`internal/plugins/webhook`](#p-internal-plugins-webhook) | core-public | mediacore/integrations/notify/webhook |
| [`internal/policy`](#p-internal-policy) | abo | internal/policy |
| [`internal/quarantine`](#p-internal-quarantine) | abo | internal/quarantine |
| [`internal/querygrammar`](#p-internal-querygrammar) | abo | internal/querygrammar |
| [`internal/readstatus`](#p-internal-readstatus) | abo | internal/readstatus |
| [`internal/realtime`](#p-internal-realtime) | core-public | mediacore/realtime |
| [`internal/reconcile`](#p-internal-reconcile) | abo | internal/reconcile |
| [`internal/remux`](#p-internal-remux) | abo | internal/remux |
| [`internal/repairs`](#p-internal-repairs) | abo | internal/repairs |
| [`internal/scanlock`](#p-internal-scanlock) | abo | internal/scanlock |
| [`internal/scanner`](#p-internal-scanner) | abo | internal/scanner |
| [`internal/scheduler`](#p-internal-scheduler) | core-internal | mediacore/internal/scheduler |
| [`internal/sdnotify`](#p-internal-sdnotify) | core-internal | mediacore/internal/sdnotify |
| [`internal/search`](#p-internal-search) | abo | internal/search |
| [`internal/searchcache`](#p-internal-searchcache) | abo | internal/searchcache |
| [`internal/security/pathvalidation`](#p-internal-security-pathvalidation) | merge | mediacore/library |
| [`internal/security/safehttp`](#p-internal-security-safehttp) | merge | mediacore/integrations/providerhttp |
| [`internal/security/safepath`](#p-internal-security-safepath) | merge | mediacore/library |
| [`internal/seqnum`](#p-internal-seqnum) | abo | internal/seqnum |
| [`internal/server`](#p-internal-server) | abo | internal/server |
| [`internal/server/absauth`](#p-internal-server-absauth) | abo | internal/server/absauth |
| [`internal/server/handlers`](#p-internal-server-handlers) | abo | internal/server/handlers |
| [`internal/server/handlers/abs`](#p-internal-server-handlers-abs) | abo | internal/server/handlers/abs |
| [`internal/server/handlers/admindebug`](#p-internal-server-handlers-admindebug) | abo | internal/server/handlers/admindebug |
| [`internal/server/handlers/aibackends`](#p-internal-server-handlers-aibackends) | abo | internal/server/handlers/aibackends |
| [`internal/server/handlers/audiobooks`](#p-internal-server-handlers-audiobooks) | abo | internal/server/handlers/audiobooks |
| [`internal/server/handlers/audiobooks/mocks`](#p-internal-server-handlers-audiobooks-mocks) | abo | internal/server/handlers/audiobooks/mocks |
| [`internal/server/handlers/dedup`](#p-internal-server-handlers-dedup) | abo | internal/server/handlers/dedup |
| [`internal/server/handlers/dedup/mocks`](#p-internal-server-handlers-dedup-mocks) | abo | internal/server/handlers/dedup/mocks |
| [`internal/server/handlers/duplicates`](#p-internal-server-handlers-duplicates) | abo | internal/server/handlers/duplicates |
| [`internal/server/handlers/duplicates/mocks`](#p-internal-server-handlers-duplicates-mocks) | abo | internal/server/handlers/duplicates/mocks |
| [`internal/server/handlers/entities`](#p-internal-server-handlers-entities) | abo | internal/server/handlers/entities |
| [`internal/server/handlers/entities/mocks`](#p-internal-server-handlers-entities-mocks) | abo | internal/server/handlers/entities/mocks |
| [`internal/server/handlers/fpworker`](#p-internal-server-handlers-fpworker) | abo | internal/server/handlers/fpworker |
| [`internal/server/handlers/metadata`](#p-internal-server-handlers-metadata) | abo | internal/server/handlers/metadata |
| [`internal/server/handlers/metadata/mocks`](#p-internal-server-handlers-metadata-mocks) | abo | internal/server/handlers/metadata/mocks |
| [`internal/server/handlers/mocks`](#p-internal-server-handlers-mocks) | abo | internal/server/handlers/mocks |
| [`internal/server/handlers/opaccess`](#p-internal-server-handlers-opaccess) | abo | internal/server/handlers/opaccess |
| [`internal/server/handlers/operations`](#p-internal-server-handlers-operations) | abo | internal/server/handlers/operations |
| [`internal/server/handlers/operations/mocks`](#p-internal-server-handlers-operations-mocks) | abo | internal/server/handlers/operations/mocks |
| [`internal/server/handlers/opsv3`](#p-internal-server-handlers-opsv3) | core-internal | mediacore/internal/taskhttp |
| [`internal/server/handlers/repairs`](#p-internal-server-handlers-repairs) | abo | internal/server/handlers/repairs |
| [`internal/server/handlers/review`](#p-internal-server-handlers-review) | abo | internal/server/handlers/review |
| [`internal/server/handlers/system`](#p-internal-server-handlers-system) | abo | internal/server/handlers/system |
| [`internal/server/handlers/system/mocks`](#p-internal-server-handlers-system-mocks) | abo | internal/server/handlers/system/mocks |
| [`internal/server/handlers/tools`](#p-internal-server-handlers-tools) | abo | internal/server/handlers/tools |
| [`internal/server/middleware`](#p-internal-server-middleware) | merge | mediacore/auth + mediacore/httpapi |
| [`internal/serverdecode`](#p-internal-serverdecode) | abo | internal/serverdecode |
| [`internal/serviceregistry`](#p-internal-serviceregistry) | delete | - |
| [`internal/startup`](#p-internal-startup) | merge | mediacore/kernel |
| [`internal/sweep`](#p-internal-sweep) | abo | internal/sweep |
| [`internal/syncapi/conformance`](#p-internal-syncapi-conformance) | abo | internal/syncapi/conformance |
| [`internal/syncapi/progress`](#p-internal-syncapi-progress) | abo | internal/syncapi/progress |
| [`internal/sysinfo`](#p-internal-sysinfo) | abo | internal/sysinfo |
| [`internal/tagger`](#p-internal-tagger) | abo | internal/tagger |
| [`internal/telemetry`](#p-internal-telemetry) | abo | internal/telemetry |
| [`internal/telemetry/contract`](#p-internal-telemetry-contract) | abo | internal/telemetrypolicy |
| [`internal/testutil`](#p-internal-testutil) | abo | internal/testutil |
| [`internal/testutil/rapidgen`](#p-internal-testutil-rapidgen) | abo | internal/testutil/rapidgen |
| [`internal/titleutil`](#p-internal-titleutil) | abo | internal/titleutil |
| [`internal/tools`](#p-internal-tools) | abo | internal/tools |
| [`internal/trackseq`](#p-internal-trackseq) | abo | internal/trackseq |
| [`internal/transcode`](#p-internal-transcode) | abo | internal/transcode |
| [`internal/transcribe`](#p-internal-transcribe) | abo | internal/transcribe |
| [`internal/undo`](#p-internal-undo) | abo | internal/undo |
| [`internal/updater`](#p-internal-updater) | core-public | mediacore/updater |
| [`internal/usermerge`](#p-internal-usermerge) | abo | internal/usermerge |
| [`internal/util`](#p-internal-util) | abo | internal/util |
| [`internal/versionprimary`](#p-internal-versionprimary) | abo | internal/versionprimary |
| [`internal/versionprimary/vptest`](#p-internal-versionprimary-vptest) | abo | internal/versionprimary/vptest |
| [`internal/versions`](#p-internal-versions) | abo | internal/versions |
| [`internal/walkresume`](#p-internal-walkresume) | merge | mediacore/tasks |
| [`internal/watcher`](#p-internal-watcher) | merge | mediacore/library |
| [`internal/wirecontract`](#p-internal-wirecontract) | abo | internal/wirecontract |
| [`internal/work`](#p-internal-work) | abo | internal/work |
| [`internal/writeback`](#p-internal-writeback) | delete | - |
| [`pkg/ops`](#p-pkg-ops) | core-public | mediacore/tasks |
| [`pkg/ops/opstest`](#p-pkg-ops-opstest) | core-public | mediacore/tasks/taskstest |
| [`pkg/plugin/sdk`](#p-pkg-plugin-sdk) | delete | - |
| [`tools/cmd/dedup-dataset-audit`](#p-tools-cmd-dedup-dataset-audit) | abo | tools/cmd/dedup-dataset-audit |
| [`tools/cmd/itunes-group-preview`](#p-tools-cmd-itunes-group-preview) | abo | tools/cmd/itunes-group-preview |
| [`tools/cmd/merge-split-books`](#p-tools-cmd-merge-split-books) | abo | tools/cmd/merge-split-books |
| [`tools/cmd/oplint`](#p-tools-cmd-oplint) | merge | mediacore/archtest/tasklint |
| [`tools/cmd/opsgen`](#p-tools-cmd-opsgen) | abo | tools/cmd/opsgen |
| [`tools/cmd/orphan-nonprimary-census`](#p-tools-cmd-orphan-nonprimary-census) | abo | tools/cmd/orphan-nonprimary-census |
| [`tools/cmd/reconcile-book-counts`](#p-tools-cmd-reconcile-book-counts) | abo | tools/cmd/reconcile-book-counts |
| [`tools/cmd/reconcile-paths`](#p-tools-cmd-reconcile-paths) | abo | tools/cmd/reconcile-paths |
| [`tools/cmd/sdkguard`](#p-tools-cmd-sdkguard) | delete | - |

## Objections to 03

1. **Public sub-packages not listed in the 03 §3.3 tree** (generated, §2.1 (f)): `auth/authtest` (`StoreSuite` for `auth.Store` (F4; MOD5-6)); `config/configtest` (`OverrideStoreSuite`, conformance suite and fake for `config.OverrideStore`, with `config.Importer`'s cases (F3); test-support like `coretest`, part of the SDK (MOD5-6)); `integrations/ai/aitest` (`JobStoreSuite` for `ai.JobStore` (F7; MOD5-6)); `integrations/ai/openai` (the OpenAI adapter (05-ai)); `integrations/download/deluge` (named in 03's `integrations/download` row text, not as a row); `integrations/integrationstest` (`InstanceStoreSuite` for `integrations.InstanceStore` (F7; MOD5-6)); `integrations/notify/mail` (named in 03's `integrations/notify` row text, not as a row); `integrations/notify/notifytest` (`RuleStoreSuite` for `notify.RuleStore` (F7; MOD5-6)); `integrations/notify/webhook` (named in 03's `integrations/notify` row text, not as a row); `integrations/providerhttp/providerhttptest` (`GateStoreSuite` for `providerhttp.GateStore` (F7; MOD5-6)); `log/logtest` (`JournalStoreSuite` for `log.JournalStore` (F5; MOD5-6)); `rbac/rbactest` (`StoreSuite`, `RowRuleSuite` for `rbac.Store` (F4; MOD5-6)); `tasks/taskstest` (test kit of `tasks` (from `pkg/ops/opstest`)). Each
   is a test kit, an analyzer, an adapter or a leaf of a listed package; no rule is contradicted. The store
   conformance suites (`<pkg>/<pkg>test`, MOD5-6) match 03 §3.3's `<pkg>/<pkg>test` pattern row, which the
   generator cannot read as a path, so they are listed here; each also carries its importer's cases (03 §3.3). The generator reads 03's multi-path rows and "sub-package" mentions as listed (round 7, MOD7-12). Internal packages (`internal/pebble`, `internal/sse`,
   `internal/bleveindex`, `internal/keyedmutex`, `internal/httpmw`, `internal/sdnotify`, …) match 03 §3.3's
   open-ended `internal/...` row under §2.1 (f)'s stated exception (MOD4-16).

No other objection is open. Resolved: the count (49 / 5 / 169, R6-2, PC-20, and the D10–D12 revision's
`internal/books/cutover`), the in-image embed list (MOD6-3), (h)'s match rule (MOD6-6, MOD6-7, MOD7-7),
`internal/storagehttp` (R7-2), the one tag-set list (MOD7-14), the struct-tag clause of (h)'s match rule (MOD8-4),
and the A-4 deletions: §3.5.1 item 4 is the one tag-set list, and the per-stage-point gate, `tools/stagecheck`,
the move rule as a CI rule, `origins.txt` and `archtest.DeferredMembersAbsent` are gone from 03.

## Out-of-scope defects found

- None in code beyond those the raw files already record (raw/06 §1.1 dead `Cancellable`, raw/07
  §1.3 `batchpoller` never built, raw/05 §1 stale SQLite help text). The three dead packages are in
  scope and mapped above.
- `Makefile.local.example:103`'s post-deploy check calls `/api/v1/system/version`, which no route
  serves (`internal/server/wire_system_routes.go:28-48`), so its "deployed version matches" step can
  never pass; release R1 moves it to `/health`'s `version` field (09 "Out-of-scope defects found" item 5, OPS4-14).
- Raw-data corrections: raw/01 classes `internal/server/absauth` audiobook-specific although web
  auth depends on it (§2.3); raw/04 §4 counts 591 consumer interfaces where the re-run finds 587
  (embed resolution); `internal/database/store.go:38` still claims "250" consumer interfaces and
  `internal/database/migrations.go:18` "398 methods" (both stale, raw/04 §7).

## Open items

1. **Mock conversion cost** (§4.6): 114 test files on `internal/database/mocks` plus ~22 on the
   hand-written `MockStore`. Owner decides whether conversion happens in release R1 to F5 or is
   deferred behind the interim `storetest` composite.
2. **`idx:` and `pref:` sub-key ownership** (§4.5): owned by 05-storage §5S.9.1 (DATA-2); this
   file's file cut follows whatever that table states.
3. **`internal/cfaccess` consolidation** (§2.4) assumes core `auth` exposes an authenticator
   extension point for header-asserted identity (05-authn).
4. **`tasks.Writer` generics** (§6): 05-tasks must specify how a module registers typed writers
   (the books `ModifyBook` family) without core naming books types.
5. **Divided files** (§4.5 `pebble_store.go`, `store.go`, `migrations.go`, `internal/operations/state.go`
   (R7-8), and the "file divided" parts of §8.6; `internal/operations/state`'s `v3.go` is no longer divided, R6-2): each needs a mechanical "cut" PR before its move PR so that the
   move is a pure file move; parts that cannot be cut that way are re-implementations (§8.6).

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- MOD-2 — internal/public column re-derived from the import graph; `compactprogress` → `storage`, `oauth` → `auth/oidc`, `lifecycle` → public `lifecycle`; `sdnotify`, `startup`, `scheduler`, executor engine, `opswriter` fence files, `taskhttp`/`authhttp`, `authstore` and the Pebble collector moved only at a named gate stage; auth type files → public `auth`; §8.3 gate table and generator check (d).
- MOD-6 — moves keyed by 09 stages (§8.1); `internal/telemetry/contract` stays in ABO as `internal/telemetrypolicy` with `ratchet_test.go`; one telemetry target `mediacore/metrics`; `lifecycle` resolved to 03's public L0 package.
- MOD-21 — `internal/httputil` `respond.go` (`RespondWithApplyCapExceeded`, ABO `applycap`) stays; `internal/appdirs` stays as the config → `library.AppDirs` adapter.
- Cross-cutting 2 / SIMP-2 — no gin retirement; gin helpers of the permanent legacy mount stay (`httputil`, `gone.go`); per-stage removal of gin copies.
- Cross-cutting 1 / SIMP-1 — promotion rule as non-circular build rule; no deferred-build package in any stage.
- Cross-cutting 7 / DATA-1 — `SettingsStore` and `setting:` stay books; `settings.go` divided; method cut 108/337.
- DATA-2 — family ownership referenced to 05-storage §5S.9.1 instead of restated; `keyfamilies.go` stays ABO; activity store stays books (03 §3.3 `log` row, 05-logging §5).
- OPS-7 (consequence) — task-run store files stay as ABO legacy writers until 05-tasks T8; no `internal/taskstore`.
- SIMP-3 — `internal/logger` and `config.AppConfig` façades kept; removal is an optional ratchet.
- SIMP-13 — books metric names unchanged in the `internal/metrics` and `internal/opsmetrics` rows.
- COR-17 — `entity.Ref` in the `realtime` and `pkg/ops` rows.
- COR-19 — §8 layer list matches 03 R4 v1.3.0.
- MOD-14 (consequence) — root row names the `. -> cmd` positive control.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-5 / MOD2-1 — MOD-2 gate rebuilt on `go list -deps -test -json` and a per-file parse, evaluated at every stage point (both UI-0 positions, T8 earliest) with every ABO user declared (§2.1 (c), §8.3); `sdnotify` moves at S2 with all five importers rewired (`ExtendStart` hook, `lifecycle.NotifySocketEnv`, kernel readiness, `app` fast exit); one generated importer count per package (in/out column, evidence prefix, "N importers" phrases, §8.2, §8.3).
- MOD2-2 — superseded wave table deleted; "earliest stage" total 158; generator check that every §8 core path is in 03 §3.3 or a §3 row.
- MOD2-3 — split check (unexported names, field/method access, foreign-type methods, forbidden edges) with move vs re-implementation parts (§2.2, §8.6); `opswriter` sends only `fence.go` to public `tasks`; `user_merge.go` moves onto an ABO-owned handler type before `user.go` leaves.
- MOD2-4 — no file of `cmd` moves; bench-tagged files listed; `cmd/` out of the history path file.
- MOD2-7 — §8.4 note 3: edges re-evaluated against the mapped core layer (03 §3.5.1), `itunes/service` edges stay.
- MOD2-10 / FE2-9 / COR2-9 — `examples/hello` from S2 (emits `core.json`), growing to S9; G6/G7 proven from S5.
- MOD2-11 — `internal/diag/flight` rewritten onto a `*slog.Logger`; R1 checked per stage (§8.5).
- MOD2-12 — Split cells are not history-path input (§2.2).
- MOD2-15 — empty `core/go.sum` committed at S0 (§8.1).
- MOD2-21 — no "wave" wording left.
- COR2-9 — §8.1 "minimum surface" column; `rbac` leaf types at S2, `events` at S4, revocation-journal types in `auth` at S4 (superseded by the round-2 integration pass: `backup/journal`); kernel without storage until S2b (refined in round 3 by COR3-1: ABO opens the store before `app.New`).
- COR2-16 — `core/internal/keyedmutex` at S3 (§8.1, §9).
- DATA2-6 — `storage/keys` lands at S0 so S0 writes `c/storage/install` and the ledger in core's encoding.
- FE2-4 — `@mediacore/ui-kit` and `@mediacore/shell` created at S9 with shell adoption; UI-0 creates only `@mediacore/api-client` (same stage as 07 §7.19 steps 2 and 4).
- SIMP2-4 — `notify` + `notify/webhook` land at S7.
- DATA2-12, DATA2-29, SIMP2-9 — verified, no change: 04's v1 ops-store statement, census mechanism to `storage`, conformance engine stays in ABO.
- integration pass (round 2, lead): journal types placed in `backup/journal` to avoid an rbac↔auth cycle.

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — no row copies, mirrors or hands over a legacy family: §1 decisions 1 and 10, §4.2, §4.4, §4.5 and §8.4 note 1 restated for legacy-format adapters through books' grants; the 15 run-row store files are the source of core `tasks`' adapter `internal/executor/legacystore` (S5) and are deleted at T8; `internal/authstore`/`internal/rbacstore` are the identity adapters; §8.1 S3/S4/S7 rows.
- R3-3 / MOD3-1 — `internal/startup` stays in ABO until S8 (`Registrar.StartupSteps`); `cmd` declared as its non-test user (`root.go:274`); the gate reads non-test and test imports of every package, `cmd/*` and the root `main` included, declares every user with its kind and fails on a mismatch or a stale declaration (§2.1 (c), (g), §8.3).
- MOD3-2 — the generator is committed at S0 as `scripts/ci/check_stage_visibility.py` and runs in root `make ci` on every PR touching root Go code; stage PRs regenerate §8.2/§8.3 (§2.1).
- MOD3-3 — `internal/telemetry` moves construction only at S1; the global installs stay in an ABO façade until S2 (§3 row, §8.1, §8.6); `internal/logger/logtest` stays in ABO, `coretest` captures through an injected handler.
- MOD3-4 / COR3-3 — `internal/openapi`, `internal/httpmw`, `internal/jsonschemax`, `compat`'s header/counter API and the `auth` leaf types at S2 (import direction `httpapi` → `internal/openapi`); `core.json` at `core/openapi/core.json`; `compat` persistence and ledger at S2b; `internal/uihttp`, `internal/modulehttp` S4; `storage/datamigrate`, `internal/storagemigrate`, `internal/logjournal` S5; `internal/eventhttp` S6 (§8.1).
- MOD3-5 — §8.4 note 3: mapped core layers feed the computed layer; `internal/httputil` → 5 at S2; test-support and `storage/*` → −1, `shared/...` → 3.
- MOD3-6 / MOD3-17 — move PRs: rename-only first commit, `go fix` and R1/R5 rewrites in later commits (§2.2, §8.6).
- MOD3-8 — §8.1 S0: `archtest`'s self-test on a synthetic graph is the core guard's S0 positive control (03 §3.5.1 item 2).
- MOD3-9 — check (f) without the first-segment shortcut; unlisted public paths generated into "Objections to 03".
- MOD3-10 — §8.2 counts per-file users still in ABO at the source's landing stage (regenerated).
- MOD3-11 — gate rows name users by their path at the gate's stage point (`internal/books/store` from S2b).
- MOD3-20 — §8 intro points at R4 (03 §3.5) instead of restating it.
- COR3-1 — `internal/config` loader for core sections at S2 over `app.Options.LegacyConfig`, façade at S3 (§7, §8.6 row); kernel without storage until S2b with ABO opening the store before `app.New` (§8 intro).
- FE3-6 — `tools/cmd/opsgen` retired at S9.
- FE3-8 — `ui.ReservedSegments()` at S2 (§8.1).
- FE3-10 — `csp.go` stays in ABO until S9 (then `ConnectSrcFrom`); `csp_report.go` deleted at S9 after `internal/uihttp`'s route (S4); `static_embed.go` noted (§3 row, §7).
- FE3-16 — `internal/modulehttp` at S4.
- SIMP3-2 — `archtest.DeferredMembersAbsent` from S2 (§8.1).
- SIMP3-10 — `internal/plugin` deleted at S7 (one stage), with a generated deletion gate over its 10 importing packages (§8.3).
- Lead relays — Basic gate (`basicauth.go`) moves at S3 (§7); `internal/database` uses the stateless `app.ExtendStart` at S2 (§8.3); §8.4 note 6 recount (54 leave, 161 remain).

Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- R4-1 / SIMP4-2 / SIMP4-3 / COR4-4 / DATA4-2 — books supplies the legacy store implementations, which stay in ABO: the `internal/database` row keeps the identity store (3,053 LOC), settings cipher, AI job, provider throttle, import-root, dirty-set, guard-projection, version-source and exact-census files as "stays:" parts; the 15 run-row files move unchanged in format to `internal/books/taskstore` (05-tasks 5T.6.2); only 12 storage-engine files move to core; `internal/ai/resultjournal` and `internal/metadata/dailyquota` stay; `apikey_expiry_stamp.go` and `provider_throttle_wire.go` stay in `internal/server`; §1 decisions 1 and 10, §4.2 (97 methods behind core contracts implemented by books), §4.4, §4.5 tables and cut lines (`store.go` row types and `settings.go` stay), §7 config, §8 intro, §8.1 native stores per stage, §8.4 note 1, §8.5; `internal/authstore`, `internal/rbacstore`, `internal/executor/legacystore` and their gate deleted; new generator check (h): no ABO key literal in a core-bound file.
- R4-5 / MOD4-2 — `internal/books` is the composition root only; helpers in `internal/books/contract` (§2.4, split destinations of `pkg/ops`, `pkg/plugin/sdk`, `internal/operations`, registry, `internal/opscatalog`); §8.4 note 3: allowance deleted, `module`/`app` importer assertion.
- R4-4 / MOD4-1 — §2.4: the composition root embeds the freshness-checked copy `internal/books/compat-surfaces.md`.
- R4-6 / COR4-1 — §4.5 and §7: ABO's settings load, encryption init and key-regeneration guard run inside books' legacy config store; §8.3 per-step table.
- MOD4-3 — §8.1 S2 (`InstallProcessDefaults` with meter/tracer/propagator, `app.Options.PromRegisterer`) and S8 (kernel `/metrics`, legacy families onto `Provider.Registerer()`); telemetry row.
- MOD4-4 — `internal/telemetry` stays as books telemetry (attribute vocabulary, views, scope helper, version, config readers); only provider construction is re-implemented in `metrics` at S1; install files deleted at S2; R1 rules for `internal/opsmetrics` and `internal/aidispatch`.
- MOD4-6 — `internal/logger` no longer split; §2.1 (e) covers exported identifiers of a staying part.
- MOD4-7 — §2.1 (c), §8.3: the per-file parse reads `IgnoredGoFiles` (every CI tag and goreleaser `GOOS`), drops `ForTest` variants and `.test` mains; every remaining stage point after `shipped_stage:` is evaluated.
- MOD4-11 / COR4-11 — §8.1 S2: `rbac` leaf list per 05-rbac §3.1 plus `Checker` and `RoleName`; `auth` leaf adds `User`, `UserID`, `UserStatus`, `Assurance`; no `compat` at S2 (it lands whole at S2b).
- MOD4-14 — §2.2: no rename-only commit; `core/scripts/split/origins.txt`; every commit builds.
- MOD4-16 — §2.1 counts regenerated by every stage PR; (f) internal-path exception; (c) three UI-0 positions (third stage order in the gate model); Objection 2 replaced.
- MOD4-17 — §8.3 per-step table for `cmd`'s use of `internal/startup` (last use ends at S2b); gate declaration reworded.
- MOD4-18 — `internal/search` takes an injected `func(step string)`; §8.4 note 3 rule for layers below 6.
- FE4-6 — §8.1 S2: `ui.ModuleManifest`, `UIIDs`, `Redirect` land with `ReservedSegments`.
- FE4-7 — `tools/cmd/opsgen` retires with the last native `ops.ts` importer (mapped at S10), repointed at `tasks`' state constants at S5.
- COR4-8 — §8.1 S5: the readiness core of `health` lands with `tasks`.
- OPS4-5 / DATA4-18 — floor release and stepping-stone release named distinctly (§8 intro, `internal/database` row).
- DATA4-5 — `internal/scheduler`: ABO's scheduler keeps ticking until T7 through `Deps.Tasks` and books' `ClockStore`; one scheduler per kind (05-tasks 5T.6.2).
- DATA4-19 — §4.5 `migrations.go`: `LegacyCap`.
- SIMP4-5 — §8.1 S8: backup encryption lands with the first app or cutover that can activate it.
- SIMP4-12 — §8.1 S7: `integrations` is the one registry for integration instances.
- OPS4-14 — out-of-scope defect: `Makefile.local.example`'s `/api/v1/system/version` check.
- integration pass (round 4, lead): store interface names aligned (`rbac.Store`, `log.JournalStore`, `legacy.RootSource`, `tasks.Stores`, `module.Manifest.LegacyStores`); package-count objection withdrawn.

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`):

- R5-2 / MOD5-1 — taskstore → store, never the reverse: `pebble_store_plan_legacy.go`, the bodies of steps 62/65 and the ops part of `InitializeStore` (`Recover`) move with taskstore; the legacy step list calls them through funcs the composition root wires; capability assertions become direct calls (`TestLegacyStepPreconditionsResolve`); residue exports and fields moved onto the taskstore type listed; §8.6 row staged S5 (T0+T1); new generator gate E (typed reference scan, declared cuts, stale check) and a package-cycle check with a negative control (§2.4, §4.2, §4.5, §8.6).
- R5-2 / MOD5-2 — new path `internal/books/configstore` for the legacy `config.OverrideStore`, `integrations.InstanceStore` and the books-root half of `legacy.RootSource` (§1, §2.4, §4.5, §7, §8.1, §8.4); count 50 leave / 5 new / 168 remain (Objection 2).
- Gate E finds three pre-existing cycle edges of v1.4's mapping (consequence of R5-2): `internal/server`'s CF Access route files stay in `internal/server`; `CFAssertionVerifier` moves into `internal/cfaccess`; `internal/config` takes the owner proof as an injected func; the extra-op wiring moves into `internal/books` (§2.4, §8.6).
- MOD5-3 — (h) reads the claim generator's output (reserved `c/`, `m/` excluded) at its current path, matches a family prefix only when not followed by a space, and uses a reviewed allowlist file (§2.1 (h)).
- MOD5-4 — (h) also reads ABO's setting and preference keys (typed constant folding at every call site) and the op IDs of `op_ids.golden`; ten files carry hits ("nine" until round 6; MOD6-10), each with its cut part (§8.6 table); the core scheduler is a re-implementation, the clock codec and pause marker go to taskstore's `ClockStore`/`PauseStore`, `taskV2DefIDs` and `pausableDefs` are books data (§2.4, §3 rows, §7, §8.6).
- MOD5-6 — the ten store conformance suites land with their stores in §8.1 and are listed under "Objections to 03" item 1.
- MOD5-9 — `internal/books/contract` is created at S5 (Objection 3); every new path enters `layerOf` in the PR that creates it (§2.4, §8.1 S2).
- MOD5-11 — `books.Module()` everywhere; the guard assertion reads non-test imports; the G6 test lives in `internal/books` (§2.4, §8.4 note 3).
- MOD5-14 — origins lines use 12's `old-path ==> new-path` separator (§2.2).
- MOD5-16 — one tag-set rule: the union of `go list` per (`GOOS`, tag set) CI and goreleaser build; `IgnoredGoFiles` parsing dropped (18 files fewer) (§2.1 (c)).
- MOD5-17 — `internal/books/rbacreach` noted as a data directory, not a package (§2.4).
- COR5-10 / SIMP5-3 — no "result-journal store": `internal/ai/resultjournal` is plain books code over `aijournal:` (§1, §2.3, §3 row, §8.1 S7).
- COR5-13 — legacy store packages named once: `internal/books/store`, `internal/books/taskstore`, `internal/books/configstore`, `internal/metadata/dailyquota` (§1, §2.4, §8 intro).
- COR5-14 — `config.OverrideStore`'s read half at S2, native implementation and `LegacyStores.Config` at S2b, write methods at S3 (§7, §8.1).
- R5-1 — the four HEAD-encrypted secret rows stay in place and are never moved; the legacy config store keeps HEAD's precedence for legacy keys (§4.5 `settings.go`, §7).
- SIMP5-8 — the store-interface list is cited from 03 §3.3 instead of restated (§1).
- FE5-12 — `tools/cmd/opsgen` stays in ABO, not a stage-bound move (§3 row, §8.4 note 6).
- integration pass (round 4, lead), folded into the generator inputs: `rbac.Store`, `tasks.Stores`, `module.Manifest.LegacyStores`.
- integration pass (round 5, lead): config optional capabilities named; `storage.ErrClosed`; objections 2–3 withdrawn.

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-2 / MOD6-1 — `internal/operations/state` stays in ABO as books' legacy run-state package (§3 row `abo`, out of the S5 `mediacore/tasks` row); core's executor state machine is a re-implementation over `tasks.RunState` (§8.6 row); taskstore imports `state` and maps `RunState` ↔ `state.V3`; opsgen keeps its HEAD input, not repointed; R1 cuts of the core-bound files that import `state`; 49 leave / 5 new / 169 remain (§1, §2.4, §4.7, §8.1 S5, §8.4 note 6).
- MOD6-2 — gate E's test half: typed scan of the test files, a declared disposition per test file (moves / external / stays), routes, copied helpers and `export_test.go` hooks, test-side reference count in the §8.6 row; taskstore's tests use `storetest` only from `package taskstore_test`; `TestLegacyStepPreconditionsResolve` lives in `internal/books` (§2.4, §8.6).
- MOD6-4 — the scan stand-down marker is taskstore's `StandDownStore` (`tasks.Stores.StandDown`); §2.4 taskstore row, §3 registry row, §7, §8.6 key table.
- MOD6-6 — (h): a `FormerKinds` literal in core fails; core-owned kinds' legacy aliases come from `app.Options.LegacyTaskAliases` (§2.1 (h)).
- MOD6-7 — (h): one match rule shared with 03 R1(c) and 05-storage; a call site whose key folds to no constant in a core-bound file needs a declared key source (generated note; none at the evidence commit) (§2.1 (h)).
- MOD6-8 — the `internal/config` → `internal/cfaccess` cut is not a cycle cut; gate E now requires every cycle cut to be needed, which also moves `internal/server` → `internal/books` to the non-cycle table (§8.6).
- MOD6-9 — gate E's graph uses final (post-rename) names; the new-path table names packages as of each creation stage and lists taskstore's `internal/operations/state` import (§8.6).
- MOD6-10 — the scheduler's ABO importers are counted at T7 (§8.2); the (h) table covers core-bound or re-implemented files, with a generated row count; MOD5-4's history count corrected (§8.6).
- MOD6-11 — `internal/books/configstore` always written with its full path; the S3 façade is the `Facade` type of ABO's `internal/config` (§2.4, §7, §8.1 S3).
- MOD6-13 — the type-checking helper is `tools/stagecheck`, its own module (§2.1, §8.6).
- COR6-1 — `rbac.LegacyAtom`/`LegacyCheck` are S2 leaf values that carry the atom; resolution at S4 (§8.1 S2).
- COR6-3 — HEAD's Start-time identity writers stay books start-hook code through S4 r1; `seed.go` and `bootstrap.go` move in the r2 flip PR (§8.1 S4).
- COR6-5 — books' legacy config `Open` writes nothing: HEAD's marker-guarded one-shots become books versioned `Migration`s; reshaping `migrate*Blob` stay pure read transforms (§2.4).
- FE6-3 — the L0 leaf `buildid` (`buildid.Binary`) lands at S0 for ABO's guard and `--version-json`; `app.BinaryID` delegates (§8.1 S0).
- OPS6-8 — the stage-visibility marker is set by the PR named in each 09 stage table's "Flip PR" row (§2.1).
- R6-3 / SIMP6-2 / SIMP6-3 — the one `legacy` package (with `legacy/legacytest`) in the stage plan: created at S2, grows at S2b, S4, S5 and S7; `legacy.RootSource` replaces `library/librarytest` (§8 intro, §8.1, Objections).
- SIMP6-1 / SIMP6-9 — config's legacy capabilities cited from 06 §6.4.10 with their correct names; the basis mechanism (`Suspended`, `Confirm`) is deferred (§7, §8.1 S2).
- Objections to 03: items 2–3 removed (resolved); 03 v1.9 adopts the round-6 count, `legacy`, `tools/stagecheck` and (h)'s rule.
- integration pass (round 6, lead): `legacy.RootSource`; `buildid` dropped from objection 1 (03 lists it).

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; generator inputs
`patch_map7.py`):

- R7-8 / MOD7-1 — the registry engine, `childop`, `opsmetrics`, `opsv3` and `pkg/ops/opstest` are re-implementations with §8.6 rows and generated counts (importer references included); the five `state` rows and the same files' `database`/`opswriter`/`logger`/`metrics`/`pkg/ops` rows left §8.5; the registry files stay as opsbridge until T8 with no `origins.txt` line (§1 decision 6, §2.2, §3 rows, §7, §8.1 S5, §8.6).
- R7-8 (generator) — new check §2.1 (i): a core-bound file using HEAD's run-state vocabulary or run-row types fails without a §8.6 row; it also found `internal/sysinfo`'s `RecentOperations`, now a §8.6 row.
- R7-8 / MOD7-2 / MOD7-11 — `internal/operations` re-split: `state.go` (divided) → `internal/books/contract`, `BulkMetadataFetchParams` → `internal/server`, only `progress.go`/`trigger_source.go` → `tasks`; registry *Split* "stays" parts for the v1 status bridge (a books subscriber on `core.task.finished.v1`) and the activity mirror (§2.4, §3 rows, §7).
- R7-8 / MOD7-3 — `maintenance_window_last_run` → taskstore's `ClockStore` with HEAD's date codec (§2.4, §7, (h) table); the (h) table counts hits by kind and every non-op-ID hit must be named in its cut text.
- MOD7-2 (03 R1(c)) — (h) reads 03's reviewed literal list (`legacy_op_id`).
- R7-2 / COR7-1 — `internal/storagehttp` at S4 r2 with the legacy path as its `Alias`; `legacy.Maintenance` and `module.StorageHandles.Maintenance` from S2b, the handle deleted at S8 (§8 intro, §8.1).
- MOD7-4 — `internal/books` injects the owner check into `internal/plugins/maintenance` (an `OPTCUT` row); `internal/cfaccess`'s `layerOf` is 5, checked against the importer table (§2.4, §8.4 note 3, §8.6).
- MOD7-5, MOD7-6 — no 04 text (09 S0, 03 §3.2 item 4).
- MOD7-7 — (h) cites 03 R1(c)'s one match rule instead of restating it (§2.1 (h)).
- MOD7-8 — the `race` timeline twin has a gate E test row; the test half reads §2.1 (c)'s union; the moving-test count is generated (19) (§2.4, §8.6).
- MOD7-9 — §8.4 note 3 lists taskstore's real importers.
- MOD7-10 — the new-path table uses final names with a per-edge stage point (§8.6).
- MOD7-12 / SIMP7-11 — objection 1 regenerated: `buildid`, `auth/oidc` and `archtest/tasklint` dropped (03 lists them; the generator reads multi-path rows and "sub-package" mentions); the legacy-case note only for interfaces 05-storage lists a legacy suite for.
- MOD7-13 — §7: `internal/executor` is one package; 05-tasks' `executor/state` and `executor/writer` are files of it.
- MOD7-14 — §2.1 (c) cites 03 §3.5.1 item 4's tag-set list instead of restating it.
- MOD7-15 — §2.1: root `make ci` vets and tests `tools/stagecheck`, with fixture tests per negative control.
- COR7-11 — `seed.go` and `bootstrap.go` dated S4 r2; S4 r1 and S4 r2 are separate stage points of the gate model (§2.1, §8.5).
- COR7-12 — §8.1 S3: `legacy` grows (`ConfigSecrets`, `ConfigCommitObserver`, `SecretsStore`; `ConfigExternalWriter` deleted).
- OPS7-8 — §2.1: the ordered `shipped_stage:` marker values.
- DATA7-13 — §2.4: the three marker one-shots are `Seed` steps, so a fresh install writes HEAD's markers.
- SIMP7-7 — §8.1 S5: T8 deletes `storage.LegacyMigration`, `Spec.Legacy`, the legacy phase and `TestNoNewLegacyMigrations`.
- SIMP7-8 (03 v1.10) — `legacy.RootSource` → `legacy.LibraryRootSource`.
- COR7-10 (R-B request) — §8.1 S2b: the L0 leaf `storage/restorejournal`; 03 v1.10.1 lists it.
- Integration pass (round 7) — `storage/restorejournal` dropped from "Objections to 03" now that 03 lists it.

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; generator inputs
`patch_map8.py`, generator `gen04.py` and `gate2/{model,gateC,gate2,gateE}.py`, round-7 copies `*.r7.*`):

- R8-6 / MOD8-1 — the move rule: an ABO import changes by import path alone or by one of §2.2's enumerated mechanical rewrites (map04.MECH: logger, store-interface, config, HTTP-helper, metrics and injected-value cuts); the generator classifies every §8.5 rule and fails one outside the list; all 21 §8.5 rows keep their `origins.txt` lines; per-generation controls (a logger cut passes, an `OperationV2Row` use without a §8.6 row fails, a non-mechanical rule fails) (§1 decision 6, §2.1 (i), §2.2, §8.5).
- R8-6 / MOD8-2 — `childop` and `opsmetrics` stay as opsbridge until their last ABO user is gone; `opsmetrics` → `mediacore/internal/executor` (core-internal row, §8.3 gate row with its ABO users, books' gauges through an observable on `metrics.Scope`); `childop`'s path gets a §8.3 deletion gate at T8; §8.6 counts in-package only; "kept through its metric prefix" deleted (§2.2, §3 rows, §7, §8.1 S5, §8.3, §8.6, §11).
- R8-6 / MOD8-3 — *Split* cells name the tag-union file set; build-tag twins travel together (`reflink_darwin.go`, `reflink_unsupported.go`, `static_embed.go`, the sysinfo darwin/windows twins, `persistence_owner_other.go`); named twins land with their part in gates B–E and (h); new twin check in gate C with a negative control (§2.1 (b), (e), §3 rows).
- MOD8-4 — (h) tokenises Go source and matches struct tags per key (03 R1(c)); it sees every core-bound file, moved or re-implemented; the `maintenance.go` cut names `MaintenanceWindowOpParams`, which stays in ABO's `internal/scheduler` until T7 (§2.1 (h), (h) table).
- MOD8-5 — `internal/sysinfo`'s `service.go` is file divided; the dashboard reader, library fields and v1 log helpers stay; in-package count (§3 row, §8.6).
- MOD8-6 — `internal/config` → `internal/cfaccess` is removed by the S4 PR as required by the ABO layering guard; one injected interface carries the owner-proof check and the access-email reader (§2.4, §8.6).
- MOD8-7 — the engine figures are generated (§3.2 row, §7, §8.6); §7's `v3_adapter.go` sentence rewritten; §1 and §10 split moved files from re-implemented parts.
- MOD8-8 — `activity_mirror.go` moves at S5 to `internal/activity`, a books subscriber on `core.task.log-appended.v1`, outliving T8 (§3 registry row, §7).
- MOD8-10 — `tools/stagecheck` keeps the `ForTest` variants and resolves test-variant imports through `ImportMap`; only the counting parse drops them; fixture (§2.1, §8.6 gate E test half).
- SEC8-7 — the ABO merge handler checks `Can(core:admin)` with key scoping; `mergeActor`'s role-name check is not carried over (§8.6 `user_merge.go` row).
- R8-1 / COR8-1 / COR8-2 / COR8-5 / COR8-6 / COR8-9 / SIMP8-4 / SIMP8-7 — §8.1: one swap journal with the reset kinds in `storage/restorejournal`; `restoreswap` (L1) imports `storage` and the L0 leaves, never the reverse (03 R4, COR8-5; round 8's text said it did not import `storage`, corrected in v1.11.4); `legacy.Maintenance` has only `StageFactoryReset(ctx, requestedBy, req storage.ResetRequest)` (R9-6; round 8 gave it a kind argument) for both legacy reset routes and is deleted with `module.StorageHandles.Maintenance` in the S4 r2 PR; `UpgradeFloors()` deleted; the S2b `legacy` members named; `internal/storagehttp` receives `storagehttp.Stager`, with both legacy paths as its aliases (§8.1 S2b, S4, S8; core-only list).
- OPS8-1 (consistency) — §8.1 S5: the T0+T1 PR deletes the five `operations_*` client_golang families.
- Consistency — §8.6's non-cycle-cut count is generated (three rows).

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; generator inputs
`patch_mapA.py` and `tplA.py`, round-8 copies `t04.r8.md`, `gen04.r8.py`; the generator's checks are unchanged):

- A-4 — 04 is informative (§2.1): no CI job runs the generator, reads 04 or regenerates §8.2/§8.3; `scripts/ci/check_stage_visibility.py`, `tools/stagecheck`, the per-stage-point gates as CI requirements, `origins.txt` and the move/re-implementation classification as a CI rule are deleted; the generator's own checks stay as authoring verification at the evidence commit; Go's `internal/` rule and 03's guards are the binding checks (§1, §2.1, §2.2, §2.4, §8.2–§8.6, §10).
- A-4 — `archtest.DeferredMembersAbsent` removed from §8.1 S2; the `shipped_stage:` marker's file `ci/stage.txt` and its values are 09 §9.2.1's; "Objections to 03" records the A-4 deletions as resolved in 03 v1.12, and the §2.1 (c) and (h) citations of 03 (§3.5.1 item 4's tag-set list, R1(c)'s match rule) were re-checked against it.
- A-5 — no metric renames: ABO's ops series keep their HEAD names under the namespace ABO passes; the `mediacore_tasks_*` rename, the gather-time alias rows and the S5 `operations_*` deletion are removed (§7, §8.1 S5, `internal/opsmetrics` and §8.6 rows, §8.3 `internal/telemetrypolicy` user).
- A-2 — no books extension family and no moved secrets or sentinels (§1 decision 10, §4.5, §7, §8.1 S3 and S7, `internal/database` row).
- A-14 — `compat` lands without usage persistence (§8.1 S2b).
- A-11 — feature PRs never regenerate or edit 04 (§2.1, §8.2).
- ADV-11 — every count is informative, measured at the evidence commit; stages generate theirs at their base commit (§2.1).

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; generator inputs `patch_map9.py`
and `tpl9.py`, advisor-pass copies `map04.rA.py`, `t04.rA.md`, `gen04.rA.py`; no new `go list` or scan data):

- R9-2 — the T0+T1 PR moves the four `GET /api/v1/operations/{id}/{activity,changes,result,results}` routes to core with `…/operations/v2/{id}` as ledger-listed core aliases over the same handlers with `library.view` (§8.1 S5, `internal/server/handlers/operations` row).
- R9-3 / MOD9-1 — one owner per metric family: the T0+T1 PR deletes `opsmetrics`' emission of every family the executor records; the v3 loop reports schedule ticks into core's scheduler through `legacy.TasksScheduleTicks` until T7; `TestABOMetricsGatherNoDuplicateOrMissing` from S5 (§1 decision 6, §8.1 S5, §8.4, `internal/opsmetrics` and §8.6 rows).
- R9-3 / COR9-14 (cross-cluster request from R-F) — the moved `telemetry.go`, `ai_metrics.go` and `internal/aidispatch/metrics.go` create no HEAD AI instrument; books' AI recorder owns every `ai.*`/`ai_dispatch.*` family through `ai.WithObserver` (`internal/ai` and `internal/aidispatch` rows and *Split* cells, §8.5 and §8.6 rows).
- R9-10 / MOD9-3 — the binding checks fail a PR for every (`GOOS`, tag set) of 03 §3.5.1 item 4's list through the required darwin and `integration` vet job; `TestCoreImportsOnlyCore` reads the tag union; a darwin twin left behind fails that job (§1 decision 8, §2.1).
- MOD9-5 — `scripts/ci/check_commits_build.py` enforces "every commit of a move PR builds" (§2.2).
- MOD9-6 — `TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames` from S0 (§8.5).
- MOD9-9 — `internal/updater`: only `updater.go` moves (335 LOC); `scheduler.go` is deleted, replaced by the `core.updater.check` task (05-updater 5U.12); §1 and §10 recounted.

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; generator inputs `patch_map10.py`
and `tpl10.py` over the round-9 copies `map04.r9.py` and `t04.r9.md`; no new `go list` or scan data):

- R10-1 / COR10-1 — the five operations routes stay books' gin routes at every stage (four in one generalised pattern, `…/v2/{id}` its own), their handlers calling core's tasks client from T0+T1; core's run route is a different path and no alias; R9-2's move removed (§8.1 S5, `internal/server/handlers/operations` row).
- R10-3 — `otelgin` stays on the legacy gin mount as at HEAD, the one recorder of requests that reach gin; no file moves for it (§4 `internal/server`).
- R10-3 / MOD10-7 — `ops_abandoned_calls` changes owner in exactly one PR: both of books' gauges move to one observable together (`internal/opsmetrics` row, §8.3 gate, §8.4).
- MOD10-10 (cross-cluster request from R-F) — `internal/ai`'s `ai_metrics.go` and `telemetry.go` are divided and counted on the ABO side (recorder half books', client-side tracing to the adapter), `ai_metrics_models.go` stays with the recorder, and `internal/aidispatch/metrics.go` goes to books' AI recorder, not core (*Split* cells, rows, §8.5, §8.6, §10); core LOC 62617 → 61438, `internal/aidispatch`'s core half 1498 → 1422.
- Integration pass (round 10, lead) — §8.1: `legacy.ConfigCommitObserver` removed from the `legacy` list (05-config deleted it; the gap-fill step is a `storage.OnCommitted` step).
- Round 11 (lead) — R11-3: `internal/server/handlers/operations` reason (every `/api/v1/operations*` route stays books', no alias); R11-1: §8.1 S2 note (wrapper clears `r.Pattern`, shared instrument set); R11-4: UI-0 release `UI0`.
- Final verification (lead) — FIN12-3/FIN12-4: `internal/server/handlers/operations` reason names `tasks.Client.Timeline`/`Log`/`Discard` and keeps `/maintenance-window/*` books'; the taskhttp REWRITE row says the legacy pause routes stay books'.

Plan-phase consistency (v1.11.4; generator inputs `patch13.py` over `map04.r12.py` and `t04.r12.md`, giving `map04.r13.py` and `t04.r13.md`; no new `go list` or scan data):

- PC-8 — §8.1 S2b and the R8-1 history line: `core/internal/restoreswap` is L1 and imports `storage` and the L0 leaves, never the reverse (03 R4 wins over round 8's text).
- PC-9 — §8.1 S2b and the R8-1 history line: `legacy.Maintenance.StageFactoryReset(ctx, requestedBy, req storage.ResetRequest)` (05-storage §5S.3.15).
- PC-20 — §2.4, §8.1 S2/S4, §8.4 note 6, §8.6 and the `internal/server/middleware` row: `internal/server/legacyauth` is the sixth new ABO path (S4; layer: the floor), so 49 leave, 6 appear and 170 remain, margin 20; `core/internal/keyedmutex` is an L0 leaf (§2.3, §8.1 S3, §9).

D10–D12 revision (2026-11-04; v1.12.0; generator inputs `patch14.py` over `map04.r13.py` and `t04.r13.md`, giving `map04.r14.py` and `t04.r14.md`; the generator reads its stage points from the mapping; no new `go list` or scan data):

- Stage keys re-keyed to 09 §9.4's releases and 03 §3.8's flips: S0 → R0 (no package moves) and release R1; S1, S2, S2b and UI-0 → release R1; S3–S9 → F3–F9 (F9 may slip to R3); S10 → R3; S4 r1/r2 → F4; the 05-tasks points T0, T0+T1, T7 and T8 → F5 (groups T2–T7 land dark); the generator models R0, R1, F3–F9 and R3 in one order (§2.1, §8, every row, gate and table).
- Deleted: the rows and prose of core `compat`, core `legacy` and `legacy/legacytest`, books' legacy stores (`internal/books/configstore`, `internal/books/taskstore`, `Manifest.LegacyStores`, `app.Options.LegacyConfigStore`), opsbridge, the compat ledger copy, aliases (`/api/v3/ops` and the reset routes as core aliases, legacy task and permission aliases, gather-time metric alias rows), HEAD-name metric keeping and its gather golden, the window and floor-release text, and the citations of 05-tasks §5T.6.2, 06 §6.4.10/§6.4.11/§6.11.6, 09 §9.13 and 05-storage §5S.3.11's legacy suites.
- Added: `internal/books/cutover` (F3; 03 §3.5.1), books' R2 cutover steps M2–M6 with M7 and M8, which receives HEAD's run-row files at F5 as the reader of step M4 (§1 decisions 1 and 10, §2.4, §4, §8.1, §8.6 gate E); `internal/database`'s "stays" parts are HEAD-format readers deleted in R3; core `config` and `secrets` land at F3 (03 §3.3 store table); the v1 status bridge is deleted at F5; HEAD's task routes in `internal/server/handlers/operations` go at F5 (03 R6).
- New-path count re-derived to 03 §3.5.1: 49 leave, 5 appear, 169 remain, margin 19 (§2.4, §8.4 note 6).

Round 15 (2026-11-05; v1.13.0; review round 13 rulings R1, R6, R14, R15 and R17 applied to 04, which the other files' round-13 revisions left untouched; generator inputs `patch15.py` over `map04.r14.py` and `t04.r14.md`, giving `map04.r15.py` and `t04.r15.md`; no new `go list` or scan data):

- R1 (IMPL-1–IMPL-5, IMPL-12, XFILE-4): F2, R2's foundation PRs landed dark before F3, is a release-table row (§8.1) and a stage point of its own at which the generator ran gates A–E (§2.1, §8). Keyed F2 because they are created there: `storage/datamigrate`, `internal/storagemigrate`, `tasks`, `tasks/taskstest`, `internal/executor`, `internal/scheduler`, `internal/taskhttp` (all formerly F5), `audit` (formerly F4), `config` (formerly F3), and `integrations/providerhttp` and `integrations/ai` (formerly F7), which hold the minimal owner registrations of `core.integrations.release-holds` and `core.ai.batch-poll` from F2; `core.diagnostics.export`'s registration is in `health/diagnostics`, and `health`'s readiness core and `rbac`'s full `core:*` catalog grow packages that exist from release R1. Every HEAD source of these packages keeps the stage of the flip that moves or deletes it, so no file changes stage (§8.2 shows each). F5 registers the three late-owner kinds over `app.Options.InterimBackends` (`providerhttp.InterimHolds`, `ai.InterimPoll`, `diagnostics.InterimExport`); F7 deletes the first two and F8 the third with the field.
- R14 (XFILE-1): metric renames land in release R1 (HTTP), F5 (ops families) and F7 (AI families) only; F8 moves only the kernel `/metrics` mount. `app.Options.PromRegisterer` is gone: from release R1 the App's registry is the served one and ABO's client_golang families register through `Provider.Registerer()` (§8.1 R1, F5, F7, F8; `internal/telemetry` and `internal/metrics` rows).
- R15 (XFILE-2): the Activity page stops reading `ops.ts` at F5; R3 deletes `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state` together (§8.1 F5 and R3; the two rows; §8.4 note 6). Both packages stay counted in ABO, as in 03 §3.5.1: 49 leave, 5 appear, 169 remain.
- R6: R3's books boot step `legacy-file-cleanup` deletes `.encryption_key` and HEAD's `library.bleve` (§8.1 R3). R17: release R1 has 07 §7.19 steps 1–2 (§8.1 R1). R12: F6's realtime client is `eventSourceManager` on `/api/core/v1/events`.
