<!-- file: changelog.d/20260807_223500_whisper_multi_endpoint_dispatcher.md -->
<!-- version: 1.0.0 -->
<!-- guid: f569af86-775d-4540-acf8-d3809836ba9d -->
<!-- last-edited: 2026-08-07 -->

### Added

- Multi-endpoint Whisper dispatch pool (`internal/transcribe/dispatcher.go`):
  jobs are allocated across remote faster-whisper servers in priority order
  (lower `priority` = preferred, e.g. GPU box 1, CPU box 100), each endpoint up
  to its `concurrency` share, with spill to lower-priority endpoints. A failing
  endpoint enters a time-based cooldown and its jobs are re-queued to surviving
  endpoints; a `*TransportError` naming every endpoint is returned only when
  the whole pool is exhausted, so an outage still writes no per-file verdicts.
  Configured via env-authoritative `WHISPER_ENDPOINTS` (JSON array, e.g.
  `[{"url":"http://whisper-1.local:8000","concurrency":2,"priority":1,"kind":"gpu"}]`);
  empty falls back to `WHISPER_REMOTE_URL` as a one-element pool (unchanged
  behaviour), else the local path.
