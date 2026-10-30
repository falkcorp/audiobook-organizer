### Fixed

`RetireBookFiles`, `UnretireBookFiles` and `RehomeRetiredVersion` no longer hold
the book-version lock while they wait between retries. When a retire, un-retire
or rehome batch lost a race with another writer it slept (up to 9 waits of 5-100
ms) with the version's lock held, so batch upserts and version edits for that
version queued behind the sleep. The lock is now taken per attempt and released
before each wait, and each retry re-checks that the version is still a retired
version of its holder before writing, so a version moved away during the wait
is refused rather than written to. No change to results; less waiting under
contention.
