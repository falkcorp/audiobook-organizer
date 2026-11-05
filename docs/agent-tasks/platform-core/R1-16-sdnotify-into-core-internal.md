<!-- file: docs/agent-tasks/platform-core/R1-16-sdnotify-into-core-internal.md -->
<!-- version: 1.1.2 -->
<!-- guid: 16c6bd67-7abe-40f4-ad12-2d2cfbca9f76 -->
<!-- last-edited: 2026-11-05 -->

# R1-16 — internal/sdnotify into core/internal/sdnotify, with all five ABO importers rewired (the kernel owns READY/STATUS/STOPPING; app.ExtendStart; module.Deps.ExtendStart; lifecycle.NotifySocketEnv)

| Field | Value |
|---|---|
| ID | `R1-16` |
| Tier | **T2** (Sonnet / high): one leaf move whose five importers must all be rewired in one PR |
| Release / wave | R1 / W16 |
| Depends on | R1-15 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 04 §8.2 (mediacore/internal/sdnotify row), §8.3; 05-health §5H.10 Landing R1 (Supervisor); 06 §6.4.5 |
| Branch | `agent/platform-core-r1-16-sdnotify-into-core-internal` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/internal/sdnotify/`
- `internal/sdnotify/`
- `core/kernel/supervisor.go`
- `core/lifecycle/notify.go`
- `main.go`
- `internal/server/server_lifecycle.go`
- `internal/database/startup_notify.go`
- `internal/search/register.go`
- `internal/operations/registry/`: `subprocess.go`, `subprocess_env_internal_test.go`
- `tools/cmd/sdkguard/internal-deps.txt`
- `internal/books/module.go`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-16" -b agent/platform-core-r1-16-sdnotify-into-core-internal origin/main
cd "$REPO/.worktrees/platform-core-r1-16"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Move `internal/sdnotify` to `core/internal/sdnotify` (`github.com/falkcorp/mediacore/internal/sdnotify`) and
rewire **all five** ABO importers in this PR, because Go's `internal/` rule makes the new path unimportable from the
root module (04 §8.2 row `mediacore/internal/sdnotify`, §8.3 rows for `internal/sdnotify`; 04 §3 row
`internal/sdnotify`; 05-health §5H.10 "Landing by release" R1 `Supervisor`; 06 §6.4.5). The kernel owns
`READY=1`/`STATUS=`/`STOPPING=1`; ABO code gets what it needs as injected values.

old ==> new: `internal/sdnotify` ==> `github.com/falkcorp/mediacore/internal/sdnotify` (core-internal).

| ABO importer (anchor at `f7ac5b11e`) | becomes (04 §8.3) |
|---|---|
| `.` — `main.go:53` `fmt.Println(sdnotify.Capability)` (`--print-capabilities` fast exit) | handled by `app` (the fast exit moves into core's `app`; ABO's `main` no longer prints it itself) |
| `internal/server` — `server_lifecycle.go:340,343` (`Ready`, `Status`) and `:552,555` (`Stopping`, `Status`) | deleted: the kernel supervisor sends them (R1-13's `kernel.ReadinessNotifier`, implemented here over `core/internal/sdnotify` in `core/kernel/supervisor.go`) |
| `internal/database` — `startup_notify.go:24` `sdnotify.ExtendTimeout(sdnotify.StartupStepBudget)` | the stateless `app.ExtendStart`, injected by `serve` as a `func(step string)` (until R1-23 the store opens before `app.New`, 06 §6.4.5) |
| `internal/search` — `register.go:55` (same call) | `IndexService` takes an injected `func(step string)`, wired by `internal/books` from `module.Deps.ExtendStart` (05-health §5H.3.4), so a layer-2 package imports no `module` (MOD4-18) |
| `internal/operations/registry` — `subprocess.go:70` `sdnotify.SocketEnv` (child-env scrub) and its test `subprocess_env_internal_test.go` | the public constant `lifecycle.NotifySocketEnv` in `core/lifecycle/notify.go` (03 §3.3 `lifecycle` row) |

## Contract

- `core/lifecycle/notify.go`: `const NotifySocketEnv = "NOTIFY_SOCKET"` (03 §3.3; the value of HEAD's
  `sdnotify.SocketEnv`, `sdnotify.go:63`).
- `core/internal/sdnotify`: HEAD's file moved verbatim (package name unchanged), `SocketEnv` kept as its own
  internal constant or replaced by `lifecycle.NotifySocketEnv` (one source; pick the latter and delete the duplicate).
- `app.ExtendStart(step string)` (stateless, 06 §6.4.5) and `module.Deps.ExtendStart` (06 §6.4.10 R1 row); both
  call the supervisor's extend with `StartupStepBudget` (10 min, `sdnotify.go:53`), exactly HEAD's semantics.
- The kernel supervisor (`core/kernel/supervisor.go`): sends `READY=1` and the serving `STATUS=` at start step 8
  (readiness true), `STOPPING=1` and the shutting-down `STATUS=` at the first stop step (06 §6.5.2); a write error is
  logged, never fatal (HEAD's behaviour, `server_lifecycle.go:340-343`).

## Dependency check

R1-15 merged: `test -d internal/books && echo ok`.

## Re-verify (expected at `f7ac5b11e`)

```bash
M=github.com/falkcorp/audiobook-organizer
ls internal/sdnotify                                           # expect sdnotify.go sdnotify_test.go
git grep -l "\"$M/internal/sdnotify\"" -- '*.go'               # expect 6 files: main.go, internal/database/startup_notify.go,
                                                               #   internal/operations/registry/subprocess.go, .../subprocess_env_internal_test.go,
                                                               #   internal/search/register.go, internal/server/server_lifecycle.go
