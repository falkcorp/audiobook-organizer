### Fixed

- **The activity-log migration restarted from the beginning every time the server
  restarted.** It kept no record of where it had got to, so each restart threw
  away every hour already spent. On 2026-09-08 it restarted three times and
  discarded roughly 81 minutes of work, then spent another three hours re-reading
  the same 6.5 million records it had already copied. It now saves its position
  continuously and picks up where it left off, and it skips whole sections it has
  already finished and verified.

- **A verification failure could have been forgotten across a restart.** The
  migration checks its own work by re-copying each section and confirming nothing
  new lands. That verdict was only ever held in memory, so a run that found a
  problem, then got interrupted, would have started over believing everything was
  fine — and could have switched the app over to a copy that was never fully
  checked. The verdict is now recorded per section and survives restarts: a
  section that failed is re-copied in full before it can pass, and the switchover
  happens only when every section has been verified.
