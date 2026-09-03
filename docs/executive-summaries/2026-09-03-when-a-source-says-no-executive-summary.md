<!-- file: docs/executive-summaries/2026-09-03-when-a-source-says-no-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 82d093cc-9210-46ea-9c4e-ef3fab0d3a54 -->
<!-- last-edited: 2026-09-03 -->

# When a source says no

**Pull request:** PR_URL_PLACEHOLDER

## Executive Summary

- The app looks up book details from outside services — Audible, Google Books, Open
  Library, Hardcover and others. Those services all have limits. Some cap how many
  questions you may ask per day, some per minute, and any of them can simply be down
  for an hour.
- Until now the app had one response to all of those, and it was the wrong one for
  most of them: wait thirty seconds, then try again. Thirty seconds is a sensible
  pause for a service having a bad minute. It is a useless pause for a service that
  has told you "that is your allowance for today" — you will spend the rest of the
  day asking a question you already know the answer to, and asking repeatedly is
  itself a good way to get blocked harder.
- On 3 September that is exactly what happened. A run to fetch details for 22,934
  books had to be abandoned. Google Books' daily allowance was gone, the app noticed
  and backed off 635 times in twelve minutes, and every one of those back-offs ended
  with it trying again anyway. Nothing was learned. Nothing was fetched. Every book
  the run reached got a failure recorded against it that said nothing about the book.
- Two things were wrong, and they compounded. The app never read the refusal, only
  its number — so "you have used your allowance for today" and "you are going a bit
  fast" arrived as the same event. And a blocked source stayed on the list of places
  to ask, so the run kept working through the library asking a source that was never
  going to answer.

## What changed

- **The refusal is read, not just counted.** The app now keeps what the service
  actually said. That is what tells a day-long block apart from a fifteen-second one.
- **The pause fits the problem.** A spent daily allowance is left alone for four
  hours. A rate limit, fifteen minutes — or longer if the service names a time. A
  rejected password or key, six hours, because retrying cannot fix it and a human
  needs to look. A service that is down, thirty minutes. A network hiccup, five.
- **A blocked source is taken off the list.** It is not called and failed once per
  book; it is not called. And if every source is blocked, the fetch **refuses to
  start** and tells you which ones are blocked and for how long. That is the change
  that matters most: it turns "22,934 books marked with a failure that was not about
  them" into "nothing touched, and here is why".
- **The pause survives a restart.** The app restarts often. Previously every restart
  forgot the block and started asking again from scratch.
- **You can override it.** There is a reset — for one source or all of them — for when
  you know the situation has changed. And looking up a single book yourself always
  goes through regardless; if it works, the block lifts on its own, so a source that
  recovered early does not sit idle waiting out a timer.

## What this does not do

- It does not get the allowance back. The Google Books daily quota was still
  exhausted when re-tested after the Pacific-time reset, so something else is
  consuming that project's allowance and that is a separate thing to chase.
- It does not add a screen. The information is available to the interface, but
  building the panel is separate work.
