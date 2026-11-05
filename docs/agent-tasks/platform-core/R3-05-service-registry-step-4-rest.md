<!-- file: docs/agent-tasks/platform-core/R3-05-service-registry-step-4-rest.md -->
<!-- version: 1.0.2 -->
<!-- guid: de826053-df6e-496f-99df-94bcc804a0ba -->
<!-- last-edited: 2026-11-05 -->

# R3-05 — Service registry step 4, the rest: constructors replace the remaining clusters (work, audiobook, organize, quarantine, scan, merge, importpath, batch, itunes) and their `Get[T]` sites, and the server construction `Register` does moves into start hooks, so books' `ServiceDefs` list is empty

| Field | Value |
|---|---|
| ID | R3-05 |
| Release | R3 |
| Tier | **T3** (Opus / high): constructors replace the remaining registry clusters and the server construction moves into start hooks; a wrong order builds a service before its dependency or does I/O inside `Register` |
| Wave | W45 (alone; it owns `internal/books/module.go` and `internal/books/services.go` in W43) |
| Depends on | OWNER-05 (R2 released and deployed) |
| Spec | 06 §6.11.1 step 4 ("constructors replace clusters … The server construction that `Register` does today moves into start hooks here, which is what lets `Describe` run over books"), §6.11.2 (the 43 registrants and their destinations), §6.4.5 (`Describe`: "Register's side-effect rule … is what makes this safe"), §6.4.1 (`Register`'s side-effect rule), §6.5.2 (start order); 09 §9.4 R3 "Content" |

**Gate:** plan, then execute autonomously in a worktree with its own PR. No user-visible change: the same services
are built, in a valid order, and serve the same routes.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-r3-05-registry" -b agent/platform-core-r3-05-registry origin/main
cd "$REPO/.worktrees/pc-r3-05-registry"
git rebase origin/main
```

## Context: what is left of the registry at your base

06 §6.11.2 lists 43 registrants. R1-34 replaced every `init()` registration with exported `ServiceDefs()` lists and
one explicit list passed to `serviceregistry.NewContainerFrom` in `internal/books/services.go` (step 3). The flips
deleted the generic services' entries as their core packages arrived (step 4): F3-07 `configupdate`; F5-18 `ophub`,
`opregistry`, the four activity services and the five plugins; F7-09 `eventbus`, `filesystem`, `searchindex`, the ten
AI services, `batchpoller` and the three metafetch services; F8-04 `updater`, `updatescheduler`, `system`,
`dashboard` and `metricsstore`. That leaves the nine audiobook services of 06 §6.11.2's last two domain rows:
`work`, `audiobook`, `organize`, `quarantine`, `scan`, `merge`, `importpath`, `batch` and `itunes`, all with
destination "books" (a plain constructor in the books module).

## Goal

1. **Constructors.** Replace each of the nine `ServiceDef`s with a plain constructor called by books in dependency
   order (06 §6.11.1 step 4), and replace every `serviceregistry.Get[T]` / `MustGet[T]` site that reads one of them
   with the constructed value passed in (a parameter, a struct field set by the composition root, or a closure). No
   global, no package-level registry (03 R5 binds core; books follows the same rule here).
2. **Server construction into start hooks.** Move the construction `books.Register` does today
   (`server.NewServer`, which loads the HNSW snapshot, and the rest of `registry_wire.go`'s build) into books' start
   hooks (06 §6.11.1 step 4, §6.5.2), so `Register` only declares (06 §6.4.1's side-effect rule). After this PR
   `app.Describe(ctx, opts, books.Module())` runs without I/O (06 §6.4.5).
3. **Empty list.** `internal/books/services.go` passes an empty `ServiceDefs` list. R3-01 then deletes
   `internal/serviceregistry` and the remaining plumbing; this PR does not.

## Invariants

- **I1.** Books' `ServiceDefs` list is empty and no non-test file calls `serviceregistry.Get`/`MustGet` for the nine
  services.
- **I2.** `Register` does no I/O: `app.Describe` over books succeeds against `vfs.NewMem` storage and a temp state
  directory (06 §6.4.5), and books' `openapi` subcommand now emits a books document.
- **I3.** Every service is built before its first user, in the order the old `Needs` lists implied; a cycle the
  registry hid is a STOP, not a lazy-init workaround.
- **I4.** Same behaviour: every existing test passes; e2e passes; the HNSW snapshot still loads at start (in a start
  hook), before readiness.
- **I5.** CLAUDE.md concurrency rules: no new loop over library-scale collections is introduced; if a moved build
  step iterates the library (the HNSW load does), it keeps its existing bounded concurrency.

## Files

| Action | Path |
|---|---|
| modify | `internal/books/services.go` (the list becomes empty; the constructors are called from the composition root) |
| modify | `internal/books/module.go` (start hooks take the server construction; `Register` only declares) |
| modify | `cmd/` (books' `openapi` subcommand: the books skip, step 5) |
| modify | `internal/work/register.go`, `internal/work/register_test.go` |
| modify | `internal/audiobooks/register.go`, `internal/audiobooks/register_test.go`, `internal/audiobooks/lifecycle.go` (`audiobook`, `organize`) |
| modify | `internal/quarantine/register.go` |
| modify | `internal/scanner/register.go`, `internal/scanner/register_test.go`, `internal/scanner/lifecycle.go` |
| modify | `internal/merge/register.go` |
| modify | `internal/importer/register.go`, `internal/importer/register_test.go` (`importpath`) |
| modify | `internal/batch/register.go`, `internal/batch/register_test.go` |
| modify | `internal/dedup/lifecycle.go` (its `Get[T]` reads of the nine) |
| modify | `internal/server/registry_wire.go` (`itunes` at `registry_wire.go:343` at the plan's base, and the rest of the build), `internal/server/server.go`, `internal/server/server_lifecycle.go` |
| create | one `changelog.d/` fragment (headerless) |

A `register.go` that holds nothing after its `ServiceDefs()` goes is deleted; say so in the PR. Re-grep at your base:
any further non-test file that reads one of the nine through `serviceregistry` is this PR's file too.

## Re-verify before editing (expected results at your base)

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. The explicit list (R1-34). Expected: NewContainerFrom called once, in internal/books/services.go
grep -rn 'NewContainerFrom' --include='*.go' internal/ cmd/ | grep -v _test
# 2. What the list still names. Expected: exactly the nine services above (by their Key* names or ServiceDefs() calls)
grep -n 'ServiceDefs()' internal/books/services.go
# 3. Every ServiceDefs() still defined outside the nine clusters. Expected: none outside the Files table
grep -rln 'func ServiceDefs()' --include='*.go' internal/ | sort
# 4. The Get[T] sites of the nine (plan base: 57 non-test Get sites in all of internal/, most removed by the flips).
#    Read every hit; each is this PR's edit
grep -rn 'serviceregistry\.\(Get\|MustGet\)\[' --include='*.go' internal/ cmd/ | grep -v _test
# 5. Where Register builds the server today. Expected: NewServer called from books' Register path
grep -rn 'NewServer(' --include='*.go' internal/books/ internal/server/ | grep -v _test
# 6. Describe over books is not called yet. Expected: 0 (the openapi subcommand skips books until this PR)
grep -rn 'Describe(.*books' --include='*.go' . | grep -v _test | wc -l
```

STOP and report if: grep 2 names a service outside the nine (a flip left its entry: report which flip, do not delete
it here unless it is one of the nine); grep 3 finds a `ServiceDefs()` outside the Files table that is still in books'
list; a dependency cycle among the nine appears once construction is explicit (I3).

## Steps

1. Write the tests first (below).
2. Draw the dependency order of the nine from their `Needs` lists at your base; record it in the PR description.
3. For each service, in that order: add or expose its constructor (`New…(deps…)`), call it from the composition root
   (`internal/books/services.go` or the start hook that needs it), and replace each `Get[T]` site with the passed
   value. Delete its `ServiceDefs()` entry and the `register.go` content that only fed the registry.
4. Move the server construction (`server.NewServer` and `registry_wire.go`'s build) from `Register` into books' start
   hooks, in 06 §6.5.2's start order; `Register` keeps only declarations (manifest, routes mounted on the legacy
   mount, task kinds, hooks).
5. Turn books' `openapi` subcommand on for books (06 §6.4.5: it skipped books until `Register` met the side-effect
   rule), in `cmd/` (the row's files).
6. Headers, fragment, acceptance.

## Tests (write first)

- `TestBooksServiceDefsEmpty` (name coined here) in `internal/books/`: books' list passed to `NewContainerFrom` has
  length 0.
- `TestDescribeOverBooksDoesNoIO` (name coined here) in `internal/books/`: `app.Describe(ctx, opts, books.Module())`
  succeeds with a filesystem that fails the test on any open outside the temp state directory (06 §6.4.5, §6.4.1).
- `TestBooksStartBuildsInOrder` (name coined here): a fake clock and fakes record the construction order; every
  service is built after its dependencies and the HNSW snapshot loads in a start hook before readiness.
- The existing `register_test.go` files of the nine packages, rewritten onto the constructors.

## Acceptance (commands and expected output)

```bash
# No ServiceDef left in books' list, no Get[T] of the nine. Expected: 0 and 0
grep -c 'ServiceDefs()' internal/books/services.go
grep -rn 'serviceregistry\.\(Get\|MustGet\)\[' --include='*.go' internal/ cmd/ | grep -v _test | wc -l

# Tests. Expected: ok
go test -count=1 -run 'TestBooksServiceDefsEmpty|TestDescribeOverBooksDoesNoIO|TestBooksStartBuildsInOrder' ./internal/books/...
go test -count=1 ./internal/work/... ./internal/audiobooks/... ./internal/quarantine/... ./internal/scanner/... \
  ./internal/merge/... ./internal/importer/... ./internal/batch/... ./internal/dedup/... ./internal/server/... ./internal/books/...

# Builds and e2e. Expected: exit 0; green
go build ./... && (cd core && go build ./...)
make test-e2e

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Deleting `internal/serviceregistry` and the `NewContainerFrom` plumbing (R3-01).
- Any generic service (already removed by the flips).
- Rewriting a books route group as native (09, 05-http-api §8.4).

## Rollback

Revert the PR; nothing stored changes, and `make rollback` to R2 is unaffected.

## Repo rules

- Worktree off fresh `origin/main`; headers bumped on every modified file.
- Conventional commit, for example `refactor(books): constructors replace the last registry clusters (R3)`, ending
  with `Co-Authored-By: Claude <noreply@anthropic.com>`; no release or flip trailer; `gh pr merge <n> --rebase`;
  never squash; never `--admin` past a required check.
- CLAUDE.md "Fix it right": a cycle or an ordering problem the registry hid is reported with its real size, not
  worked around with lazy lookups.
