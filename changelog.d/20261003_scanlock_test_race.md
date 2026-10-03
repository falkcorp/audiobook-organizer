### Fixed

- **Flaky `scanlock` test:** `TestLockSetIdle…` signalled the test before releasing the lock, so the "table not empty" check raced the release and failed the Coverage Floor gate on unrelated PRs. The goroutine now releases first.
