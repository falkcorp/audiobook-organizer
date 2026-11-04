<!-- file: docs/agent-tasks/platform-core/PC-03-05-config-stage-0-mutate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1da47223-8084-4cec-a370-47ad17f4261f -->
<!-- last-edited: 2026-11-04 -->

# PC-03-05 — 05-config stage 0: the eight bypassing `config.AppConfig` writes go through `Mutate`

| Field | Value |
|---|---|
| ID | PC-03-05 |
| Tier | **T1 Sonnet/medium.** Eight assignments in four files, each with its exact replacement below, plus an AST test built from helpers that already exist. |
| Wave | W03 (S1, release `S1`) |
| Depends on | PC-03-02 (shares `internal/server/handlers/system/handler.go`, `internal/server/`) |
| Blocks | PC-03-06 |
| Trailer | `Stage-Release: S1 1` |
| Digest | `docs/plans/platform-core/digests/S1.md` §5 rule 5 |
| Spec | 05-config §1.2 (row "Writes that bypass `Mutate`", with the anchors), §1.12.1 item 1 ("Stage 0 … add `TestNoDirectAppConfigWrites` (AST: no assignment to `config.AppConfig` outside `internal/config`)"), out-of-scope defect 1; 09 S1 row "ABO-local fixes"; 09 §9.4.1 row "05-config stage 0" |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-03-05" -b platform-core/pc-03-05-config-stage0 origin/main
cd "$REPO/../audiobook-organizer-pc-03-05"
git rebase origin/main
git worktree list
make web-deps
```

Precondition: PC-03-02 merged (`ls core/library` lists files). Otherwise STOP.

## Exact files

Create: `internal/config/direct_writes_test.go`. Modify: `cmd/root.go`, `internal/metafetch/openlibrary.go`,
`internal/server/handlers/scheduler_admin.go`, `internal/server/handlers/system/handler.go`.

## Code contract

`func Mutate(fn func(*Config))` (`internal/config/config.go`, unchanged): every write below runs inside one call.
Test name from the spec: `TestNoDirectAppConfigWrites`.

## Re-verify (expected at the planning checkout `51a2c2acd`; the spec's anchors at `748fd787a` are the same lines)

```bash
grep -rnE --include=*.go 'config\.AppConfig(\.[A-Za-z0-9_]+)+ *(=|\+=|-=)[^=]' . | grep -v '_test.go' | grep -v '^./internal/config/'
# exactly these 8 lines (line numbers may move; the text must match):
# ./cmd/root.go:650:		config.AppConfig.LogLevel = logLevel
# ./internal/metafetch/openlibrary.go:78:			config.AppConfig.OpenLibraryDumpEnabled = true
# ./internal/server/handlers/scheduler_admin.go:275:			config.AppConfig.ScanOnStartup = false
# ./internal/server/handlers/scheduler_admin.go:533:	config.AppConfig.Maintenance.Enabled = req.Enabled
# ./internal/server/handlers/scheduler_admin.go:534:	config.AppConfig.Maintenance.WindowStart = req.WindowStart
# ./internal/server/handlers/scheduler_admin.go:535:	config.AppConfig.Maintenance.WindowEnd = req.WindowEnd
# ./internal/server/handlers/system/handler.go:585:	config.AppConfig.RootDir = ""
# ./internal/server/handlers/system/handler.go:586:	config.AppConfig.SetupComplete = false
grep -n '^func Mutate(fn func(\*Config))' internal/config/config.go                 # 1 hit
grep -n 'if len(writes) > 0 && binding.foldLegacy != nil {' internal/server/handlers/scheduler_admin.go   # 1 hit (line 451)
grep -n '^func appConfigLocalName\|^func appConfigWriteTarget\|^func appConfigRepoRoot' internal/config/appconfig_reads_test.go   # 3 hits
```

More or fewer than 8 lines: STOP and report the list (feature work moved them). The spec counts "8 assignments in
5 non-test files" but its anchors name 4 files; this brief follows the anchors (Planner notes).

## Steps (exact replacements)

1. `cmd/root.go` (the `if logLevel != ""` block):
   ```go
   if logLevel != "" {
   	config.Mutate(func(c *config.Config) { c.LogLevel = logLevel })
   }
   ```
2. `internal/metafetch/openlibrary.go`:
   ```go
   config.Mutate(func(c *config.Config) { c.OpenLibraryDumpEnabled = true })
   ```
3. `internal/server/handlers/scheduler_admin.go`, `foldLegacy` (line 275) **and** its caller: the closure also writes
   through the `sched`/`maint` pointers into `config.AppConfig`, so the correct fix runs the fold and the field writes
   under one `Mutate`. Replace the caller block
   ```go
   if len(writes) > 0 && binding.foldLegacy != nil {
   	binding.foldLegacy()
   }
   for _, write := range writes {
   	write()
   }
   ```
   with
   ```go
   if len(writes) > 0 {
   	// Fold and writes go through Mutate as one write (05-config stage 0): the
   	// binding's targets point into config.AppConfig, which Mutate locks.
   	config.Mutate(func(*config.Config) {
   		if binding.foldLegacy != nil {
   			binding.foldLegacy()
   		}
   		for _, write := range writes {
   			write()
   		}
   	})
   }
   ```
   and inside `foldLegacy` keep the three statements but change `config.AppConfig.ScanOnStartup = false` to a write
   through a pointer taken at the top of `bindingForTask`, next to `sched` and `maint`:
   `legacy := &config.AppConfig.ScanOnStartup` … `*legacy = false`, and read it as `if !*legacy { return }`. (The
   pointer is only dereferenced inside the `Mutate` above.)
4. `internal/server/handlers/scheduler_admin.go` lines 533–535:
   ```go
   config.Mutate(func(c *config.Config) {
   	c.Maintenance.Enabled = req.Enabled
   	c.Maintenance.WindowStart = req.WindowStart
   	c.Maintenance.WindowEnd = req.WindowEnd
   })
   ```
5. `internal/server/handlers/system/handler.go` lines 585–586:
   ```go
   config.Mutate(func(c *config.Config) {
   	c.RootDir = ""
   	c.SetupComplete = false
   })
   ```

Before each replacement, check the enclosing function is not itself called inside a `config.Mutate` callback
(`Mutate` is not re-entrant): `grep -rn 'config.Mutate' <file>` must show no `Mutate` whose callback calls the
function you edit. If one does, STOP.

## TDD tests to write first: `internal/config/direct_writes_test.go`

`TestNoDirectAppConfigWrites` (spec name): walk every non-test `.go` file under `internal/`, `cmd/`, `pkg/`, `tools/`
and the repository root's own `*.go` files (skip `internal/config/`, `vendor/`, `node_modules/`, `testdata/`,
`core/` and dot-directories), parse with `go/parser`, take the file's local name for the `internal/config` import with
the existing `appConfigLocalName`, and report every `*ast.AssignStmt` LHS and `*ast.IncDecStmt` operand for which the
existing `appConfigWriteTarget(e, alias)` is true, as `path:line`. The test fails when the list is non-empty and
prints it. Locate the root with the existing `appConfigRepoRoot`. Add one table case in the same file showing it
catches `config.AppConfig.X = 1`, `config.AppConfig.M["k"] = v` and `config.AppConfig.N++` in a parsed snippet, and
ignores `x := config.AppConfig.X`.

Run it before the replacements: it must fail listing exactly the 8 lines. Then make the replacements; it passes.

## Acceptance

```bash
go test ./internal/config/ -run 'TestNoDirectAppConfigWrites|TestAppConfigDirectReadRatchet' -count=1 -v 2>&1 | grep -E '^(--- |ok)'
                                               # --- PASS: TestNoDirectAppConfigWrites, --- PASS: TestAppConfigDirectReadRatchet, ok
