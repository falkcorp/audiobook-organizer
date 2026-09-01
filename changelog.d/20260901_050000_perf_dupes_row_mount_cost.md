### Changed

#### The /review dupes lane no longer freezes the page when you filter 50 or 100 rows

Typing in the dupes lane's "Search this page" box re-filters the loaded page, and
at the 100-row page size that meant unmounting ~99 rows and mounting ~99 more in
one uninterruptible chunk of work. Measured on a production build, that was a
single **216 ms** freeze of the whole page — long enough to feel — while the
metadata and regroup lanes at the same row counts froze for 0 ms. Memoizing the
rows had already been tried and did not help, for a good reason: memoization
skips re-renders, and nothing here was re-rendering. Every row was being built
from scratch.

Two independent causes, both found by ablation rather than guessed at:

**The re-filter ran as one indivisible render.** The search term now goes through
`useDeferredValue`, so React treats the re-filter as a background transition and
yields between rows instead of running it to completion. Typing itself is
unaffected — the text box is still bound directly to the live value.

**Each row was building far more machinery than it showed.** Removing only the
MUI `Tooltip` wrappers from a row — leaving an identical 58 DOM nodes behind —
cut blocked main-thread time by 45% on its own, because a `Tooltip` costs roughly
six times a `Button` and twenty times a `Chip` to build, and there were four per
row that nobody ever points at. Three are now plain `title` hints; one of those
was already repeating the button's own accessibility label word for word.
Alongside that, the file-list popover on each book is no longer constructed until
its chip is actually clicked, and the path-abbreviation setting is now read once
per page instead of once per path — it was 200 separate reads at the 100-row cap,
each triggering its own re-render when it arrived.

Measured before and after on the same harness, machine and session:

| at 100 rows | before | after |
|---|---|---|
| filter — blocked main-thread time | 755 ms | **0 ms** |
| filter — longest single freeze | 216 ms | **0 ms** |
| loading the lane — blocked time | 170 ms | 104 ms |
| loading the lane — longest freeze | 220 ms | 154 ms |

At 50 rows, filtering also went from 260 ms blocked / 111 ms longest freeze to
zero. On a deliberately slowed machine (a 6x CPU handicap, standing in for older
hardware) the same filter went from 6,174 ms blocked / 1,314 ms longest freeze to
0 ms / 50 ms. "0 ms" means no single task crossed the 50 ms threshold the browser
counts as a freeze — not that no work happens.

Two honest caveats. **Loading the lane fresh still stalls ~154 ms at 100 rows**;
deferring the filter does nothing for the first render, and only the per-row
reductions moved that number. And a filter now takes slightly longer in total
wall-clock (40 ms → 44 ms at 100 rows) because the work is spread across more
frames rather than done in one — that is the trade, and it is the right way
round: nothing blocks long enough to be felt.

The one visible change: the three converted hints use the browser's own tooltip
styling and timing rather than the app's.
