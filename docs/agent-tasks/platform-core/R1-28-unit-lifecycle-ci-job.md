<!-- file: docs/agent-tasks/platform-core/R1-28-unit-lifecycle-ci-job.md -->
<!-- version: 1.1.4 -->
<!-- guid: b4eed708-389c-43cd-a552-675242dd7363 -->
<!-- last-edited: 2026-11-05 -->

# R1-28 — unit-lifecycle CI job on the systemd runner VM: the 12-minute fake-migration cell and the staged-restore cell

| Field | Value |
|---|---|
| ID | `R1-28` |
| Tier | **T2** (Sonnet / high): systemd drop-ins on a runner VM; the checks themselves are small |
| Release / wave | R1 / W21 |
| Depends on | R1-27, R1-25 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.2.1 (unit-lifecycle row and triggers); 09 §9.4 R1 Verification; 09 §9.10 |
| Branch | `agent/platform-core-r1-28-unit-lifecycle-ci-job` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `.github/workflows/unit-lifecycle.yml`
- `scripts/tests/test_unit_lifecycle.py`
- `scripts/tests/unit_lifecycle/dropin.conf`
- `core/kernel/`: `fakemigration_hook.go`, `boot.go`, `fakemigration_nohook.go`
- `ci/gates.txt`
- `core/archtest/tags.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-28" -b agent/platform-core-r1-28-unit-lifecycle-ci-job origin/main
cd "$REPO/.worktrees/platform-core-r1-28"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

The `unit-lifecycle` required check (09 §9.2.1 required-checks table, its row and triggers; 09 §9.4 R1 Verification
"the two `unit-lifecycle` cells"; 09 §9.10): `scripts/tests/test_unit_lifecycle.py` runs on GitHub's Ubuntu runner
VM, where systemd is PID 1. It installs the **committed** unit (`deploy/audiobook-organizer.service`) with a drop-in
(`scripts/tests/unit_lifecycle/dropin.conf`) that overrides **only** `ExecStart` and the database path and adds
`PrivateNetwork=yes`, and runs **two cells**:

1. **Fake migration:** a 12-minute fake migration under the committed unit is not killed (the boot extends its start
   timeout per step, `EXTEND_TIMEOUT_USEC`), and `scripts/deploy-wait-ready.sh` (R1-27, with `SSH_HOST` pointed at the
   local host through the fake-ssh shim or a local-mode flag) does not report it failed.
2. **Staged restore:** after a restore is staged (R1-25's offline path or ABO's staging, which exits 75), the service
   comes back by itself (`Restart=on-failure`) and reports ready.

The fake migration is a **test-only** kernel hook (`core/kernel/fakemigration_hook.go`) behind the build tag
`unitlifecycle` (03 §3.5.1 item 4 names it on the one tag-set list): N one-minute kernel boot steps for the duration
in `MEDIACORE_FAKE_MIGRATION`, **each calling `ExtendStart` before it starts**, so it exercises 05-health §5H.3.4's
per-step rule and never extends from a timer. It is compiled into the binary this job builds and into no release
build. The job is **outside** the 35-minute budget
(09 §9.2.10).

Triggers (09 §9.2.1, copied exactly): every PR that changes `deploy/**`, `cmd/**`, `main.go`, `internal/backup/**`,
`internal/updater/**`, `internal/server/server_lifecycle*.go`, `internal/books/cutover/**`, `core/kernel/**` or
`core/internal/restoreswap/**`, and nightly. A PR that changes none of them skips through a job-level `if:` so the
required check reports success (the classifier rule of 09 §9.2.1).

## Tests this task owns

- `scripts/tests/test_unit_lifecycle.py` (the two cells). On a developer machine without systemd as PID 1 it skips
  with a clear reason; in the job it must run (the job fails if it skipped).

## Dependency check

R1-27 and R1-25 merged:
`grep -q 'RestartPreventExitStatus=78' deploy/audiobook-organizer.service && test -f core/app/offline.go && echo ok`.

## Re-verify

```bash
grep -n '^Type=notify\|^TimeoutStartSec\|^TimeoutStopSec\|^KillMode\|^RestartPreventExitStatus\|^Restart=' deploy/audiobook-organizer.service   # expect R1-27's values
grep -n 'SSH_HOST' scripts/deploy-wait-ready.sh | head -2                                  # expect R1-27's unit-polling mode
test ! -e .github/workflows/unit-lifecycle.yml && echo NEW                                 # expect NEW
grep -n 'unit-lifecycle' ci/gates.txt                                                      # expect 0 before; 1 after
```

## Steps

1. `core/kernel/fakemigration_hook.go` (`//go:build unitlifecycle`): reads the duration from
   `MEDIACORE_FAKE_MIGRATION` (03 §3.5.1 item 4) and returns that many one-minute boot steps, each calling
   `ExtendStart(step)` immediately before it sleeps its minute (never a ticker or timer goroutine). Its untagged twin
   `core/kernel/fakemigration_nohook.go` (`//go:build !unitlifecycle`) returns no steps. `core/kernel/boot.go` calls
   the one function both files define (name coined here: `fakeMigrationSteps()`) where the phase list appends boot
   steps; this row is W21's one owner of the kernel wiring files. Without the tag no fake step exists (assert with
   `go list -f '{{.GoFiles}}' ./kernel` listing `fakemigration_nohook.go` and not `fakemigration_hook.go`).
1a. `core/archtest/tags.go`: add `unitlifecycle` to the exported tag-set list (03 §3.5.1 item 4), so the tag-union
   tools (`TestCoreImportsOnlyCore`, `TestCoreHoldsNoAppKeyNames`) read the hook file.
2. `scripts/tests/unit_lifecycle/dropin.conf`: `[Service]` with `ExecStart=` reset and the test binary, the database
   path, `PrivateNetwork=yes`. Nothing else (the point is to test the committed unit's own settings).
3. `scripts/tests/test_unit_lifecycle.py`: installs the unit and drop-in under `/etc/systemd/system/`, daemon-reloads,
   runs cell 1 (`MEDIACORE_FAKE_MIGRATION=12m`) and cell 2, and cleans up in `finally`. Timeouts generous (cell 1 ≈ 15
   minutes).
4. `.github/workflows/unit-lifecycle.yml`: `ubuntu-latest`, path triggers above plus `schedule` nightly, job-level
   skip-as-success `if:` on a changes step, `timeout-minutes: 40`, actions pinned to SHAs, builds
   `go build -tags unitlifecycle -o /tmp/abo .`, runs the test with `sudo`.
5. `ci/gates.txt`: register `unit-lifecycle` (`check`, `own-job`); `scripts/tests/test_run_gates.py` stays green.
6. Header bumps; fragment `changelog.d/<today>_unit_lifecycle.md` (`### Added`: "CI: unit-lifecycle job proves a long
   first boot is not killed and a staged restore restarts the service."). Commit:
   `ci(deploy): unit-lifecycle job with the fake-migration and staged-restore cells`.

## Code contract

Test-only: `core/kernel/fakemigration_hook.go` under the `unitlifecycle` tag and `core/kernel/fakemigration_nohook.go`
under `!unitlifecycle`, both defining the same unexported `fakeMigrationSteps()`; no exported name in either build.

## Acceptance

```bash
go build ./... && go -C core build -tags unitlifecycle ./kernel/            # expect exit 0
go -C core vet -tags unitlifecycle ./kernel/                                  # expect exit 0
grep -c 'unitlifecycle' core/archtest/tags.go                                 # expect >= 1
go -C core test ./archtest/ -count=1                                          # expect ok (tag-union tools read the hook)
go -C core list -f '{{.GoFiles}}' ./kernel | grep -c fakemigration          # expect 0 (not in the default build)
python3 -m pytest scripts/tests/test_unit_lifecycle.py -q                    # expect "skipped" locally without systemd PID 1
python3 -m pytest scripts/tests/test_run_gates.py -q                         # expect all passed
make ci && make -C core ci                                                   # expect exit 0
```

On the PR (it touches `core/kernel/**`, so the job runs): both cells green; paste their summary lines in the PR.

## Out of scope

The production-shaped store run (F2); any change to the committed unit (R1-27 owns it).

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
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.


## Planner notes

- 03 §3.5.1 item 4 says "core's `ci` target vets `./kernel/` with `-tags unitlifecycle`, so every PR compiles it",
  but `core/Makefile` is not in this row's files, and W21's other row, R1-30, edits `core/Makefile` (its `web-test`
  target). Either give the one vet line to R1-30 (its file, same wave) or add `core/Makefile` to R1-28 and move one
  of the two rows to another wave. Until then this PR proves the tagged build with the acceptance commands only.
