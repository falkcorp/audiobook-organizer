### Fixed

#### The Activity Log page could keep an already-struggling server on the floor

Prod was OOM-killed three times on the night of 2026-08-10, with a heap profile
putting **8.86 GB (71.9% of heap)** in the activity query. The server-side query
is being fixed separately. This is the other half: the client was re-triggering
it forever, and hiding the fact that anything was wrong while it did.

Four defects compounded.

**Nothing anywhere cut a request off.** `apiFetch` set no timeout and no
`AbortController` unless the caller passed one, and the activity fetches did
not; the server's `WriteTimeout` is `0`. A query that never finished left the
tab spinning forever while the server kept the work — and its memory — alive.
`apiFetch` now accepts an opt-in `timeoutMs`. The default is still *no* timeout,
deliberately: `/activity/compact`, scans and transcodes legitimately run for
minutes and a blanket default would break them. The two activity reads set 15s —
comfortably above any healthy response, and below the page's 30s idle refresh so
one wedged request cannot silently disable auto-refresh for a whole cycle.

**Polls did not wait for the previous poll.** The feed refreshed every 5s (with
active operations) or 30s (idle) on a fixed schedule, regardless of whether the
last request had returned, so requests stacked up. This is what turned a single
open tab into an unbounded server-side memory leak. Background refreshes now
drop their tick entirely while a request is outstanding; user-driven loads —
mount, filter change, page change, Refresh — abort the older request and
supersede it instead, since its answer is already stale. A monotonic sequence
guard stops a superseded request that finishes late from clobbering the state of
the request that replaced it. Both endpoints are covered, not just the feed:
`/activity/sources` is polled on the same tick and aggregates over the same
range.

Measured with the guard removed, against a request that never returns: 1 fetch
becomes **4** over 95 seconds, growing without limit. With it, 1.

**Every mount fetched the expensive query twice.** Two `useEffect`s both loaded
the feed on mount — one keyed on the filters, one on page/pageSize.
Consolidated into a single effect; a filter change resets to page 1 and lets the
re-run do the one fetch.

**There was no error state at all.** The catch logged to the console and cleared
the table, so a 500, a 401, a network failure and a genuinely empty log all
rendered the identical **"No activity entries found."** — which invites the user
to refresh a server that is falling over. The page now distinguishes four
states: loading, error (with the reason and a Retry button), empty, and
populated. A failed *background* refresh no longer wipes what you were reading;
it keeps the last good page under a "showing the last successful result"
warning. Failures from the sources endpoint get their own advisory banner
instead of vanishing into the console.

#### The Activity Log's Since/Until filters never worked

Found while adding the error state, and a good illustration of why it was
needed. The page sent the raw `datetime-local` input value
(`YYYY-MM-DDTHH:mm`), but the handler parses with
`time.Parse(time.RFC3339, ...)` and rejects anything else with a 400. Every date
filter the page has ever sent was refused — and with no error state, that 400
rendered as "No activity entries found.", so the filters looked like they worked
and simply found nothing. Values are now converted to RFC3339 before sending.

#### The Activity Log no longer asks for all of history by default

With the date filters silently broken, the page sent no time bound at all: every
load, and every poll tick, asked for the entire log. The feed is now bounded to
the last 24 hours by default. This is a **visible** default, not a silent cap —
it is filled into the "Since" field, labelled "Default: last 24h — clear for all
history", freely editable, and the empty state offers a one-click "Search all
history" so a quiet 24 hours can never be mistaken for an empty log.

Covered by `ActivityLog.test.tsx` (8 cases) and `apiFetch.test.ts` (3). Each
regression test was verified by reverting its fix and confirming the test fails:
the error test falls back to the empty state, the mount test reads 2 fetches
instead of 1, the poll test reads 4 instead of 1, and the timeout test hangs for
the full 30s — the prod symptom, reproduced.
