<!-- file: docs/agent-tasks/platform-core/PC-04-04-inject-start-extension-functions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 64d22444-e50d-4aab-a253-c4d58e416770 -->
<!-- last-edited: 2026-11-04 -->

# PC-04-04 — Inject the start-extension and notify functions so ABO packages below layer 6 never import app (behaviour-neutral)

| Field | Value |
|---|---|
| ID | PC-04-04 |
| Tier | **T1 Sonnet/medium** — a function seam threaded through three packages and one constant added to core; no behaviour changes and every existing test stays as it is |
| Wave | W04 (stage S2, release `S2r1`), serialized step 1 (may run beside PC-04-01, PC-04-02, PC-04-06: no shared file) |
| Depends on | OWNER-03 (the `S1` soak row) |
| Trailer | `Stage-Release: S2 1` on every commit |
| Spec | 05-health §5H.3.4 (use 1, "Startup steps"); 04 §8.3 rows `internal/sdnotify → mediacore/internal/sdnotify`; 04 §8.4 note 3; 03 §3.3 `lifecycle` row; 09 §9.4 S2 "Moves" |
| Digest | `docs/plans/platform-core/digests/S2.md` §3 (the `mediacore/internal/sdnotify` row) |

**Gate.** You may open the PR and make it green at any time. It merges only after OWNER-03 has merged the
`S1` row into `ci/soak.txt`; `check_soak.py` refuses the merge otherwise.

**What this task is.** At S2 the package `internal/sdnotify` moves to `core/internal/sdnotify` (PC-04-07), where
Go's `internal/` rule makes it non-importable from ABO. Its five ABO importers must stop importing it first. This PR
prepares three of them, behaviour-neutral:

- `internal/database` and `internal/search` each get an injectable start-extension function `func(step string)`.
  While nothing is injected they keep calling `internal/sdnotify` exactly as at HEAD, so every existing
  startup-notify test passes unchanged. PC-04-07 injects `app.ExtendStart` (database, from `serve`) and
  `Deps.ExtendStart` (search, from `books.Module()`) and deletes the HEAD fallback with the move.
