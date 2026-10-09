<!-- file: docs/proposals/2026-10-holistic/07-design-decisions-and-modularity/A-measurements.md -->
<!-- version: 1.1.0 -->
<!-- guid: 85d4dbee-1393-4289-a567-f958cc02ccd7 -->
<!-- last-edited: 2026-10-09 -->

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

*(r4, 2026-10-09)* Re-measured at `ebda30d47` with the program below (same
numbers: Store 455, catalogStore 130, operationsStore 97, mediaStore 77,
enrichmentStore 65, accountStore 44, platformStore 42, BookStore 62,
BookFileStore 46, OpsV2Store 39; 0 methods from other packages). Run it from any
scratch directory as `go run flatten.go <repo>/internal/database Store catalogStore …`;
07-G1 turns it into `internal/database/store_width_test.go`.

```go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

func main() {
	dir := os.Args[1]
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir,
		func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		panic(err)
	}
	ifaces := map[string]*ast.InterfaceType{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, s := range gd.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						if it, ok := ts.Type.(*ast.InterfaceType); ok {
							ifaces[ts.Name.Name] = it
						}
					}
				}
			}
		}
	}
	var flatten func(name string, seen map[string]bool) map[string]bool
	flatten = func(name string, seen map[string]bool) map[string]bool {
		out := map[string]bool{}
		it, ok := ifaces[name]
		if !ok || seen[name] {
			return out
		}
		seen[name] = true
		for _, m := range it.Methods.List {
			if len(m.Names) > 0 {
				for _, n := range m.Names {
					out[n.Name] = true
				}
				continue
			}
			switch t := m.Type.(type) {
			case *ast.Ident: // embedded interface from this package
				for k := range flatten(t.Name, seen) {
					out[k] = true
				}
			case *ast.SelectorExpr: // embedded interface from another package
				out["<external:"+t.Sel.Name+">"] = true
			}
		}
		return out
	}
	names := os.Args[2:]
	sort.Strings(names)
	for _, n := range names {
		m := flatten(n, map[string]bool{})
		ext := 0
		for k := range m {
			if strings.HasPrefix(k, "<external") {
				ext++
			}
		}
		fmt.Printf("%s\t%d methods (%d from other packages)\n", n, len(m), ext)
	}
}
```

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

## A.9 CI throughput (r4, measured 2026-10-09)

`gh run list --workflow ci.yml --limit 12 --json databaseId,status,conclusion,event,headBranch,createdAt,updatedAt`
(duration = `updatedAt - createdAt`):

| Run | Event | Branch kind | Wall | Conclusion |
|---|---|---|---:|---|
| 37865901335 | push | `main` (docs-only merge) | 45 min | failure |
| 37865893192 | pull_request | docs-only PR | 22 min | failure |
| 37843473291 | push | `main` | 39 min | failure |
| 37843467591 | pull_request | feature | 24 min | failure |
| 37810595550 | push | `main` | 48 min | failure |
| 37810591463 | pull_request | feature | 19 min | failure |
| 37731107628 | push | `main` | 4 min | cancelled (superseded) |
| 37731105360 | pull_request | feature | 24 min | failure |
| 37731107628 and older | | | | all failure or cancelled; 0 successes in 12 |

Per-job wall on the docs-only PR run `37865893192` (`gh run view --json jobs`,
all 16 jobs started together at 00:39:39 UTC, no queueing):

| Job | Minutes | Result |
|---|---:|---|
| Coverage Floor (PR gate) (`make test-short`, `-race -coverprofile`) | 22.7 | failure (slog guard test) |
| Minimal CI / Go Tests (short, race) (`go test -short -race ./...`) | 19.3 | failure (slog guard test) |
| Fixture Tests 1/3, 2/3, 3/3 | 6.7, 9.3, 8.5 | success |
| Minimal CI / Go Vet & Build | 5.0 | success |
| Minimal CI / Frontend Unit Tests | 3.8 | success |
| Mock Freshness | 3.1 | success |
| Repo Guards (`make fmt-check` and others) | 3.5 | failure (`internal/database/ops_v2_recent_test.go` not gofmt-clean) |
| Errcheck Ratchet | 2.9 | failure (779 → 770, baseline not lowered) |
| Super Linter (advisory), Frontend Lint & Build, Go Lint | 1.8, 1.5, 1.2 | success |
| Interface Width Ratchet | 0.9 | failure (`iface_bookfile.go:54`, 9 methods) |
| TODO Fragment Headers | 0.1 | success |

So the PR's wall time is the two full short-test runs, which run the same
suite twice in parallel. The `main` run `37865901335` for the same commit shows
the second cost: its 11 passing jobs started at 00:39:45 and its 5 failing jobs
restarted at 01:02:43, which is `auto-revert.yml`'s "one free re-run" of the
failed jobs (`auto-revert.yml:85-97`, `gh run rerun --failed`), adding 23 min.

Why `main` is red, from `gh run view 37865893192 --log-failed` (all four
inherited, none caused by the PR):

1. `make fmt-check` → `internal/database/ops_v2_recent_test.go` (`gofmt -l`
   lists it locally too with go1.27.1; unformatted since `300d26dd3`,
   2026-10-06).
2. `TestGuard_NoDirectSlogCalls` (`internal/logger/slog_guard_test.go:61`): 10
   files "not on the ratchet" or over their allowance, plus
   `internal/itunes/library_watcher.go: ratchet allows 2, file now has 0`, a
   two-way failure on an improvement. This single test fails both the Go Tests
   job and the Coverage Floor job.
3. Errcheck Ratchet: 779 → 770, two-way.
4. Interface Width Ratchet: `internal/database/iface_bookfile.go:54:23` has 9
   methods (`interfacebloat` limit 8), baseline 0.

Other inputs to 07-C2/C3: `ci.yml` has no `paths` filter (`frontend-ci.yml`,
`e2e.yml`, `memory-leak-scan.yml` do); the reusable
`falkcorp/github-common/.github/workflows/reusable-ci-minimal.yml` exposes
`run-frontend` but no input to skip its Go test job (inputs read via
`gh api repos/falkcorp/github-common/contents/...` on 2026-10-09); `make
test-short` runs `go test ./... -short -race -coverprofile=coverage.out
-covermode=atomic -timeout 25m` (`Makefile:269-272`); the fixture job already
shards by measured package weight (`scripts/ci/fixture_test_packages.py`).
Test counts in the two slow packages: `internal/plugins/maintenance` 182
`_test.go` files, 1,589 `func Test`; `internal/server` 272 files, 1,397.
