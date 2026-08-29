### Fixed

- **`prune-book-snapshots` ignored its `keep_count` parameter.** The job read run
  parameters via `GetOperationParams`, which resolves a PebbleDB key that only
  `operations.SaveParams` writes — and the maintenance dispatcher stopped calling
  that when the v1 operation minter was retired. Every run therefore silently used
  the default retention of 10 regardless of what was requested. It now reads the
  live parameter channel, and a zero or unparseable `keep_count` falls back to the
  default instead of being treated as "keep nothing".
