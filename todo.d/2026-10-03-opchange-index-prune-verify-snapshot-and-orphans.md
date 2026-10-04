- [ ] **OPCHANGE-INDEX-FOLLOWUPS** Two gaps left in the `opchange_by_book:`
      index (PR #3704). (1) `PruneOperationChanges` and
      `verifyOpChangeByBook` each hold one iterator (one Pebble snapshot, and
      the sstables it pins) for the whole pass over the journal. Re-open an
      iterator per chunk at a cursor, as `backfillOpChangeByBook` does. (2)
      Index entries orphaned by a rollback binary (one that deletes or
      rewrites journal rows without maintaining the index) are never
      collected: verify only counts journal rows missing an entry, and the
      rebuild only Sets entries. Done means: verify counts extra entries whose
      journal row is gone or names another book, reports them, and the
      rebuild deletes them in the same chunked batches, with a test that
      plants an orphan and sees it removed.
