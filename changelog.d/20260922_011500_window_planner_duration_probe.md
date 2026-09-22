### Fixed

- `acoustid.window-backfill` now measures a missing duration instead of
  declining over it. A row with no duration was rejected as `unknown_duration`
  — 3,121 files and climbing, deferred indefinitely — because the window
  planner cannot place a window without one, and a remote worker cannot probe
  on the server's behalf. The server can: `ProbeDurationSeconds` reads the
  container header (mediainfo, falling back to
  `ffprobe -show_entries format=duration`) and never decodes audio, so it is
  permitted server-side where decoding is not.
- Successful and failed probes are counted separately and reported in the plan
  summary (`duration_probed`, `duration_probe_failed`). A row with no duration
  and a row whose prober is broken are different problems, and previously both
  looked identical.
