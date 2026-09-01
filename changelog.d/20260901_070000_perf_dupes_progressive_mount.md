### Changed

#### The dupes review lane no longer freezes the page while it loads

Opening `/review` on the dupes lane with the 100-row page size rendered every
row in ONE synchronous main-thread task. Measured on
`benchmark-review-lanes.spec.ts`, that task was **156 ms**, with **106 ms** of
it counted as blocking — against a 5-row noise floor that produces no long task
at all. Anything past about 50 ms in a single task is a freeze the user can
feel: during it the page cannot paint, scroll, or respond to a click.

The cause was that the fetch resolving is a plain `setState`, so React rendered
all 100 rows to completion before yielding. The cost is close to linear in the
row count — 50 rows measured 87 ms and 100 rows 156 ms, i.e. roughly 18 ms of
fixed route/mount cost plus ~1.4 ms per row — so it got worse with exactly the
page-size option a reviewer picks to get more done.

The lane now commits the first 20 rows synchronously and lifts the cap to the
full page inside a `startTransition`, which lets React yield between rows
instead of running the pass to completion. The same mechanism was already proven
one interaction over: the client-side search re-renders ~99 rows through
`useDeferredValue` and measures zero tasks over 50 ms.

Measured at the 100-row page size, before and after on one machine in one
session, with the 5-row noise-floor row collected in each run:

| | blocked main-thread time | longest single task |
|---|---|---|
| before | 106 ms | 156 ms |
| after | 0 ms | 0 ms |

Zero here means the browser's longtask observer reported nothing at all, which
it does for any task under 50 ms — so the remaining load cost is real but below
the threshold at which it can be felt. Wall-clock to a fully interactive page
did not regress (995 ms → 970 ms, inside run-to-run noise).

Two behaviours were deliberately preserved rather than traded away, because both
guard an irreversible bulk merge:

- **"Select all on screen" still cannot reach a row the reviewer cannot see.**
  It reads the same value the rows are rendered from, so during the brief
  window where only the first slice is mounted it selects exactly that slice —
  never the full page waiting behind it.
- **The shift-click anchor still resolves.** It is an index, and lifting the cap
  only ever extends a prefix, so an index taken before the rest of the page
  arrived still points at the same row afterwards.

The first slice is mounted synchronously rather than deferring the whole list
for a specific reason: deferring everything would hand the first render an empty
list while loading had already finished, and the spine renders its "no duplicate
candidates" empty state when it has no rows — so the reviewer would see a
committed frame saying there is nothing here, immediately before 100 rows
appeared.

The metadata and regroup lanes are untouched and were re-measured on the same
build to confirm it. Metadata's pre-existing ~51 ms task at 100 rows is
unchanged and predates this work.
