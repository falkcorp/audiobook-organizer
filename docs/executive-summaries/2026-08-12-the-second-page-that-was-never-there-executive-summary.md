<!-- file: docs/executive-summaries/2026-08-12-the-second-page-that-was-never-there-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f21c8d4-77ab-4e59-9c02-5db8e6a1f470 -->
<!-- last-edited: 2026-08-12 -->

# Executive Summary: The Second Page That Was Never There

**Date:** 2026-08-12
**Change:** PR #2326
**Written for:** anyone who uses the audiobook organiser, not the people who build it

---

## In one paragraph

Searching your library was broken, and it had been broken for as long as the current
search has existed. You would type something in, get a handful of results, click through
to the second page — and find **nothing at all**. Not "no more results": an empty page,
every time, for every search. The number shown next to your results was wrong too. It was
never the number of matches; it was just however many rows happened to fit on the page you
were looking at.

---

## What you would have noticed

Search that seemed to *almost* work. The first page came back, so search was clearly
functioning. But it always looked like your library barely contained what you asked for —
five results, six results — and going to page two gave you a blank screen. The obvious
conclusion is that there was nothing more to find, so most people would simply stop there
and believe the library was thinner than it is.

The count made this worse rather than better. Because it reported the size of the page
rather than the size of the result set, asking for 5 results "found" 5, and asking for 250
"found" 249. Any figure you read was really a description of what you had asked for, not
of what existed.

---

## What was actually wrong

Two mistakes on the same path, both about **doing things in the wrong order**.

**The filtering happened after the page was cut.** The search engine was asked for one
page — say five books. Only *then* were your filters applied to those five, throwing most
of them away, with nothing left to top the page back up. So a page of five became a page
of one. Then the code tried to take "page two" of what remained — but it had one book
left, and page two of one book is nothing. That is the empty page, and it is why the
problem got worse the further you paged rather than better.

**The count was measured after the damage.** It reported how many rows survived on the
page in front of you, which is only ever the right answer when everything happens to fit
on one page — which is exactly the case you would try when testing casually.

The library screen always applies one filter automatically, whether or not you choose
anything. That is why this was not an edge case affecting unusual searches. It was every
search, for everyone, always.

---

## What was done

The order was reversed. The search now collects the matches first, applies your filters to
the whole set, and cuts the page **last** — so page two contains the sixth through tenth
results, as anyone would expect. The count is now the real number of matches and no longer
changes when you ask for a different page size.

One detail is worth mentioning because it says something about how the bug survived. The
correct version of this logic **already existed a few lines away**, guarding a different
branch of the same function, with a comment describing precisely this failure. One route
through the code had been fixed; the neighbouring route had not. The repair was modelled on
the version that was already right.

---

## How we know it is fixed

Measured on the live server before the change, searching for "honour" with the standard
filter, five per page:

| page | results before | results after |
|---|---|---|
| 1 | 1 | **5** |
| 2 | **0** | **1** |
| 3 | 0 | 0 |

There are six matching books, so five on the first page and one on the second is exactly
right. The reported count is now **6 regardless of page size** — it reads 6 whether you
ask for 1 result or 250. Before, the same query reported 5, 3 and 21 depending purely on
what you asked for.

---

## What this does not fix

**Search still returns loosely related results.** A multi-word search behaves closer to
"any of these words" than "all of these words", so a two-word title can pull in a great
many books that match only one of the words. That is a separate, still-open problem, and
this change does not touch it.

**Very large result sets report an approximate count.** Beyond ten thousand matches the
number shown becomes a lower bound rather than an exact figure. The system records when
this happens, so it is visible rather than silent.

---

## The uncomfortable part

Every earlier attempt to judge how well search *worked* was made using this broken
counting. Conclusions were drawn about search quality from result counts that were really
just measuring the page size — and at least one written assessment had to be withdrawn
because of it.

The general lesson: **a broken measurement does not look broken.** It returns a plausible
number, on time, every time. The count was never blank or obviously wrong — it was simply
answering a different question from the one being asked, and nothing about it announced
that. It is worth more scepticism when a number looks reasonable than when it looks absurd,
because the absurd one gets investigated and the reasonable one gets believed.
