- [ ] **Consolidate the space-reclaim maintenance ops into one job.**
      `purge-old-logs`, `cleanup-activity-log`, `cleanup-old-backups`,
      `trash-cleanup`, `temp-file-cleanup`, `metadata-cache-reap`,
      `purge-deleted` and the new `activity-reclaim` are eight separate triggers
      for one operator intention ("give me space back"), and none of them ends
      by compacting, so none of them actually returns bytes on its own.
