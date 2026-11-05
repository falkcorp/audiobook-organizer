<!-- file: docs/agent-tasks/platform-core/R1-11-config-stage-0-mutate.md -->
<!-- version: 1.1.0 -->
<!-- guid: d00a5d7b-4968-462d-afd6-f66e1d7f40e9 -->
<!-- last-edited: 2026-11-05 -->

# R1-11 — 05-config stage 0: the eight bypassing config.AppConfig writes go through Mutate

| Field | Value |
|---|---|
| ID | `R1-11` |
| Tier | **T1** (Sonnet / medium): eight assignments and one AST test |
| Release / wave | R1 / W05 |
| Depends on | R1-01 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-config §1.2, §1.12.1 item 1; 09 §9.4.1 (05-config stage 0) |
| Branch | `agent/platform-core-r1-11-config-stage-0-mutate` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `cmd/root.go`
- `internal/metafetch/openlibrary.go`
- `internal/server/handlers/scheduler_admin.go`
- `internal/server/handlers/system/handler.go`
- `internal/config/direct_writes_test.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-11" -b agent/platform-core-r1-11-config-stage-0-mutate origin/main
cd "$REPO/.worktrees/platform-core-r1-11"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

05-config stage 0 (05-config §1.12.1 item 1; 09 §9.4.1 row "05-config stage 0"): the writes to
`config.AppConfig` outside `internal/config` that bypass `config.Mutate` go through `Mutate`, and an AST
test, `TestNoDirectAppConfigWrites`, fails on any new one. Today these writes race readers and are invisible
to `config.Generation()` (05-config §1.2 row "Writes that bypass `Mutate`"); from F3 the façade's
projection would silently drop them (05-config §1.12.1 item 1). No behaviour change: the same fields get the
same values, now under the config lock.

The spec counts 8 assignments (05-config §1.2, at `748fd787a`). The brief regenerates the list at your base
(the row's rule: AST search outside `internal/config`).

## Dependency check

R1-01 merged: `test -f core/go.mod && echo ok` prints `ok`. Otherwise STOP.

## Re-verify (the work list; expected at `f7ac5b11e`)

```bash
git grep -n -E 'config\.AppConfig(\.[A-Za-z0-9_]+)*\s*(=|\+=|-=|\+\+|--)[^=]' -- '*.go' ':!internal/config/**' ':!*_test.go'
```

Expected exactly these 8 lines (same lines as 05-config §1.2 at `748fd787a`; 05-config says "5 non-test
files", the anchors name 4, and 4 is what HEAD has):

```
cmd/root.go:650:		config.AppConfig.LogLevel = logLevel
internal/metafetch/openlibrary.go:78:			config.AppConfig.OpenLibraryDumpEnabled = true
internal/server/handlers/scheduler_admin.go:275:			config.AppConfig.ScanOnStartup = false
internal/server/handlers/scheduler_admin.go:533:	config.AppConfig.Maintenance.Enabled = req.Enabled
internal/server/handlers/scheduler_admin.go:534:	config.AppConfig.Maintenance.WindowStart = req.WindowStart
internal/server/handlers/scheduler_admin.go:535:	config.AppConfig.Maintenance.WindowEnd = req.WindowEnd
internal/server/handlers/system/handler.go:585:	config.AppConfig.RootDir = ""
internal/server/handlers/system/handler.go:586:	config.AppConfig.SetupComplete = false
```

Line numbers may shift; the eight statements must be the same. If the list has a line not shown here, STOP
and report it (a new write landed; the lead decides whether this PR takes it). If a line is missing, it was
already converted: skip that step.

```bash
grep -n '^func Mutate(fn func(\*Config))' internal/config/config.go     # expect 1 hit (2011 at f7ac5b11e)
grep -n 'foldLegacy' internal/server/handlers/scheduler_admin.go        # expect 4 hits: 151 (comment), 168 (field), 269, 451-452
```

## Steps

1. **Write the test first** (it must fail on the eight writes). Create `internal/config/direct_writes_test.go`
   (package `config_test`) with `TestNoDirectAppConfigWrites`:
   - Walk the module root (`../..` from `internal/config`), skipping `internal/config/`, `vendor/`,
     `node_modules/`, `web/`, `core/`, `.worktrees/`, any directory starting with `.`, and every
     `*_test.go` file.
   - Parse each `.go` file with `go/parser` (`parser.SkipObjectResolution`), find the local name of the
     import `github.com/falkcorp/audiobook-organizer/internal/config` (default `config`; honour an alias;
     skip files that do not import it).
   - Report every `*ast.AssignStmt` (any token except `:=`) and `*ast.IncDecStmt` whose left-hand side,
     after stripping selectors, index expressions, `*` and parentheses, is the selector `<local>.AppConfig`.
   - On any finding, `t.Errorf("%s:%d: direct write to config.AppConfig; use config.Mutate (05-config §1.12.1)", file, line)`.
   - Also fail if fewer than 100 `.go` files were parsed (the walk is broken).
   Run `go test ./internal/config/ -run TestNoDirectAppConfigWrites -count=1`: it must fail listing the 8.
2. `cmd/root.go:650`: replace

   ```go
   	if logLevel != "" {
   		config.AppConfig.LogLevel = logLevel
   	}
   ```

   with

   ```go
   	if logLevel != "" {
   		config.Mutate(func(c *config.Config) { c.LogLevel = logLevel })
   	}
   ```

3. `internal/metafetch/openlibrary.go:78`: replace `config.AppConfig.OpenLibraryDumpEnabled = true` with
   `config.Mutate(func(c *config.Config) { c.OpenLibraryDumpEnabled = true })`. Leave the read on line 76
   unchanged.
4. `internal/server/handlers/scheduler_admin.go`, the `foldLegacy` hook (one function, three edits):
   - field (line 168): `foldLegacy func()` becomes `foldLegacy func(c *config.Config)`; update its doc
     comment (line 151) to say it runs inside `config.Mutate` and writes through `c`.
   - closure (lines 269-276): becomes

     ```go
     		b.foldLegacy = func(c *config.Config) {
     			if !c.ScanOnStartup {
     				return
     			}
     			c.Scheduled.LibraryScan.Enabled = true
     			c.Scheduled.LibraryScan.OnStartup = true
     			c.ScanOnStartup = false
     		}
     ```

   - apply site (lines 451-456): replace the fold call and the `for _, write := range writes` loop with

     ```go
     	if len(writes) > 0 {
     		config.Mutate(func(c *config.Config) {
     			if binding.foldLegacy != nil {
     				binding.foldLegacy(c)
     			}
     			for _, write := range writes {
     				write()
     			}
     		})
     	}
     ```

     `write()` assigns through pointers that `bindingForTask` took into `config.AppConfig` (lines 209-210);
     `Mutate` passes `&AppConfig`, so the same fields change, now under the lock. `write()` and `foldLegacy`
     call nothing that takes the config lock (Mutate is not re-entrant, `config.go:4133`).
5. `internal/server/handlers/scheduler_admin.go:533-535`: replace the three assignments with

   ```go
   	config.Mutate(func(c *config.Config) {
   		c.Maintenance.Enabled = req.Enabled
   		c.Maintenance.WindowStart = req.WindowStart
   		c.Maintenance.WindowEnd = req.WindowEnd
   	})
   ```

6. `internal/server/handlers/system/handler.go:585-586`: replace the two assignments with

   ```go
   	config.Mutate(func(c *config.Config) {
   		c.RootDir = ""
   		c.SetupComplete = false
   	})
   ```

   It follows `config.ResetToDefaults()`, which returns after its own `Mutate`, so there is no nesting.
7. Register the gate in `ci/gates.txt`? No: the row's files do not include it and `go test ./internal/config/`
   already runs it in the short suite. Do not edit `ci/gates.txt`.
8. Header bump on the five files; headerless fragment `changelog.d/<today>_appconfig_writes_mutate.md`
   (`### Fixed`: "Eight configuration writes outside the config package now go through the config lock.").
