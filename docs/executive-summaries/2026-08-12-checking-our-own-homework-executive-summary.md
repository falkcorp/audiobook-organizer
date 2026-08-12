<!-- file: docs/executive-summaries/2026-08-12-checking-our-own-homework-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4f1c8e27-9db3-4a56-b0e8-6c395a71d2f4 -->
<!-- last-edited: 2026-08-12 -->

# Executive Summary: Checking Our Own Homework

**Date:** 2026-08-12
**Written for:** anyone who wants to know what changed and why it mattered, without
needing to read code.

---

## In one paragraph

This was a day of **checking claims** — most of them our own. A phone app was being told
a feature existed when it didn't. Our documentation described dozens of things the server
cannot do. A maintenance script reported success after doing nothing at all. Our to-do
list said an important job had never run, when in fact it had finished cleanly weeks
earlier. And the tests meant to prove our phone-app compatibility were checking that
answers had the right *fields*, never what was *in* them — a check that could not fail.
Switching it on found five more real problems.

And some of what we found and "fixed" turned out to be wrong — one was a mistaken
accusation, one actively broke something, the repair for *that* missed a piece, and two
later findings were reported before they were understood. All five were caught and
corrected the same day. The honest summary is not "we fixed nine things"; it is "we fixed
nine things, got five claims wrong along the way, and caught every one of them before
anyone was affected."

---

## 1. The app was told a feature existed

Audiobook apps on a phone don't ask "what can you do?" — they *probe*. They send a
request to a known address and treat any friendly answer as "yes, supported."

Our server had a habit of being friendly to everything. Ask it for any address it doesn't
recognise and it would hand back the web page — a perfectly valid response that, to a
phone app, reads as **yes**. So when an app checked whether we supported live updates
("this book just finished, refresh the screen"), it got a yes. We do not support live
updates. The app then waited for updates that would never come.

The server now says **no** to those questions, plainly. Saying no lets the app hide a
feature it can't use. Saying yes made it look present and broken — which is worse,
because a person then reports a bug against a feature that was never there.

## 2. The manual described things that don't exist

We keep a written specification of everything the server can do — the reference other
tools and future work are built against. We had **two** of them. They had drifted apart
over time until each described things the other didn't, and neither matched reality.

They are now a single document, and it has been checked against the server's own list of
what it actually answers. Along the way we found that **48 described features do not
exist**. One of them had been copied out of a note in the code — a comment describing an
idea, filed as though it were a working feature.

Those 48 are now written down as a known problem rather than quietly presented as truth.
Nothing was removed on a guess.

## 3. A script that reported success after doing nothing

A maintenance script would report a clean run when it had processed **zero** items. If
the thing it was meant to read was empty or unreadable, it found nothing, had no errors
to report, and said everything was fine.

This is the most expensive kind of bug, because it doesn't look like one. It now stops
and says exactly what it found and what it expected.

## 4. The to-do list was wrong, not the job

Our notes said a large cleanup job had never run. Production's own records show it ran,
processed **7,891 items**, and finished with zero errors. The job was fine; the to-do
list was stale.

Checking that turned up something more useful than the original question: the backlog
that job cleared has been **filling back up** — from about 1,300 items to nearly 6,000 in
three and a half weeks. Running the cleanup again would clear it and it would refill.
Something upstream is producing the work, and that is now the recorded problem.

## 5. Three things we got wrong

**A mistaken accusation.** We concluded the server was overstating what it lets apps do —
claiming it accepts changes when nothing could be changed. That was wrong. It had been
checked in one place only, and missed **nine** other places where the server genuinely
does accept changes: saving your position in a book, and bookmarks. Acting on the finding
would have told every app "we don't accept changes," turning off progress-saving and
bookmarks — the two things that matter most to someone actually listening. The finding
was withdrawn, and the wrong reasoning was kept on the record rather than deleted.

**A fix that broke something — and the repair that missed a piece.** Part of the work
above made the server answer "no" to features it doesn't have. Six were listed. Three of
them — authors, series and playlists — are things the server *does* have, under a
slightly different address. The old behaviour quietly forwarded requests to the working
version; the change replaced that with a flat "no", switching off **46 working
functions** for anyone using the older address. It also did this on every installation,
including ones with the phone-app support turned off entirely.

That was caught the same day and reversed for those three. But the repair checked the
list by searching the source code for each address — and **some addresses cannot be found
that way at all.** Most are written out plainly in the code, but six groups of them are
assembled from pieces when the server starts, so the finished address appears nowhere to
be searched for. User management is one of those six. It stayed broken because of it —
including the password-reset page.

