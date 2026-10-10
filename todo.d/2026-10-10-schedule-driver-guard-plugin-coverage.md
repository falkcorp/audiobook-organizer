- [ ] **SCHED-GUARD-PLUGIN-COVERAGE** `TestScheduledOpsHaveADriver`
      (`internal/server/op_schedule_driver_test.go`) enumerates ops through
      `bootRegisteredOpIDs`, which registers a hand-written list of plugin
      `OperationDefs()` (acoustid, dedup, deluge, itunes, metafetch). A plugin
      that declares a `Schedule` but is missing from that list is invisible to
      the guard. Derive the plugin list from the real serviceregistry wiring, or
      fail when a registered plugin is absent from the list.
