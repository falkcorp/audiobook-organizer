### Fixed

- A rename whose files moved on disk but whose new path failed to reach the database now retries the write (4 attempts, 100/200/400ms backoff, stops on cancellation). If it still fails, the old and new paths are recorded durably and the rename still returns its error. The new `maintenance.repoint-unrecorded-renames` op (dry-run by default) repoints each recorded row to its new path when the file is there and the row still holds the old path. It never moves files, never deletes rows, and skips iTunes paths.
