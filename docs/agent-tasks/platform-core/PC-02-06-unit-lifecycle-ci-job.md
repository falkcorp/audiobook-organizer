<!-- file: docs/agent-tasks/platform-core/PC-02-06-unit-lifecycle-ci-job.md -->
<!-- version: 1.0.0 -->
<!-- guid: f12a1453-deed-4dd7-811d-08ccc71bd3fb -->
<!-- last-edited: 2026-11-04 -->

# PC-02-06 — `unit-lifecycle` CI job on the systemd runner VM with S0's long-migration check

| Field | Value |
|---|---|
| ID | PC-02-06 |
| Tier | **T2 Sonnet/high.** Systemd drop-ins on a CI runner need care; the check itself is small and fully given. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-02-04 (`deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]`) |
| Blocks | PC-03-06 (adds S1's restore check to the same test file) |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §6.1, §8 (`unit-lifecycle` clause) |
| Spec | 09 §9.2.1 required-checks table row `unit-lifecycle` (line 355 at `51a2c2acd`); 09 S0 CI table row `unit-lifecycle`; 09 S0 "Done when" ("a 12-minute fake migration under the committed unit is not killed and is not reported failed by `deploy-wait-ready.sh`"); 09 §9.1 warmup row |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-06" -b platform-core/pc-02-06-unit-lifecycle origin/main
cd "$REPO/../audiobook-organizer-pc-02-06"
git rebase origin/main
git worktree list
make web-deps
```

Precondition: `grep -c 'SSH_HOST' scripts/deploy-wait-ready.sh` prints 1 or more (PC-02-04). Otherwise STOP.

## Exact files

Create: `.github/workflows/unit-lifecycle.yml`, `scripts/tests/test_unit_lifecycle.py`,
`scripts/tests/unit_lifecycle/dropin.conf`, `internal/database/startup_fake_migration_unitlifecycle.go`.
Modify: `ci/gates.txt`, `internal/database/startup_notify.go`, `internal/database/store.go`.
(The last two files of these lists are not in the inventory row; see Planner notes.)

## Re-verify greps (expected counts at the planning checkout `51a2c2acd`)

```bash
grep -n 'func extendStartTimeout' internal/database/startup_notify.go            # 1 hit (line 23)
grep -n 'const StartupStepBudget = 10 \* time.Minute' internal/sdnotify/sdnotify.go   # 1 hit (line 53)
grep -n 'if err := RunMigrations(s); err != nil' internal/database/store.go      # 1 hit (line 1857)
grep -n '^TimeoutStartSec=300$' deploy/audiobook-organizer.service               # 1 hit (line 87)
grep -n '^Restart=on-failure$' deploy/audiobook-organizer.service                # 1 hit (line 111)
grep -n '^ExecStart=/usr/local/bin/audiobook-organizer serve' deploy/audiobook-organizer.service   # 1 hit (line 92)
grep -n '^User=audiobook$' deploy/audiobook-organizer.service                    # 1 hit
grep -n '^WorkingDirectory=' deploy/audiobook-organizer.service                  # 1 hit
grep -n 'Environment=DATABASE_PATH' deploy/audiobook-organizer.service           # 1 hit
grep -n 'uses: actions/checkout@' .github/workflows/ci.yml | head -1             # a SHA-pinned line
grep -n 'uses: actions/setup-go@' .github/workflows/ci.yml | head -1             # a SHA-pinned line
```

A zero-hit target: STOP.

## Code contract: the fake migration (the test-only hook the plan asks this brief to name)

- Build tag **`unit_lifecycle`** and environment variable **`ABO_UNIT_LIFECYCLE_FAKE_MIGRATION`** (both names
  coined here). Production builds never carry the tag, so the hook is absent from every shipped binary.
- `startup_notify.go` adds `var fakeMigrationHook func()` (nil unless the tagged file sets it) and
  `func runFakeMigrationHook() { if fakeMigrationHook != nil { fakeMigrationHook() } }`.
- `store.go`: in `InitializeStore`, call `runFakeMigrationHook()` on the line before `RunMigrations(s)`.
- `startup_fake_migration_unitlifecycle.go` (`//go:build unit_lifecycle`): an `init()` sets `fakeMigrationHook` to
  a function that parses the env value as a `time.Duration` (absent or invalid → return at once) and then runs
  `ceil(d / time.Minute)` steps, each calling `extendStartTimeout()` and then sleeping one minute. That is a long
  migration run as HEAD runs one: every step gets its own 10-minute budget (`StartupStepBudget`), and none exceeds it.

## TDD tests to write first

- `scripts/tests/test_unit_lifecycle.py::test_s0_long_migration_not_killed` (below), written before the workflow;
  it skips outside the runner VM.
- `TestFakeMigrationHookNilWithoutTag` (name coined here, in `internal/database/startup_notify_test.go`): in a normal
  build `fakeMigrationHook` is nil and `runFakeMigrationHook()` returns at once.

## The job

`.github/workflows/unit-lifecycle.yml`:
- Triggers: `pull_request` (all paths), `schedule` nightly (pick a minute not on the hour), `workflow_dispatch`.
- Job `changes` (`ubuntu-latest`) sets `run=true` when the PR diff touches any of `deploy/**`, `cmd/**`,
  `internal/backup/**`, `internal/updater/**`, `internal/server/server_lifecycle*.go`, `core/kernel/**`,
  `core/internal/restoreswap/**`, or changes the `shipped_stage:` line of `ci/stage.txt` (a flip PR), and always on
  `schedule` and `workflow_dispatch`. The list is 09 §9.2.1's, copied exactly.
- Job `unit-lifecycle` (`ubuntu-latest`, `timeout-minutes: 30`), `needs: changes`, job-level
  `if: needs.changes.outputs.run == 'true'`, so a skip reports success (09 §9.2.1). Steps: checkout and setup-go
  pinned to the same SHAs as `ci.yml`, `go-version` equal to the other workflows' pin at your base;
  `go build -tags unit_lifecycle -o "$RUNNER_TEMP/abo" .`; then
  `sudo -E python3 scripts/tests/test_unit_lifecycle.py --binary "$RUNNER_TEMP/abo"` with `UNIT_LIFECYCLE=1`.
- Actions pinned to SHAs, never tags (CLAUDE.md).

`scripts/tests/unit_lifecycle/dropin.conf` overrides only `ExecStart` and `DATABASE_PATH` and adds
`PrivateNetwork=yes` (09 §9.2.1):

```ini
[Service]
ExecStart=
ExecStart=/usr/bin/env ABO_UNIT_LIFECYCLE_FAKE_MIGRATION=12m /usr/local/bin/audiobook-organizer serve --host 127.0.0.1 --port 8484
Environment=DATABASE_PATH=/var/lib/audiobook-organizer/unit-lifecycle.pebble
PrivateNetwork=yes
```

`scripts/tests/test_unit_lifecycle.py` (Python, standard library, `unittest`):
- `setUpClass` skips everything unless `UNIT_LIFECYCLE=1`, the process is root, and PID 1 is systemd
  (`/proc/1/comm` reads `systemd`), so `make ci` and developers' machines skip it.
- Setup, as the install docs do: create system user `audiobook`; create the unit's `WorkingDirectory` and state
  directories owned by it; copy `--binary` to `/usr/local/bin/audiobook-organizer`; copy the **committed**
  `deploy/audiobook-organizer.service` unchanged to `/etc/systemd/system/`; install the drop-in under
  `/etc/systemd/system/audiobook-organizer.service.d/zz-unit-lifecycle.conf`; `systemctl daemon-reload`.
- Health URL check under `PrivateNetwork=yes`: the service's loopback is private, so the test puts a `curl` shim
  first on `PATH` that runs the real `curl` inside the unit's network namespace
  (`nsenter --target <MainPID> --net curl "$@"`, `MainPID` from `systemctl show -p MainPID`), and an `ssh` shim that
  drops its first argument and runs the rest locally. Both shims are written into a temp directory by the test.
- `test_s0_long_migration_not_killed` (09 S0 "Done when"): `systemctl start --no-block audiobook-organizer.service`;
  run `bash scripts/deploy-wait-ready.sh http://127.0.0.1:8484 300 localhost audiobook-organizer.service` with the
  shims; assert it exits 0; assert `systemctl show -p NRestarts` is `NRestarts=0`, the unit's journal holds no
  `start operation timed out`, and the unit was `activating` for at least 12 minutes (the time from the start to
  the first `active` poll, measured by the test).
- `tearDownClass`: stop the unit, remove the drop-in, unit, binary, user and directories.

`ci/gates.txt`: one row, tab-separated, `unit-lifecycle	python3 scripts/tests/test_unit_lifecycle.py	-	check	-	own-job`.
PC-01-04 made `scripts/tests/test_run_gates.py` accept an `own-job` gate that any workflow under
`.github/workflows/` runs; if `python3 -m unittest scripts/tests/test_run_gates.py` still fails on this row, STOP
and report.

## Acceptance

```bash
go build ./... && go build -tags unit_lifecycle ./...                   # exit 0
go vet -tags unit_lifecycle ./internal/database/                        # exit 0
go test ./internal/database/ -run 'TestInitializeStoreExtendsStartTimeoutBeforeOpen|TestRunMigrationsExtendsStartTimeoutBeforeEachUp' -count=1   # ok (the hook is nil without the tag)
UNIT_LIFECYCLE= python3 -m unittest scripts/tests/test_unit_lifecycle.py -v   # skipped, final line "OK (skipped=…)"
python3 -m unittest scripts/tests/test_run_gates.py                     # OK
python3 scripts/check_toolchain_versions.py                             # exit 0
make ci; echo "exit=$?"                                                 # exit=0
```

On the PR: the `unit-lifecycle` job runs (this PR touches no trigger path, so dispatch it once with
`gh workflow run unit-lifecycle.yml --ref <branch>`) and is green in under 30 minutes, the 12-minute case included.

## Out of scope

S1's restore case (PC-03-06), S2's stop case and S2b's reset case (their stages add them to this file).

## Rollback

Revert the PR; the job and the tag-gated hook disappear; shipped binaries never contained the hook.

## Repo rules

- Worktree only; never `go work init`. Headers on every new or changed file (the YAML and `.conf` files take `#`
  comment headers); one `changelog.d/` fragment.
- Every commit ends with `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- Never push workflow changes through the MCP contents API; use `git push` (CLAUDE.md "GitHub Operations").
- `gh pr merge <n> --rebase` after the required checks pass; never squash or `--admin`.

## Planner notes

- The inventory row lists only `startup_notify.go` in `internal/database`; the hook needs a build-tagged file
  (`startup_fake_migration_unitlifecycle.go`, a build tag is per file) and one call in `store.go`'s
  `InitializeStore`. PC-02-01 also edits `store.go` and precedes this row, so no same-step collision.
- 09 §9.2.1 requires `PrivateNetwork=yes`, which hides the service's port from the runner; the spec's
  `deploy-wait-ready.sh` check needs the URL, so this brief reaches it through `nsenter` (a plan choice).
