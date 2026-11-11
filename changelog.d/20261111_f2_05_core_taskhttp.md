### Added

- core: the native task routes, `core/internal/taskhttp` (dark: nothing mounts them until the kernel wiring row). It serves the 19 routes of the tasks API under `/api/core/v1/tasks` over a `tasks.Client`: definitions, start (`Idempotency-Key` replays answer 200 with `Idempotent-Replayed: true`; a `dry_run` key is a 400), runs, rows, items, log, cancel, discard, approve, decide, counts, timeline, schedules (a merge patch written through config, 409 when an operator locked the key) and pause. Every run goes out through `tasks.RedactRunView`, a held run's mutations answer 409, and approve and decide need the definition's scope and an interactive sign-in. Approve, reject and cancel-by-another are audited.
- core examples: `hello.count`, the task definition of the hello module's counters (`core/examples/hello/count.go`), not registered yet.

### Fixed

- core executor: a manual start of a maintenance window now needs every member's start scope as well as `core:tasks.manage`. Before, a caller holding only `core:tasks.manage` could start a window that runs its members as their own modules' system actors. The check fails closed when the member list is missing.
