<!-- file: docs/agent-tasks/platform-core/R1-34-service-registry-explicit-list.md -->
<!-- version: 1.0.4 -->
<!-- guid: 27c7bc09-4001-4dfe-a02e-1a93adb8163e -->
<!-- last-edited: 2026-11-05 -->

# R1-34 — Service registry step 3: the 24 registering files export ServiceDefs() instead of an init() Register, books passes one explicit list to NewContainerFrom, and Register, ResetForTest, IncludeAll and IncludeGroup are deleted

| Field | Value |
|---|---|
| ID | `R1-34` |
| Tier | **T2** (Sonnet / high): 24 registering files changed the same way in one PR, with no service changed |
| Release / wave | R1 / W17 |
| Depends on | R1-15 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 06 §6.11.1 steps 2–3, §6.11.2 (the 43 registrants) |
| Branch | `agent/platform-core-r1-34-service-registry-explicit-list` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1
(09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this
list without recording it under the PR's "Scope notes"):

- `internal/serviceregistry/`
- `internal/books/`: `module.go`
- `internal/activity/`: `register.go`, `register_backend_test.go`
- `internal/ai/`: `register.go`, `register_test.go`
- `internal/audiobooks/`: `register.go`, `register_test.go`
- `internal/batch/`: `register.go`, `register_test.go`
- `internal/config/`: `register.go`, `register_test.go`
- `internal/fileops/`: `register.go`, `register_test.go`
- `internal/importer/`: `register.go`, `register_test.go`
- `internal/merge/register.go`
- `internal/metafetch/`: `register.go`, `register_test.go`
- `internal/operations/registry/register.go`
- `internal/opscatalog/catalog_link_test.go`
- `internal/plugin/`: `register.go`, `register_test.go`
- `internal/plugins/acoustid/register.go`
- `internal/plugins/dedup/register.go`
- `internal/plugins/deluge/register.go`
- `internal/plugins/itunes/register.go`
- `internal/plugins/metafetch/register.go`
- `internal/quarantine/register.go`
- `internal/scanner/`: `register.go`, `register_test.go`
- `internal/search/register.go`
- `internal/server/`: `backup_optout_test.go`, `batch_poller_register.go`, `flight_recorder_test.go`, `registry_wire.go`, `registry_wire_dedup_score_config_test.go`, `server.go`
- `internal/sysinfo/`: `register.go`, `register_test.go`
- `internal/updater/register.go`
- `internal/work/`: `register.go`, `register_test.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-34" -b agent/platform-core-r1-34-service-registry-explicit-list origin/main
cd "$REPO/.worktrees/platform-core-r1-34"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1). Line numbers and counts below
were read at the planning base `ac4368bdb` (no code changed between it and HEAD). Re-run every grep at your base.

## Goal (06 §6.11.1 step 3, verbatim in substance)

"Books-internal, first PR after the skeleton — remove the global factory map: each of the 24 registering files
replaces `func init(){ serviceregistry.Register(def) }` with `func ServiceDefs() []serviceregistry.ServiceDef`;
`books` passes one explicit list to `serviceregistry.NewContainerFrom(defs...)`; `Register`, `ResetForTest`,
`IncludeAll/IncludeGroup` deleted. This alone removes test-isolation defects 1–4 … and the `batchpoller` class of
bug." `serviceregistry` keeps working inside books (06 §6.11.1 step 2: it is app code; R5 binds core only).

**No service changes.** The 43 registrants of 06 §6.11.2 keep their names, `Needs`, `Groups` and `Build` bodies
byte-for-byte; the production container builds exactly the set HEAD builds. Step 4 (constructors replacing clusters,
deleting `ServiceDef`s and `Get[T]` sites) is paced by the flips F3-07, F5-18, F7-09, F8-04 and finished by R3-05; it
is not this PR.

## The set HEAD builds (what the explicit list must reproduce)

`internal/server/server.go:814-826` builds one container with `IncludeGroup("core", "ai", "scheduler", "plugins")`
and adds `IncludeGroup("activity")` only when `config.AppConfig.DatabasePath != ""`, with overrides `store`,
`config`, `appversion` (and `activityStoreOverrideForTest` in tests). Every one of the 43 defs carries exactly one of
those five groups (19 `core`, 10 `ai`, 5 `scheduler`, 5 `plugins`, 4 `activity` at the plan's base). So the list is:
**all 43 defs, the 4 `activity` defs only when `DatabasePath` is set.**

## Code contract

```go
package serviceregistry

// NewContainerFrom creates a Container whose factory list is exactly defs (06 §6.11.1 step 3).
// Panics, as Register did, on an empty Name, a nil Build or a duplicate Name among defs.
// Every def passed is included in the build set; Override and Include keep their meaning.
func NewContainerFrom(defs ...ServiceDef) *Container

// Deleted: Register, ResetForTest, the package-level `registered` map, (*Container).IncludeAll,
// (*Container).IncludeGroup, and NewContainer (callers use NewContainerFrom(), which with no defs is the empty
// container NewContainer returned).
```

```go
// In each of the registering files (one function per file, name fixed by 06 §6.11.1 step 3):
func ServiceDefs() []serviceregistry.ServiceDef
```

`ServiceDef`'s fields (`Name`, `Needs`, `Groups`, `Build`) do not change; `Groups` stays as data (step 4 deletes it
with the defs). Deleting `NewContainer` is this plan's choice (06 names only `NewContainerFrom`); it keeps one
constructor and removes the last way to build a container from a hidden list.

## Where the one explicit list lives (read before step 4)

`internal/books` imports `internal/server` (R1-15), and `internal/server` builds the container inside `NewServer`
(`server.go:814`), whose ~44 `Get[T]` sites in `registry_wire.go` need every service. `internal/server`'s own tests
call `NewServer(store)` in 14 files and must keep getting the full set, and they cannot import `internal/books` (an
import cycle). Therefore:

- the **one explicit production list** is the unexported `serviceDefs(databasePathSet bool) []serviceregistry.ServiceDef`
  (name coined here) in `internal/server/registry_wire.go`: it concatenates, in 06 §6.11.2's row order, every
  registering package's `ServiceDefs()` and the server's own two contributions (the `registry_wire.go` defs and
  `batch_poller_register.go`'s), and appends `activity.ServiceDefs()` only when `databasePathSet`;
- `NewServer` builds `serviceregistry.NewContainerFrom(serviceDefs(config.AppConfig.DatabasePath != "")...)` with
  the same three overrides and the same test override;
- the list stays in `internal/server/registry_wire.go`: `internal/server`'s tests need the full set and cannot import
  `internal/books`. `internal/books/services.go` is not in this row's files and is not touched; `internal/books/module.go`
  changes only if R1-15 left the container construction in `internal/books` at your base. Check with
  `git grep -n 'serviceregistry.NewContainer' -- internal/books internal/server/server.go` and say in the PR which
  case applies.

There is no other list: no test file may build a production-shaped list of its own.

## Dependency check

R1-15 merged: `test -d internal/books && echo ok` prints `ok`. Otherwise STOP.

## Re-verify (expected at the plan's base)

```bash
git grep -ln 'serviceregistry.Register(' -- '*.go' | grep -v '^internal/serviceregistry/' | grep -v _test.go | wc -l
#   expect 24 at ac4368bdb; R1-15 replaced the init() registrations of internal/operations/registry/register.go and
#   internal/updater/register.go with explicit construction in internal/books, so expect 22 at your base. Convert
#   whatever registers at your base; if either of those two files still registers, convert it too.
git grep -n 'serviceregistry.Register(' -- '*.go' | grep -v '^internal/serviceregistry/' | grep -v _test.go | wc -l
#   expect 43 calls at ac4368bdb (06 §6.11.2's count), fewer by the defs R1-15 removed
git grep -n 'ResetForTest()' -- '*.go' | grep -v '^internal/serviceregistry/'           # expect 2 hits, both internal/plugin/register_test.go
git grep -n 'IncludeAll()\|IncludeGroup(' -- '*.go' | grep -v '^internal/serviceregistry/'   # expect 2 hits, internal/server/server.go:818 (chained) and :825
git grep -n 'serviceregistry.NewContainer()' -- '*.go' | grep -v '^internal/serviceregistry/' | wc -l   # expect 19 call sites
grep -rho 'Groups: *\[\]string{[^}]*}' internal --include=*.go | grep -v _test | sort | uniq -c   # expect core 19, ai 10, scheduler 5, plugins 5, KeyActivity 4
git grep -n 'func init()' -- internal/server/registry_wire.go internal/server/batch_poller_register.go   # expect :96 and :17
```

If a count differs for a reason other than R1-15, use your base's grep output as the work list and record it in the
PR.

## Steps

1. **Tests first (red).** In `internal/serviceregistry/`:
   - replace `registry_test.go`'s `TestRegister_*` with `TestNewContainerFrom_DuplicatePanics`,
     `TestNewContainerFrom_RequiresName`, `TestNewContainerFrom_RequiresBuild` (names coined here; same assertions);
   - delete `groups_test.go` (it tests only `IncludeGroup`);
   - port `container_test.go`, `graph_test.go` and `typed_accessor_regression_test.go` from `Register` +
     `NewContainer()` to `NewContainerFrom(defs...)`;
   - add `TestNewContainerFrom_TwoContainersIsolated` (name coined here): two containers built from different def
     lists in one test process see only their own defs (the isolation the global map broke).
   In `internal/server/registry_wire_dedup_score_config_test.go` (or a new test in the same file), add
   `TestServiceDefs_BuildsHEADSet` (name coined here): `serviceDefs(true)` yields exactly the names your base's
   container builds with `DatabasePath` set (a literal list in the test), `serviceDefs(false)` the same minus the 4
   `activity` names, no duplicates. Capture the literal **before any edit**: on your base, build a server with
   `DatabasePath` set and print `s.container.Names()` (`Container.Names()` exists, `container.go:99`). At the plan's
   base that is 06 §6.11.2's 43 names; at yours expect fewer by whatever R1-15 moved to explicit construction
   (`ophub`, `opregistry`, `updater`, `updatescheduler`) unless it kept them in the container.
2. **`internal/serviceregistry`.** Move the factory list into the `Container` (a per-container `defs` map); add
   `NewContainerFrom`; make `Include`, `Resolve`, `Build` and the graph code read the container's map; delete
   `Register`, `ResetForTest`, the global `registered`, `IncludeAll`, `IncludeGroup` and `NewContainer`. Update the
   package and `Container` doc comments that describe the global list. Bump headers.
3. **The registering files.** In each file the re-verify grep lists, replace the `init()` that calls
   `serviceregistry.Register(def)` (once or several times) with
   `func ServiceDefs() []serviceregistry.ServiceDef { return []serviceregistry.ServiceDef{ def, … } }`, the defs in
   their original order and **unchanged**. A file whose `init()` also does other work keeps that work in `init()`
   and loses only the `Register` calls. In `internal/server/`, `registry_wire.go` and `batch_poller_register.go`
   are in package `server`: give them unexported functions (`serverServiceDefs()` and `batchPollerServiceDefs()`,
   names coined here) instead of exported ones. Bump each file's header.
4. **The one list.** Write `serviceDefs(databasePathSet bool)` per "Where the one explicit list lives" and replace
   `server.go:814-826`'s `NewContainer()…IncludeGroup(…)` with `NewContainerFrom(serviceDefs(...)...)` plus the same
   overrides; the comment block above it now says "books passes one explicit list (06 §6.11.1 step 3)".
5. **Tests that built containers from the global list.** Port every listed `register_test.go` and
   `register_backend_test.go` from `NewContainer().Include(names…)` to
   `NewContainerFrom(<pkg>.ServiceDefs()...)` (plus the defs their `Needs` pull in, or `Override`s for them);
   `internal/plugin/register_test.go` drops `ResetForTest()` and builds its isolated containers from local def
   lists; `internal/server/flight_recorder_test.go` uses `NewContainerFrom()`. `internal/opscatalog/catalog_link_test.go`
   scans the AST for `serviceregistry.Register` calls: make it find the `ServiceDefs` functions (a `FuncDecl` named
   `ServiceDefs` returning `[]serviceregistry.ServiceDef`) instead, with the same report. `internal/server/backup_optout_test.go`
   changes only if it breaks. Assertions keep their meaning.
6. Header bumps; fragment `changelog.d/<today>_serviceregistry_explicit_list.md` (no header; `### Changed`:
   "Internal: the service registry is built from one explicit list; the global registration map is gone."). Commit:
   `refactor(serviceregistry): explicit ServiceDefs lists replace init() registration`.

