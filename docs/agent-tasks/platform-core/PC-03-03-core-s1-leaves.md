<!-- file: docs/agent-tasks/platform-core/PC-03-03-core-s1-leaves.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3dcdcceb-ec81-45b7-a3b0-292a3fda1e8b -->
<!-- last-edited: 2026-11-04 -->

# PC-03-03 — Core S1 leaves: `version` 0.1.0, `entity`, `coretest` log capture, the flight recorder rewritten into `core/health` on a `*slog.Logger`, `check_core_version.py` and the `core-checkpoint` job

| Field | Value |
|---|---|
| ID | PC-03-03 |
| Tier | **T1 Sonnet/medium.** Three tiny new leaves, one constructor rewrite with its importers, one script and one workflow job; every shape is given below. |
| Wave | W03 (S1, release `S1`) |
| Depends on | PC-03-01 (inventory). **Also run it after PC-03-02 has merged** (see Planner notes: both edit `internal/server/`). |
| Trailer | `Stage-Release: S1 1` |
| Digest | `docs/plans/platform-core/digests/S1.md` §3 (rows `health`, `version`, `entity`, `coretest`), §5 rule 7 |
| Spec | 09 S1 row "Moves"; 03 §3.3 rows `version`, `entity`, `coretest`; 03 §3.5.1 item 2 (the staged positive control: "from S1: the S1 PR names a real edge it creates (e.g. `health` → `log` …)"); 03 R5 (no `slog.SetDefault` in core); 12 §12.4.1 (`core-checkpoint`), §12.4.3 (`check_core_version.py`); 09 S0 CI table rows `pprof-check`, `check_core_version.py`, `core-checkpoint` |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-03-03" -b platform-core/pc-03-03-core-leaves origin/main
cd "$REPO/../audiobook-organizer-pc-03-03"
git rebase origin/main
git worktree list
make web-deps
export GOTOOLCHAIN=$(make -s -C core print-toolchain)
MOD=$(go -C core list -m); echo "$MOD"         # expected: github.com/falkcorp/mediacore
```

Preconditions: `ls core/log core/library` lists both (PC-03-01 and PC-03-02 merged). Otherwise STOP.

## Exact files

Create: `core/version/version.go`, `core/entity/entity.go`, `core/coretest/coretest.go`, `core/coretest/logcapture.go`,
`core/health/flight.go` (by `git mv` of `internal/diag/flight/flight.go`), `core/health/flight_test.go` (by `git mv`),
`core/health/flight_pprof_test.go` (by `git mv`), `core/scripts/ci/check_core_version.py`.
Modify: `core/archtest/coreimports_test.go`, `core/Makefile`, `.github/workflows/release-prod.yml`, and the flight
importers: `internal/server/flight_recorder.go`, `internal/server/flight_recorder_test.go`, `internal/server/server.go`,
`internal/server/wire_media_routes.go`, `internal/server/handlers/diagnostics_traces.go`,
`internal/server/handlers/diagnostics_traces_test.go`, `internal/arch/layering_test.go`, `scripts/ci/fixture_weights.json`.
Delete: the `internal/diag/flight/` directory (and `internal/diag/` if empty).

## Re-verify (expected values at the planning checkout `51a2c2acd`; a difference: STOP)

```bash
ls internal/diag/flight                                                       # flight.go  flight_pprof_test.go  flight_test.go
grep -n 'var flightLog = logger.New("flight-recorder")' internal/diag/flight/flight.go   # 1 hit (line 108)
grep -c 'flightLog\.' internal/diag/flight/flight.go                         # 12
grep -n 'const EnvSwitch = "ABK_FLIGHT_RECORDER"' internal/diag/flight/flight.go         # 1 hit
grep -n 'os.Getenv(EnvSwitch)' internal/diag/flight/flight.go                # 1 hit
grep -c 'EnvSwitch' internal/diag/flight/flight_test.go                      # 4
grep -rl --include=*.go '"github.com/falkcorp/audiobook-organizer/internal/diag/flight"' . | sort
          # 5 files: internal/server/{flight_recorder.go,flight_recorder_test.go,server.go}
          #          internal/server/handlers/{diagnostics_traces.go,diagnostics_traces_test.go}
grep -n 'internal/diag/flight' internal/server/wire_media_routes.go          # 1 hit (a comment only)
grep -n '"internal/diag/flight"' internal/arch/layering_test.go              # 1 hit
grep -c '"internal/diag/flight"' scripts/ci/fixture_weights.json             # 2
grep -n 'func Sanitize(s string) string' core/log/sanitize.go                # 1 hit
grep -n '^pprof-check:' Makefile                                             # 1 hit (root: `go test -tags pprof -run Pprof -count=1 .`)
ls core/version core/entity core/coretest core/health 2>&1 | grep -c 'No such file'      # 4
```

## Contracts

```go
// core/version/version.go (03 §3.3; 12 §12.4.3)
package version
// Version is the core version. Never derived from debug.ReadBuildInfo.
const Version = "0.1.0"

