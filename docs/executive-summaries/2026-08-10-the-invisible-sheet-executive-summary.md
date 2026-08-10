<!-- file: docs/executive-summaries/2026-08-10-the-invisible-sheet-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 10d717df-5ad7-4776-addb-6ef87e529c62 -->
<!-- last-edited: 2026-08-10 -->

# The invisible sheet that made the page stop responding

## What was wrong

Using a dropdown or the filter side panel could leave the whole page dead. Not
slow — dead. Every click after that did nothing at all, and the only way out was
to reload the page and start over.

Nothing on screen indicated a problem. The dropdown had already faded away, the
panel had already slid shut, and the page looked completely normal. What was
actually left behind was a transparent sheet covering the entire window,
soaking up every click before it could reach anything underneath.

That invisibility is the whole story of why this survived so long. A visibly
stuck menu would have been reported and fixed months ago.

## Why it was missed twice before

This had already been investigated twice, and written off both times.

The first pass concluded the test machine was simply too slow — that the
closing animation was being starved of processing power, and that no real user
would ever see it. That explanation was written into the test configuration as
a comment saying, in effect, *neither the app nor the tests are wrong*. A note
like that is worse than no note, because it tells the next person not to look.

The second pass correctly rejected the slow-machine theory and suspected a real
defect, but the check it used could not see the problem. It waited for the menu
to become "hidden" — and the menu *was* hidden. It had faded to fully
transparent. Being invisible and being harmless are not the same thing, and the
sheet that was blocking clicks was invisible.

## What actually happened

The closing animation starts correctly and then never finishes. The panel fades
out on schedule, and then the browser simply never gets told the animation is
over. Because the page only removes that click-blocking layer once the close is
*confirmed* complete, the layer stays forever.

This was pinned down by making it happen on demand rather than waiting for it.
Run on its own, the affected test passed every time. Run twenty copies at once,
it failed twenty times out of twenty. That reliable failure is what made the
rest possible — a fix that is only ever tested against something already
working proves nothing.

The deciding measurement was shortening the closing animation and watching the
failure rate move with it:

| closing animation | test passed |
|---|---|
| default (~0.28s) | 0 of 20 |
| 0.25s | 8 of 20 |
| removed | 20 of 20 |

A rate that slides smoothly like that means a timing race — two things
competing, where a longer animation means more chances to lose. A broken timer
or a logic error would have failed all-or-nothing instead. That distinction is
what identified the fix.

## What was fixed

Dropdowns and side panels now close instantly instead of animating out. Opening
them is unchanged — only the closing animation was ever involved, and removing
it removes the window in which the race can happen.

Two separate controls were affected. The first fix covered dropdowns; the full
test run afterwards still showed one failure, which turned out to be the *same
defect* on the side panel. Notably, the side panel had none of the technical
characteristics originally blamed for the dropdown case, and broke anyway —
which is how we know the first explanation was wrong.

## What we still do not know

**Why the animation never reports finishing is unexplained.** We know exactly
what the symptom is, we can reproduce it at will, and we have removed the
conditions it needs. We have not identified the underlying cause inside the
interface library the site is built on.

That is written into the code as an open question rather than smoothed over,
because the first explanation for this bug was confidently wrong and cost two
investigations. An honest unknown is cheaper than a plausible mistake.

## Where things stand

The browser test suite is fully passing again — 278 tests, no failures — and
the two misleading comments that stopped anyone looking have been corrected in
place, with the measurements that disprove them.

This one is worth remembering less for the bug than for the shape of it: a
failure that leaves the screen looking perfectly normal, that two reasonable
investigations explained away, and that only became findable once someone could
make it happen twenty times in a row on purpose.
