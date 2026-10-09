<!-- file: docs/proposals/2026-10-holistic/07-design-decisions-and-modularity/A-measurements.md -->
<!-- version: 1.0.1 -->
<!-- guid: 85d4dbee-1393-4289-a567-f958cc02ccd7 -->
<!-- last-edited: 2026-10-08 -->

# Appendix A: measurements behind workstream 07

All figures were measured on HEAD `f7211eb39` (2026-10-08) in the
`aorg-holistic` worktree. Each row gives the command so it can be re-run.

## A.1 Go package census

`go list ./... | wc -l` → **184** packages (module `github.com/falkcorp/audiobook-organizer`, `go 1.27.0`).
`ls internal | wc -l` → **107** top-level directories under `internal/`.

Lines of code per package, non-test vs test (`cat *.go | wc -l`, test files split by `_test.go`):

| Package | Non-test LOC | Test LOC | Non-test files |
|---|---:|---:|---:|
| internal/database | 82,524 | 74,993 | 215 |
| internal/plugins/maintenance | 78,102 | 68,679 | 130 |
| internal/server | 36,336 | 60,324 | 154 |
| internal/database/mocks (generated) | 32,024 | 71 | |
| internal/metafetch | 18,127 | 20,148 | 42 |
| internal/server/handlers | 14,833 | 14,310 | |
| internal/dedup | 14,541 | 18,227 | |
| internal/scanner | 14,258 | 22,513 | |
| internal/server/handlers/abs | 14,241 | 17,775 | |
| internal/audiobooks | 12,203 | 15,499 | |
| internal/metadata | 12,101 | 11,757 | |
| internal/maintenance/jobs | 11,919 | 10,471 | |
| internal/server/handlers/mocks (generated) | 9,986 | 0 | |
| internal/organizer | 9,948 | 16,655 | |
| internal/operations/registry | 9,641 | 14,709 | |
| Whole module | 562,171 | 534,147 | |

The three largest packages hold 196,962 non-test lines, 35% of the module.

## A.2 Import graph, internal edges only

Command: `go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./...`, then a
Python count of edges whose target is a module package.

Fan-in (how many module packages import it): database 94, logger 62, config 47,
operations/registry 34, metadata 25, pathutil 25, serviceregistry 24, merge 21,
dedup 18, personname 18, util 17, httputil 17, metafetch 16, fileops 16,
versionprimary 15, itunes 13, metrics 13, logging 13, operations 13, authorname 12.

Fan-out (how many module packages it imports): server 105, plugins/maintenance 57,
server/handlers 39, metafetch 31, scanner 27, audiobooks 26, maintenance/jobs 26,
server/handlers/audiobooks 23, dedup 22, itunes/service 21, organizer 21,
scheduler 21, metadata 18, plugins/dedup 17, reconcile 16, handlers/metadata 16,
importer 15, cmd 13, database 13, metabatch 13.

Layering violations found (`go list -f '{{join .Imports "\n"}}' ./internal/<pkg>`):

- `internal/config` imports `database`, `auth`, `backup`, `aidispatch`,
  `dedup/unified`, `serviceregistry`, `tools`. Config is imported by 47
  packages, so all of them depend transitively on the data layer and on dedup.
  Files: `internal/config/{persistence,register,update_service,ai_endpoints}.go`.
- `internal/metadata` imports `operations/registry`
  (`internal/metadata/enhanced.go:25`): a provider-client package depends on
  the job runner.
- `internal/database` imports `matcher`, `fingerprint`, `chaptershape`,
  `metastate`, `personname`, `titleutil`, `syncapi/progress`: domain logic sits
  under the storage layer.

## A.3 database.Store width