- `internal/operations/registry` stops importing `internal/sdnotify` completely and reads the public constant
  `lifecycle.NotifySocketEnv` from core instead (04 §8.3 row: "`subprocess.go:70` and its test use
  `lifecycle.NotifySocketEnv`").

Below layer 6 an ABO package never imports `module` or `app` (05-health §5H.3.4; 03 §3.5.1). Nothing in this PR
imports either; `app` does not exist yet when this PR runs.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-04-04 -b feat/pc-04-04-inject-start-extension origin/main
cd ../audiobook-organizer-pc-04-04
git rebase origin/main
git worktree list            # confirm you are in the new worktree, not the primary checkout
make web-deps                # or: npm ci --prefix web   (before PC-01-06 merged)
```

Do not run `go work init` anywhere (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| modify | `internal/database/startup_notify.go` |
| modify | `internal/database/store.go` (one call site, see step 2) |
| modify | `internal/database/migrations.go` (one call site, see step 2) |
| modify | `internal/search/register.go` |
| modify | `internal/operations/registry/subprocess.go` |
| modify | `internal/operations/registry/subprocess_env_internal_test.go` |
| create | `core/lifecycle/notify.go` (name coined here) |
| create | `internal/database/startup_extender_test.go` (name coined here) |
| create | `internal/search/start_extender_test.go` (name coined here) |
| create | `changelog.d/<date>_pc_04_04_start_extender.md` (headerless fragment) |
| no change | `main.go` (listed in the inventory row; see Planner notes) |

Do **not** edit `internal/database/startup_notify_test.go` or `internal/search/register_sdnotify_test.go`: the
done-check is that they pass unchanged.

## Re-verify (run at your base commit; counts measured at `f07f516be`)

```bash
# 1. The five non-test importers of internal/sdnotify plus one registry test (expect 6 files).
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/sdnotify"' --include=*.go . | sort
#    expect exactly: ./internal/database/startup_notify.go, ./internal/operations/registry/subprocess.go,
#                    ./internal/operations/registry/subprocess_env_internal_test.go, ./internal/search/register.go,
#                    ./internal/server/server_lifecycle.go, ./main.go
# 2. Edit target: the database helper (expect 1 definition).
grep -n '^func extendStartTimeout()' internal/database/startup_notify.go
# 3. Its two production call sites (expect exactly 2: store.go ~1845, migrations.go ~559).
grep -rn 'extendStartTimeout()' --include=*.go internal/database | grep -v _test
# 4. Edit target in search (expect 1).
grep -n 'sdnotify.ExtendTimeout(sdnotify.StartupStepBudget)' internal/search/register.go
# 5. Edit target in registry (expect 1).
grep -n 'sdnotify.SocketEnv+"="' internal/operations/registry/subprocess.go
# 6. The registry test that names the constant (expect 1).
grep -n 'sdnotify.SocketEnv != "NOTIFY_SOCKET"' internal/operations/registry/subprocess_env_internal_test.go
# 7. core/lifecycle exists (created by PC-03-01's move; expect a non-empty list) and has no NotifySocketEnv yet (expect 0).
ls core/lifecycle/*.go
grep -rn 'NotifySocketEnv' core/lifecycle | wc -l
# 8. The core module path (expect: module github.com/falkcorp/mediacore, or the path OWNER-01 recorded for Q-H).
head -1 core/go.mod
# 9. registry already imports core/lifecycle after PC-03-01 (expect >= 1 file).
grep -rln '"github.com/falkcorp/mediacore/lifecycle"' --include=*.go internal/operations/registry
```

At `f07f516be` greps 1–6 give the counts above. PC-02-06 (S0) edits `internal/database/startup_notify.go` to add
its fake-migration hook, so the file at your base is longer than at `f07f516be`; that is expected. **STOP and
report** if grep 2, 3, 4, 5 or 6 returns 0 hits, if grep 7 shows no `core/lifecycle` package or an existing
`NotifySocketEnv`, or if grep 8 names a module path other than the one OWNER-01 recorded.

## Steps

1. **core constant.** Create `core/lifecycle/notify.go` with the file header and:

   ```go
   package lifecycle

   // NotifySocketEnv names the variable systemd sets for a Type=notify service
   // (sd_notify(3)). Apps that start child processes strip it from the child's
   // environment, because only the main process may notify (NotifyAccess=main).
   // 03 §3.3 lifecycle row; 04 §8.3.
   const NotifySocketEnv = "NOTIFY_SOCKET"
   ```

   It must equal HEAD's `sdnotify.SocketEnv` (`internal/sdnotify/sdnotify.go`, value `"NOTIFY_SOCKET"`).

2. **database seam.** In `internal/database/startup_notify.go`:
   - Keep the import of `internal/sdnotify` (removed by PC-04-07).
   - Add the exported type, the setter and an `atomic.Pointer` (import `sync/atomic`):

     ```go
     // StartExtender is called right before a startup step that makes real
     // progress (store open, each applied migration). serve injects
     // app.ExtendStart from S2 (05-health §5H.3.4 use 1; 04 §8.3).
     type StartExtender func(step string)

     var startExtender atomic.Pointer[StartExtender]

     // SetStartExtender installs fn and returns a func that restores the
     // previous value. A nil fn restores HEAD's behaviour.
     func SetStartExtender(fn StartExtender) (restore func()) {
         var p *StartExtender
         if fn != nil {
             p = &fn
         }
         prev := startExtender.Swap(p)
         return func() { startExtender.Store(prev) }
     }
     ```
   - Add `func extendStartStep(step string)`: if `startExtender.Load()` is non-nil, call it with `step` and
     return; otherwise run HEAD's body unchanged (the `sdnotify.ExtendTimeout(sdnotify.StartupStepBudget)` call and
     its `Warn` on error).
   - Keep `func extendStartTimeout()` with its doc comment and make its body `extendStartStep("database")`. It
     stays because the unchanged `startup_notify_test.go` calls it.
   - Change the two production call sites found by grep 3:
     `internal/database/store.go` → `extendStartStep("store-open")`;
     `internal/database/migrations.go` → `extendStartStep(fmt.Sprintf("migration-%d", m.Version))`
     (`fmt` is already imported there; check with `grep -n '"fmt"' internal/database/migrations.go`).

3. **search seam.** In `internal/search/register.go`:
   - Add the same exported type, setter and atomic pointer under the names `StartExtender`,
     `SetStartExtender` and `startExtender` (package `search`).
   - Add an unexported field `extendStart func(step string)` to `IndexService`.
   - In `Start`, replace the `if err := sdnotify.ExtendTimeout(...)` block with:

     ```go
     b.extendStartStep("search-index-open")
     ```
     and add the method: if `b.extendStart != nil` call it; else if `startExtender.Load()` is non-nil call that;
     else run HEAD's block unchanged (`sdnotify.ExtendTimeout(sdnotify.StartupStepBudget)` with the
     `indexLog.Warn` on error).
   - Leave the `init()` registration exactly as it is. Keep the `internal/sdnotify` import (removed by PC-04-07).
   - The field exists so that `books.Module()` can hand the service `Deps.ExtendStart` at PC-04-07 (05-health
     §5H.3.4: "its `IndexService` takes an injected `func(step string)`, which `books.Module()` wires").

4. **registry rewire.** In `internal/operations/registry/subprocess.go` replace `sdnotify.SocketEnv` with
   `lifecycle.NotifySocketEnv`, import `github.com/falkcorp/mediacore/lifecycle` (use the alias the file's other
   imports already use for that package, if any), and delete the `internal/sdnotify` import. In
   `subprocess_env_internal_test.go` change `TestChildBaseEnv_NotifySocketName` to assert
   `lifecycle.NotifySocketEnv == "NOTIFY_SOCKET"` and drop its `internal/sdnotify` import. Update the comment above
   `childBaseEnv` only where it names `sdnotify`.

5. **Tests first (write them before steps 2–3, see TDD).**

6. Bump the header (version minor, `last-edited`) of every modified file; new files get a fresh header.

7. Add `changelog.d/<YYYY-MM-DD>_pc_04_04_start_extender.md` (no header): "Internal: database and search startup
   steps take an injectable start-extension function; the operations runner reads the notify-socket name from
   core. No behaviour change."

## Code contract

```go
// internal/database (names coined here)
type StartExtender func(step string)
func SetStartExtender(fn StartExtender) (restore func())

// internal/search (names coined here)
type StartExtender func(step string)
func SetStartExtender(fn StartExtender) (restore func())

// core/lifecycle (name fixed by 03 §3.3 and 04 §8.3)
const NotifySocketEnv = "NOTIFY_SOCKET"
```

The function shape `func(step string)` is fixed by 05-health §5H.3.4 and 04 §8.3.

## TDD tests to write first (all names coined here)

`internal/database/startup_extender_test.go` (package `database`):

- `TestStartExtender_InjectedFuncCalledPerStep` — install a recorder with `SetStartExtender`, `t.Cleanup(restore)`;
  run `RunMigrations` over the same three-migration registry shape `TestRunMigrationsExtendsStartTimeoutBeforeEachUp`
  uses (one already recorded); assert the recorder saw `migration-9102`, `migration-9103` in order and nothing for
  the recorded one; then `InitializeStore("pebble", <tempdir>)` and assert `store-open` was recorded before the store
  opened.
- `TestStartExtender_InjectedFuncSendsNoDatagram` — with `NOTIFY_SOCKET` pointing at a test socket and a recorder
  injected, no datagram arrives (the injected func replaces HEAD's write).
- `TestStartExtender_RestoreReturnsToHEAD` — after `restore()`, the HEAD datagram `EXTEND_TIMEOUT_USEC=600000000`
  arrives again for one store open.

`internal/search/start_extender_test.go` (package `search`):

- `TestIndexService_FieldExtenderWins` — field set and package extender set: only the field is called, with
  `search-index-open`.
- `TestIndexService_PackageExtenderUsedWhenNoField`.

Run them red first (they do not compile before steps 2–3), then implement.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)
#   expect: exit 0, no output
go test ./internal/database/ -run 'StartExtender|ExtendsStartTimeout|ExtendStartTimeout' -count=1
#   expect: ok  github.com/falkcorp/audiobook-organizer/internal/database
go test ./internal/search/ -run 'IndexService|SDNotify|Sdnotify|Extend' -count=1
#   expect: ok  github.com/falkcorp/audiobook-organizer/internal/search
go test ./internal/operations/registry/ -run 'ChildBaseEnv' -count=1
#   expect: ok  github.com/falkcorp/audiobook-organizer/internal/operations/registry
(cd core && go test ./lifecycle/ -count=1)
#   expect: ok  github.com/falkcorp/mediacore/lifecycle
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/sdnotify"' --include=*.go internal/operations/registry | wc -l
#   expect: 0
git diff --stat origin/main -- internal/database/startup_notify_test.go internal/search/register_sdnotify_test.go
#   expect: no output (both files unchanged)
grep -rln '"github.com/falkcorp/mediacore/app"\|"github.com/falkcorp/mediacore/module"' --include=*.go internal | grep -v _test | wc -l
#   expect: 0 (no package below layer 6 imports app or module)
make ci
#   expect: exit 0
make -C core ci
#   expect: exit 0
```

## Out of scope

- Moving `internal/sdnotify` to `core/internal/sdnotify`, deleting the HEAD fallback, rewiring `main.go`'s
  `--print-capabilities` and `internal/server`'s READY/STOPPING (all PC-04-07).
- Creating `app.ExtendStart` (PC-04-02) and wiring `Deps.ExtendStart` into search (PC-04-07 via `books.Module()`).
- Any change to step budgets, timers or unit files.

## Rollback

Revert the PR (`git revert <merge-commit>`); nothing is persisted and no behaviour changed.

## Repo rules

- Worktree and branch per task, off fresh `origin/main`; never commit to `main`; never edit the primary checkout.
- Every new or changed file carries the 4-line header (`file`, `version`, `guid`, `last-edited`); bump the
  version on change, keep the guid. `changelog.d/` fragments get no header.
- Conventional commit, for example `refactor(startup): inject start-extension funcs; registry reads lifecycle.NotifySocketEnv`,
  body ending with:

  ```
  Stage-Release: S2 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- `gh pr create`, then `gh pr merge <n> --rebase` once the required checks are green on an up-to-date branch and the
  gate is open. Never squash. Never `--admin` past a required check.
- CLAUDE.md concurrency rules: this PR adds no loop over a library-scale collection.

## Planner notes

- The row lists `main.go` but this PR has nothing to change there: `main.go`'s only `sdnotify` use is the
  `--print-capabilities` fast exit, which 04 §8.3 rewires to `app`, and `app` does not exist until PC-04-02. That
  rewire, and the injection from `serve` (in `cmd/root.go`, not `main.go`), belong to PC-04-07.
- The row's files omit `core/lifecycle/` (where `NotifySocketEnv` must be added, 03 §3.3) and the two database
  call-site files `internal/database/store.go` and `internal/database/migrations.go`; this brief adds them.
- Because "existing startup-notify tests unchanged" requires the HEAD fallback to stay in this PR, PC-04-07 must
  delete the fallback branch in `internal/database/startup_notify.go` and `internal/search/register.go` and adapt
  `internal/database/startup_notify_test.go` and `internal/search/register_sdnotify_test.go` when `internal/sdnotify`
  moves. None of those four files is in PC-04-07's row.
