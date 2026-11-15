- [ ] **TODO-OUTBOX-DEAD-AUDIT-SCAN** Make the dead-letter audit in `core/outbox`
      cheaper after an operator action — `RefreshStatus` (which every operator
      action calls through `changed`) runs `auditDeadCounts`, a keys-only scan of
      the whole `dead/` family, so a retry or discard in a store holding many
      letters pays one full scan each time. The scan is the audit that finds a
      drifted counter, so it cannot simply be dropped; options are to audit only
      on the relay's periodic refresh and have `changed` recount from the
      counters alone, or to audit at most once a minute. Whichever is chosen,
      keep the audit on the periodic refresh and keep the forced audit for a
      counter that does not decode, and add a test that a burst of operator
      actions reads a number of keys independent of the letters stored.
