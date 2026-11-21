### Fixed

- The dark shell's task tracker now reads core's real task-run shape (flat `done` and `total`, `canceled` with one l) and knows all twelve of core's run states. Before, a run that ended `canceled`, `timed_out` or `interrupted_dropped` was never seen as finished, so the shell kept polling it forever; and it fetched a run from `/api/core/v1/tasks/{id}` when core serves it at `/api/core/v1/tasks/runs/{id}`. The shell's state list and terminal set are now one definition, and a test reads `core/tasks/client.go` and fails if either drifts from it.
