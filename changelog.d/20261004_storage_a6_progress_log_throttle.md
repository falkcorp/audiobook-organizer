### Changed

#### Operation progress updates no longer write one log row each

`dbReporter.UpdateProgress` logged one `op_logs_v2` row per distinct message, and counter messages such as `Books 3/76994` are always distinct: one `library.scan` wrote about 300,935 rows, one full `metafetch.asin-backfill` about 76,996 and one 8-second `repairs.plan` about 15,894, each with `pebble.Sync` and kept forever. A progress log line is now written only when the message shape changes (digit runs normalised), 30 seconds have passed since the last progress line, or the operation ends (the newest suppressed line is written at the end, so the final state is never lost). The progress columns, bus event and Prometheus gauges still update on every call.
