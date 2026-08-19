## Dedup

- [ ] **Clean up the 2,504 already-orphaned dedup candidates — use the existing
      `dedup.purge-stale`, do NOT build a new op.** A 2026-08-19
      `dedup.breakdown-backfill` dry run reported `skipped_no_book: 2504`: pending
      candidates whose book row has been hard-deleted. Such a row is permanently
      stuck — resolving it 500s on "book not found", and it is never re-scored
      because every producer iterates live books only — so it sits in the pending
      queue forever. Together with the 2,713 zero-signal rows this is roughly half
      the pending backlog.

      The *recurrence* is fixed: `PebbleStore.DeleteBook` now cascades the teardown
      of a book's pending candidates, so no new orphans are created by any of its
      16 call sites. That commit does not clean the existing rows, because their
      books are already gone and there is no delete left to hook.

      The cleanup already exists and needs no new code: `PurgeStaleCandidates`
      (`internal/dedup/engine.go`) lists every pending book candidate across all
      layers and hard-deletes those with a missing book on either side — exactly
      this population. It is exposed as the `dedup.purge-stale` operation.

      **Why they accumulated:** `dedup.purge-stale` has no `Schedule:` on its
      OperationDef. It runs only when invoked manually, or as a step inside
      `dedup.full-scan` and the embedding backfill. With the cascade in place the
      source is closed, so scheduling it is likely unnecessary — but that should be
      confirmed after the cleanup run, by re-running `dedup.breakdown-backfill`
      as a dry run and checking `skipped_no_book` stays at 0.

      **Blocked on a user decision:** running it with `apply:true` mutates prod
      data, the same gate that `dedup.breakdown-backfill`'s apply is waiting behind.
      Note it deletes only `pending` rows — `merged` / `dismissed` rows are the
      historical records behind the UI's Merged / Dismissed tabs and are preserved
      by both `PurgeStaleCandidates` and the new cascade.
