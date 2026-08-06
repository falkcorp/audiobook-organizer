<!-- file: docs/executive-summaries/2026-08-06-series-that-were-really-book-numbers-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b06e2f4-1c57-4a93-b2e8-40d19f7c6a15 -->
<!-- last-edited: 2026-08-06 -->

# Series that were really book numbers

## What was wrong

A series name is supposed to name the *series*. Many in this library named the series
**and** the book's number in it, jammed together into one string:

- `Evil Genius: Book 4: Becoming the Apex Supervillain`
- `Legend of Drizzt Book 14 - The Sellswords`
- `Vampire Hunter D: Vol 09: The Rose Princess`

The consequence is that a six-book series stops being one series. It becomes six
separate one-book series, because `Evil Genius: Book 4` and `Evil Genius 6` are
different strings, and nothing in the system knew they were the same shelf. Browsing by
series showed you fragments instead of series, and nothing downstream could tell that
those books belonged together.

The library already handled the simplest version of this — a number at the *end*, like
`Discworld 05`. The rest had never been touched.

## Why it had never been touched

Because the same shape means opposite things, and the two cases are indistinguishable
by looking at the text.

`86—EIGHTY-SIX` is a **real series title**. The "86" is the name. This library holds
seventeen books in it.

`08. Battle for the Abyss` is a **real book number**. The "08" is position 8 in the
Horus Heresy.

Both are a number, then a separator, then words. Any rule confident enough to fix the
second one will destroy the first — scattering seventeen books into a series called
"EIGHTY-SIX" and deleting the real name. That is not recoverable by re-running
anything, because the original name is gone.

## What was done

Rather than write one clever rule and hope, each pattern was **scored by how much the
name itself vouches for the number**:

- **A keyword vouches for it** — `Book 4`, `Vol 09`, `Part 2`. Words like that do not
  appear by accident. These are safe to fix automatically.
- **Brackets mark it** — `Dragon Born [04]`. Suggestive, but nothing confirms it.
  Requires someone to explicitly opt in.
- **A bare number** — `08. Battle for the Abyss`. Reported for a human to look at, and
  **cannot be applied at all**, by construction. There is no setting that turns this
  on, because there is no amount of tuning that separates it from `86—EIGHTY-SIX`.

Names offering *two* candidate numbers — `The Demon Wars Saga [07] Immortalis [02]` —
are refused outright rather than guessed at. Picking the wrong one writes a wrong
number *and* a wrong series name.

Before anything is changed, the system now writes a full list of every proposed change
to a file. Fixing a series creates and deletes rows, so there is no "undo" button —
that file is the only way back, and the operation refuses to run if it cannot write it.

## What actually changed in your library

**25 fragmented series were folded back into 21 real ones, and 52 books got a proper
book number.** No failures. Re-running the check afterwards confirmed the work was done
and nothing was left half-finished.

The other 664 candidates were deliberately left alone and written to a report.

## The thing worth knowing about

The cautious setting paid for itself immediately.

Checking the "brackets mark it" group — the 198 that *would* have been changed under
the less careful reading — showed that most of them were not series at all. They were
**single books that had been shattered into pieces**, with the piece number ending up in
the series field:

- One Megan E. O'Keefe novel had become **80 separate rows**, numbered (1) through (80).
- One Jill Santopolo novel had become **25 rows**.
- Sixty-three more rows turned out to be *web page titles* from a Scribd scrape, with
  the page number in brackets.

Applying that group would have created an eighty-volume "series" out of a single novel.
It was stopped by defaulting to the stricter of two readings — and by looking at the
report before pressing the button rather than after.

Those ~180 books have a real problem, but it is a different one: they need reassembling
into single books, not renaming. They are now recorded as their own piece of work.
