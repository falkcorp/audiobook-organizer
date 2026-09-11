### Fixed

#### IP rate limiter no longer sweeps the whole IP map on every request (SV-04)

`IPRateLimiter` in `internal/server/middleware/ratelimit.go` used to walk and prune the entire per-IP map under one mutex on every request, so the abuse-mitigation control itself got slower and more lock-contended exactly as the number of distinct client IPs grew (an abuse burst, IPv6 rotation). The request path is now a single map lookup plus an O(1) LRU move under a short critical section; idle entries are evicted by a background sweeper (`Start`, wired to the server's background context so shutdown stops it, with an idempotent `Stop`) that walks from the least-recently-seen end and stops at the first fresh entry; and the map is bounded at 10,000 entries, evicting least-recently-seen on insert so an IP flood cannot grow it without limit. Rate-limit semantics and configuration are unchanged: an IP returning after the 15-minute idle TTL still gets a fresh bucket, refreshed in O(1) on lookup.
