- [ ] **Backfill legacy operation rows stuck at `pending`.** #2483 fixed the forward path
      (terminal status now mirrors from `publishOpTerminal`), but rows created before it
      stay frozen at whatever status they started with. `/api/v1/operations` shows several
      on page one alone (`archive-sweep`, `trash-cleanup`, `temp-file-cleanup`,
      `cleanup_activity_log`, `maintenance-window`, `purge-deleted`). Needs a one-off
      supervised pass — it rewrites historical records, so run it watching, not unattended.
