<!-- file: docs/executive-summaries/2026-08-13-the-books-search-could-not-see-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d915a69-5d11-4e2a-93fd-bdc1f27e70f1 -->
<!-- last-edited: 2026-08-13 -->

# Executive Summary: The Books Search Could Not See

**Date:** 2026-08-13
**In one line:** searching the library missed most of the recently added books, because
the thing that builds the search catalogue treated "I have started" as "I am finished".

---

## What you saw

You searched the library for *All Jobs and Classes* and got five books back, none of
them the one you wanted. The same search in the phone app returned the right ones. That
split is what made this findable — and it turned out to be the whole story.

## What was actually happening

The library keeps a separate catalogue for searching — think of a card index sitting
next to the shelves. Searching does not walk the shelves; it reads the cards.

**Three books on the shelf had the title you searched for. Only two had cards.** The one
without a card was the copy the library page actually shows you. The page hides
duplicate copies of the same book and shows you the main one — and the main one was
exactly the one with no card. So the search found nothing it was willing to display, and
what you were left looking at was five other books whose *plot summaries* happen to
mention a job and a class. They were never wrong answers to a badly-ranked search; they
were the only answers that survived.

The phone app returned the right books because it does not hide duplicate copies. It saw
one of the two that did have cards.

## Why the cards were missing

The catalogue gets built in one pass, oldest book first. If the server shuts down
part-way through, the pass stops where it is.

The problem was what happened next. On the following start-up the program asked one
question — *"is the catalogue empty?"* — and, finding it not empty, concluded it was
finished and never went back. A half-built catalogue and a complete one looked identical
to it. Because the pass runs oldest-first, the books that never got cards were always
the **newest** ones.

We measured it on the live server. Books added in April: 97 out of 100 findable. Books
added in August: **2 out of 100**. Almost everything added recently was invisible to
search, and would have stayed invisible indefinitely — nothing in the system was ever
going to notice.

There was already a repair mechanism, added earlier this month, and it did not help
here. It repairs cards that were *dropped* when the system got busy. A book whose card
was never attempted in the first place was never on its list.

## What changed

On start-up the server now compares how many cards it has against how many books exist.
If it is short, it adds the missing books to the existing repair list and lets the
already-tested repair process work through them. Deliberately that way round: rebuilding
the catalogue in one pass is what created a permanent hole the first time, and the repair
list is written to disk, so an interrupted repair picks up where it stopped instead of
starting over or giving up.

Separately, search had three ways of quietly falling back to a much simpler, weaker
matching method, none of which wrote anything to the log. Diagnosing this required
probing the live server to work out which method had answered — something a single log
line should have told us. All three now say so.

## What this does not fix

Two other genuine problems surfaced while investigating. Neither caused this one, and
both are written up rather than fixed here, because bundling unrelated fixes into a bug
investigation is how fixes get harder to trust:

- The words **"all"** and **"and"** are silently discarded from searches. Typing
  *All Jobs and Classes* actually searches for *Jobs* and *Classes*. There is a good
  reason it works this way, but you are not told half your query was dropped.
- **Putting quotes around a phrase does not search for that phrase.** It currently
  appears to work by accident.

One decision is yours: the repair runs on the next restart and will work through roughly
40,000 books in the background. It can happen on the next natural restart or be
scheduled deliberately.

## How we know it is fixed

The repair has a test that reproduces the exact failure — a catalogue built half-way,
then a restart — and checks that the missing books become findable, that the right book
comes back **first**, and that the five unrelated ones are **absent**. The test was
confirmed to fail when the fix is switched off, which is the only thing that proves a
passing test is measuring anything at all.
