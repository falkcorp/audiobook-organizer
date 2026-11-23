### Added

- The task events (dark until the flip) carry two fields the ops bell and the activity mirror need. A run payload, on `core.task.run-changed.v1`, `core.task.finished.v1` and the run REST routes, gains `message`, the run's latest progress message (the bell's `op.updated.message`); it is the same text as `current` and is blanked by the same redaction. A log-appended payload gains `run_id`, so a consumer holding only the payload can name the run; the log REST route's lines carry it too. `tasks.NewLogAppended` now takes the run id.
