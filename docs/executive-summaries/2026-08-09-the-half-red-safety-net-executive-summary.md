<!-- file: docs/executive-summaries/2026-08-09-the-half-red-safety-net-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 0d4f8a91-6c27-4e35-b8d0-51a97c3e2f46 -->
<!-- last-edited: 2026-08-09 -->

# The safety net that was still half on the floor — and the eleven things it caught

## What was wrong

Yesterday's summary said the automated browser tests were restored and could be
trusted again. That was wrong, and it is worth being blunt about why.

The suite that runs a real browser through the website had been checked with a
command that quietly hid most of its own output, and against a server that had been
left running for hours. Both problems flattered the result. When it was finally run
properly — fresh server, full output written to a file — the real number came back:
**146 of 288 tests failing.** Roughly half the safety net was still on the floor,
across 22 different areas of the site.

None of those 146 were reporting that the website was broken in the way they
described. They were describing a version of the site that no longer exists —
buttons that had been renamed, panels that had moved to a different tab, dialogs
that had been replaced. The tests had simply never been updated to follow.

## What happened

**All 146 are now passing.** Fourteen changes, one area of the site at a time, each
one measured before and after so the number could not drift.

But the interesting part is not the number.

Bringing a test back to life means working out, for every difference, which side is
wrong: has the test gone stale, or has the website actually lost something? Doing
that 146 times turned up **eleven things where the website was the one at fault.**
Two were fixed on the spot; the rest are written down with enough detail that
someone can decide what to do about them.

### The two that were fixed

**The "Deleted" filter in the library did nothing.** Pick Deleted from the filter
list and you would get the entire library back, unfiltered — while the Filters
button sat there showing a count, so it looked like the filter had been applied.
It only ever worked on a completely fresh page load, which is exactly why nobody
caught it by hand: the second time you tried it in a session, it silently stopped
working.

**Editing a book's details showed an empty Genre box** no matter what genre the
book actually had. A book with no genre and a book whose genre simply was not being
displayed looked identical.

### The two you will want to decide on

**The library can no longer be sorted.** Sorting works — the site still remembers
your choice, still puts it in the address bar, still asks the server for books in
that order. There is simply no longer a control anywhere on the page to change it.
The only way to sort the library today is to hand-edit the web address.

**The search box asks the server a fresh question after every single letter you
type.** Typing a ten-letter title sends ten separate full searches of the entire
library. On top of that, the moment you start typing, every filter you had set is
silently dropped — search for an author while filtered to "Organized" and you get
results from everywhere, with the filter still showing as active.

That second one matters beyond search. There is already a plan to move heavy
filtering work off the browser and onto the server, because the page can grow to an
unreasonable size. That plan will not help much while the browser is sending ten
queries for one search — the fix has to happen on both sides.

### The other seven

A page that would crash if a single author record arrived missing one optional
field.† A dialog that fifty lines of code still build but which nothing can open any
more. The ability to jump between different versions of the same book, gone —
replaced in the same spot by a button that moves files between them instead, which
is not a thing you want to click by accident. A summary that used to say "part of a
group of 3" now says only "linked", with no count and no indication which one you
are looking at. The one-click "use the value we found online" button on individual
fields, gone. And two controls that a screen reader cannot describe at all, one of
which is now the only route to four different actions.

> **† Correction, added later.** That first one was overstated. The page's code really
> is unguarded, but checking the server showed it has always filled that field in — so
> nothing was actually breaking, and there was no way for a user to hit it. The page was
> hardened anyway, because a guarantee that lives only on the other side of a network
> call is worth not depending on. But "would crash" was a guess about the server made
> from reading the browser code, and it should have been checked before being written
> down as a finding.

## Update, later the same day: the second browser is green too

The four remaining failures in the second browser engine have been resolved, and the
whole safety net now passes: **552 tests passing, none failing**, across both browsers.

Three of the four turned out not to be faults in the product at all. The test software
that pretends to be a person clicking was failing to press a button — the same button,
in the same place, that works every time when pressed by other means. Establishing that
took a direct comparison rather than an argument: pressed one way it failed four times
out of four, pressed the other way it worked six times out of six. Those three tests now
press the button in a way that works, and they announce it in the log every time they
have to try twice, so if the situation ever changes it will be visible rather than
quietly absorbed.

This is worth stating because it had been recorded twice as a defect in the product,
with confident-looking evidence attached both times, and both times that was wrong. The
correction is now on the record next to the original claim rather than replacing it.

The fourth failure happened once and has not happened again in over a thousand
subsequent test runs. It has deliberately **not** been "fixed", because it passes
whenever it is run on its own — which means nobody has yet established what was actually
wrong, and changing the test to accept the behaviour would be assuming an answer. It is
written up, with the frequency measured and the next step recorded, and left honest.

## What this means going forward

The suite is green, and the number was measured the careful way and can be repeated:
**552 passing, 0 failing**, on a clean checkout, across both browsers.

Seven tests are deliberately marked as expected-to-fail, each one attached to a
real defect above. They are not skipped or deleted — if someone fixes the
underlying problem, those tests immediately report it as a surprise, which is
exactly the behaviour you want.

Nothing was disabled to make this number look better. That distinction is the whole
point: the four-month blind spot this work exists to prevent happened because six
test files stopped running and nobody noticed. Turning red tests off is how you get
there again.
