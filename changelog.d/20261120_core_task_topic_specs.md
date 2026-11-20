### Added

- Platform core: the three `core.task.*` catalog entries are declared (`core/tasks/topics.go`) and listed in `kernel.coreTopics()`: `core.task.run-changed.v1` (a `RunPayload`, realtime with a 250 ms window, `RunViewRedactor()`), `core.task.log-appended.v1` (a `LogAppended` line, realtime, redacted by `LogAppendedRedactor()` unless the subscriber holds `core:users.view`) and `core.task.finished.v1` (the terminal `RunPayload`, notifiable, `RunViewRedactor()`). `RunPayload` is the lower-case-keyed wire form of a `RunView`, shared with the REST run body so a run reads the same over both. Each entry's `Visible` fails closed for realtime delivery: only a holder of `core:admin` sees an envelope until the wiring installs the definition's view rule (notification rules do not consult `Visible`; that is the executor wiring's hand-off). Dark: nothing declares the catalog until the kernel wiring lands and nothing publishes the topics until the executor does.

### Fixed

- `GET /api/core/v1/tasks/runs/{id}/log` now blanks each line's message and field values for a caller without `core:users.view`, the same rule the live log stream applies. It returned the raw text to any caller who could view the run.
