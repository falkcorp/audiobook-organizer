### Fixed

- Merging chapter files into one book no longer risks losing a file record. Each file used to be removed from its chapter book and then re-added to the merged book as two separate writes, so a crash or error in between left the file attached to no book at all. The whole merge now moves every file in one step that either fully happens or does not happen.
- After a chapter merge, the merged book's file count and runtime are recalculated straight away, and the file list in the web UI shows the new owner immediately instead of after the next restart.
