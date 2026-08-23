- [ ] **Decide the fate of `api.startBulkMetadataFetch`, now caller-less.** Deleting
      the unreachable Bulk Fetch Metadata dialog (TASK-092) removed
      `Library.tsx:handleBulkFetchMetadata`, which was the helper's only production
      caller. `web/src/services/api.ts:1928` now has zero callers in `web/src`
      outside its own unit test in `api.test.ts`.

  **Why this is being written down rather than fixed in TASK-092.** The helper is
  `export`ed, so `noUnusedLocals` does not flag it and neither does the linter —
  exactly the shape that let `linkAsVersion` survive a dead-code sweep with
  test-only callers (see `WAVE-1-STATE.md`, "DEAD-1 is not resolved"). Left alone
  it is invisible: not dead by any automated measure, not reachable by any user.

  **What has to be decided, because the answer is not obvious.** The client helper
  is gone from the UI but the backend v2 bulk-metadata-fetch operation it enqueues
  is untouched and still works. So either:

  - the feature was retired on purpose — the `REMOVED 2026-08-09` note in
    `web/tests/e2e/batch-operations.spec.ts` says the e2e coverage was deliberately
    deleted then, which points this way — and the helper plus its test should go
    too, and possibly the backend op with them; or
  - the dialog was collateral damage and a bulk metadata fetch is still wanted, in
    which case the helper is the surviving half of a feature that needs re-wiring
    to a reachable control, not deletion.

  Do not resolve this by deleting the helper on the strength of "no callers" alone.
  The live "Fetch Selected" flow (`handleFetchReview` -> `api.batchFetchCandidates`)
  is a *different* operation, not a replacement for this one, so its existence does
  not prove this feature was superseded.
