<!-- file: docs/agent-tasks/platform-core/R1-33-stop-settings-outside-unit.md -->
<!-- version: 1.0.2 -->
<!-- guid: b2d0c031-dfd0-4ae2-b924-b3c0f628deee -->
<!-- last-edited: 2026-11-05 -->

# R1-33 — Stop settings outside the unit: docker-compose.yml's stop_grace_period 60s with ABO_CORE_SHUTDOWN_BUDGET=25s, the launchd plist's ExitTimeOut 60, and ABO reading ABO_CORE_SHUTDOWN_BUDGET into app.Options.R1Values

| Field | Value |
|---|---|
| ID | `R1-33` |
| Tier | **T1** (Sonnet / medium): two deploy-file values and one value handed through R1Values |
| Release / wave | R1 / W17 |
| Depends on | R1-15 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-health §5H.4 ("The budget must fit the supervisor's stop timeout"), §5H.10 R1 row; 05-config §1.12.1 (`R1Values`, field `ShutdownBudget`) |
| Branch | `agent/platform-core-r1-33-stop-settings-outside-unit` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1
(09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (touch nothing outside this list without recording it under the PR's
"Scope notes"):

- `docker-compose.yml` (modify)
- `deploy/com.audiobook-organizer.plist` (modify)
- `cmd/root.go` (modify)
- `cmd/shutdown_budget_test.go` (create)

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-33" -b agent/platform-core-r1-33-stop-settings-outside-unit origin/main
cd "$REPO/.worktrees/platform-core-r1-33"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1). Line numbers below were read at
the planning base `f7ac5b11e` (no code changed between it and HEAD). Re-run every grep at your base before editing.

## Goal

Core's shutdown budget `core.shutdown.budget` is 25 s, but **8 s in a container when no layer sets it** (05-health
§5H.4 table). The supervisor must wait longer than the budget before it kills the process. 05-health §5H.4 says R1
ships, besides the systemd unit's `TimeoutStopSec=60` and `KillMode=mixed` (R1-27's):

1. `stop_grace_period: 60s` in `docker-compose.yml`, **together with** the environment entry
   `ABO_CORE_SHUTDOWN_BUDGET=25s`;
2. `ExitTimeOut` 60 in the launchd plist;
3. ABO reading `ABO_CORE_SHUTDOWN_BUDGET` itself in R1 and handing the value to the kernel through
   `app.Options.R1Values` (field `ShutdownBudget time.Duration`, "0 = core's default", 05-config §1.12.1). From F3
   core config reads the variable instead (05-health §5H.4).

Without item 1's environment entry a container keeps core's 8 s default even under a 60 s grace period (05-health
§5H.4). The README's `docker run --stop-timeout 60` line is R1-27's, not this row's.

## Dependency check

R1-15 merged (`cmd/root.go` builds `app.Options` with an `R1Values` func):
`git grep -n 'R1Values' -- cmd/root.go | head -3` prints at least one line, and
`git grep -n 'ShutdownBudget' -- core/app` prints the `R1Values` field. If either prints nothing, STOP.

## Re-verify (expected at the plan's base)

```bash
grep -n 'stop_grace_period\|ABO_CORE_SHUTDOWN_BUDGET' docker-compose.yml        # expect 0 hits
grep -n '^    environment:' docker-compose.yml                                  # expect 1 hit (line 17)
grep -n '^    restart: unless-stopped' docker-compose.yml                       # expect 1 hit (line 23)
grep -n '^# version:' docker-compose.yml                                        # expect "# version: 1.2.0"
grep -n 'ExitTimeOut' deploy/com.audiobook-organizer.plist                      # expect 0 hits
grep -n '<key>KeepAlive</key>' deploy/com.audiobook-organizer.plist             # expect 1 hit (line 51)
grep -n '<!-- version:' deploy/com.audiobook-organizer.plist                    # expect "<!-- version: 2.0.0 -->"
git grep -n 'ABO_CORE_SHUTDOWN_BUDGET' -- '*.go' | wc -l                        # expect 0
```

## Steps

1. **Test first.** Create `cmd/shutdown_budget_test.go` (`package cmd`, 4-line header) with the two tests below; run
   `go test ./cmd/ -run 'TestShutdownBudgetFromEnv|TestDeployFilesCarryStopTimeouts' -count=1` and see both fail.
