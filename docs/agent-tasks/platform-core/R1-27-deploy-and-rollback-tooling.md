<!-- file: docs/agent-tasks/platform-core/R1-27-deploy-and-rollback-tooling.md -->
<!-- version: 1.1.0 -->
<!-- guid: e981aa06-b2f6-4fd5-91ce-9cc3d994ad37 -->
<!-- last-edited: 2026-11-05 -->

# R1-27 — Deploy and rollback tooling: DEPLOY_READY_TIMEOUT, deploy-wait-ready.sh polling the unit over ssh, the unit's RestartPreventExitStatus=78 and stop settings, make rollback-restore with one in-flight probe, the README compose notes

| Field | Value |
|---|---|
| ID | `R1-27` |
| Tier | **T2** (Sonnet / high): shell and Python tooling against a fake host; well specified |
| Release / wave | R1 / W18 |
| Depends on | R1-25, R1-19 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.4 R1 Content: deploy tooling; 09 §9.2.1 (rollback, rollback-restore); 05-storage §5S.3.12 (make rollback-restore); 05-health §5H.4 (stop timeouts, KillMode=mixed) |
| Branch | `agent/platform-core-r1-27-deploy-and-rollback-tooling` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `Makefile`
- `Makefile.local.example`
- `scripts/deploy-wait-ready.sh`
- `deploy/audiobook-organizer.service`
- `README.md`
- `scripts/tests/test_deploy_wait_ready.py`
- `scripts/tests/test_make_rollback.py`
- `scripts/tests/fake_ssh.py`
- `docs/system/deploy-and-gpu-ops.md`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-27" -b agent/platform-core-r1-27-deploy-and-rollback-tooling origin/main
cd "$REPO/.worktrees/platform-core-r1-27"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Deploy and rollback tooling that R1 and R2 need, tested against a fake host (09 §9.4 R1 "Content: deploy tooling";
09 §9.2.1 "Rollback"; 05-storage §5S.3.12 `make rollback-restore` row; 05-health §5H.4 stop timeouts and
`KillMode=mixed`, and the deploy-wait note):

1. **`DEPLOY_READY_TIMEOUT`** in `Makefile` (`rollback`) and `Makefile.local.example` (`deploy`), default 300 s,
   counted from when the unit leaves `activating` (it replaces the literal `300` at `Makefile:920` and
   `Makefile.local.example:100,127`, `f7ac5b11e`).
2. **`scripts/deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]`**: with `SSH_HOST`, polls the unit over ssh
   (`systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus <unit>`): `activating` counts as progress (the
   timeout starts when it leaves `activating`), `failed`, `inactive` or a rising `NRestarts` fails at once, and
   `ExecMainStatus=78` is reported by name ("install the release the log names first", 09 §9.2.2), then waits for
   `"ready":true` on `/health`; without `SSH_HOST` (Docker, launchd) it polls the URL with a documented larger
   timeout. HEAD's script takes only `BASE_URL [TIMEOUT]` (`scripts/deploy-wait-ready.sh:8-13`).
3. **The committed unit** (`deploy/audiobook-organizer.service`): `RestartPreventExitStatus=78`,
   `TimeoutStopSec=60` and `KillMode=mixed` (05-health §5H.4 "The budget must fit the supervisor's stop timeout":
   "ships `TimeoutStopSec=60` in the committed unit … The same unit sets `KillMode=mixed`"), with comments citing the spec. `Restart=on-failure`
   stays (exit 75 restarts; exit 78 does not).
4. **`make rollback-restore`** (05-storage §5S.3.12 row, 09 §9.2.1): stop the unit; refuse while a swap is in flight
   (one probe, `restore_inflight`, no override); **before it touches anything** run `.prev --print-storage-levels`
   and require equality with the source's levels (the checkpoint record's `from_vector`, or with
   `ROLLBACK_ARCHIVE=<file>` the levels read from the archived store itself), refusing with a message naming the
   release to install; run the current binary's `storage restore-offline --checkpoint <latest> --accept-loss` (or
   `--archive <file> --accept-loss`); reinstall `.prev`; start; wait with `deploy-wait-ready.sh`. Its `RELEASE=`
   branch (`updater fetch-release`) arrives with F8-02: here, `RELEASE=` set → refuse with "needs F8's updater
   fetch-release". `make rollback` and `make rollback-restore` share **one** in-flight probe (no override).
