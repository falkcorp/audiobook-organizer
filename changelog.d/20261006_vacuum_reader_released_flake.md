### Fixed

- After an activity-log vacuum, the WAL truncate no longer blocks activity writes for seconds while it copies the frames written during a long checkpoint. `truncateWALAfterVacuum` issued the TRUNCATE right after the first PASSIVE checkpoint that reported complete. But a PASSIVE only copies the frames that were in the WAL when it started. Everything Records committed while it ran was left for the TRUNCATE, which copies under the WAL write lock: on a loaded CI runner a complete 6.13 s PASSIVE was followed by a 3.79 s TRUNCATE and a 3.84 s Record behind it, and on prod an 11 GB PASSIVE would leave minutes of writes. Each attempt now repeats PASSIVE until one is complete and had at most `vacuumTruncateMaxLeftoverFrames` (256) frames of its own to copy. After `vacuumPassiveConvergeRounds` (32) PASSIVEs without that, it logs the frame counts and issues no TRUNCATE for that attempt. TRUNCATE still never runs while a reader holds the copy short.
- `TestVacuumActivity_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy` and `TestVacuumActivity_TruncateDoesNotStallForegroundWrites` no longer use wall-clock Record-latency limits, which depended on the runner (CI run 37453234558). They now check frame counts and ordering from the checkpoint results:
  - no TRUNCATE is issued while the reader is held;
  - every TRUNCATE follows a complete PASSIVE;
  - that PASSIVE had already copied every frame the VACUUM left;
  - that PASSIVE had at most 256 frames of its own to copy (`requireTruncateLeftoverBounded`).

  The old single-PASSIVE behaviour fails both tests.
