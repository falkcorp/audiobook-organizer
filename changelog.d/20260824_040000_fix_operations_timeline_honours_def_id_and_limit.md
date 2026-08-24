### Fixed

#### The operations timeline now answers the question you asked it

`GET /api/v1/operations/timeline` accepted `def_id` and `limit` in the sense that
it did not complain about them. It did not read them. Gin drops unknown query keys
silently, so the handler saw only `since` — and `since` defaults to **fifteen
minutes**.

A query written the natural way, `?def_id=library.scan&limit=200`, reads as *"the
last 200 runs of the library scan."* What it actually asked for was *"everything in
the last quarter hour."* On a quiet system that comes back with one unrelated row,
which is indistinguishable from **"this job has never run."**

That is not a hypothetical misreading. It produced three wrong conclusions in two
days: a scan population recorded as 9 runs when the true 7-day figure was 21, a
nightly-maintenance failure count recorded as 3 nights when it was 7 for 7 — in a
document that shipped with the undercount — and a diagnosis of a "broken filter"
that was briefly confirmed against a second, unrelated bug.

Both parameters are now real. `def_id` selects one operation type; `limit` bounds
the rows returned, up to 1000.

**Where the filter is applied is the whole fix.** The store sorts and truncates
before the handler sees anything, so filtering a page the store had already cut
would answer *"the runs of this job that happen to fall in the newest 200 overall"* —
a different plausible wrong answer in place of the old one. Queued rows would go
first, because they sort last. The filter therefore runs across the whole window,
and the limit is applied after it.

#### The timeline says what it looked at

An answer that describes its own scope cannot be mistaken for a complete census, so
the response now carries the window it measured (`since`, `window_start`), the
filter it applied (`def_id`), and the bound it used (`limit`).

It also reports `matched` — how many runs were in the window before the limit — and
`truncated`. Those are facts rather than the usual guess: counting the matches
before trimming is the only way to tell *"exactly 200 existed"* from *"there were
more."* When a scan is large enough to hit the server's own internal bound, the
reply says `scan_capped`, meaning the total is a floor and no *"it never happened
before X"* claim can rest on it.

Two inputs are now refused instead of quietly ignored: a negative `since`, which put
the window boundary in the future and returned a near-empty list that read as
"nothing happened", and a `limit` that is not a positive number, which used to fall
back to the default with no way for the caller to notice.

The one behaviour left deliberately unchanged is the fifteen-minute default for
`since`. It is now stated in every reply rather than being invisible.

### Removed

#### A second, unreachable copy of this endpoint

`internal/server/operations_v2_handlers.go` held a near-identical twin of the
timeline handler plus two others, and no route registered any of them. Every symbol
in the file was unused outside it.

It mattered because the file came with its own tests, which built their own router
and pointed it at the dead code. Anyone strengthening this endpoint's tests there
would have watched them pass green while production behaviour never changed. The
file and its tests are gone; the live handler's tests are in
`internal/server/handlers/`.

#### The timeline says which of its results predate the window it names

Operations that are still running are returned however old they are — deliberately,
because an operation that has not finished is current no matter when it started.
A library scan running for nearly two hours once returned an *empty* timeline in
production while it was logging once a second, which reads as "nothing is
running."

That is the right behaviour and it made the new self-describing window partly
untrue. A scan queued three weeks ago and never finished answers a one-hour
query with one result, under a stated window of one hour — which reads as "this
job ran once in the last hour."

Those rows are now counted separately, so the window the reply names is true of
the rows it is claimed for. An answer that describes its own scope has to describe
the results that escape that scope too, or it is just a more confident version of
the wrong answer.
