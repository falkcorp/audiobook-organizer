### Fixed

#### `TestGetBookListingFields_MemdbMatchesPebble` no longer panics on a nil memdb

The `seedTrash` test helper returned a store while the asynchronous memdb
warmup was still running, so the test's `p.mem()` was nil in about one full
`internal/database` run in four. The helper now waits for warmup and fails
clearly if memdb did not publish. Production code was not affected:
`GetBookListingFields` already checks `p.mem() != nil` before using memdb.
