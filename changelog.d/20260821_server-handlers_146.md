### Fixed

#### N-10: advertised login rate limit (10/10min) now matches real throttle (15/15min)

The advertised rate limit values in `GET /status` (10 requests per 600000ms) did not match the real throttle enforced in `absauth` (15 failures per 15 minutes). Clients that paced themselves according to the advertisement would not be throttled, and discovery of the real limit came only via 429 errors. Now both values are derived from the same `absauth` constants, ensuring clients can pace themselves correctly and the advertisement stays synchronized with reality.
