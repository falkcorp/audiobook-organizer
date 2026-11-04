<!-- file: docs/agent-tasks/platform-core/PC-04-07-s2r1-flip-app-new.md -->
<!-- version: 1.0.0 -->
<!-- guid: a2b3c6a3-504a-48a7-bb98-8ae2bd70ac9e -->
<!-- last-edited: 2026-11-04 -->

# PC-04-07 — S2 r1 flip: main.go runs app.New, serve wiring, supervisor stop settings, one state-directory resolver, listeners and sdnotify from core

| Field | Value |
|---|---|
| ID | PC-04-07 |
| Tier | **T4 Opus/xhigh–max** — the process changes owner on every production boot; the stop order, the shutdown budget, the state directory (where the legacy key file lives) and readiness signalling must match HEAD or a host loses its keyring, gets SIGKILLed, or never reports ready |
| Wave | W04 (S2, release `S2r1`), serialized step 3 — **the last PR of W04** |
| Depends on | PC-04-01, PC-04-03, PC-04-05, PC-04-06 |
| Trailer | `Stage-Release: S2 flip` (this PR sets `release:`; never `S2 1` as well) |
| Required checks | everything of 09 §9.2.1's table **plus** `compat-rehearsal` (flip PR and `release:` change) and `unit-lifecycle` (every flip PR) |
| Spec | 09 §9.4 S2 rows Goal, Moves, HTTP (the mount at r1), Lifecycle, Config, Deploy, Rollback, Done when; 06 §6.5.1 (S2 phases), §6.5.2 (start/stop, `RequestRestart`); 05-health §5H.3.4, §5H.4 (budget table, supervisor settings); 05-config §1.7 (one resolver), §1.4 step 0; 04 §8.3 rows for `internal/sdnotify` and `internal/startup`; 09 §9.2.1 (trailer, `ci/stage.txt`, release grammar) |
| Digest | `docs/plans/platform-core/digests/S2.md` §1, §3, §4.1, §5 rules 1–8, §8 |

