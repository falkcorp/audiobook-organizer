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

**The categorical result, which does not depend on wall-clock at all:** at
N≤100 the dupes lane is the only review lane that blocks the main thread. Every
metadata and regroup interaction reports zero long tasks, zero blocking time and
zero max-task at both sizes; dupes reports 288 ms blocking / 120 ms longest task
at N=50 and 830 ms / 234 ms at N=100. Long-task counters are gathered inside the
page, so unlike the wall-clock figures they carry no CDP round-trip floor.

The wall-clock table, measured on an M-series laptop under load average 8–12 on
10 cores, chromium, one worker, median of 5 repetitions (min–max in brackets).
The N=5 column is a noise floor running the identical code path; a row whose
N=100 figure does not clear its own floor is labelled as such rather than
quoted as a trend:

| lane | metric | N=5 floor | N=50 | N=100 | N=100 @6x | resolves above floor? |
| --- | --- | --- | --- | --- | --- | --- |
| dupes | filter (client) | 17 [14–29] | 32 [25–33] | 50 [47–67] | 271 | yes |
| dupes | checkbox toggle | 14 [13–23] | 41 [39–50] | 67 [63–83] | 423 | yes |
| metadata | filter (client) | 15 [14–30] | 22 [18–33] | 25 [24–39] | 174 | **no** |
| metadata | checkbox toggle | 9 [8–20] | 19 [17–26] | 26 [26–39] | 217 | marginal |
| regroup | filter (250 ms debounce incl.) | 377 [374–395] | 389 [382–402] | 399 [394–421] | 619 | marginal |
| regroup | sort change (client) | 221 [213–304] | 240 [234–288] | 263 [256–295] | 527 | **no** |

So only the two dupes rows are a measured scaling result. The metadata filter
and the regroup sort move by less than their own N=5 spread and must be read as
"no cost detectable at this N", not as a trend.

The regroup figures also need reading with their zero blocking time in mind:
377 ms at N=5 with no main-thread work at all is the 250 ms search debounce plus
a MUI transition and the idle wait, and 221 ms for a sort at N=5 is the Select
menu's open/close animation. `regroupSortOnce` is largely measuring the control,
not the re-sort. Neither number is compute.

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
apply-and-clear cycle at N=100 produces a 234 ms longest single task while
metadata's produces none at all. A 234 ms main-thread task is above the
threshold where an interaction stops feeling instant. (The long-task counters
are reset once per 5-repetition batch and span both the apply and the clear of
each repetition, so they do not divide into the per-interaction medians — they
answer "did this lane block the main thread", not "what did one apply cost".)
Reported, not fixed: this change is test-only.

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
