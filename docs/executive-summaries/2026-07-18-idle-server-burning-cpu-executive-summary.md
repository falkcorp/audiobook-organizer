<!-- file: docs/executive-summaries/2026-07-18-idle-server-burning-cpu-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: bd117eee-bafa-4b55-a227-b3e64987b4d9 -->
<!-- last-edited: 2026-07-18 -->

# The server was burning two CPU cores around the clock while doing nothing

## What was wrong

Even when nothing was happening — no imports, no scans, no one using the app — the
production server sat at roughly two CPU cores of constant load, indefinitely. From the
outside it looked busy; in the logs it looked idle. Nothing explained the gap.

Separately, the app's health check — the quick "are you alive?" endpoint that monitoring
tools poll — took about **5.6 seconds** to answer. A health check is supposed to be
instant; a slow one risks being misread as "the server is down."

Both had the same single cause.

## What was actually happening

The app shows a live count of how many books are in your library. To get that number it
was reading **every book in the database and fully decoding each one**, just to count them
— about 44,000 books, roughly 5.6 seconds of work each time it was asked.

A background task refreshes the dashboard's live stats **every five seconds**. Because
counting the books took longer than five seconds, the counts never finished before the next
one was due, so they ran **back to back, forever** — one core doing the counting, a second
core cleaning up the mountain of throwaway data each count produced. The health check called
the same count, which is why it was slow too.

It had nothing to do with recent work. This had been running since early May — weeks of two
cores wasted, hidden because the task logged nothing while it spun.

## What we changed

The book count is now **remembered for 30 seconds** instead of recomputed from scratch on
every request. The expensive full read happens at most once every half-minute rather than
continuously; every caller in between gets the remembered number instantly. A count that is
up to 30 seconds out of date is completely fine for a status widget and a health check.

We deliberately did **not** refresh the count on every book change. During an import — when
books change constantly — that would have re-triggered the expensive read on the very next
tick and put the problem right back. A steady 30-second cap avoids that entirely.

A test locks the behavior in: it fails if the count ever goes back to reading the whole
library on every call.

## How we found it

It surfaced while doing a routine health check on a disposable, full-fidelity copy of the
production system (the same throwaway replica used for the fingerprinting fix). The copy
showed the identical two-core idle load, so we could safely capture a snapshot of exactly
what the program was doing at that instant — without touching production. The snapshot
pointed straight at the book-counting loop. Timing the health check on production then
confirmed the 5.6-second cost precisely.

## The result

After deploying the fix, an idle production server dropped from about two cores of constant
load to **effectively zero**, and the health check went from 5.6 seconds to a fraction of a
second. Freeing those two cores also leaves more headroom for the work that actually matters
— fingerprinting, duplicate detection, and imports.