**Gate.** Open and make green any time; merge only after OWNER-03's `S1` row is current and PC-04-01, PC-04-03,
PC-04-05, PC-04-06 have merged. `check_soak.py` checks the predecessor row for a `release:` change.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-04-07 -b feat/pc-04-07-s2r1-flip origin/main
cd ../audiobook-organizer-pc-04-07 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| modify | `main.go`, `cmd/root.go` |
| modify | `internal/server/server_lifecycle.go`, `internal/server/server.go` |
| move | `internal/sdnotify/` → `core/internal/sdnotify/` (`git mv`; see Planner notes) |
| modify | `internal/serviceregistry/` (only as Step 6 requires) |
| modify | `internal/config/state_dir.go`; create `internal/config/state_dir_resolver_test.go` |
| modify | `deploy/audiobook-organizer.service`, `docker-compose.yml`, `deploy/com.audiobook-organizer.plist`, `README.md` |
| modify | `scripts/tests/test_unit_lifecycle.py` |
| modify | `ci/stage.txt` |
| also touched (not in the row, see Planner notes) | `core/kernel/`, `core/app/` (listeners, readiness, sdnotify), `internal/database/startup_notify.go` + test, `internal/search/register.go` + `register_sdnotify_test.go`, `internal/books/module.go` |
| create | `changelog.d/<date>_pc_04_07_s2r1_flip.md` (headerless; carries the release-note lines below) |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
grep -n 'server.SetVersion(version)\|server.SetEmbeddedFS(WebFS)' main.go                 # 2 (~32-34)
grep -n 'sdnotify.Capability' main.go                                                       # 1 (~53)
grep -n 'newServer              = server.NewServer\|srv := newServer(store)' cmd/root.go     # 2 (~84, ~414)
grep -n 'config.EnsureSecureStateDir()' cmd/root.go                                         # 1 (~336)
grep -n 'serveCmd.Flags().String("port"\|serveCmd.Flags().String("http3-port"\|serveCmd.Flags().Int("workers"' cmd/root.go   # 3 (~526, ~533, ~535)
grep -n 'sdnotify.Ready()\|sdnotify.Stopping()' internal/server/server_lifecycle.go        # 2 (~340, ~552)
grep -n 'context.WithTimeout(context.Background(), 30\*time.Second)' internal/server/server_lifecycle.go   # 1 (~582)
grep -n 'time.Sleep(500 \* time.Millisecond)' internal/server/server_lifecycle.go          # 1 (~572)
grep -n 'signal.Notify(quit' internal/server/server_lifecycle.go                            # 1 (~390)
grep -n '^func (s \*Server) configureAndStartHTTP' internal/server/server_lifecycle.go     # 1 (~903)
grep -n '^func EnsureSecureStateDir\|^const SecureStateDirEnv' internal/config/state_dir.go # 2
grep -n 'TimeoutStopSec\|KillMode\|RestartPreventExitStatus' deploy/audiobook-organizer.service | wc -l   # 0 at f07f516be
grep -n 'stop_grace_period\|ABO_CORE_SHUTDOWN_BUDGET' docker-compose.yml | wc -l           # 0
grep -n 'ExitTimeOut' deploy/com.audiobook-organizer.plist | wc -l                          # 0
grep -n 'stop-timeout' README.md | wc -l                                                    # 0
grep -rn 'serviceregistry.Register(' --include=*.go internal | grep -v _test | wc -l        # 43 at f07f516be (06 §6.11.2's 43); record yours
cat ci/stage.txt                                                                            # shipped_stage and release lines (release: S1 expected)
ls scripts/tests/test_unit_lifecycle.py .github/workflows/unit-lifecycle.yml                # exist (PC-02-06)
ls core/httpapi/listen.go core/httpapi/legacy.go core/config internal/books/module.go internal/books/configstore/store.go   # exist
```

**STOP and report** if any precondition file is missing, if `ci/stage.txt` does not say `release: S1`, or if an S0
PR already added any of the supervisor settings with different values.

## Steps

1. **`internal/sdnotify` → `core/internal/sdnotify`** (`git mv`, then fix the package path; no code change) and
   rewire all five importers in this PR (04 §8.3): `main.go`'s `--print-capabilities` fast exit prints the capability
   token through `app` (see Planner notes for the member name); `internal/server` loses READY/STATUS/STOPPING (the
   kernel's supervisor sends them: READY after listeners are up and startup completed, `STOPPING=1` at shutdown
   start, 05-health §5H.3.4); `internal/database` and `internal/search` drop PC-04-04's HEAD fallback, so the
   injected function is the only path (`serve` calls `database.SetStartExtender(app.ExtendStart)` before
   `initializeStore`; `books.Module` already wires search from `Deps.ExtendStart`); `internal/operations/registry`
   already uses `lifecycle.NotifySocketEnv`. Replace PC-04-02's temporary datagram helper in `app.ExtendStart` with the
   moved package. Adapt the two startup-notify tests to assert the injected function is called per step (they cannot
   see the socket from ABO any more).
2. **`serve` (cmd/root.go)** — keep opening the store and running books' migrations, encryption init,
   `loadConfigFromDB`, `SyncConfigFromEnv` and `config.Validate` **before** `app.New`, in HEAD's order (06 §6.5.1
   "Stages"; 05-config §1.12.2 "At S2"). Replace `EnsureSecureStateDir` with `app.ResolveStateDir(opts)` (Step 4).
   Map the flags `--port --host --tls-cert --tls-key --http3-port --external-url --read-timeout --write-timeout
   --idle-timeout --workers` onto `app.Options` (they feed `core.http.*`/`core.tasks.workers`). Build `opts` with
   ABO's values: `AppID "abo"`, `MetricPrefix`/`MetricNamespace "audiobook_organizer"`, `ServiceName
   "audiobook-organizer"`, `MetricViews` = `telemetry.HistogramBuckets()`, `HTTPMetricsScope` = otelgin's `ScopeName`
   and `Version` constants with `SchemaURL ""`, `ServedMeterProvider` = the meter provider the S1 façade installs as
   the OTel global, `PromRegisterer` nil, `LegacyAuthGinUntil: "books"`, `EnvPrefix "ABO"`, `LegacyConfig` (ABO's
   legacy core-key names), `LegacyConfigStore: configstore.New(store, legacy)`. Then
   `a, err := app.New(ctx, opts, books.Module(store, books.Options{Version: version, WebFS: WebFS}))` and
   `a.Run(ctx)`, with `ctx` from `signal.NotifyContext(..., SIGINT, SIGTERM)` (signal handling stays in `main`/`serve`;
   delete `Server.Start`'s own `signal.Notify`). `serve`'s OTel/slog bootstrap stays as it is at r1 (the façade
   installs remain until S2 r3, PC-06-02).
3. **`main.go`** — stop calling `server.SetVersion` and `server.SetEmbeddedFS` (09 S2 Moves); the values travel in
   `books.Options`. Prefer making them `Server` construction parameters over moving the global setter calls into
   `books.Register`; if that needs more than `internal/server/server.go` and `static_embed.go`, move the setter calls
   into `books.Register` and file a `todo.d/` fragment for the parameter refactor. Keep `--print-storage-format` and
   the operation-runner child mode as they are (`cmd/child_mode.go` keeps its own `newServer` for the runner).
4. **One state-directory resolver** (05-config §1.7): `internal/config/state_dir.go`'s `EnsureSecureStateDir` is no
   longer called by `serve`; its decision rule must equal `config.ResolveStateDir`. Keep the HEAD memo
   (`SecureStateDir()`) answering the resolved directory for ABO callers. Test file
   `internal/config/state_dir_resolver_test.go`: `TestStateDir_OneResolverYAMLDBPath` (a database path only in the
   YAML file plus an uncreatable default → the kernel and `serve` resolve the boot record, a restore intent and the
   legacy key file to that path's directory, as HEAD does) and an ABO-side `TestStateDirFallbackMatchesHEAD` table
   that runs HEAD's `EnsureSecureStateDir` rule and `app.ResolveStateDir` side by side. **No shipped file sets the
   canonical state-directory variable** (no `ENV` in either Dockerfile, none in compose or the plist; 09 S2 Deploy).
   The image job's second run (`--db /config/x.pebble`, PC-01-05) must keep passing: the keyring file beside the
   database on that volume.
5. **The kernel owns the process.** `books.Register` builds `*server.Server` and mounts its gin engine
   (`MountLegacy`, `LegacyAuthGin`, `ServesSPA: true`); everything `Server.Start`/`Shutdown` does that is books'
   becomes start/stop hooks registered by `books.Register` (06 §6.4.3 `Hook`; a long index open uses
   `ExtendsStart`). The listener code in `server_lifecycle.go` (`configureAndStartHTTP` and its helpers) is replaced by
   the kernel starting PC-04-03's `httpapi.StartListeners` with the core handler; `startupPeerAllowList` and
   `startupLoginSaturated` still refuse a bad config before listeners open. Stop order and budget per 06 §6.5.2 and
   05-health §5H.4: readiness false → `STOPPING=1` → listeners `Shutdown` within `core.shutdown.http_drain` (10 s;
   HEAD 30 s) → workers → hooks in reverse (books' hooks keep HEAD's internal order: the `system.shutdown` broadcast
   and its 0.5 s pause, the ops v3 streams closed, stores closed last) — all inside `core.shutdown.budget` (25 s; 8 s in
   a container when unset). Start order: core services → hooks → workers → listeners → readiness (06 §6.5.2).
6. **`serviceregistry`.** Done-check: `grep -rn 'serviceregistry.Register' --include=*.go internal | grep -v _test`
   lists none of the services 04 moves. Generate that set at your base from 06 §6.11.2: the registrants whose
   destination is a core package that serves the service **at S2**. At the evidence commit the only candidate is
   `configupdate` (`internal/config/register.go`, destination core `config`'s DB override layer), but core config is
   read-only until S3, so `PUT /api/v1/config` still needs ABO's update service at S2. If you conclude the S2 set is
   empty, record the generated list and the reason in the PR body and in the stage record; if any registrant's S2
   destination is unclear, **STOP and report** (see Planner notes). Do not delete `internal/serviceregistry` (S10).
7. **Supervisor settings (09 S2 Lifecycle; 05-health §5H.4):**
   - `deploy/audiobook-organizer.service` `[Service]`: `TimeoutStopSec=60`, `KillMode=mixed`,
     `RestartPreventExitStatus=78` (comment each line with its reason; keep `Type=notify`, `TimeoutStartSec=300`,
     `Restart=on-failure`).
   - `docker-compose.yml`: `stop_grace_period: 60s`; `ABO_CORE_SHUTDOWN_BUDGET=25s` in `environment:`.
   - `deploy/com.audiobook-organizer.plist`: `<key>ExitTimeOut</key><integer>60</integer>`.
   - `README.md`: document `docker run --stop-timeout 60`.
8. **`scripts/tests/test_unit_lifecycle.py`** — add the S2 case: `systemctl stop` during a running operation, once
   in-process and once in subprocess mode, stamps the run interrupted within the budget and the journal shows no
   SIGKILL (09 S2 Done when; 06 §6.10 `TestRestartRequest_ExitsRestartCode`'s unit-lifecycle half: `RequestRestart`
   exits 75 and the unit restarts it).
9. **`ci/stage.txt`** — set `shipped_stage: S2r1` and `release: S2r1` in this one PR.
10. **Changelog fragment = release-note lines** (prepended to the generated notes by the coordinator): HTTP drain
    30 s → 10 s (`core.shutdown.http_drain`); stop order of 06 §6.5.2 within `core.shutdown.budget` 25 s (8 s in a
    container with the key unset); the unit, compose and plist changes and that the owner must install the unit and
    plist on each host and `systemctl daemon-reload`; `docker run --stop-timeout 60`.

## Code contract (fixed by the spec)

```go
// cmd (shape)
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
a, err := app.New(ctx, opts, books.Module(store, books.Options{...}))
err = a.Run(ctx)
```

`app.Options` field values are digest §4.1's list; `httpapi.InstrumentScope{Name, Version, SchemaURL string}`.

## TDD tests to write first

- `TestStateDir_OneResolverYAMLDBPath` and the ABO-side `TestStateDirFallbackMatchesHEAD` (names fixed by 05-config
  §1.11/§1.7), in `internal/config/state_dir_resolver_test.go`.
- `TestLegacyAuthGin_RefusedWithoutOption` (05-http-api §12): `LegacyAuthGin` without `LegacyAuthGinUntil` naming the
  owner, and `OwnAuth` without a registered method, are boot errors. Add it in `core/kernel` if PC-04-02/PC-04-01
  did not.
- A guard test (name coined here: `TestOnlyRootCmdBooksImportModuleOrApp`) in `internal/arch` if PC-04-05 did not add
  the assertion: only `.`, `cmd` and `internal/books` import `mediacore/module` or `mediacore/app` (non-test).
- The unit-lifecycle case of step 8 (red first on the runner).
- Keep green, unchanged: the invite and bootstrap limiter tests (`go test ./internal/server/ -run
  'Invite|Bootstrap'`), PC-04-01's four real-engine tests, PC-04-06's oracles, `TestHistogramBucketsPinned`,
  `TestHEADSeriesStillPresent`, the seeded series contract test (golden diff empty: no removed family).

## Acceptance

```bash
grep -c 'server.NewServer' main.go                                                       # 0
grep -n 'newServer(store)' cmd/root.go | wc -l                                           # 0 (serve no longer builds the server; child_mode.go keeps its own)
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/sdnotify"' --include=*.go . | wc -l   # 0
ls internal/sdnotify 2>&1 | grep -c 'No such file'                                        # 1
grep -rln '"github.com/falkcorp/mediacore/module"\|"github.com/falkcorp/mediacore/app"' --include=*.go . | grep -v _test | grep -v '^./core/' | sort
#   expect only: ./cmd/root.go (and other cmd files if needed), ./internal/books/module.go, ./main.go if it uses app
grep -rn 'serviceregistry.Register' --include=*.go internal | grep -v _test              # none of the generated S2 set (Step 6)
grep -E '^(shipped_stage|release):' ci/stage.txt                                         # shipped_stage: S2r1 / release: S2r1
grep -c 'TimeoutStopSec=60\|KillMode=mixed\|RestartPreventExitStatus=78' deploy/audiobook-organizer.service   # 3
grep -c 'stop_grace_period: 60s\|ABO_CORE_SHUTDOWN_BUDGET=25s' docker-compose.yml        # 2
go build ./... && (cd core && go build ./...)                                            # exit 0
go test ./internal/config/ -run 'StateDir' -count=1                                      # ok
go test ./internal/server/ -run 'TestLegacyManifest_|TestLegacyMount_|TestS2Manifest_|Invite|Bootstrap' -count=1   # ok
go test ./internal/wirecontract/ -count=1                                                # ok (20 S2 goldens match)
make ci                                                                                  # exit 0
make -C core ci                                                                          # exit 0
#   on the PR: compat-rehearsal (cell c included) and unit-lifecycle green; docker-image green (both runs; credentialed smoke scrape in the image probe)
```

## Invariants and STOP conditions

- Authentication and authorization do not change (09 S2 Goal): gin's `RequireAuth`, `s.perm`, credential guards
  and limiters run as at HEAD; core stages 12–13 are off for the mount.
- No stored format changes and no new durable write (09 S2 Data/keys "None"); a rollback is a binary swap.
- The perimeter stays in gin at r1 (logger, recovery, headers, CORS, Basic gate, gzip, otelgin, rate limiter): any
  change in the wire goldens or the series golden → STOP.
- `PromRegisterer` stays nil; gin keeps serving `/metrics` from the default registry.
- Readiness: READY only after listeners are up and startup completed, never after module warm-up (05-health
  §5H.3.4). A boot that sends READY earlier → STOP.
- CLAUDE.md concurrency: no library-scale loop added; startup and serve keep HEAD's behaviour.

## Out of scope

Perimeter move (W05), logging and telemetry installs (W06), storage (S2b), deleting `internal/serviceregistry` (S10),
`internal/startup` (stays until S8).

## Rollback

Binary swap to the S1 build (`make rollback`); nothing persisted changed. The unit changes are harmless to older
builds (`TimeoutStopSec=60` exceeds HEAD's worst case: 0.5 s sleep + 30 s drain + store close).

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every new or changed file (unit, plist, compose and
README keep or gain their comment headers); headerless changelog fragment; conventional commit
`feat(s2): S2 r1 flip — serve runs app.New with books.Module` whose body ends with `Stage-Release: S2 flip` and
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase` once every required check (including
`compat-rehearsal` and `unit-lifecycle`) is green on an up-to-date branch; never squash; never `--admin`.

## Planner notes

- Files outside the row that this flip cannot avoid: `core/internal/sdnotify/` (the move's destination),
  `core/kernel/` and `core/app/` (listeners, readiness, the sdnotify switch in `app.ExtendStart`),
  `internal/database/startup_notify.go` and `startup_notify_test.go`, `internal/search/register.go` and
  `register_sdnotify_test.go` (PC-04-04's fallbacks), `internal/books/module.go` (hooks), and possibly
  `internal/server/static_embed.go` (Step 3).
- Spec gap: 04 §8.3 says the `--print-capabilities` fast exit is "handled by `app`", but 06 §6.4.10's S2 `app` row
  names no member for it. This brief lets the agent add one; its name is coined in the PR and should be added to 06.
- Spec gap: "none of the services 04 moves" has no S2 list. 06 §6.11.2 gives destinations by subsystem, all of
  which serve their service at a later stage; the brief's rule derives an expected empty set and asks the agent to
  record it. The owner or the spec should confirm.
- The row's done-check "main.go has no server.NewServer" is already true at `f07f516be` (the call is in
  `cmd/root.go:414`); the brief checks `cmd/root.go` too.
