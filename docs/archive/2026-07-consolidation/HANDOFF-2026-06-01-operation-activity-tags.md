<!-- file: docs/HANDOFF-2026-06-01-operation-activity-tags.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b7fd2db-4f3c-4f24-8e33-d25399c6a8b7 -->
<!-- last-edited: 2026-06-01 -->

# Operation Activity + Tag Handoff

## Branch

- Worktree: `/Users/jdfalk/repos/github.com/jdfalk/.worktrees/audiobook-organizer-operation-activity`
- Branch: `fix/operation-activity-tags`
- Base: `main` at `d8b2965d`

## What Changed

- UOS operation reporter now accepts an `ActivityRecorder` and mirrors every `reporter.Log` / `reporter.Logger()` line into the unified activity log.
- Mirrored operation activity entries include structured details:
  - `def_id`
  - `op_name`
  - `plugin`
  - `component`
  - existing slog attrs such as `phase`, progress counts, and outcomes
- `/api/v1/operations/:id/activity` now returns a lean operation-activity response shape and falls back to `op_logs_v2` when the activity store has no rows for older operations.
- Operation SSE `op.log` payloads now include normalized level and `created_at`.
- Operations store keeps the latest `op.log` SSE event so UI panels can append instead of refetching.
- Activity page expanded operation logs now load once and append live SSE log lines. The per-operation refresh button is the explicit full reload path.
- Operation Activity modal no longer polls/repaints on an interval; it appends live SSE lines and refreshes only when the user clicks refresh.
- Tag enrichment now produces more useful operation tags:
  - `plugin:<plugin>`
  - `def:<def_id>`
  - `phase:<phase>`
  - `component:<component>`
  - derived domain tags for metadata, dedup, fingerprint, HTTP, TLS, cache, and common error classes
- Generic `source:server` and `action:system` tags are no longer added for plain server/system rows.
- Activity tag chips now use clearer coloring:
  - errors use red
  - warnings use yellow, not orange
  - plugin/def/phase/http/domain/network/error namespaces have distinct treatments
  - the Activity page hides legacy redundant `source:server` and `action:system` chips if old rows still contain them

## Files Touched

- `internal/operations/registry/reporter_db.go`
- `internal/operations/registry/registry.go`
- `internal/operations/registry/worker.go`
- `internal/operations/registry/subprocess.go`
- `internal/server/registry_wire.go`
- `internal/server/activity_handlers.go`
- `internal/activity/api.go`
- `internal/activity/api_test.go`
- `web/src/stores/useOperationsStore.ts`
- `web/src/pages/ActivityLog.tsx`
- `web/src/components/OperationActivityPanel.tsx`
- `web/src/services/activityApi.ts`
- `web/src/utils/activityTagColors.ts`

## Validation

- `go test ./internal/activity ./internal/operations/registry ./internal/server`
  - First run found an expected test failure after intentionally removing `source:server` / `action:system`; tests were updated.
  - Rerun passed.
- `npm test -- --run src/components/OperationActivityPanel.test.tsx src/stores/useOperationsStore.test.ts src/utils/__tests__/activityTagColors.test.ts`
  - Blocked locally because `jsdom` is not installed in the current Node environment:
    `Cannot find package 'jsdom' imported from /Users/jdfalk/node_modules/vitest/...`
  - `npm list jsdom` reports `(empty)`.
- `npm run build`
  - Blocked locally because frontend dependencies are not installed/resolved in this worktree environment. TypeScript reported missing packages such as `react`, `@mui/material`, `react-router-dom`, and `zustand`.
  - The earlier syntax error in `web/src/stores/useOperationsStore.ts` was fixed before this build attempt; the remaining build output is dependency-resolution noise.

## Remaining Overnight Burndown

- Run the focused frontend tests in an environment with `jsdom` installed.
- Consider adding a dedicated test for `/api/v1/operations/:id/activity` fallback from `op_logs_v2`.
- Consider adding a UI test that expanding an operation does not create repeated `/logs` or `/activity` polling requests.
- Audit whether HTTP request rows should be source-classified as `http` instead of `server` at parse time; current patch improves tags but leaves source as stored for historical compatibility.
- Decide whether high-frequency progress rows should remain in `Tier: info` or be downgraded/batched for very long scans. Current reporter logs one row per distinct progress message, which matches the existing progress-message throttle.
