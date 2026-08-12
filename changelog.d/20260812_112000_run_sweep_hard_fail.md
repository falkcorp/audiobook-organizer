### Fixed

- **`run-sweep.sh` no longer reports success after doing nothing.** It discovers work with
  `find -maxdepth 1 -name 'TASK-*.md'`, but four of the ten live agent-task packages keep their
  work in `AWAIT-APPROVAL.md`, `HOLD-STATUS.md`, or an inline `TASKS.md` instead. For those it
  created no worktrees, emitted no prompts, and still printed "Next steps (coordinator)" — a
  silent no-op indistinguishable from "this workstream has nothing to do." It now exits 2 with a
  diagnostic naming what the package contains and what the gate means. `set -euo pipefail` could
  not catch this: iterating an empty list is not a command failure.
