### Fixed

- Tests: `wire_metadata_bulk_fetch_gone_test.go` passes the credential epoch to `CreateSessionWithOrigin`. #4329 merged after #4324 added that parameter, so the `internal/server` test package stopped compiling on main.