grep -rnE --include=*.go 'config\.AppConfig(\.[A-Za-z0-9_]+)+ *(=|\+=|-=)[^=]' . | grep -v '_test.go' | grep -v '^./internal/config/' | wc -l   # 0
go test ./internal/server/handlers/ ./internal/server/handlers/system/ ./internal/metafetch/ ./cmd/ -count=1 -race   # ok for each
make ci; echo "exit=$?"                        # exit=0
```

If `TestAppConfigDirectReadRatchet` prints a `NOTE … lower` line, the baseline may be lowered by the ratchet bot;
do not edit the baseline by hand in this PR unless the test fails.

## Out of scope

The façade (05-config stage 1, S3); the `&config.AppConfig` pointer passing in other files (not writes); any
config value change. No stored data changes.

## Rollback

Revert the PR.

## Repo rules

- Worktree only; never `go work init`. Headers bumped on every changed file; one `changelog.d/` fragment.
- Every commit carries `Stage-Release: S1 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase` after the required checks pass (this PR touches `cmd/**`, so `unit-lifecycle` runs).

## Planner notes

- 05-config §1.2 says "8 assignments in 5 non-test files" but lists 4 files; the inventory row copies "five". The
  re-verify grep finds the same 8 lines in 4 files at `51a2c2acd`.
- `scheduler_admin.go`'s `bindingForTask` writes `config.AppConfig` through field pointers (`sched.LibraryScan.Enabled = true`
  and every `write()`), which bypass `Mutate` too but are invisible to the spec's count and to an AST check on
  `config.AppConfig` selectors. Step 3 puts them under the same `Mutate` because it is the same defect at the same
  call site; the reviewer may narrow it to the one counted line if preferred.
