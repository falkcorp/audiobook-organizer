<!-- file: docs/agent-tasks/platform-core/PC-17-04-s8-flip-kernel-metrics-native-routes.md -->
<!-- version: 1.0.0 -->
<!-- guid: f4f24719-2175-4b72-929f-a787261ba971 -->
<!-- last-edited: 2026-11-04 -->

# PC-17-04 — S8 flip: the kernel serves `/metrics` from the App registry; gin's route and ABO's backup staging deleted; native backup, update, system and health routes with aliases

| Field | Value |
|---|---|
| ID | PC-17-04 |
| Title | S8 flip: the kernel serves /metrics from the App registry; gin's route and ABO's backup staging deleted; native backup, update, system and health routes with aliases |
| Tier | **T3 — Opus / high.** The scrape path changes owner and registry in one PR; every series name must stay HEAD's and no family may get two emitters. |
| Wave | W17 (stage S8, release `S8`) — **the flip PR** (after the live PRs; PC-17-05 follows it) |
| Depends on | PC-17-03 |
| Trailer | `Stage-Release: S8 flip` (sets `shipped_stage: S8` and `release: S8` together) |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §3 (row "metrics registry"), §4 (05-metrics §3), §5 rules 6–10, §6 row 11, §7 S8 |
| Owning spec | 09 S8 ("Flip PR", "Moves", "Metrics", "Endpoints", "Config", "Done when"); 05-metrics §3, §6, §13; 05-backup §5B.3.8, §5B.10; 05-updater §5U.10; 05-health §5H.3.3 |

