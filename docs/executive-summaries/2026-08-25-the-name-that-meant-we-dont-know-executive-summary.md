<!-- file: docs/executive-summaries/2026-08-25-the-name-that-meant-we-dont-know-executive-summary.md -->
<!-- version: 2.0.0 -->
<!-- guid: c41f8b7d-3e29-4a65-8d10-9f2b6e0a35c7 -->
<!-- last-edited: 2026-08-25 -->

# The name that meant "we don't know"

**2026-08-25 — the app wrote "Unknown Author" to mean *we could not work this out*,
then read it back and believed it**

**PR:** `fix/unknown-author-is-not-an-author` — the fix, nine tests, the measured
diagnosis, and this report.

---

## The short version

- When the app files a book whose author it cannot work out, it puts the book in a
  folder called **"Unknown Author"**, and because the naming scheme includes the
  author, the name ends up in the **filename** too. An organised book with no known
  author is literally called `Some Title - Unknown Author.mp3`.
- That is fine on its own. The problem is what happened on the **next scan**.
- The scan reads information out of filenames. It looked at
  `Some Title - Unknown Author.mp3`, split it, and recorded **"Unknown Author" as
  the author** — indistinguishable, from that point on, from an author a person had
  typed in themselves.
- The app has a feature that finds books with a missing author and fills it in
  automatically. It decides which books need help by asking a simple question:
  *does this book already have an author?* For these books the answer was now
  **yes**.
- So the one group of books that most needed an author was the exact group the
  repair feature skipped. **They could never be fixed.** Not by rescanning, not by
  waiting, not by turning anything on.
- Thousands of books in the library are in this state. The exact count is given
  below, and getting to a number we trust took three attempts.

## Why it would not have fixed itself

This is the part worth dwelling on. Most problems of this kind clear up on their
own once the underlying cause is fixed — you re-run the job and it catches up.

This one could not, because the app had **overwritten its own record of not
knowing**. "We could not determine the author" and "the author is a person named
Unknown Author" were stored identically. Once that happened there was no
information left anywhere in the book's record to distinguish them, so no amount of
re-running would have singled these books out. The knowledge that something was
missing had itself gone missing.

It was also **self-sustaining**. Each pass through the system reinforced it: the
name went into the folder, the folder name went into the filename, the filename
went back into the database, and the database entry told the repair feature to stay
away.

## What changed

- The scan now **recognises "Unknown Author" as the app's own marker** rather than
  as a person, and does not record it as an author. The book's title is still read
  from the same filename, as before.
- The "does this book need an author?" check now **treats the placeholder as no
  author at all**, so these books are offered to the automatic repair again.
- Crucially, this had to be done in **two** places. The app has two separate
  copies of the code that reads names out of file paths, and the first version of
  this fix only corrected one of them — the one that almost never runs. The
  automatic repair would have been invited to look at the book, produced the right
  answer, and had it **discarded on arrival**, because the placeholder was still
  sitting in the author field. A review pass caught that before it shipped.
- The name itself had been written out separately in three different parts of the
  codebase that could not see one another. It is now defined **once**, so the three
  cannot drift apart and reopen this.

## What this does not fix

Two things, stated plainly so they are not mistaken for solved:

1. **The books already in this state are not repaired by this change.** The change
   stops new books falling in and re-opens the door so the repair feature will
   consider them. Actually correcting them is separate work, still to be agreed.
   Encouragingly, for at least some of them the app **already stores the correct
   author elsewhere in the record** — on 80 confirmed books it holds
   "Terry Pratchett" in one field while the field the repair check consults says
   "Unknown Author" — so that repair may need no outside lookup at all.
2. **Re-opening the door is not the same as walking through it.** The scan skips
   files it has already seen and recorded as unchanged, and that skip happens
   *before* the "does this book need an author?" check. So an affected book is
   only reconsidered when its file changes or a full rescan is run — not on the
   next ordinary scan.
3. **A separate, unrelated outage was found while investigating.** The machine that
   does the automatic author lookups was unreachable from the server — powered off
   or off the network. That is why recent scans logged timeouts. It needs to be
   switched back on; no code change affects it. Books missed purely because of that
   outage were **not** damaged and will be picked up on the next scan.

## How we know

The numbers come from the live library, using a filter that was checked first: a
deliberately nonsensical search returns 0 and an unfiltered one returns all 61,412
books, so the filter genuinely filters. An earlier draft of the investigation led
with a much larger figure, 25,304; that was checked and **withdrawn**, because most
of those books turned out to have correct authors and merely to be sitting in the
old folder. The figure that survived scrutiny is 3,407.

Each of the three code changes was then verified by deliberately re-introducing the
original bug and confirming the new tests fail — including one attempt that had to
be redone because the first version of the broken code would not compile, and a
test that cannot compile proves nothing.
