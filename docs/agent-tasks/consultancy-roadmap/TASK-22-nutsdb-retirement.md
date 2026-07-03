<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-22-nutsdb-retirement.md -->
<!-- version: 1.0.0 -->
<!-- guid: 065970de-8d58-4ac9-9021-6682a4ac19d4 -->
<!-- last-edited: 2026-07-03 -->

# TASK-22 — NutsDB retirement (activity dual-write cutover + metrics to Pebble)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend
subagent · **Wave:** 2 · **Depends on:** TASK-19

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-22-nutsdb-retirement" -b agent/cr-22-nutsdb-retirement origin/main
cd "$REPO/.worktrees/cr-22-nutsdb-retirement"
git rebase origin/main
```

## Goal

Finish the NutsDB→Pebble migration that has been sitting in its "safe rollback
window" since ~2026-06-10 and stop paying its costs forever: (1) collapse
`DualWriteActivityStore` to a Pebble-only pass-through so activity stops
double-writing and NutsDB stops being opened at every boot (this also
eliminates the accepted-but-annoying `NutsActivityStore.Close()` goroutine
leak, because NutsDB will no longer be opened at all — see Background); (2)
flip `metricsstore` in `registry_wire.go` from `NutsMetricsStore` to the
already-built `PebbleMetricsStore` (its TTL sweep job already exists and is
registered); (3) verify (do not assume) whether anything is retained in
NutsDB that never made it to Pebble before any deletion; (4) remove the
`github.com/nutsdb/nutsdb` dependency and delete the NutsDB source files as a
**separate, later commit**, gated on a soak period — do not do this in the
same PR as steps 1–2.

This task is split into two PRs on purpose. PR 1 (steps 1–3) is safe to ship
immediately once tests pass. PR 2 (step 4, dependency removal) requires an
owner greenlight after a soak window — see Acceptance criteria.

## Background (verify before editing)

- **Activity dual-write.** `internal/activity/register.go` unconditionally
  opens `activity.nutsdb` (`database.NewNutsActivityStore`) at every boot and
  wraps it with `database.NewDualWriteActivityStore(nutsStore, pebbleStore,
  readFromPebble)` whenever a Pebble backend is available. `readFromPebble` is
  computed once at startup from `database.IsActivityPebbleBackfillDone(pebbleStore.DB())`
  — i.e. the flag `system:backfill:activity_pebble_v1_done` set in Pebble.
  As of this writing that flag has been set on prod (per `docs/consultancy/01-storage-architecture.md`
  and `TODO.md`), meaning reads already come from Pebble; only writes are
  still duplicated to NutsDB with no read benefit.
- **`DualWriteActivityStore`** (`internal/database/dual_write_activity_store.go`)
  writes to both `nuts` and `pebble` on every mutating call (`Record`,
  `Summarize`, `Prune`, `WipeAllActivity`, `CompactByDay`,
  `MigrateSystemActivityLogs`, `RecompactDigests`) and its `Close()` (verified
  at lines ~195-201) closes both backends — this is where the NutsDB
  `Close()` goroutine (see next bullet) gets triggered in every process that
  builds this wrapper.
- **The NutsDB goroutine "leak" is a known, accepted, third-party limitation** —
  see `TODO.md` entry `NUTSDB-CLOSE-GOROUTINE-LEAK` (investigated
  2026-07-01, marked benign because the activity store is a process-lifetime
  singleton). Do NOT attempt to patch nutsdb itself or add workarounds inside
  `nuts_activity_store.go` — the fix here is to stop opening NutsDB at all,
  which makes the leak moot rather than "fixed" per se. State this precisely
  in the commit message; don't claim you "fixed a goroutine leak" — you
  removed the code path that triggered it.
- **`PebbleActivityStore`** (`internal/database/pebble_activity_store.go`)
  already implements the full `ActivityStorer` interface standalone
  (`Record`, `Query`, `Summarize`, `Prune`, `GetDistinctSources`,
  `WipeAllActivity`, `CompactByDay`, `MigrateSystemActivityLogs`,
  `RecompactDigests`, `Close`) and shares the main PebbleDB instance
  (constructed from `ps.DB()` in `register.go`, no separate file). This means
  the activity cutover is "delete the wrapper, return the Pebble store
  directly" — no new store code is needed.
- **Metrics.** `internal/server/registry_wire.go` registers a service named
  `"metricsstore"` (verify line numbers — see re-verify grep below) that
  builds a `database.NutsMetricsStore` at `{dirname(DatabasePath)}/metrics.nutsdb`
  and returns `(*database.NutsMetricsStore)(nil)` on any error or when
  `DatabasePath` is empty (test paths). `database.PebbleMetricsStore`
  (`internal/database/pebble_metrics_store.go`) already implements the full
  `MetricsStorer` interface (`RecordCacheStatsSnapshots`,
  `GetCacheStatsHistory`, `PruneCacheStatsHistory`, `Close`) **plus**
  `SweepExpiredMetrics() (int64, error)` to compensate for Pebble having no
  native per-key TTL — and that sweep is already wired as a maintenance job
  in `internal/maintenance/jobs/sweep_pebble_metrics_ttl.go` (registered via
  `maintenance.Register(&sweepPebbleMetricsTTLJob{})` in that file's `init()`).
  There is nothing left to build for metrics — only the registry wiring needs
  to change.
- **`MetricsStorer` interface** is defined in `internal/database/activity_storer.go`
  (verify — same file as `ActivityStorer`, not a separate `metrics_storer.go`).
  Its doc comment currently says "NutsMetricsStore is the production
  implementation" — update that comment when you flip the wiring.
- **Retained-only-in-NutsDB check (step 3).** Before deleting/soaking,
  confirm there is no data that exists in NutsDB but never migrated to
  Pebble. The activity backfill flag mechanism
  (`internal/database/pebble_activity_backfill.go`) implies a one-time
  backfill op already ran; find it (grep for `activity_pebble_v1_done` or
  `ActivityPebbleBackfillKey` usage in `internal/maintenance/jobs/` or
  `internal/operations/`) and confirm it covers full history, not just
  forward writes since the flag was introduced. Metrics (`metrics.nutsdb`)
  has **no equivalent backfill** — `NutsMetricsStore` and `PebbleMetricsStore`
  are independent stores that were never cross-migrated; `metrics.nutsdb`
  history (cache-stats snapshots) will NOT carry forward automatically when
  you flip the registry wiring. Since `CacheStatsSnapshot` data is a rolling
  30-day operational metric (not user data), losing continuity at cutover is
  low-risk, but you must say this explicitly in the PR description rather
  than silently dropping history — do not build a metrics backfill op unless
  asked; just document the gap.

**Re-verify these anchors before editing — line numbers drift:**

```bash
grep -n "func init\|NewNutsActivityStore\|NewDualWriteActivityStore\|IsActivityPebbleBackfillDone\|pebble-activitystore\|KeyActivityStore" internal/activity/register.go
grep -n "func (d \*DualWriteActivityStore)\|func NewDualWriteActivityStore" internal/database/dual_write_activity_store.go
grep -n "metricsstore\|NutsMetricsStore\|PebbleMetricsStore" internal/server/registry_wire.go
grep -n "type ActivityStorer\|type MetricsStorer" internal/database/activity_storer.go
grep -n "func New\|func (s \*PebbleActivityStore)\|func (s \*PebbleMetricsStore)" internal/database/pebble_activity_store.go internal/database/pebble_metrics_store.go
grep -n "NUTSDB-CLOSE-GOROUTINE-LEAK" TODO.md
```

## Step-by-step

### PR 1 — activity + metrics cutover (ship immediately once green)

1. In `internal/activity/register.go`, in the `activitystore` service's
   `Build` func:
   - Keep opening `pebbleStore` via the existing `"pebble-activitystore"`
     dependency exactly as today.
   - Remove the `database.NewNutsActivityStore(activityDir)` call and the
     `activityDir := filepath.Join(...)` line entirely — do not open
     `activity.nutsdb` at all.
   - Remove the `database.NewDualWriteActivityStore(...)` call; return
     `pebbleStore` directly (it already satisfies `ActivityStorer`).
   - Keep the existing nil/non-Pebble fallback behavior conceptually, but
     since there's no NutsDB fallback anymore, a missing Pebble backend
     should now be a hard error (`fmt.Errorf("activitystore: pebble activity
     store not available")`) rather than a NutsDB-only degraded mode — there
     is no more degraded mode to fall back to.
   - Update the package doc comment at the top of the file (the "WHY backend
     selection during the migration window (T024)" block) — remove the
     stale forward-looking language about NutsDB/dual-write/T024b, replace
     with a short note that the migration is complete and reads/writes are
     Pebble-only.
   - Drop the now-unused `filepath` import if nothing else in the file uses
     it — verify with `goimports`/`go build` rather than assuming.
2. In `internal/database/dual_write_activity_store.go`: do NOT delete this
   file yet (see PR 2). Add a doc comment at the top marking it
   **unused-as-of-<date>, retained for one release cycle as a rollback
   reference; delete in the NutsDB-removal PR**. Do not wire it anywhere in
   PR 1 — leaving it merely present-but-unreferenced is fine and avoids an
   unnecessary file-deletion diff in the same PR as the behavior change.
3. In `internal/server/registry_wire.go`, in the `"metricsstore"` service's
   `Build` func:
   - Replace the `database.NewNutsMetricsStore(dir)` call with
     `database.NewPebbleMetricsStore(...)`. It needs a `*pebble.DB` — get it
     the same way `"pebble-activitystore"` does in `register.go`
     (`serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)`,
     type-assert to `*database.PebbleStore`, call `.DB()`). Add
     `serviceregistry.KeyStore` to this service's `Needs` list if not
     already present.
   - On a non-Pebble backend (test doubles, SQLite), fall back to returning
     `(*database.PebbleMetricsStore)(nil)` — mirror the existing nil-fallback
     pattern used elsewhere in this file, do not panic.
   - Remove the now-unused `dir := filepath.Join(...)` NutsDB path
     construction and the `database.NewNutsMetricsStore` call.
   - Update the comment above the service registration (currently says
     "NutsDB-backed cache-stats snapshot store... Lives at
     {dirname(DatabasePath)}/metrics.nutsdb") to describe the Pebble-backed
     store instead.
   - Update the `MetricsStorer` interface doc comment in
     `internal/database/activity_storer.go` ("NutsMetricsStore is the
     production implementation") to name `PebbleMetricsStore` instead.
4. Confirm `sweep-pebble-metrics-ttl` (already registered in
   `internal/maintenance/jobs/sweep_pebble_metrics_ttl.go`) is enabled by
   default / on the standard maintenance schedule — if it's currently
   dormant because `PebbleMetricsStore` was never the primary, check how
   maintenance jobs get their store dependency (likely via the same
   `"metricsstore"` service) and confirm this cutover activates it without
   further changes. Do not build a new job.
5. Run the NutsDB-only-retained-history check from Background step 3. Write
   your findings (what you checked, what you found) into the PR description
   verbatim — this is required evidence, not optional color.
6. Update/extend tests:
   - `internal/activity` tests that assert dual-write or NutsDB-specific
     behavior in the `activitystore` service build path need updating to
     assert a plain `*database.PebbleActivityStore` (or the
     `ActivityStorer` interface) is returned instead.
   - `internal/server` tests covering `"metricsstore"` wiring need updating
     to assert a `*database.PebbleMetricsStore` is returned.
   - Do not delete or weaken existing `NutsActivityStore`/`NutsMetricsStore`
     unit tests themselves in PR 1 — those stores still exist as files,
     just unwired. They get deleted in PR 2 along with the files.
7. Bump the file header (version bump + `last-edited`) on every file you
   touch, per `.standards/instructions/file-headers.md`.

### PR 2 — dependency removal (separate PR, after soak — see Acceptance)

8. After the soak window has passed and an owner has greenlit removal:
   delete `internal/database/nuts_activity_store.go`,
   `internal/database/nuts_metrics_store.go`,
   `internal/database/dual_write_activity_store.go`, their `*_test.go`
   companions, and the `github.com/nutsdb/nutsdb` line from `go.mod`
   (run `go mod tidy` after). Also remove/gate any one-time backfill op
   code and the `ActivityPebbleBackfillKey` sentinel machinery if it is no
   longer referenced by anything (verify with `grep -rn
   ActivityPebbleBackfillKey` first — it may still be read by a
   still-useful migration-status check; do not remove blindly).
   Add a `TODO.md` entry closing out `NUTSDB-CLOSE-GOROUTINE-LEAK` as
   "resolved by removal" rather than leaving it as an accepted limitation.

## How to test

```bash
go build ./...
go test ./internal/activity/... ./internal/database/... ./internal/server/... ./internal/maintenance/... -count=1
go vet ./internal/activity/... ./internal/database/... ./internal/server/...
```

For PR 2 additionally run `go mod tidy && go build ./...` and confirm no
remaining references:

```bash
grep -rn "nutsdb" --include="*.go" internal/ | grep -v _test.go
```

## Acceptance criteria

**PR 1 (ship immediately once green):**

- [ ] `internal/activity/register.go` no longer opens `activity.nutsdb`;
      `activitystore` service returns the Pebble activity store directly
      (no `DualWriteActivityStore` in the live path).
- [ ] `internal/server/registry_wire.go`'s `"metricsstore"` service builds
      and returns a `PebbleMetricsStore`, not `NutsMetricsStore`.
- [ ] `sweep-pebble-metrics-ttl` maintenance job is confirmed active against
      the now-primary Pebble metrics store.
- [ ] PR description documents the NutsDB-only-retained-history check
      (Background step 3 / step-by-step step 5) with concrete findings —
      not a bare assertion that it's fine.
- [ ] PR description explicitly states the `metrics.nutsdb` cache-stats
      history-continuity gap (no backfill exists) rather than silently
      dropping it.
- [ ] All updated/existing tests green; `go vet` clean.
- [ ] File headers bumped on every changed file.
- [ ] `dual_write_activity_store.go` retained (not deleted) in this PR,
      marked as unused/pending-removal.

**PR 2 (dependency removal) — STOP HERE without owner greenlight:**

- [ ] Do not open PR 2 until an owner has explicitly approved that the soak
      period (one release cycle, per the original T024 plan) has completed
      with no rollback needed.
- [ ] Once greenlit: `nutsdb`/`NutsActivityStore`/`NutsMetricsStore`/
      `DualWriteActivityStore` files deleted, `go.mod` no longer lists
      `github.com/nutsdb/nutsdb`, `go mod tidy` clean, full build/test green.
- [ ] `TODO.md`'s `NUTSDB-CLOSE-GOROUTINE-LEAK` entry updated to record
      resolution-by-removal.

## Commit message

PR 1:

```
refactor(storage): cut activity + metrics over to Pebble-only, retire NutsDB dual-write

Activity has dual-written to NutsDB and Pebble since the T024 migration
window opened ~2026-06-10; the backfill flag has been set and reads already
come from Pebble, so the NutsDB write is pure cost with no benefit. Metrics
was still NutsDB-primary despite PebbleMetricsStore (with TTL sweep) already
being fully built. Collapse activitystore to PebbleActivityStore directly and
flip metricsstore to PebbleMetricsStore; this also stops NutsDB from being
opened at all, which is the real fix for the previously-accepted
NutsDB-Close goroutine limitation (not opening it is better than living with
it). NutsDB source files and the go.mod dependency are retained pending a
soak period — removed in a follow-up PR.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

PR 2 (separate commit, after greenlight):

```
chore(storage): remove NutsDB dependency after activity/metrics soak

Soak period for the Pebble-only activity/metrics cutover has passed with no
rollback needed. Delete NutsActivityStore, NutsMetricsStore,
DualWriteActivityStore and their tests; drop github.com/nutsdb/nutsdb from
go.mod. Closes NUTSDB-CLOSE-GOROUTINE-LEAK by removal.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-22-nutsdb-retirement
gh pr create --fill
gh pr merge <number> --rebase
```

Open PR 2 as its own branch/worktree off main after PR 1 has merged and the
soak/greenlight condition is met — do not bundle both into one PR.

## Idempotency / Rollback

If `internal/activity/register.go`'s `activitystore` service already returns
a bare `*database.PebbleActivityStore` (no `DualWriteActivityStore` in the
build path) **and** `registry_wire.go`'s `"metricsstore"` service already
builds a `PebbleMetricsStore`, PR 1 of this task is done — verify with:

```bash
grep -n "NewDualWriteActivityStore\|NewNutsActivityStore" internal/activity/register.go
grep -n "NewNutsMetricsStore\|NewPebbleMetricsStore" internal/server/registry_wire.go
```

If both greps show only the Pebble constructors (or no matches for the Nuts
ones) in the live build paths, stop — do not re-do the cutover. If
`github.com/nutsdb/nutsdb` is already absent from `go.mod`, PR 2 is also
done; do not attempt to remove it again.

Rollback for PR 1 = revert the commit; this restores
`DualWriteActivityStore`/`NutsMetricsStore` wiring, which still exist as
files and still function (assuming PR 2 has not yet run). Rollback for PR 2
is **not simple revert** — once NutsDB files/dependency are deleted, a
revert only restores source; any data written exclusively to Pebble in the
interim needs no backfill (Pebble was already the source of truth for
activity reads), but restarting NutsDB-backed metrics after a gap will start
from an empty `metrics.nutsdb` file. Do not attempt PR 2's rollback by
reverting alone without re-confirming this.
