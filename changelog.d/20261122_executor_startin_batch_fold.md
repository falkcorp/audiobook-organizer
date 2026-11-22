### Fixed

- The task executor's `StartIn` now reads the queued runs once per batch and computes each queued run's dedupe key once, so a bulk start of N runs over M queued ones decodes M runs instead of N times M (#4829 m4).
- Two starts staged into one batch that merge into the same queued run (a `Dedupe.Merge` kind) now chain: the second merges into the first's result. Before, both read the committed record, so the second overwrote the first and both appended the same event number. `StartIn` is used by the scheduler (`core/internal/scheduler/scheduler.go`, `window.go`), the AI batch poller (`core/integrations/ai/batchpoll.go`) and the outbox relay (`Tx.StartRun`). Every other start result is unchanged.
