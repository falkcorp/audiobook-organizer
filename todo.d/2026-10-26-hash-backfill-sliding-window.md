- [ ] **TODO-HASH-BACKFILL-SLIDING-WINDOW** `backfill-file-hashes` already hashes
      on a pool of 8 (`ABK_HASH_BACKFILL_WORKERS`) in ordered 512-row chunks, but
      each chunk is a barrier: one slow 100 MB file idles the other 7 workers,
      and the chunk's commit (journal, stores, report fsync) runs while no row is
      being hashed. A sliding window that hashes ahead and commits the contiguous
      completed prefix would remove both. Measure first: a prod dry run reached
      about 21 rows/s, which may simply be the ZFS read ceiling, in which case
      neither more workers nor a window helps. Needs the per-chunk journal and
      report-fsync invariants kept (a chunk is still all-or-nothing).
