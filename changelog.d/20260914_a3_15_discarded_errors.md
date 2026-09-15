### Fixed

- Two discarded errors are now logged (A3#15): a failed rescan flag after a tag write-back, which left the next incremental scan reading the pre-write tags, and a failed fetch-cache write in bulk metadata fetch, now counted as `cacheWriteFailed` in the run summary.
