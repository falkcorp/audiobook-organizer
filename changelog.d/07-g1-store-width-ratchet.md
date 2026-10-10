### Internal

- Added two ratchet tests in `internal/database` that fail when the flattened `database.Store` method count (baseline 453) or the number of non-test references to the wide `database.Store` type (baseline 31) rises. Improvements pass with a note to lower the baseline by hand. No behaviour change.