5. **README compose notes**: the `/config` volume preparation (09 §9.4 R1) and the documented
   `docker run --stop-timeout 60` line (05-health §5H.4: the container's stop timeout must exceed the 25 s budget);
   `docs/system/deploy-and-gpu-ops.md` gains the sudoers lines the new ssh reads need.

## Tests this task owns (spec names)

- `scripts/tests/test_deploy_wait_ready.py` (exit 78 included; 09 §9.4 R1 Verification): over `scripts/tests/fake_ssh.py`
  (a stub `ssh` on `PATH` that answers `systemctl show` from a scripted sequence) and a local HTTP server: `activating`
  for longer than TIMEOUT then `active` + ready passes; `failed` fails at once; rising `NRestarts` fails at once;
  `ExecMainStatus=78` exits with the named message; no `SSH_HOST` polls the URL only.
- `scripts/tests/test_make_rollback.py` (09 §9.4 R1 Verification): runs `make rollback` and `make rollback-restore`
  (with a checkpoint and with an archive) against the fake-ssh harness; asserts the refusal on a level mismatch, the
  refusal while `inflight.json` exists, the command order, and that nothing is swapped before the level check.

## Dependency check

R1-25 and R1-19 merged: `test -f core/app/offline.go && test -f core/app/binaryid.go && echo ok`.

## Re-verify (expected at `f7ac5b11e`)

```bash
sed -n 8p scripts/deploy-wait-ready.sh          # expect: # Usage: deploy-wait-ready.sh BASE_URL [TIMEOUT_SECONDS]   (default 300)
grep -n 'deploy-wait-ready.sh' Makefile Makefile.local.example   # expect Makefile:920 (rollback) and Makefile.local.example:100,127
grep -n '^rollback:' Makefile                   # expect 896 (09 cites Makefile:892-917 at 748fd787a)
grep -n 'TimeoutStopSec\|KillMode\|RestartPreventExitStatus' deploy/audiobook-organizer.service | grep -v '^\S*:\s*#'   # expect 0 (none set today)
grep -n '^Restart=on-failure' deploy/audiobook-organizer.service      # expect 111
```

## Steps

1. Tests and `fake_ssh.py` first (red). 2. The script. 3. Makefile targets and `Makefile.local.example`.
4. The unit file. 5. README and the deploy doc. 6. Header bumps; fragment `changelog.d/<today>_deploy_tooling.md`
(`### Changed`: "Deploy waits on the systemd unit and reports exit 78 by name; the unit no longer restarts on exit
78 and stops within 60 s; `make rollback-restore` for releases that change the store."). Commit:
`feat(deploy): unit-aware ready wait, rollback-restore, unit stop settings`.

## Code contract

`scripts/deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]` (exit 0 ready; 1 timeout; 78 reported by name with exit 78; 2 bad answer); `make rollback-restore [ROLLBACK_ARCHIVE=<file>]`.

## Acceptance

```bash
python3 -m pytest scripts/tests/test_deploy_wait_ready.py scripts/tests/test_make_rollback.py -q   # expect all passed
bash -n scripts/deploy-wait-ready.sh && shellcheck scripts/deploy-wait-ready.sh                     # expect clean (shellcheck if installed)
systemd-analyze verify deploy/audiobook-organizer.service 2>&1 | grep -v 'Command .* is not executable' ; true   # expect no unknown-key error
grep -c 'DEPLOY_READY_TIMEOUT' Makefile Makefile.local.example                                      # expect >= 1 each
grep -c -- '--stop-timeout 60' README.md                                                           # expect >= 1
make ci                                                                                              # expect exit 0
```

## Out of scope

The `RELEASE=` branch of `rollback-restore` (F8-02); the `unit-lifecycle` CI job (R1-28); the same 05-health §5H.4
paragraph's `stop_grace_period: 60s` with `ABO_CORE_SHUTDOWN_BUDGET=25s` in `docker-compose.yml`, `ExitTimeOut` 60 in
the launchd plist and ABO reading `ABO_CORE_SHUTDOWN_BUDGET`: those are R1-33's. This row owns the unit's
`TimeoutStopSec=60` and `KillMode=mixed` and the README's `docker run --stop-timeout 60` line.

## Rollback

Revert the PR. The unit change needs `systemctl daemon-reload` on the owner's host to take effect, at deploy time.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

