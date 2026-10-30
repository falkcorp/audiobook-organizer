<!-- file: docs/decisions/2026-10-30-activity-filters-silently-ignored.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e2a9c14-7b30-4d68-a1f5-90c3d8e6b472 -->
<!-- last-edited: 2026-10-30 -->

# Two filters the endpoint accepted and ignored

## /system/logs?operation_id=

Chosen: the `OperationLogsProvider` method is now `OperationLogsByID(c, id)`.
The old `GetOperationLogs(c)` is removed; its only caller was `getSystemLogs`,
because `GET /operations/:id/logs` was retired on 2026-08-16 (so there is no
route handler left to switch over). The visibility gate (`mayReadLogsOf`,
`readableLogs`) stays inside the one shared method.

Rejected: setting a fake `id` gin param before delegating (hides the contract in
mutable request state); keeping both methods (an unused method that the
dead-code ratchet would flag).

## /activity/sources counts

Finding: the store's scan already applied `ExcludeEntry` (via `matchesFilter`);
the real defects were that the handler never set it and that
`GetDistinctSources` memoizes by a key that cannot include a function, so a
restricted caller could be served an unrestricted caller's counts, or the
reverse. The `GetDistinctSources` signature already takes an `ActivityFilter`,
so no interface or mock change was needed; Pebble is the only store.

Chosen: a filter carrying `ExcludeEntry` bypasses the result cache both ways.
The scan stays bounded (`activityQueryScanBudget`) and decodes no Details.
Cost: every page load by a caller with a predicate (all handler callers) now
runs the bounded scan instead of a cache hit.

Rejected: keying the cache by caller class (visibility also depends on per-op
records that change, so a stale key would show wrong counts); computing counts
from a paged list (undercounts).

Follow-up for the owner: if the uncached scan shows up in profiles, add a short
TTL cache keyed by a caller-supplied discriminator.
