<!-- file: docs/decisions/2026-10-31-search-cache-rename-flake-drain-budget.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8f0c1d5e-3a47-4b62-9e1b-6c2d7a9f4e30 -->
<!-- last-edited: 2026-10-31 -->

# Decision log: TestSearchResultCache_RenamesAreReflected drain flake

## Evidence

GitHub CI run 41270758828 ("Fixture Tests (no -short, race) 1/3") failed
`TestSearchResultCache_RenamesAreReflected/title/incremental` with
`search_result_cache_test.go:269: index queue did not drain within timeout`.
Line 269 is `setupRenameTarget`, which waits (`drainQueue`, 5 s) after creating
the rename target and before any rename. The subtest took 7.52 s, so the seed
took about 2.5 s and the single-document wait expired. No assertion about a
stale result fired, so there is no evidence production can serve a stale result
after a rename.

## Chosen

`drainQueue` delegates to `drainTB` (60 s, same fixtures' seed wait). The
timeout message now carries `indexWorkerBusy`, queue depth and the oldest
in-flight Bleve write, so a genuinely wedged worker is distinguishable from a
slow one.

## Rejected

- Raising only `drainQueue`'s own deadline: leaves two waiters with two budgets
  that can drift again.
- Replacing the wait with a sleep or polling the search result: the documented
  signal (`indexWorkerBusy == 0`, incremented before enqueue, decremented after
  the commit) is already exact; only the budget was wrong.
- Changing production: the worker accounting (`enqueueIndex`, `runIndexWorker`,
  `fanOut`) was re-read and balances on every path, including coalesce, drop and
  rename fan-out.
