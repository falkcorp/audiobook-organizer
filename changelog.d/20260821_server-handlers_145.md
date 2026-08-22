### Fixed

#### ABS listening-stats read failures are now logged and counted instead of silent (ABS-N6)

`GET /api/me/listening-stats` deliberately keeps answering 200 with `totalTime:
0` when the underlying `ListenedSeconds` read fails — a 5xx here trips the
same AudioBooth connection-status indicator the endpoint exists to keep green,
so failing loudly would trade one cosmetic bug for the identical one. That
tradeoff was undocumented at runtime: the failure produced no log line and no
metric, so a persistently-failing read looked identical to a user who really
had listened zero seconds.

The read failure now logs `slog.Warn` with the user ID and error, and
increments a new `audiobook_organizer_abs_listening_stats_read_failures_total`
Prometheus counter, so the silent 0 becomes observable without changing the
response the client sees. The counter has no labels — it counts one specific
read path, not a dimensioned family — so it is a plain `prometheus.Counter`
rather than a `CounterVec`.
