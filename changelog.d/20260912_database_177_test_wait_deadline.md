### Changed

#### `internal/database` tests — blocking waits now fail fast and say what they were waiting for

A hung `wg.Wait()` or channel receive in an `internal/database` test used to sit
there until `go test`'s package-wide `-timeout` fired, burning the whole
package budget and printing a goroutine dump instead of the condition that
never arrived (the embedding-store chaos tests hit this for real). A shared
helper in `internal/database/test_deadline_test.go` (`waitGroupOrFatal`,
`recvOrFatal`, `waitOrFatal`) now bounds those waits with `context.WithTimeout`
on `t.Context()`: 30s, or less when the test's own `t.Deadline()` is closer,
keeping a 5s margin so cleanup runs. On timeout the test fails with a
`t.Fatalf` that names the helper and what it was waiting for. It covers 21
test-level waits in 16 test files, and `waitForWorkers` in the embedding-store
chaos tests (3 call sites) now delegates to it. Test-only; no production code changed.
