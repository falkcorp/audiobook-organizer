### Added

#### Always-on flight recorder with watchdog snapshots

The server now keeps a runtime trace of its last few seconds in memory, a ring
of at most 32 MiB (`runtime/trace.FlightRecorder`, new package
`internal/diag/flight`). When something looks wedged, that window is saved to
disk, so the next silent stall leaves evidence. The two earlier ones, the
2026-07-05 dedup run and the 2026-09-24/25 search-index wedge, left none.

- A snapshot is saved when the ops watchdog writes a `stuck` or
  `never_reported` strike, and when the search-index watchdog reports a stalled
  write. Files go to a `traces/` directory next to the database, named
  `<UTC time>-<reason>.trace`, and open with `go tool trace`.
- Snapshots are limited to one per reason every 5 minutes and one write at a
  time. At most 10 files are kept, and the oldest are removed first. Asking
  for a snapshot never blocks the watchdog.
- Admins (`settings.manage`) can list snapshots with
  `GET /api/v1/diagnostics/traces` and download one with
  `GET /api/v1/diagnostics/traces/:name`.
- Set `ABK_FLIGHT_RECORDER=off` in the service environment to turn recording
  off without a code change. Traces already on disk can still be listed and
  downloaded.
- If tracing fails to start, the server logs a warning and keeps running.
