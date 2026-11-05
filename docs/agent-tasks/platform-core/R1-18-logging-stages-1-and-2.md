<!-- file: docs/agent-tasks/platform-core/R1-18-logging-stages-1-and-2.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0af0f945-e47e-4822-ac2b-f63544c667ad -->
<!-- last-edited: 2026-11-05 -->

# R1-18 — 05-logging stage 1 (the second slog.SetDefault, log.SetOutput and setupFileLogging deleted, the activity Writer fed by a books Logging.Tap, core file output) and stage 2 (the books logger shim)

| Field | Value |
|---|---|
| ID | `R1-18` |
| Tier | **T2** (Sonnet / high): small deletions plus a tap; the activity feed must keep its output |
| Release / wave | R1 / W15 |
| Depends on | R1-15, R1-07 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-logging §11 items 1-2, 5; 09 §9.4 R1 Content: runtime, release note (05-logging's log-file and level lines) |
| Branch | `agent/platform-core-r1-18-logging-stages-1-and-2` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `cmd/root.go`
- `internal/server/server.go`
- `internal/activity/`
- `internal/logger/`
- `core/log/`
- `internal/books/logging.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-18" -b agent/platform-core-r1-18-logging-stages-1-and-2 origin/main
cd "$REPO/.worktrees/platform-core-r1-18"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

05-logging's R1 stages (05-logging §11 items 1, 2 and 5; 09 §9.4 R1 "Content: runtime", the release note's
05-logging lines). R1-15 already calls `app.InstallProcessDefaults`, so every `slog.*` call and every
`logger.New` var reaches the App pipeline. This PR removes what HEAD layered on top and keeps the activity feed's
output identical:

1. **Stage 1 deletions (05-logging §11 item 1), nothing else:** `setupFileLogging` (`cmd/root.go:698-723` at
   `f7ac5b11e`, and its three call sites `:108,201,260`), the second `slog.SetDefault` and `log.SetOutput(aw)`
   (`internal/server/server.go:1130-1155` at `f7ac5b11e`; 05-logging cites `server.go:1134-1155` at `748fd787a`).
2. **The activity `Writer` keeps its input** through a books `Logging.Tap` at info that renders each record with a
   `slog.TextHandler` into it (`internal/books/logging.go`), so its line parsing and its `system` entries are
   unchanged; ABO's `StandardLogger` keeps writing system-log rows and HEAD's pruner
   (`internal/logger/retention.go`) keeps pruning them until R2 (05-logging §11 item 1; row notes).
3. **File output from core:** `core.log.output=file` and `core.log.file.path` set by the books module's defaults to
   keep a file under the state dir (the old cwd-relative path is not reproduced; release-noted). In R1 there is no
   core config, so these reach the kernel as `app.Options` values ABO's `main` sets (04 §8.1 R1: "no core `config`
   yet"); implement the file sink in `core/log`.
4. **Stage 2, the shim (05-logging §11 item 2):** `internal/logger` becomes the books-side shim
   `logger.FromSlog(*slog.Logger) Logger`: printf methods format, then log with `subsystem` as an **attribute**, so
   per-subsystem levels work. Progress, changes and cancel keep their HEAD behaviour until the tasks flip.
5. **Item 5:** `internal/logger/logtest` stays ABO for ABO's tests; core tests use `coretest`'s injected capture
   (R1-09), never `slog.SetDefault`.
6. `log_level`/`--log-level` set the handler level (05-logging §11 item 6: "today it does not, §2"): pass it to the
   App's logging options so it now takes effect; release-note it.

## Tests (write first; names coined here unless marked)

- `TestActivityTapKeepsSystemEntries`: a record logged at info through the App pipeline arrives in the activity
  `Writer` as the same `system` entry HEAD produced (golden from HEAD's text rendering).
- `TestNoSecondDefaultLogger`: after boot, `slog.Default()`'s handler is the App pipeline's (no second
  `SetDefault`), and `log.Writer()` is not the activity writer.
- `TestFileOutputUnderStateDir`: with the books defaults, the log file is created under the state dir and receives
  records.
- `TestShim_SubsystemIsAttribute`: `logger.FromSlog(l).Info("x %d", 1)` emits message `x 1` with
  `subsystem=<name>` as an attribute; a per-subsystem level filters it.
- `TestLogLevelFlagApplies`: `--log-level debug` makes debug records reach the handler.
- Existing: `internal/activity` and `internal/logger` suites unchanged.

## Dependency check

R1-15 and R1-07 merged: `test -d internal/books && test -d core/log && git grep -q 'InstallProcessDefaults' -- core/app && echo ok`.

## Re-verify (expected at `f7ac5b11e`)

```bash
grep -n 'func setupFileLogging\|setupFileLogging()' cmd/root.go       # expect 698 and call sites 108, 201, 260
grep -n 'log.SetOutput\|slog.SetDefault' internal/server/server.go    # expect 1139 and 1154
grep -n 'slog.SetDefault' cmd/root.go                                 # expect 718 (inside setupFileLogging)
grep -n '^func New(\|^func NewWithActivityLog(' internal/logger/standard.go   # expect 25 and 33
ls internal/activity/writer.go internal/logger/retention.go           # expect both (they stay until R2)
```

R1-11 changed `cmd/root.go:650` only; R1-15 changed `cmd/root.go`'s `serve` wiring. Locate each line by its grep.

## Steps

1. Tests first. 2. The deletions (item 1) and the tap (item 2) in one commit, so the feed never loses records
between commits. 3. File sink in `core/log` and the books defaults (in `internal/books/logging.go`, passed where `cmd/root.go`'s
`serve` builds `app.Options`; do not edit `main.go`, which R1-16 edits in this wave). 4. The shim. 5. The log-level fix.
6. Header bumps; fragment `changelog.d/<today>_logging_r1.md` with 05-logging's R1 release-note lines: the log
file's new location under the state directory, and that `log_level`/`--log-level` now set the level. Commit(s):
`refactor(logging): remove the second default logger and file setup; activity tap`,
`feat(log): core file output`, `refactor(logger): books shim over slog`.

## Code contract

`logger.FromSlog(*slog.Logger) Logger` (05-logging §11 item 2); the books `Logging.Tap` consumer in `internal/books/logging.go`.

## Acceptance

```bash
git grep -n 'setupFileLogging\|log.SetOutput' -- cmd internal | grep -v _test | wc -l     # expect 0
git grep -n 'slog.SetDefault' -- cmd internal | grep -v _test | wc -l                     # expect 0 (the only install is core's InstallProcessDefaults)
go test ./internal/activity/ ./internal/logger/... ./internal/books/ ./cmd/ -count=1      # expect ok
go -C core test ./log/ -count=1                                                            # expect ok
make test-e2e                                                                              # expect green (Activity page shows system entries)
make ci && make -C core ci                                                                 # expect exit 0
```

## Out of scope

The journal, M5, deleting the activity `Writer`'s parsing or `StandardLogger`'s system-log writes (R2);
call-site injection (optional ratchet, 05-logging §11 item 3).

## Rollback

Revert the PR; the second default logger and the cwd-relative log file return.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
