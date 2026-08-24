### Fixed

- **Nightly maintenance has not run since Aug 21 — fixed.** The maintenance
  window crashed on its first task every night, taking all 12 nightly jobs down
  with it. It supervised each task by polling the *legacy* operations table for
  an id that only exists in the v2 registry; that lookup returns "not found" as
  a nil with no error, and the guard checked only the error, so it dereferenced
  nil five seconds into task one. The tasks themselves were healthy the whole
  time — `dedup.author-scan` completed successfully at each of the timestamps
  the window reported as failed.
- **The window no longer waits forever on an interrupted task.** Its terminal
  status set omitted `interrupted_dropped` and `interrupted_quiesced`, which is
  how `library.scan` and `metadata.batch-save` routinely end, so a window that
  survived the crash would have blocked on them until its context expired.
- **Interrupted tasks are now reported honestly.** A dropped task counts as a
  failure (its work is discarded and never resumed); a quiesced or canceled one
  is reported as incomplete rather than as success. The nightly summary also
  separates "never started" from "started and did not finish".
