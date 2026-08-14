<!-- file: docs/executive-summaries/2026-08-14-the-series-that-vanished-from-under-13322-books-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4d81f2ba-6c39-4e17-8a52-9f0b3c7e15d8 -->
<!-- last-edited: 2026-08-14 -->

# Executive Summary: The Series That Vanished From Under 13,322 Books

**Date:** 2026-08-14
**In one line:** A weekly tidy-up job was deleting series that still had books in them,
and 13,322 books ended up filed under a series that no longer exists.

---

## What you saw

Books with no series. Not an error, not an empty shelf — the book is there, it plays, its
title and author are right, and the series line is simply blank. And a library with far
more series than seemed possible: 14,626 series for 31,163 books.

## What was actually happening

There is a weekly job whose whole purpose is to delete series nobody uses any more. Before
deleting one, it asked a reasonable-sounding question: *how many books are in this series?*

The problem is which counter it asked. That counter was built to fill in the number you see
next to a series on the website, so it deliberately skips two kinds of book:

- books you have moved to the trash, and
- alternate versions of a book you already own (the duplicate copies the library hides
  behind the main one).

For a badge on a web page, skipping those is exactly right. As the test for *"is anything
still pointing at this series?"* it is dangerous, because those books **are** still pointing
at it. A series whose books were all trashed, or all alternate versions, counted as zero.
The job deleted it. The books survived — still carrying a reference to a series that had
just been erased.

By the time we measured it, books in the library referred to **21,190 different series, and
only 14,626 of them existed**. The missing 6,893 were held by 13,322 books still in the
library, plus another 702 in the trash.

## The second problem: the repair looked like it did nothing

The fix went out, and the job was re-run. It reported plainly:

> 17 duplicates merged, 326 orphans deleted, 0 errors

Then the series page was checked — and showed the same 14,629 series it had before, with the
same 329 empty ones. Nothing had changed.

Everything *had* changed; the page was reading from a stored copy. The series list is cached
for a full day and rebuilt when the server starts, and until now only the website's own edit
buttons knew to throw that copy away. A background job could merge and delete all it liked
and the page would keep showing yesterday's list.

That is the worst possible failure to have while repairing data, because *"the repair worked
and you can't see it"* and *"the repair silently did nothing"* look identical from the
outside. The same fault was found and fixed in the author list four hours earlier; this is
the same fault, one shelf over.

## What was fixed

- **The counting.** The delete check now counts every book pointing at a series, whatever
  state it is in. If it cannot get that number, it refuses to delete anything rather than
  guessing — the wrong guess is what caused this.
- **Proof it works.** After the fix, the job deleted 326 genuinely empty series and *kept*
  "Queen of Fire: A Raven's Shadow Novel", whose only book is in the trash. That is precisely
  the row the old code would have destroyed.
- **The stale page.** The three background jobs that create, rename, merge or delete series
  now clear the cached list — but only when they actually changed something, so a job that
  had nothing to do doesn't throw away a good copy for nothing.

## What is not fixed yet

The 13,322 books whose series was deleted still show no series. We can tell **which books
used to belong together**, because they still carry the same reference — but the series
*names* are gone for good. They were never stored on the book itself, the deletions were not
recorded in the operation history, and the folder names on disk don't contain them either.

The deliberate decision was to **leave those references alone rather than erase them**. They
are not doing any harm, they still record which books were grouped together, and when the
library next reads a series name from a file's own tags it will overwrite them correctly.
Erasing them would throw away the grouping and gain nothing.

One thing remains open, though it turned out to be smaller than it first looked. The damage
did not arrive gradually — it came in bursts, on ten separate days, and the biggest single
day was **2026-08-11, when 5,367 books appeared carrying references to series that don't
exist**. That looked at first like something actively breaking new books.

It isn't. Every one of those books points at a series that was **already** broken before
that day — 5,068 series references, and not one of them was new. The last time a genuinely
new broken reference appeared was **19 July**. What happened in August was copying, not
breaking: something split existing books into per-chapter pieces (hence titles like
*Chapter 06*), and each new piece inherited the broken series reference from the book it
came from.

That also rules out ordinary scanning as a cause, for a simple reason: when a scan reads a
series name off a file, it looks the series up **by name** and creates it if it's missing.
It can't end up pointing at something that isn't there. Only copying an existing record can
do that.

So nothing new is being broken. What's left is to stop the copying from spreading the old
damage — the places that duplicate a book's record should drop a series reference that no
longer points anywhere, rather than passing it on.

## Why it took so long to spot

A book with a blank series looks like a book whose file never had series information — which
is common and normal. There was nothing to distinguish "we never knew" from "we knew and
deleted it". The damage was only visible by counting the series the books *refer to* against
the series that *exist*, which nothing did until now.
