<!-- file: docs/executive-summaries/2026-08-12-checking-our-own-homework-executive-summary.md -->
<!-- version: 1.0.0 -->
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
earlier. And two of the things we found and "fixed" turned out to be wrong — one was a
mistaken accusation, the other actively broke something. Both were caught and undone the
same day. The honest summary is not "we fixed eight things"; it is "we fixed eight
things, got two of them wrong, and caught both before anyone was affected."

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

## 5. Two things we got wrong

**A mistaken accusation.** We concluded the server was overstating what it lets apps do —
claiming it accepts changes when nothing could be changed. That was wrong. It had been
checked in one place only, and missed **nine** other places where the server genuinely
does accept changes: saving your position in a book, and bookmarks. Acting on the finding
would have told every app "we don't accept changes," turning off progress-saving and
bookmarks — the two things that matter most to someone actually listening. The finding
was withdrawn, and the wrong reasoning was kept on the record rather than deleted.

**A fix that broke something.** Part of the work above made the server answer "no" to
features it doesn't have. Six were listed. Three of them — authors, series and playlists
— are things the server *does* have, under a slightly different address. The old
behaviour quietly forwarded requests to the working version; the change replaced that
with a flat "no", switching off **46 working functions** for anyone using the older
address. It also did this on every installation, including ones with the phone-app
support turned off entirely.

This was caught the same day, before any release, and reversed for those three. Thirty
days of production records show **no request from anyone** to the affected addresses, so
no user is known to have been affected. A test now guards it, and that test was checked
by deliberately reintroducing the bug to confirm it fails.

---

## Why the mistakes are in this summary

They could have been left out. Both were found and fixed by us, on the same day, with no
user impact — the kind of thing that never has to be mentioned.

They are here because the pattern is the point. Both errors came from **checking one side
of a question and concluding we had checked all of it**: one place that accepts changes,
not nine; who *calls* an address, but not whether the address *works*. Every item in this
summary is a version of the same failure — something that reported a state it had not
actually verified. Leaving our own two out would have made the write-up an example of the
problem it describes.

---

## Where this leaves things

- The phone-app compatibility work has a verified test suite and a known, written list of
  what is still missing.
- The API specification is one document, valid, and honest about its 48 gaps.
- Four decisions are waiting on the owner, all written up: a security question about a
  key stored in the browser, what to do about those 48 phantom entries, a stale status
  column in an older security review, and whether an unfinished redesign is still wanted.
- Nothing in this summary is deployed to production yet.
