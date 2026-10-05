- [ ] **ARS-Q1** Owner question: a book the listener marked *unstarted* by hand,
      but that Audible says is finished or in progress, is now skipped as the
      user's choice (`skipped_manual_unstarted`), like abandoned. This applies
      whether the mark is older or newer than Audible's timestamp. Confirm,
      or say which way it should go.
- [ ] **ARS-LOCK** Move the remaining user-state writers under
      `database.LockUserBookState`: `readstatus` Recompute/Rebuild together
      with the position writes before them (web reading heartbeat, iTunes
      position sync), the iTunes position backfill job, and the merge follow
      and merge combine paths, taken inside `merge.LockMergeRMW`. Each is
      listed on the lock. SetManualStatus and the iTunes finished seed take
      it since the ARS review follow-ups.
