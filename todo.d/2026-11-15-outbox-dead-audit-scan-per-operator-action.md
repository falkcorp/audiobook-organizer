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

- [ ] **TODO-OUTBOX-DEADN-REV-ABA** Close two small gaps in the `core/outbox`
      dead-letter counters — (1) counting a `deadn/` counter down to zero deletes
      its row, so a later write starts `Rev` over and a repair that read
      `{1,2}` can see the same bytes again after a delete, a clamp and an add
      (the repair then writes a count one too high; the next audit fixes it):
      keep a zero row or take `Rev` from a monotone source; (2) `healDeadCounts`
      at the top of every relay loop reads one `deadn/` row per pair that holds
      letters, which is not O(1) when healthy: bound it (check only when a
      counter write failed, or at most once per status interval) and add a
      counted-reads assertion for idle loops.
