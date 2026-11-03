### Fixed

- Operations: `maintenance.bookfile-acoustid-index-repair` now checkpoints its scan cursor and running counts after each page, so a restart continues at the next page instead of re-judging every key from the first, and its final report still counts the whole run. `maintenance.opchange-book-index-rebuild` keeps its resume cursors in the store and is recorded as store-anchored in the `ResumeRestart` checkpoint guard (`TestResumeRestartDefsCheckpoint`) rather than checkpointing twice.
