### Fixed

- Test: `TestInviteRateLimit_Global` no longer fails on a heavily loaded CI agent. The global invite limiter is a token bucket that refills 5 tokens a second on the real clock, so a 200 ms stall between the first refusal and the final "fresh token and client" probe let a token refill and answered 200. `IPRateLimiter` gains `UseClock`, `ReserveKey` and `Commit` now honour the injected clock (they read `time.Now()` directly), and the test holds the global bucket's clock still. No change to production behaviour: the clock defaults to `time.Now`.