9. Commit: `fix(config): route the eight bypassing AppConfig writes through Mutate (05-config stage 0)`.

## Tests (write first)

- `TestNoDirectAppConfigWrites` (spec name, 05-config §1.12.1) in `internal/config/direct_writes_test.go`:
  red with the 8 writes, green after steps 2–6.
- The existing tests of the touched packages stay green unchanged:
  `go test ./internal/server/handlers/ -run 'TaskConfig|Maintenance|Scheduler' -count=1`,
  `go test ./internal/server/handlers/system/ -count=1`, `go test ./internal/metafetch/ -count=1`,
  `go test ./cmd/ -count=1`.

## Code contract

`func Mutate(fn func(*Config))` is unchanged. One internal signature changes: the `foldLegacy` field becomes `func(c *config.Config)`.

## Acceptance

```bash
git grep -n -E 'config\.AppConfig(\.[A-Za-z0-9_]+)*\s*(=|\+=|-=|\+\+|--)[^=]' -- '*.go' ':!internal/config/**' ':!*_test.go' | wc -l   # expect 0
go test ./internal/config/ -run TestNoDirectAppConfigWrites -count=1 -v   # expect "--- PASS: TestNoDirectAppConfigWrites"
go test ./internal/server/handlers/... ./internal/metafetch/ ./cmd/ -short -count=1   # expect ok for every package
go vet ./... && go -C core vet ./...                                      # expect exit 0
make ci                                                                    # expect exit 0 and "All CI checks passed"
```

## Out of scope

Pointer aliases of `config.AppConfig` handed to other code (`&config.AppConfig` in about 30 non-test places)
and writes in test files: the façade (05-config §1.12.1 item 2, F3) covers them. Reads. Any change in what is
persisted.

## Rollback

Revert the commit. No stored data changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

## Planner notes

- 05-config §1.2 says the 8 bypassing writes sit in "5 non-test files"; its own anchors name 4 files, and HEAD
  (`f7ac5b11e`) has the same 8 writes in 4 files. The brief uses the regenerated list.
- Outside this row's scope but the same defect class: about 30 non-test sites hand `&config.AppConfig` (or a
  field address, e.g. `scheduler_admin.go:209-210,302`, `server.go:982`, `wire_handlers.go:691`) to code that can
  write through the pointer without the lock, which `TestNoDirectAppConfigWrites` cannot see. The brief fixes the
  one such write path inside the function it already edits (the task-config writes). The F3 façade
  (05-config §1.12.1 item 2) is where the rest is decided; the lead may want a 05-config line naming it.