## Invariants (a reviewer checks each)

1. `git grep -n 'serviceregistry.Register(\|ResetForTest\|IncludeAll\|IncludeGroup\|NewContainer()' -- '*.go'`
   returns nothing.
2. No `func init()` in the listed packages calls into `serviceregistry`.
3. The production container builds the same names as HEAD with `DatabasePath` set and unset
   (`TestServiceDefs_BuildsHEADSet`).
4. No `ServiceDef` field or `Build` body changed (the diff of each registering file is the `init()` → `ServiceDefs()`
   wrapper only; review with `git diff -w`).
5. `internal/serviceregistry` holds no package-level mutable state.

## STOP conditions

- A `Build` body would have to change to compile (it read the global list or called `Register`): stop and report.
- The production set differs from HEAD's for any configuration.
- A registering file not in the row's list registers at your base (a feature PR added one): convert it, record it
  under Scope notes, and say so in the PR; if it is outside `internal/`, stop and ask.

## Acceptance

```bash
go build ./... && go vet ./internal/serviceregistry/ ./internal/server/                     # exit 0
git grep -n 'serviceregistry.Register(\|ResetForTest\|IncludeAll()\|IncludeGroup(\|NewContainer()' -- '*.go' | wc -l   # expect 0
git grep -ln 'func ServiceDefs() \[\]serviceregistry.ServiceDef' -- '*.go' | wc -l          # expect 22 (or 24 if R1-15 kept the two inits)
go test ./internal/serviceregistry/ -race -count=1 -v | grep -c '^--- FAIL'                  # expect 0
go test ./internal/server/ -run 'TestServiceDefs_BuildsHEADSet' -count=1 -v | grep -c '^--- PASS'   # expect 1
go test -race -count=1 ./internal/serviceregistry/ ./internal/activity/ ./internal/ai/ ./internal/audiobooks/ ./internal/batch/ ./internal/config/ ./internal/fileops/ ./internal/importer/ ./internal/metafetch/ ./internal/plugin/ ./internal/scanner/ ./internal/sysinfo/ ./internal/work/ ./internal/opscatalog/   # ok
go test -short -count=1 ./internal/server/...                                               # ok
make test-e2e                                                                               # green
make ci; echo "exit $?"                                                                     # expect exit 0
```

## Out of scope

Step 4 of 06 §6.11.1 (constructors replacing clusters, `Get[T]` sites, deleting `ServiceDef`s, the `batchpoller`
explicit construction and the move of the server construction into start hooks): F3-07, F5-18, F7-09, F8-04 and
R3-05. Deleting `internal/serviceregistry` (R3). Any service's behaviour.

## Rollback

Revert the PR (one PR, so the registry and its callers go back together). No stored data changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A deleted file needs nothing. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** No loop over a library-scale collection in this task; the container's build order is unchanged.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