**Gate.** Changes `release:`: `check_soak.py` check (a) needs a current `S7` row at least 14 days old (OWNER-20).
Required on this PR besides the usual set: `compat-rehearsal` and `unit-lifecycle`.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-17-04" -b feat/pc-17-04-s8-flip origin/main
cd "$REPO/../abo-pc-17-04"
git worktree list
make web-deps
command -v promtool || echo "install promtool (Prometheus release tarball) before running the alert tests"
```

## Goal

In one PR (05-metrics §6 table, row "S8 on"):
1. The kernel mounts `Provider.Handler()` at `core.metrics.path` (`/metrics`) as `Router.Infra(path,
   "core:metrics.read", h)`, outside compression. ABO keeps HEAD's reach: every non-`Confined()` principal holds
   `core:metrics.read` through `app.Options.LegacyAmbientScopes`; a worker-scoped key never does.
2. The App's registry becomes **private** (`PromRegisterer` nil); `app.Options.PromRegisterer` is **removed**; ABO's
   legacy client_golang families move to `Provider.Registerer()` with their names unchanged; `cache.go:99` and
   `cache_snapshotter.go:24` read `Provider.Gatherer()`; gin's `/metrics` route is deleted.
3. The seeded series contract test's registry becomes the private one in this PR (it builds the stage's Provider
   through `coretest.MetricsProvider` without `PromRegisterer`), and `TestContractScrapesServedHandler` asserts the
   handler under test is the kernel mount.
4. New: `<namespace>_build_info{version}` (the binary's version, never the UI build ID) with the supported level
   vector; `deploy/prometheus` alert snippets for store level above supported (`<namespace>_storage_level`) and
   undecodable legacy rows above 0, with promtool tests.
5. `internal/backup`'s staging is deleted (core's backup has served it since PC-17-01; the kernel's P0a swap since
   S2b). Native `/api/core/v1/{backup,update,system,health}` routes are mounted with window aliases:
   `/api/v1/backup` (4), `/api/v1/update` (3), `/api/v1/system` (7), `/api/health`, `/api/v1/health`. Permanent:
   `/health`, `/metrics`. Config `backup_*` → `core.backup.*` (aliases; check PC-17-01 did it).

## Files

Create: `deploy/prometheus/storage-level.rules.yml`.

Modify: `internal/metrics/metrics.go`, `internal/server/server_lifecycle.go` (gin `/metrics` route deleted),
`core/app/options.go` (`PromRegisterer` removed), `main.go`, `internal/telemetrypolicy/` (contract test on the private
registry), `deploy/prometheus/alert-rules.yml`, `deploy/prometheus/alert-rules.test.yml`, `docs/compat-surfaces.md`,
`internal/books/compat-surfaces.md`, `core/openapi/core.json`, `ci/stage.txt`.

See Planner notes for files the row does not list.

## Re-verify before editing

Counts at `748fd787a` (equal at this plan's HEAD); generated counts at your base go in the PR description.

```bash
git grep -n 's.router.GET("/metrics"' -- internal/server/server_lifecycle.go        # 1 (:1746)
git grep -n 'prometheus.DefaultGatherer.Gather()' -- internal/server/handlers/cache.go internal/server/cache_snapshotter.go   # 2 (cache.go:99, cache_snapshotter.go:24)
git grep -n 'Name:      "process_memory_alloc_bytes"\|Name:      "process_goroutines"' -- internal/metrics/metrics.go   # 2 (books' process gauges; they stay books', fed by the status worker)
git grep -n '^func Register()' -- internal/metrics/metrics.go                         # 1 (:300)
git grep -n 'GET("/system/\|"/system/reset"\|"/system/factory-reset"' -- internal/server/wire_system_routes.go | wc -l   # 7 lines naming /system routes (5 GETs + reset + factory-reset)
git grep -n 'GET("/health"\|GET("/api/health"\|GET("/api/v1/health"' -- internal/server/server_lifecycle.go   # 3 (:1753-1755)
grep -n '^rule_files:' -A2 deploy/prometheus/alert-rules.test.yml                     # rule_files: alert-rules.yml
git grep -n 'PromRegisterer' -- core/app/options.go main.go | head                    # >= 1 at your base (set at S2 r3, PC-06-02)
git grep -ln 'func TestContractScrapesServedHandler\|func TestHEADSeriesStillPresent\|func TestHistogramBucketsPinned' -- '*.go'   # the contract test files (internal/telemetrypolicy at your base)
```

Note: `/system/reset` and `/system/factory-reset` are already `Alias`es of the `internal/storagehttp` route since
S4 r2 (09 §9.2.1 row 1); do not re-alias them. Re-derive which of the 7 `/api/v1/system` routes get a new alias here
(09 S8 "Endpoints"; `/system/status` stays a books route, 05-health §5H.10) and write the table in the PR description.

## Steps

1. Read 05-metrics §3 and §6 in full (the stage table, "Series-name checks", the gather test, the golden-diff table,
   the smoke check), §13; 05-rbac §6.3 (`LegacyAmbientScopes`); 05-http-api §3.5 (`Router.Infra`); 05-backup §5B.3.8;
   05-updater §5U.10; 05-health §5H.3.3; 09 §9.2.9 (ledger rows).
2. Write the tests below first; run the seeded contract test before any change and keep its output for the diff.
3. Move the legacy client_golang families' registration from the default registry to `Provider.Registerer()`
   (names, help, label sets unchanged); delete `app.Options.PromRegisterer` and every use; point
   `cache.go:99`/`cache_snapshotter.go:24` at `Provider.Gatherer()`.
4. Mount the kernel's `/metrics` (`Router.Infra`, `core:metrics.read`, excluded from compression; promhttp negotiates
   gzip); delete gin's route in the same PR. Keep the Cloudflare recovery exclusion for `/metrics`.
5. Add `<namespace>_build_info{version}` and the supported level vector; add alert rules to
   `deploy/prometheus/storage-level.rules.yml` (store level above supported) and to `alert-rules.yml` (undecodable
   legacy rows above 0), each with a promtool test case in `alert-rules.test.yml` (add the new rule file to its
   `rule_files`).
6. Mount the native routes (`core/internal/backuphttp`, `updaterhttp`, `healthhttp`) and register the aliases with
   the compat counter; delete gin's handlers for the aliased paths and `internal/backup`'s staging code. The legacy
   `POST /api/v1/backup/restore` becomes stage-restore (it can no longer extract into an arbitrary `target_path`).
7. Ledger rows (C8) in `docs/compat-surfaces.md`, refreshed embedded copy, `core/openapi/core.json` regenerated.
8. `ci/stage.txt`: `shipped_stage: S8`, `release: S8`.
9. Draft the release-note line (09 §9.2.1 row 11) for the coordinator: "If you restore a backup on this release,
   restore one while rolled back, or restore one made before a rollback, everyone signs in again and every API key must
   be re-enabled on the keys page by its owner or an admin."

## Code contract (05-metrics §3; 05-http-api §3.5)

```go
func (p *Provider) Registerer() prometheus.Registerer
func (p *Provider) Gatherer() prometheus.Gatherer
func (p *Provider) Handler() http.Handler
// kernel: router.Infra(cfg.Metrics.Path /* "/metrics" */, "core:metrics.read", provider.Handler())
```

Routes and permissions: 05-backup §5B.3.8 table; 05-updater §5U.10 table; 05-health §5H.3.3 table (copy them).

## Tests to write first (09 S8 Done when; 05-metrics §6)

- The seeded series contract test scraping the private registry's `Provider.Handler()`: golden passes; every books
  series name equals S7's; `http_server_*` stay otelgin's; the golden diff adds only `<namespace>_build_info` and the
  PR's other new core families (not `go_memstats_alloc_bytes` or `go_goroutines`, present since HEAD).
- `TestHEADSeriesStillPresent`, `TestHistogramBucketsPinned`, `TestContractScrapesServedHandler` (kernel mount),
  `TestABOMetricsGatherNoDuplicateOrMissing` on the private registry (no family with two owners).
- `TestWorkerKey_NoAmbientScopes` on `/metrics` (a worker-confined key gets 403; any other credential 200), and
  `TestInfra_MetricsRequiresScope` (an unauthenticated scrape gets 401).
- `promtool test rules deploy/prometheus/alert-rules.test.yml` green, with cases for both new alerts.
- `TestBackupRoutesPermissions`, `TestBackupDownload_SystemManageOnly403`, `TestAliasHeadersAndShape` for one alias of
  each family, `TestCompatLedgerMatchesCode`, `TestCompatLedgerCopyFresh`, `TestNoContractBeforeSunset`.
- R-test cell (e) and the restore matrix stay green (`make compat-rehearsal`).

## Acceptance

```bash
git grep -n 's.router.GET("/metrics"' -- internal/server | wc -l                   # 0
git grep -n 'PromRegisterer' -- '*.go' | wc -l                                      # 0
git grep -n 'prometheus.DefaultGatherer\|prometheus.DefaultRegisterer' -- internal | grep -v _test.go | wc -l   # 0
grep -c '^shipped_stage: S8$' ci/stage.txt; grep -c '^release: S8$' ci/stage.txt   # 1 and 1
promtool test rules deploy/prometheus/alert-rules.test.yml                          # SUCCESS
promtool check rules deploy/prometheus/storage-level.rules.yml deploy/prometheus/alert-rules.yml   # SUCCESS for each
go build ./... && (cd core && go build ./...)                                       # exit 0
go test ./internal/telemetrypolicy/... ./internal/metrics/... ./internal/books/... ./internal/server/... -race -count=1   # all "ok"
make compat-rehearsal                                                               # exit 0
make ci                                                                             # exit 0
make -C core ci                                                                     # exit 0
```

After the deploy (owner, OWNER-21): the credentialed smoke scrape returns 200 with `process_resident_memory_bytes`,
`go_goroutines` and `<namespace>_build_info`.

## Out of scope

The UI calls (PC-17-05); any rename of a series; adding the status topic to projection A; encryption; the updater's
deferred-build members.

## Rollback

Binary swap: no stored format changes; S7 serves `/metrics` from gin again over the default registry, with the same
series names. Archives written on S8 restore on S7 and HEAD.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files (YAML rule files included); one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S8 flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Check `docs/process/executive-summaries.md`; a flip with this blast radius usually qualifies.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row omits `internal/backup/` (its staging is deleted here: 09 S8 "Flip PR"), the native route packages
  `core/internal/backuphttp/` (in no row), `core/internal/updaterhttp/` and `core/internal/healthhttp/` (created by
  PC-17-02/03; mounted here), `internal/server/handlers/` (`cache.go:99`), `internal/server/wire_system_routes.go` and
  the kernel mount in `core/kernel/`.
- 09 S8 counts 7 `/api/v1/system` aliases, but 05-health §5H.10 keeps `/system/status` a books route and the two
  reset routes have been storagehttp aliases since S4 r2; the alias set needs a ruling, recorded in the PR.
- `/api/health` and `/api/v1/health` are listed as window aliases, while `GET /api/core/v1/health` needs
  `core:system.manage` and HEAD's paths answer every caller; the SPA's reconnect probe (`web/src/App.tsx`) and
  `getAppVersion` call `/api/v1/health` unauthenticated. The alias must keep HEAD's reach for those paths (alias onto
  the permanent `/health` shape, not the admin route); confirm with the spec owner.
