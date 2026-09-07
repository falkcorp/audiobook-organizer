### Changed

- **The reconcile-scan and candidate-fetch indexes no longer read the retired v1
  operations keyspace.** Both listed runs from v1 and v2 and merged them, so that
  history keyed under a v1 id stayed visible during the migration. Nothing has
  created a v1 row since that minter was retired on 2026-08-23, so the v1 half
  could only ever return operations from before then. It has been removed, and
  that history is intentionally dropped.

  Runs from before 2026-08-23 no longer appear in the Resume Review picker, the
  reconcile "latest scan" view, or the metadata dedup guard, and an operation id
  minted before then no longer resolves. Everything from after that date is
  unaffected — those runs were always v2.

  Per-operation **results are not affected**: they live in their own keyspace
  keyed by operation id, which this does not touch.