The check no longer works that way. Instead of searching the code, the test now asks the
running server for its own list of addresses, which is the only version that is
guaranteed complete. It fails if any address we've marked "we don't have this" turns out
to exist, and also if one we've marked "keep this working" ever disappears. Both
directions were confirmed by deliberately reintroducing each bug.

Thirty days of production records show **no request from anyone** to any of the affected
addresses, so no user is known to have been affected by either mistake. Nothing had been
released.

---

## 6. The compatibility tests were checking the shape of the answer, not the answer

To make the phone apps work, we keep recordings of how the reference audiobook server
answers each request, and compare our own answers against them. Twenty-two of these
comparisons run on every change.

They were only checking that the right **fields were present**. Not what was in them. An
answer that named every field correctly and filled every one with the wrong number passed
all twenty-two. The suite looked like proof of compatibility and was closer to proof of
vocabulary.

Turning on the check of the actual values found **five real problems**, none of which
anything else had noticed:

- **A book's position drifts, and drifts further the deeper you are into it.** We store
  the length of each audio file rounded to a whole second. The player works out where each
  file starts by adding up the lengths before it, so those roundings accumulate. On the
  six-part recording we test against, the last part starts 2.2 seconds later than it
  really does; a book of twenty-odd parts would be out by roughly ten seconds by the end.
  If you close the app and come back, that is how far off you can be put down.
- **The "your listening sessions" list reports the wrong page size**, which can mislead an
  app into asking for pages that do not exist. Two neighbouring pieces of code get this
  right; this one was written differently.
- **We never record what kind of device connected** — phone, tablet, watch — and report
  "unknown" for all of them.
- **Audio quality figures are rounded** to the nearest thousand.
- **A publication year of "800BC" comes back as "800."** We store years as plain numbers,
  so anything that is not one is lost.

None of these were fixed here. They are all written down with measurements, and the first
needs a decision about changing how the data is stored — a bigger job than the testing work
that found it.

The test data itself was part of the problem: it had been typed in by hand and no longer
resembled the real recording — six identical files of two kilobytes standing in for six
different ones of eleven to twenty-one megabytes. It is now generated from the recording,
so it cannot drift again.

**Two corrections.** We reported a sixth problem — that we were sending a track number in a
different format than the reference server. That was wrong. We had looked at one line of
the comparison and inferred a rule from it; reading all six lines showed the reference
server does exactly what we do, and our own hand-typed test data had invented the
difference. **One row of a comparison is one example, not a pattern.**

And a test failure that we could not reproduce afterwards was written up as a possible
instability. It was not: the failing run simply predated a correction made in response to
it, so the 27 clean runs that followed were the fix working. But the *guess* offered for
it — that the machinery picking which known-difference applies could pick inconsistently —
turned out to describe a real flaw, just not the one observed. It was found and fixed
because an unexplained result was written down instead of shrugged off.

---

## Why the mistakes are in this summary

They could have been left out. All were found and fixed by us, on the same day, with no
user impact — the kind of thing that never has to be mentioned.

They are here because the pattern is the point. All of them came from **concluding we had
checked something when the check could not have seen the answer**: one place that accepts
changes, not nine; who *calls* an address, but not whether the address *works*; a search
of the code that structurally cannot find half the addresses it was searching for; and a
rule inferred from a single line of a comparison whose other five lines said the opposite.
The third is the sharpest, because it was the *fix* for the second — the correction
repeated the original shape of the mistake, one level down.

The compatibility suite in section 6 is the same failure worn by a test rather than a
person: twenty-two checks reporting conformity, none of them able to see a wrong value. A
test that cannot fail is more dangerous than no test, because it is the same blindness
with a green tick on top.

Every item in this summary is a version of that failure: something reporting a state it
had not actually verified. Leaving our own three out would have made the write-up an
example of the problem it describes.

---

## Where this leaves things

- The phone-app compatibility suite now checks the values in an answer, not just its
  shape, and was verified by being made to fail on purpose before being trusted. Where we
  genuinely cannot match the reference server, the difference is written down **with a
  limit on how large it is allowed to be** — so a known small discrepancy staying small
  passes, and the same field going badly wrong still fails. That limit immediately caught
  a chapter-boundary error of over four minutes that a plain "this may differ" note would
  have waved through.
- Five newly-found problems are written up with measurements and are waiting on decisions,
  the largest being how audio file lengths are stored.
- The API specification is one document, valid, and honest about its 48 gaps.
- Four decisions are waiting on the owner, all written up: a security question about a
  key stored in the browser, what to do about those 48 phantom entries, a stale status
  column in an older security review, and whether an unfinished redesign is still wanted.
- Nothing in this summary is deployed to production yet.