// core/entity/entity.go (03 §3.3 row entity: "the one polymorphic reference")
package entity
type Ref struct{ Module, Kind, ID string }

// core/coretest/logcapture.go (names coined here; 03 R5: never slog.SetDefault)
package coretest
type LogCapture struct{ /* mutex-guarded []slog.Record */ }
func CaptureLogs(t testing.TB) *LogCapture        // a slog.Handler that records every record; reset by t.Cleanup
func (c *LogCapture) Logger() *slog.Logger         // slog.New(handler)
func (c *LogCapture) Records() []slog.Record      // a copy
func (c *LogCapture) Messages() []string          // each record's Message

// core/health/flight.go (rewritten from internal/diag/flight; names coined here)
package health
type FlightConfig struct{ /* HEAD's Config fields, unchanged, Disabled included */ }
type FlightRecorder struct{ /* HEAD's Recorder */ }
type FlightFileInfo struct{ /* HEAD's FileInfo */ }
func NewFlightRecorder(cfg FlightConfig, logger *slog.Logger) *FlightRecorder   // nil logger = slog.New(slog.DiscardHandler)
```

Rewrite rules for `flight.go` → `core/health/flight.go`:
- `package flight` → `package health`; `Config` → `FlightConfig`, `Recorder` → `FlightRecorder`, `FileInfo` →
  `FlightFileInfo`, `New` → `NewFlightRecorder`; every method keeps its name and behaviour.
- Delete `var flightLog`; the recorder holds `log *slog.Logger`; each `flightLog.Info(format, args...)` becomes
  `r.log.Info(corelog.Sanitize("flight-recorder: " + fmt.Sprintf(format, args...)))`, and `Warn` likewise, so the
  journal text stays HEAD's `flight-recorder: <message>` with HEAD's sanitising (import
  `corelog "<MOD>/log"`). The `dropf` default becomes `r.log.Info`-based in the same way.
- Delete `EnvSwitch` and the `os.Getenv` read: core holds no ABO environment name (03 R1). ABO's
  `internal/server/flight_recorder.go` gains `const flightRecorderEnvSwitch = "ABK_FLIGHT_RECORDER"` and sets
  `Disabled: strings.EqualFold(strings.TrimSpace(os.Getenv(flightRecorderEnvSwitch)), "off")`, then calls
  `health.NewFlightRecorder(cfg, slog.Default())`.
- In `flight_test.go`, the four `t.Setenv(EnvSwitch, …)` lines become `FlightConfig{Disabled: true}` (for `"off"`)
  or are deleted (for `""`); add one ABO test in `internal/server/flight_recorder_test.go`,
  `TestFlightRecorderEnvSwitchOff` (name coined here), asserting `ABK_FLIGHT_RECORDER=off` yields a disabled
  recorder.
- Add `TestFlightRecorderLogsThroughInjectedLogger` (name coined here) in `core/health/flight_test.go`: with
  `coretest.CaptureLogs(t).Logger()`, `Start` logs one message starting `flight-recorder: flight recorder started`.

## TDD tests to write first

`TestCaptureLogsRecordsMessages` (`core/coretest/logcapture_test.go`), `TestFlightRecorderLogsThroughInjectedLogger`
(`core/health/flight_test.go`), `TestFlightRecorderEnvSwitchOff` (`internal/server/flight_recorder_test.go`),
`TestCoreGuardPositiveControl_HealthImportsLog` (`core/archtest/coreimports_test.go`); every HEAD flight test moves
with the file and stays green (names coined here; the spec names none).

## Steps

1. Leaves: write `version.go`, `entity.go`, `coretest.go` (package doc: "Package coretest is core's public test kit
   (06 §6.4.8). At S1 it holds the injected-handler log capture; it grows each stage."), `logcapture.go`, each with a
   4-line header and a `_test.go` only if you add behaviour beyond the struct (the log capture gets
   `TestCaptureLogsRecordsMessages`, name coined here, in `core/coretest/logcapture_test.go`).
2. Flight: `git mv internal/diag/flight/flight.go core/health/flight.go` (and the two test files) in one commit that
   also rewrites them and the five importers (and the comment in `wire_media_routes.go`) (qualifier `diagflight.` → `health.` with import
   `"<MOD>/health"`; the renamed types), removes the `"internal/diag/flight"` entry from `layerOf` in
   `internal/arch/layering_test.go`, and renames both `"internal/diag/flight"` keys in
   `scripts/ci/fixture_weights.json` to nothing (delete those two lines; core's tests are not in the root fixture
   shards). Build both modules before committing.
3. Positive control (03 §3.5.1 item 2): in `core/archtest/coreimports_test.go` add
   `TestCoreGuardPositiveControl_HealthImportsLog` (name coined here) asserting the loaded core graph has the edge
   `<MOD>/health → <MOD>/log`; keep the S0 synthetic self-test.
4. `core/Makefile`: add targets `pprof-check` (`go vet -tags pprof ./health/` then
   `go test -tags pprof -run Pprof -count=1 ./health/`) and `version-check`
   (`python3 scripts/ci/check_core_version.py`), both prerequisites of `ci`.
5. `core/scripts/ci/check_core_version.py` (12 §12.4.3; standard library only): reads `version/version.go` with the
   regex `^const Version = "(\d+)\.(\d+)\.(\d+)"$` (exit 1 if absent); then (a) the newest checkpoint from
   `git for-each-ref --sort=-v:refname --format='%(refname:short)' refs/core-checkpoints/` — when there is none or
   git has no such refs, print `checkpoint: not applicable` and pass, else require Version ≥ it; (b) hello's
   `MinCoreVersion` in `examples/hello/` — absent at S1: print `hello: not applicable`; (c) every
   `web/packages/*/package.json` `version` equals Version — none at S1: print `npm lockstep: not applicable`.
   Exit 0 when every applicable check passes, printing `version-check: OK 0.1.0`.
6. `.github/workflows/release-prod.yml`: add job `core-checkpoint` (12 §12.4.1), which no other job `needs`, with
   `continue-on-error: true` and `permissions: contents: write`: checkout (SHA-pinned like the file's other
   checkouts, `fetch-depth: 0`), `git fetch origin '+refs/core-checkpoints/*:refs/core-checkpoints/*'`, find the newest
   checkpoint, and when `core/` changed since it (or none exists) and `version.Version` is greater: `git update-ref
   refs/core-checkpoints/v<Version> HEAD` and `git push origin refs/core-checkpoints/v<Version>`; never record a
   ref whose name differs from `v<Version>`; when `core/` changed and the constant was not bumped, open or update one
   issue labelled `core-version-bump-needed` with `gh issue`. Push with `git push` (CLAUDE.md: never the MCP
   contents API for workflow files).
7. Headers on every new or changed file; `changelog.d/` fragment.

## Acceptance

```bash
go build ./... && (cd core && go build ./...) && echo built                   # built
go -C core test ./version/ ./entity/ ./coretest/ ./health/ ./archtest/ -count=1   # ok for each package with tests
make -C core pprof-check                                                      # exit 0 (pprof per module, 09 S0 CI table)
make pprof-check                                                              # exit 0 (root, unchanged)
python3 core/scripts/ci/check_core_version.py                                 # last line: version-check: OK 0.1.0
grep -rn 'ABK_\|audiobook' core/health/ core/coretest/ core/version/ core/entity/ | wc -l   # 0
grep -rn 'slog.SetDefault' core/ | grep -v _test.go | wc -l                   # 0
grep -rn 'slog.SetDefault' core/coretest/ | wc -l                             # 0
go test ./internal/server/ -run 'TestFlightRecorderEnvSwitchOff|Flight' -count=1   # ok
go test ./internal/arch/ -count=1                                             # ok
ls internal/diag 2>&1 | grep -c 'No such file'                                # 1
make -C core ci; echo "exit=$?"                                               # exit=0
make ci; echo "exit=$?"                                                       # exit=0
```

`core-checkpoint` cannot run on a PR; check it with `actionlint` if installed, and in the PR description say it is
exercised by the next production release.

## Out of scope

The rest of `health` (S8); the rest of `coretest` (grows each stage); `MinCoreVersion` checks (S2's hello); the
`prerelease.yml` auto-promote path calling `core-checkpoint` (see Planner notes).

## Rollback

Revert the PR. `core-checkpoint` records a ref only; nothing reads it before SC1 counting.

## Repo rules

- Worktree only; never `go work init`. Headers everywhere; one `changelog.d/` fragment.
- Every commit carries `Stage-Release: S1 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
- Actions pinned to SHAs. Concurrency (CLAUDE.md): no library-scale loop.
- `gh pr merge <n> --rebase` after `core-ci`, `extraction-gates` and the other required checks pass.

## Planner notes

- Wrong file list and a same-step collision: the inventory row lists only `internal/diag/flight/` on the ABO side, but
  the constructor rewrite changes five importer files under `internal/server/` (and a comment in a sixth) plus `internal/arch/layering_test.go`
  and `scripts/ci/fixture_weights.json`. `internal/server/` is also PC-03-02's, and the plan puts PC-03-02 and PC-03-03
  in the same step. Serialize PC-03-02 → PC-03-03 (this brief's precondition does).
- 12 §12.4.1 says the `core-checkpoint` job "also runs when `prerelease.yml` auto-promotes an RC"; `prerelease.yml`
  is not in this row and the auto-promote path goes through the shared `reusable-release.yml`, which only the owner
  reads (OWNER-06). Left out here; it needs an owner decision.
- `core/log` (moved by PC-03-01) still carries ABO's invite-path redaction (`RedactInvitePath`,
  `InviteTokenRedacted`); not this row's scope, but it is an app name in core (03 R1).
