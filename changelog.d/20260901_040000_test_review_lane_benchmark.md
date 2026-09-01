### Added

#### First wall-clock measurement of the `/review` route at 50 and 100 items

Every responsiveness claim about `/review` up to now was an inference. The
commit that memoized the spine rows (`perf(review): memoize spine rows so one
checkbox re-renders one row`) argued from a re-render COUNT — "at the 100-row
page cap that is 99 wasted row renders per click, which is the sluggishness the
larger page sizes actually exhibit" — and nothing measured whether that
translated into time a user could feel. `web/tests/e2e/benchmark-review-lanes.spec.ts`
is that measurement.

It is a measuring instrument, not a gate: skipped unless `E2E_PERF=1`, excluded
from the chromium/webkit gate projects, and named in `GATE_EXEMPT`. It prints a
table rather than asserting a threshold, because a wall-clock threshold on a
shared runner is a flake factory and one loose enough not to flake would pass
while the page is slow.

Measured on an M-series laptop under load average 8–12 on 10 cores, chromium,
one worker, median of 5 repetitions per interaction (so these are ceilings for
this machine, not clean-room numbers):

| lane | metric | N=50 | N=100 | N=100 @6x CPU |
| --- | --- | --- | --- | --- |
| dupes | filter (client) | 32 ms | 50 ms | 271 ms |
| dupes | checkbox toggle | 41 ms | 67 ms | 423 ms |
| metadata | filter (client) | 22 ms | 25 ms | 174 ms |
| metadata | checkbox toggle | 19 ms | 26 ms | 217 ms |
| regroup | filter (250 ms debounce incl.) | 389 ms | 399 ms | 619 ms |
| regroup | sort change (client) | 240 ms | 263 ms | 527 ms |

Initial load to N interactive rows is 930–1015 ms for every lane at every size,
dominated by the lazy route chunk rather than by row count.

**The memo commit's claim is now measured rather than argued.** Reverting
`CompareSpine.tsx` to its pre-memo state and re-running the metadata lane at
N=100 doubles the checkbox cost, 26 ms → 52 ms. The optimization is real and
roughly halves the interaction; the revert was local and is not part of this
change.

**The dupes lane is the outlier and `DupesSpine.tsx` has no memoization at
all** — the memo commit covered `CompareSpine` and `RegroupSpine` only. At
N=100 a dupes checkbox costs 67 ms against metadata's 26 ms, and a dupes filter
keystroke produces a 234 ms longest-single-task against metadata's 55 ms. A
234 ms main-thread task is above the threshold where a keystroke stops feeling
instant. Reported, not fixed: this change is test-only.

Two things the harness deliberately does not cover, stated because a number
without its scope invites over-reading. Rows are seeded by `page.route`
interception, so it measures render and client state cost only — never server
latency, Pebble read cost, or any server-side filter (the dupes band/status
filters and the regroup kind filter all round-trip, and are excluded for exactly
that reason). And two of the four requested interactions do not exist to
measure: neither the dupes nor the metadata lane has a sort control, and the
regroup lane has no row selection. Those are reported as absent rather than
synthesised.

The instrument is verified against a known-bad input rather than assumed to
work: a 6x CPU throttle raises every figure 2–7x and takes blocked main-thread
time from 0 ms to 1.8–7.4 s, and an N=5 noise-floor row runs the identical path
so that the N=50-vs-N=100 comparison can be told apart from harness overhead.

#### Observed while measuring: the metadata rail checkbox is not clickable by coordinate

At the benchmark viewport the QueueRail row checkbox's centre is outside the
viewport, and once scrolled into view a `MuiDivider` / `MuiStack` receives the
pointer event instead — `check()` retries until timeout and `check({force:true})`
fails with "Clicking the checkbox did not change its state". Dispatching the
event directly at the element does flip it, so the handler is correct and the
problem is layout. The dupes lane's checkbox reports `self` under the identical
harness. Whether a human at a normal window size is actually blocked is NOT
established and needs confirming in a real browser; it is recorded in the
harness output rather than fixed here.
