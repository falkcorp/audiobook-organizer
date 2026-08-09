<!-- file: docs/executive-summaries/2026-08-31-august-monthly-roundup-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: e7a3f109-52d8-4c6b-91f4-08b7c2d64e35 -->
<!-- last-edited: 2026-08-09 -->

# Executive Summary: August 2026 Monthly Roundup

**Period covered:** 2026-08-01 through 2026-08-09 (**month in progress** — this is
updated as work lands, not a closed record).
**Individual write-ups this consolidates:** the seven dated summaries in this directory
from 2026-08-04 to 2026-08-09, linked inline below.

This is a monthly roundup rather than a single-change summary: each theme groups an arc
of related work instead of listing changes one by one. Everything below is written for a
reader who does not work on the code.

---

## The month in one paragraph

August so far has been about **things that lied**. Not features that were missing —
numbers, controls and safety checks that were present, looked fine, and were reporting
something untrue. A running time that said 158 hours for a twelve-hour book. A library
page that told a person with forty-four thousand books that they owned none. A review
queue with 777 items where 762 said the same sentence. A test suite reporting green
while half of it was broken. The work has largely been finding out which readings could
be trusted, and fixing the ones that could not.

---

## 1. Numbers that were wrong

**[Almost every audiobook was showing the wrong running time](2026-08-04-audiobook-running-times-executive-summary.md)** (Aug 4)

Book lengths were nonsense — a twelve-hour book listed as **158 hours**, another at
**294 hours** (twelve solid days), and in the other direction a seventy-eight-minute book
listed as **4 minutes**. This was not cosmetic: running time is the number other things
divide by, so wrong lengths quietly corrupted anything derived from them.

**[Series that were really book numbers](2026-08-06-series-that-were-really-book-numbers-executive-summary.md)** (Aug 6)

A series name is supposed to name the series. Many named the series *and* the book's
position in it, jammed into one string — `Evil Genius: Book 4: Becoming the Apex
Supervillain`. Every such book was its own one-book "series", so series that should have
grouped a dozen books grouped one.

**[The outage that marked the library broken — undone](2026-08-07-the-outage-that-marked-the-library-broken-executive-summary.md)** (Aug 7)

On July 1st the machine that listens to audiobook openings was unreachable for about a
day. Nothing was lost — but the system recorded the outage as if **every book it tried
that day had personally failed**. Roughly **31,000 books, about three quarters of the
library**, were marked "transcription failed" while being perfectly fine. This is the
clearest example of the month's theme: an infrastructure problem written down as a
property of the data.

---

## 2. Controls that did not do what they said

**[A review queue you could not work](2026-08-06-a-review-queue-you-could-not-work-executive-summary.md)** (Aug 6)

The library holds back anything it is unsure about, because guessing wrong can **merge
six novels into one and delete five of them**. That queue had 777 items and **762 of them
carried the identical sentence** — "flat folder shares a title but ordering is unclear."
A queue where 98% of entries say the same thing is not a queue, it is a wall.

**[Two controls that lied](2026-08-08-two-controls-that-lied-executive-summary.md)** (Aug 8)

After any server restart the library page announced **"No Audiobooks Found"** — not
"loading", not "cannot reach the server", but a plain statement that you owned nothing.
Two decisions made it that bad: a failed request **threw away the books already on
screen**, and the page could not distinguish "the request failed" from "there is
genuinely nothing here." Those two arrive looking identical and only one has an honest
answer.

---

## 3. The safety net, and the fact that it had stopped catching

This is the longest thread of the month, and it reversed itself twice. That is worth
reading as a sequence rather than a conclusion.

**[The safety net that had stopped catching — restored](2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md)** (Aug 8) reported that the automated browser tests
were working again.

**[The safety net that was still half on the floor](2026-08-09-the-half-red-safety-net-executive-summary.md)** (Aug 9) established that this was **wrong**. The suite had
been checked with a command that hid most of its own output, against a server left
running for hours. Measured properly: **146 of 288 tests failing.** Roughly half the net
was still on the floor.

All 146 were then fixed — but the genuinely valuable output was **eleven product
regressions** the repair uncovered, including that **the library cannot be sorted from
the UI at all**, that **typing in the search box silently discards every active filter**,
and that the search box **is not debounced**, sending one full query per keystroke.

**And then a third correction.** Even at zero failures locally, the suite was still
failing on the build server — **179 failures there while showing zero on a developer
machine**. The causes were all environmental rather than product defects: image files
stored in a way the build server could not read, and two browsers competing for two
processor cores so aggressively that ordinary animations timed out.

**The outcome that matters:** as of Aug 9 the tests **block on every run** — a change
that breaks the browser suite can no longer merge. Both browsers now pass completely:
**544 tests, none failing.** That was the entire point, and until this month it was not
true.

Worth recording what the build-server failures turned out to be, because none of them
were faults in the product and two were written up as such before the evidence came in:
two browsers competing for two processor cores so hard that ordinary animations timed
out; a time limit that was fine for one browser and too tight for the slower one; and
image files stored in a way that meant the build server received a text placeholder
instead of a picture. That last one had made a check impossible to pass on the build
server since it was written.

---

## 4. Planning work: how search should be built

Not a change to the product, but a decision document written this month at the owner's
request, covering the options for search and their trade-offs.

Its central finding is worth stating: **a full search engine already exists in the
product and is running in production.** The problems people notice are almost entirely
elsewhere — the search box discarding filters, the missing debounce, and one ordering bug
where results are filtered *after* being cut into pages, which produces short pages and
wrong totals.

Two decisions were taken during the month: **sorting moves to the server**, and "the
system should not suck" is the bar. A follow-up section analyses what that costs, and
records an open question that has to be settled first — **the code disagrees with itself
about how many books the library holds**, and the answer changes the design.

---

## Themes worth carrying into next month

1. **A silent fallback is worse than a loud failure.** The transcription outage, the
   empty library page, and the search index's "run without search if it cannot open"
   behaviour are the same shape: something degrades, nothing says so, and the result is
   indistinguishable from normal.
2. **A measurement is only as good as the thing it ran against.** The test suite reported
   green three separate ways this month — hidden output, a stale server, and a
   developer machine that was not the build server. Each looked like evidence.
3. **Being wrong on the record is fine; leaving it there is not.** Three summaries this
   month correct earlier ones. That is the system working.
