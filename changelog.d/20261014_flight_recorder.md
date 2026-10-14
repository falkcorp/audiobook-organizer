### Added

#### Always-on flight recorder with watchdog snapshots

The server now keeps a runtime trace of its most recent activity in memory
(`runtime/trace.FlightRecorder`, new package `internal/diag/flight`), sized at
about 8 MiB (a hint; the runtime always keeps at least the last complete
generation, about 1 s) over a window of up to 10 s. A busy process therefore
holds, and a saved file reaches, more than 8 MiB. When something looks wedged,
that window is saved to disk next to a goroutine dump, so the next silent stall
leaves evidence. The two earlier ones, the 2026-07-05 dedup run and the
2026-09-24/25 search-index wedge, left none.

- A snapshot is saved when the ops watchdog writes a `stuck` or
  `never_reported` strike, and when the search-index watchdog reports a stalled
  write. Files go to a `traces/` directory next to the database, as a pair named
  `<UTC time>-<reason>.trace` (opens with `go tool trace`) and
  `<UTC time>-<reason>.goroutines.txt` (every goroutine stack, taken just after
  the trace). Asking for a snapshot never blocks the watchdog. If a second
  reason is requested while a snapshot is being written, the file name records
  both reasons.
- A reason that fires again backs off: the gap between its snapshots starts at
  5 minutes and doubles on every repeat, up to 2 hours, and starts over once the
  reason has been quiet for as long as the current gap. Only one snapshot is
  written at a time. The first snapshot of each reason in a process is pinned
  and never rotated away (up to 4 pinned), so a flapping reason cannot push out
  the first capture of an incident. Beyond those, the 10 most recent snapshots
  are kept.
- Disk use is bounded by 14 snapshots (10 rotating, 4 pinned), not by the 8 MiB
  figure. Trace file sizes seen at the 8 MiB hint came from synthetic goroutine
  workloads: 4 to 10 MB under a moderate load and about 39 MB under a maximum
  churn load (the worst seen), and a goroutine dump was a few tens of KB. The
  scanner itself was not measured at 8 MiB; at other hints it produced 4.8 MB
  files at 4 MiB and 33 to 65 MB files at 32 MiB. From the synthetic sizes,
  `traces/` should stay under about 140 MB in normal use and about 550 MB at the
  worst size seen. These numbers are derived from observed file sizes and are
  not enforced limits.
- Admins (`settings.manage`) can list snapshot files with
  `GET /api/v1/diagnostics/traces` (`{"data": {"traces": [...]}}`, newest
  first) and download one with `GET /api/v1/diagnostics/traces/:name`.
- Set `ABK_FLIGHT_RECORDER=off` in the service environment to turn recording
  off without a code change. Traces already on disk can still be listed and
  downloaded.
- If tracing fails to start, the server logs a warning and keeps running.
