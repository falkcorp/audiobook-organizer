### Fixed

- **`GET /api/v1/activity/sources` took 35 seconds on every poll.** The distinct-sources
  scan decoded each stored entry into a full `ActivityEntry`, whose `Details` field is a
  `map[string]any` — so a class of `change`-tier rows carrying ~9.6 MB iTunes ITL dumps was
  materialized into roughly 1.2 million boxed values (52.8 MB of garbage per row) purely to
  read one short `source` string. The scan now decodes into a projection that omits
  `Details`: benchmarked on an 8.1 MB entry, 84.7 ms and 52,783,781 B/op becomes 16.9 ms and
  86 B/op. The dropped field is one no filter predicate reads, so results are unchanged; a
  reflection test now enforces that the projection stays in step with `ActivityEntry`.

- **The distinct-sources memo could never be hit.** Its cache key used `Since`/`Until` at
  nanosecond precision while the UI sends a rolling 24-hour window truncated to the minute,
  so the key changed every 60s against a 45s TTL — on production, four consecutive polls
  measured 35.38s, 1.46ms, 35.68s and 35.05s, the single hit being a repeat inside one
  minute. Time bounds are now quantized to a 5-minute bucket and the TTL raised to 5
  minutes, so a steady-state poll reuses the cached counts. Every other filter field is
  still matched exactly.
