### Fixed

- core/executor: a task body's child start (`rc.Enqueue`) is now bounded by the module whose body makes it: its own kinds plus its manifest's `TaskStarts`, within its system actor's permissions. Before, a run started by a user ran no check on its children, so a body could start any kind. A start option can no longer change a child's actor, parent, trigger or idempotency key: such a start is refused and creates no run (F2-03 follow-up).
- core/executor: a claimed run that a stand-down hold or shutdown overtakes after a cancel has committed now ends canceled with its body never started. Before, it stayed stuck in `stopping` until the next boot, and a repeat cancel answered success (F2-03 follow-up).
- core/executor: a Live Fixer run that names no plan now fails and applies nothing, instead of evaluating and applying every candidate live. `Decide` now refuses a call with no caller identity, as `Approve` does (F2-03 follow-up).
