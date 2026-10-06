### Fixed

- `TestVacuumActivity_TruncateDoesNotStallForegroundWrites` no longer fails when the first PASSIVE checkpoint reports busy. The checkpoint connection has `busy_timeout` 0, so a PASSIVE that meets a concurrent Record can return busy=1 with nothing copied, and `truncateWALAfterVacuum` retries it. The test asserted that checkpoint index 0 did the copy. It now asserts the real property: every TRUNCATE follows a complete PASSIVE, and the PASSIVE right before the first TRUNCATE had copied every frame the VACUUM left.
