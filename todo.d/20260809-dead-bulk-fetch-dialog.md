<!-- file: todo.d/20260809-dead-bulk-fetch-dialog.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c9a6f31-7d04-48e5-a1b2-6f80e39c4a75 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Delete the unreachable "Bulk Fetch Metadata" dialog and its handler.**
      `web/src/components/library/LibraryDialogs.tsx:920` renders
      `<Dialog open={bulkFetchDialogOpen}>`, but `setBulkFetchDialogOpen(true)` appears
      **nowhere** in `web/src` — the state is initialised to `false` at
      `web/src/pages/Library.tsx:352` and is only ever set back to `false` (by
      `handleCancelBulkFetch`). The dialog can never open. `handleBulkFetchMetadata`
      (`Library.tsx:1218`), the `bulkFetchProgress` state, and the props threaded
      through `LibraryDialogs` for them are reachable only from that dead dialog.
      The flow it belonged to was replaced: **Fetch Selected** now calls
      `batchFetchCandidates` and toasts "Click Review when complete", and a separate
      **Review** button opens the candidates dialog once the cache is populated. Five
      e2e tests covering the old synchronous progress dialog were deleted on
      2026-08-09 rather than rewritten, since rewriting them against the new
      async flow would be new coverage rather than repair. Removing the dead code is
      a separate change from the e2e repair and was deliberately not bundled with it.

- [ ] **Audit `setupMockApi` for more branches shadowed by earlier prefix catch-alls.**
      `web/tests/e2e/utils/test-helpers.ts` had `pathname === '/api/v1/audiobooks/batch'`
      sitting *below* `pathname.startsWith('/api/v1/audiobooks/') && method === 'POST'`,
      so every batch update silently got the generic `{ message: 'OK' }` back and
      Library's toast read "Updated metadata for 0 audiobooks." Fixed 2026-08-09 by
      moving the specific branch above the prefix one, but the same ordering hazard
      applies to every other `startsWith` catch-all in that dispatcher — a specific
      branch placed after one is dead and fails silently rather than loudly. Worth one
      pass to confirm no others are shadowed.
