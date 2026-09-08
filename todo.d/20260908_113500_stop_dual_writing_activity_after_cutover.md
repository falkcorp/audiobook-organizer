- [ ] **Stop dual-writing activity to Pebble after the SQLite cutover.**
      `MigratingActivityStore.Record` fans out to BOTH backends unconditionally
      and there is no post-cutover stop, so once `read_secondary` flips, every
      new activity row is still written to a Pebble copy nothing reads. The new
      `maintenance.activity-reclaim` op deletes the accumulated bulk, but it has
      to be re-run forever because the accumulation never stops. The real fix is
      for `Record` to skip the primary once `readSecondary` is true — which also
      removes the dual-write race that forces the reclaim to prune behind a time
      cutoff instead of clearing the prefix outright.
- [ ] **Consolidate the space-reclaim maintenance ops into one job.**
      `purge-old-logs`, `cleanup-activity-log`, `cleanup-old-backups`,
      `trash-cleanup`, `temp-file-cleanup`, `metadata-cache-reap`,
      `purge-deleted` and the new `activity-reclaim` are eight separate triggers
      for one operator intention ("give me space back"), and none of them ends
      by compacting, so none of them actually returns bytes on its own.