AST flattening of embedded interfaces in `internal/database/*.go`
(script in the analyst's scratchpad, method names de-duplicated):

| Interface | Methods (flattened) | Declared entries |
|---|---:|---:|
| Store | **455** | 6 |
| catalogStore | 130 | 7 |
| operationsStore | 97 | 6 |
| mediaStore | 77 | 7 |
| enrichmentStore | 65 | 7 |
| BookStore | 62 | 5 |
| BookFileStore | 46 | 8 |
| OpsV2Store | 39 | 8 |

The same script run on `a0312c104` (2026-08-19, the end of the sweep; non-test
files extracted read-only with `git show` into the scratchpad) gives 398, which
matches the sweep's recorded figure. A name diff gives 60 methods added and 3
removed. No role interface embeds an interface from another package, so the
flattening is complete.

References to the wide type, non-test, non-mock, comment lines dropped
(`grep -rn 'database\.Store\b' --include='*.go' internal cmd`): 31 lines at
HEAD, 9 lines at `a0312c104` by the same grep (`git grep`). The 31 classify as:

- 11 conformance assertions (`var _ X = database.Store(nil)`);
- 13 wiring or unwrap lines;
- 2 test helpers;
- about 5 genuine wide consumers.

Separately, `grep -rnE '= \(?database\.Store\)?\(nil\)'` finds 14 conformance
assertions at HEAD against 3 at `a0312c104`.

Why the interface grows: `internal/server/indexed_store.go:41-54` embeds
`database.Store` in the prod decorator, and only methods declared on `Store`
are promoted. Any new `*PebbleStore` method that a maintenance op reaches by
type assertion must be added to `Store`, or it fails only in prod (memory
`project_prod_store_is_indexedstore_capability_assertions`).
`database.AsCapability` (`internal/database/store_capability.go:90`) has 189
non-test call lines.

## A.4 Storage engines linked into the binary

| Engine | Where | Live in prod |
|---|---|---|
| Pebble v2.1.7 | main store `pebble_store.go:492`, AI scans `ai_scan_store.go:108`, OpenLibrary `openlibrary/store.go:41`, activity (shares main DB) | yes |
| hashicorp/go-memdb | in-memory mirror, `internal/database/memdb_*.go` (5,567 non-test LOC) | yes |
| Bleve v2.6.1 (bbolt underneath) | `internal/search` | yes |
| coder/hnsw and chromem-go | embedding index, `registry_wire.go:50,125` | hnsw default, chromem fallback |
| NutsDB v1.1.0 | `nuts_activity_store.go`, `nuts_metrics_store.go` | **no**: no production constructor call; only tests open it (`internal/activity/writer_test.go:95`, `service_test.go:25`) |
| modernc SQLite v1.59.0 | `sql_activity_store.go`, `sql_activity_migrating_store.go` | **no** in prod (`ACTIVITY_BACKEND=pebble` in the systemd drop-in since 2026-09-19), **but it is the code default**: `internal/activity/register.go:86-106` treats empty as SQLite |

Activity-store code (nuts_*, sql_*, pebble_activity_store) totals 14,241 lines
including tests.

## A.5 Global state

| Global | Non-test references | Files |
|---|---:|---:|
| `config.AppConfig` | 636 | 204 |
| `config.Snapshot()` | 37 | |
| `config.Mutate(` | 4 | |
| `database.GetGlobalStore()` | 3 | 2 |
| `viper.Get*` | 329 | 4 |

`internal/config/config.go:1791-1816` documents that direct field reads of
`AppConfig` are "tolerated with residual risk". `type Config struct` has 158
top-level fields, 250 `viper.SetDefault` calls and 134 `BindEnv` calls; the DB
loader `applySetting` (`internal/config/persistence.go:993`) is a hand-written
`switch` with 133 `case` labels. `SaveConfigToDatabase`
(`persistence.go:1544`) persists the whole struct as one blob.

## A.6 Book model width and write paths

- `type Book struct` (`internal/database/store.go:208`): 115 top-level fields.
- `type BookFile struct`: 65 top-level fields.
- `.ModifyBook(` non-test call lines: 137. `.UpdateBook(` non-test, non-mock: 11
  (`pebble_store.go:3654`, `migrations.go:1229`, `merge/combine_journal.go:1453`,
  `organizer/service.go:1419,2490`, `organizer/move.go:85`,
  `organizer/rename.go:226`, `server/indexed_store.go:104`,
  `server/handlers/organize.go:411`, `dedup/book_dedup.go:693`).
- No row-version / stale-write check exists (`grep -rn 'RowVersion\|StaleWrite' internal/database` → 0).
- `IsPrimaryVersion != nil && *` reads: 35 lines at HEAD, 11 at `63a5eb807`
  (2026-08-24) by the same `git grep`. `EffectiveIsPrimaryVersion(` calls: 20.
  `IsPrimaryVersion` appears on 407 non-test lines.

## A.7 Test wall time, five largest packages

Command: `go test -short -count=1 -json ./internal/plugins/maintenance ./internal/server ./internal/database ./internal/metafetch ./internal/scanner`.
The run used no `-race`, the five packages ran in parallel, the TMPDIR was a normal macOS one, and the machine was shared with other agents.
The figures are each package's elapsed time from the JSON stream.

| Package | Result | Seconds |
|---|---|---:|
| internal/plugins/maintenance | pass | 578.1 |
| internal/server | pass | 414.0 |
| internal/metafetch | pass | 239.6 |
| internal/database | pass | 156.9 |
| internal/scanner | **fail: hit the default 10-minute `go test` timeout** | 600.5 |

The scanner timeout was not investigated, as the charter requires: it is a machine-load data point, not a diagnosed failure. `make test-short` sets `-timeout 25m` for this reason.

Compile time alone for the test binary, warm cache (`go test -run '^$'`): internal/server 17.95 s, plugins/maintenance 6.99 s, database 3.35 s.

## A.8 GitHub CI on main

- `gh run list --workflow ci.yml --branch main --limit 30`: 1 success, 9 failures, 20 cancelled.
- Latest run 37843473291, job wall times:
  - Go Tests (short, race): 19.1 min;
  - Coverage Floor: 16.9 min;
  - Fixture shards: 7.6, 6.3 and 8.6 min;
  - Go Vet & Build: 5.0 min;
  - Frontend Unit Tests: 4.0 min;
  - Mock Freshness: 3.8 min.
- Failures on that run:
  - Errcheck Ratchet: "findings went DOWN (779 -> 770) but the baseline was" not lowered;
  - Interface Width Ratchet: "baseline=0 actual=1";
  - Repo Guards;
  - Go Tests.