2. **`cmd/root.go`: read the variable.** Add, next to the code that builds `app.Options` (R1-15's `serve` path):

   ```go
   // shutdownBudgetEnv is the R1 reading of core.shutdown.budget's operator name (05-health §5H.4;
   // 05-config §1.12.1). F3 deletes it with R1Values: core config reads the variable from then on.
   const shutdownBudgetEnv = "ABO_CORE_SHUTDOWN_BUDGET"

   // shutdownBudgetFromEnv returns 0 (core's default) when the variable is unset or empty, the parsed
   // duration when it is a positive time.ParseDuration value, and an error naming the variable otherwise.
   func shutdownBudgetFromEnv(lookup func(string) (string, bool)) (time.Duration, error)
   ```

   (both names coined here). Call it once with `os.LookupEnv` at the start of `serve`'s run function, before the
   store opens and before `app.New`; on error, `serve`
   returns the error (the process exits non-zero with the message
   `ABO_CORE_SHUTDOWN_BUDGET: <reason>`; the reject rule is coined here, so an operator's typo never silently falls
   back to 8 s). Set the result as `ShutdownBudget` in the `app.R1Values` value R1-15's `R1Values` func returns. Add
   no other field and change no other value.
3. **`docker-compose.yml`.** Under `services.audiobook-organizer`:
   - add the line `    stop_grace_period: 60s` directly after `    restart: unless-stopped`;
   - add the list entry `      - ABO_CORE_SHUTDOWN_BUDGET=25s` as the last entry of `environment:`;
   - bump `# version: 1.2.0` to `# version: 1.2.1` (the file has no `last-edited` line; do not add one).
4. **`deploy/com.audiobook-organizer.plist`.** Directly after the `KeepAlive` key and its `<true/>`, add:

   ```xml
       <!-- Stop timeout: core's 25 s shutdown budget must finish first (05-health §5H.4) -->
       <key>ExitTimeOut</key>
       <integer>60</integer>
   ```

   and bump the header comment `<!-- version: 2.0.0 -->` to `<!-- version: 2.0.1 -->`. Change nothing else in the
   file (its `file:` comment and paths are out of scope).
5. Run the tests and the acceptance block.
6. Header bump on `cmd/root.go` (patch version, `last-edited`); fragment `changelog.d/<today>_stop_settings.md` (no
   header; `### Changed`: "Docker Compose now gives the server 60 seconds to stop and sets a 25-second shutdown
   budget; the macOS launchd file waits 60 seconds."). Commit:
   `fix(deploy): stop timeouts in compose and launchd, ABO_CORE_SHUTDOWN_BUDGET read in R1`.

## Tests to write first (all names coined here)

| Test | Proves |
|---|---|
| `TestShutdownBudgetFromEnv` | table over a fake lookup: unset → `0, nil`; `""` → `0, nil`; `"25s"` → `25*time.Second`; `"1m"` → `time.Minute`; `"abc"` → error containing `ABO_CORE_SHUTDOWN_BUDGET`; `"-5s"` and `"0s"` → error |
| `TestDeployFilesCarryStopTimeouts` | reads `../docker-compose.yml` and `../deploy/com.audiobook-organizer.plist` (paths relative to `cmd/`): the compose file contains `stop_grace_period: 60s` and `ABO_CORE_SHUTDOWN_BUDGET=25s`; the plist contains `<key>ExitTimeOut</key>` followed by `<integer>60</integer>` |

## Acceptance

```bash
go test ./cmd/ -run 'TestShutdownBudgetFromEnv|TestDeployFilesCarryStopTimeouts' -count=1 -v | grep -c '^--- PASS'   # expect 2
grep -c 'stop_grace_period: 60s' docker-compose.yml                                       # expect 1
grep -c 'ABO_CORE_SHUTDOWN_BUDGET=25s' docker-compose.yml                                 # expect 1
docker compose -f docker-compose.yml config >/dev/null && echo COMPOSE_OK                 # expect COMPOSE_OK (skip if docker is absent; say so in the PR)
plutil -lint deploy/com.audiobook-organizer.plist 2>/dev/null || python3 -c "import plistlib,sys;plistlib.load(open('deploy/com.audiobook-organizer.plist','rb'));print('PLIST_OK')"   # expect OK or PLIST_OK
git grep -n 'ABO_CORE_SHUTDOWN_BUDGET' -- '*.go' | grep -v _test.go | wc -l                # expect 1 (the const)
make build-api && ABO_CORE_SHUTDOWN_BUDGET=abc timeout 30 ./audiobook-organizer serve 2>&1 | grep -c ABO_CORE_SHUTDOWN_BUDGET   # expect >= 1 (refused at boot, before the store opens)
make ci; echo "exit $?"                                                                   # expect exit 0
```

The e2e suite and every required check of 09 §9.2.1 that exists at your base are green on the PR; `unit-lifecycle`
runs because `cmd/**` changed.

## Out of scope

- The systemd unit's `TimeoutStopSec=60` and `KillMode=mixed`, and the README's `docker run --stop-timeout 60` line
  (R1-27).
- Any other `R1Values` field (R1-15, R1-17); core config reading the variable (F3); the canonical rename table (F3).
- Changing core's 25 s / 8 s defaults or the per-phase slices (05-health §5H.4).

## Rollback

Revert the PR. A deployed compose stack needs `docker compose up -d` to pick up the old grace period; launchd needs
the plist reloaded. No stored data changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped (and `last-edited` set to today where the header has one), keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** No loop over a library-scale collection in this task.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
