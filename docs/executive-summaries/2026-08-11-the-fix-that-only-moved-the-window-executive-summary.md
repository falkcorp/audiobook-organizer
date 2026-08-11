<!-- file: docs/executive-summaries/2026-08-11-the-fix-that-only-moved-the-window-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 65822952-722b-4f17-9d40-d7b614cbfc6a -->
<!-- last-edited: 2026-08-11 -->

# The fix that only moved the window

## Short version

Yesterday we reported that the "invisible sheet" bug — the one where the page
went completely dead after using the filter panel, with nothing on screen to
show why — had been fixed. That report was premature. The change we made had
made the bug much rarer, not impossible. It could still happen to you.

It is now actually fixed, and we can demonstrate the difference rather than
assert it.

## What was still wrong

The original problem: closing the filter panel occasionally left an invisible
sheet stretched across the entire window. The panel looked shut. The page looked
normal. But every click landed on that sheet instead of on the page, so nothing
responded, and the only escape was to reload and lose whatever you were part-way
through.

Yesterday's fix removed the closing animation on the panel, on the theory that
the bug needed the animation's duration as a window in which to happen. Shorten
the window to nothing, and the bug has nowhere to live.

That theory was close enough to look right. Shortening the animation genuinely
did make the failure much rarer, and the tests went green. What we did not
realise is that removing the animation's *duration* does not remove the
*deferred step* underneath it. The software still scheduled "now finish closing"
as a separate later task, just a task set to happen immediately instead of a
fifth of a second later. Immediately is not the same as never, and the bug lived
in that gap.

## How we caught it

Two things had been hiding it.

The first was a measuring instrument that changed the thing it measured. The
diagnostic used to investigate this took a reading from the browser immediately
before each test action. Taking that reading is itself a pause — and the pause
was enough to make the bug not happen. So the diagnostic had been reporting, in
good faith, that everything worked, while the real product without the
diagnostic attached did not.

The second was that we had only ever run the comparison twice per side. Two runs
out of two is not enough to tell "this version is broken" apart from "this
version happens to lose the race more often". Running it ten times per side told
a different story: the old version was not safe at all. It failed nine times out
of ten under a slightly different sequence of actions — a sequence a real person
can easily produce.

So the upgrade we were blocking on did not introduce this bug. It made an
existing bug easy to hit. We had been about to congratulate ourselves for fixing
a regression that was never a regression.

## What we changed

The panel now closes without scheduling anything for later. It finishes closing
as part of the same step that starts it. There is no deferred task left to be
lost, so there is no window for the failure to occur in — as opposed to a window
so short we could not catch it.

Visually nothing changed. The panel disappeared instantly before this change and
disappears instantly after it.

Measured on the same machine, ten runs per configuration: the failing case went
from failing ten times out of ten to passing ten times out of ten, and the
end-to-end test that had been blocking the upgrade went from failing to passing
ten out of ten.

## What we still do not know

The underlying reason the deferred step gets lost — a task that we can see run,
that asks the page to update, on a component that is demonstrably still alive,
and the update simply never takes effect — remains unexplained. We have
eliminated the usual suspects and written the elimination list into the code so
the next person does not repeat it.

We have also corrected the comment in the code that credited yesterday's change
with fixing this, and replaced it with the measurements that disprove it. A
confident wrong explanation left in place is how this survived three
investigations.

## The lesson worth keeping

A fix that makes a failure rarer looks exactly like a fix that makes it
impossible, right up until it doesn't. The tell is whether you can say *why* it
can no longer happen, rather than only that it stopped happening. Yesterday we
could only say the second thing, and we reported it as though it were the first.
