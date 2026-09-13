### Fixed

- `TestIPRateLimiter_StartStopsOnContextCancel` no longer flakes: it waited for the idle entry to be evicted and then separately asserted the sweep counter, but `sweep()` bumps the counter after releasing the lock, so the counter could still read 0. The test now waits for both conditions together.
