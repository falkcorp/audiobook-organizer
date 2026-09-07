### Fixed

- **"Clear Stale" can now clear the operations that were actually stuck.** The
  button only ever swept **v1** operation rows, and nothing has created a v1 row
  since the v1 minter was retired on 2026-08-23. So pressing it returned
  `{"cleared": 0}` and did nothing at all — while a canceled "Transcribe book
  intros" op sat in the Activity page's **Active Operations** panel at 199/200
  from 2026-06-26 to 2026-09-07, with no user action able to remove it.

  Clear Stale now also repairs **v2** rows that hold a terminal status but have
  no `completed_at` timestamp. Those rows are finished as far as the worker is
  concerned, but every reader decides "still in flight" on `completed_at` being
  null, so they read as permanently running. The response reports the two halves
  separately (`v1_failed`, `v2_repaired`); `cleared` remains their sum, so the
  existing Activity page is unaffected.

  Operations that are genuinely still live — queued, running, waiting on
  dependencies, or parked for the startup resume sweep — are deliberately **not**
  touched. Those belong to the scheduler, which decides per-operation whether to
  restart, requeue, or drop them; a button that force-failed them would silently
  throw away work that was about to resume.

- **Canceling a queued operation no longer risks marking a resumable one
  finished.** The `completed_at` stamp added alongside the fix above originally
  triggered on "any status that is not running or queued," which also captured
  `interrupted_quiesced` (resumable) and `waiting_deps` (waiting on the
  dependency scheduler). Only the fact that both real callers pass a literal
  `canceled` kept it from firing. It is now an explicit list of genuinely
  terminal statuses, so an unrecognized status is treated as live rather than
  finished — the safe direction for something that writes.