git grep -n 'sdnotify\.' -- main.go internal/database/startup_notify.go internal/search/register.go internal/server/server_lifecycle.go internal/operations/registry/subprocess.go | wc -l   # expect 8 (lines above)
grep -c 'internal/sdnotify' tools/cmd/sdkguard/internal-deps.txt   # expect 1 (regenerate it, step 6)
grep -n '"internal/sdnotify"' internal/arch/layering_test.go       # expect 1 layerOf key
```

R1-15 changed `main.go` and `server_lifecycle.go`; if the `sdnotify` lines moved, locate them by the `sdnotify.`
grep and apply the table row by row.

## Steps (one commit that builds)

1. Tests first: `core/kernel` test that a fake notifier receives `Ready` and `Status` after start step 8 and
   `Stopping` before listeners close (name coined here: `TestSupervisor_NotifiesReadyAndStopping`); a test that
   `app.ExtendStart` extends through the notifier (`TestExtendStart_ExtendsTimeout`, coined); the existing
   `sdnotify_test.go` moves with the package.
2. `git mv internal/sdnotify/sdnotify.go internal/sdnotify/sdnotify_test.go core/internal/sdnotify/`; update their
   `file:` lines; implement `core/lifecycle/notify.go` and the supervisor's notifier.
3. Rewire the five importers per the table. `internal/search`: add the injected `func(step string)` to
   `IndexService` (constructor or field), set by `internal/books/module.go` (in the row) with the kernel's
   `Deps.ExtendStart`; `internal/operations/registry/subprocess_env_internal_test.go` imports `internal/sdnotify` too
   and is rewired with `subprocess.go`; `internal/database`: `serve` injects
   `app.ExtendStart`. `main.go`: drop the `--print-capabilities` branch and let `app` serve it (keep the flag's output
   byte-identical: it prints `sd_notify`, `sdnotify.go:44`).
4. `internal/arch/layering_test.go`: delete the `"internal/sdnotify"` key.
5. `scripts/ci/short_test_weights.json`: no entry names it (check: `grep -c sdnotify scripts/ci/short_test_weights.json` → 0).
6. `go run ./tools/cmd/sdkguard/main.go -update`, bump that file's version line; the only diff is the removed
   `internal/sdnotify` line (the file is in this row's list).
7. Header bumps; fragment `changelog.d/<today>_sdnotify_core.md` (`### Changed`: "Internal: systemd readiness
   notifications are sent by the core runtime."). Commit:
   `refactor(core): sdnotify into core/internal with every ABO user rewired`. PR lists the `old ==> new` line.

## Acceptance

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -n "\"$M/internal/sdnotify\"\|\"github.com/falkcorp/mediacore/internal/sdnotify\"" -- '*.go' ':!core/**' | wc -l   # expect 0
test ! -e internal/sdnotify && echo GONE                                              # expect GONE
make build-api && ./audiobook-organizer --print-capabilities | grep -x sd_notify      # expect sd_notify
go -C core test ./internal/sdnotify/ ./kernel/ ./lifecycle/ -count=1                  # expect ok
go test ./internal/search/ ./internal/operations/registry/ ./internal/database/ -short -count=1   # expect ok
make sdkguard                                                                          # expect "SDK guard passed"
make ci && make -C core ci                                                             # expect exit 0
```

`unit-lifecycle` (R1-28) later proves READY/EXTEND under systemd; nothing here may change the unit.

## Out of scope

The watchdog ping (F8); `internal/startup` (stays ABO until F8).

## Rollback

Revert the PR.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.


## Planner notes

- 04 §8.2's count is 4 importer packages; the row title says five (04 §3's row counts `internal/database` as the
  fifth). Both agree once `internal/database` is counted; the brief rewires all five.
