### Removed

- Deleted the superseded v1 startup resume sweep from the server lifecycle
  (`resumeInterruptedOperations` and its `resumeV2Op` / `resumeLegacyOp` /
  `countLegacyV1Ops` helpers, ~280 lines). Operations interrupted by a restart are
  resumed by the operations registry's `resumeAfterStartup`, which reads the v2
  keyspace and applies each operation's declared resume policy — it supersedes the
  deleted code and handles cases the old sweep never could. The sweep's only source
  of candidates was the v1 `operation:` keyspace, whose last writer was retired on
  2026-08-23, so it had been running on every boot and finding nothing. No change to
  restart-resume behavior.
