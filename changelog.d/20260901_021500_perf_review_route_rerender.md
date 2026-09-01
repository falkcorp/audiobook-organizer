### Fixed

#### The review count poller re-rendered the whole /review route every 30 seconds

`useReviewStore.loadCount` installed a freshly-parsed `byKind` object on every
poll tick, whether or not a single number in it had changed. A new object is
never `Object.is`-equal to the last one, so every `(s) => s.byKind` subscriber
woke up twice a minute on an idle queue.

On `/review` that subscriber is `useRegroupLane`, and `byKind` feeds a `useMemo`
dependency array there — so the new identity rebuilt the lane's buckets,
produced a new lane object, and re-rendered `ReviewWorkspace` and every panel
beneath it. `useRegroupLane` is called unconditionally, and its `active` flag
gates only the fetch, not the subscription, so this fired in **all three lanes**
regardless of which one was on screen.

`loadCount` now compares the incoming breakdown against the stored one and keeps
the existing object when the counts are identical. `count` is a primitive and
zustand's own equality check already absorbed it, so `byKind` was the only
identity churn on this path; an unchanged tick now causes zero re-renders
anywhere. Real movement — a changed count, a kind appearing, a kind
disappearing — still propagates, and the badge still updates when only the
total moved.

#### Selecting one candidate re-rendered all 100 rows in the compare spine

`CompareSpine`'s row renderers each received the whole `SpineContext`, which is
rebuilt whenever `rowStates`, `selectedIds` or `expandedId` changes. Ticking a
single checkbox therefore re-rendered every row on the page — the sluggishness
that shows up at the larger page sizes.

The spine now derives a stable handlers object from the context and passes each
row its own `selected` / `rowState` / `expanded` values as plain props, so the
memoized renderers only re-render when something about *that row* changed.
`SpineContext`'s public shape is unchanged, so `useMetadataLane` and the spine's
existing tests were not touched.

#### No request on the review route had a timeout

None of the route's four data calls passed `timeoutMs`, so a server that
accepted the connection and then stalled left the reviewer on a spinner forever,
with the loading state and the hung state rendering identically. Each call now
carries a deadline sized to what it actually does — 15s for the polled count,
30s for the review queue, 60s for dedup candidates, and a deliberately generous
120s for the cached metadata review set, which the lane requests in full. A
caller-supplied abort signal still cancels as before and is still distinguished
from a deadline, so switching lanes does not flash an error.

### Known issues

The metadata lane still fetches its entire review set with `limit=0` and
paginates client-side. That is load-bearing rather than an oversight: filtering,
the staleness set, and candidate grouping all span the whole library, and the
server endpoint accepts only `limit`/`offset` with no filter push-down. Making
the client honour the server's pagination would confine filters to a single page.
It needs server-side filter push-down first and is not addressed here.
