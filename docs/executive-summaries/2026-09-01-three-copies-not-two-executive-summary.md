<!-- file: docs/executive-summaries/2026-09-01-three-copies-not-two-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e91d7b2-5c08-4a36-9f14-7b2e8a0c6d53 -->
<!-- last-edited: 2026-09-01 -->

# Three copies, not two — and the guard that let the bad one through

**Pull requests:** [#3033](https://github.com/falkcorp/audiobook-organizer/pull/3033),
[#3034](https://github.com/falkcorp/audiobook-organizer/pull/3034) and
[#3035](https://github.com/falkcorp/audiobook-organizer/pull/3035), all merged.
Follows on from [#3029](https://github.com/falkcorp/audiobook-organizer/pull/3029),
summarised in "Five copies of one question, and none of them right".

## Executive Summary

- **Searching the review queue only searched what was on screen.** The queue holds 728
  items waiting for a decision, but the page could only hold 500 of them, so typing a
  title searched those 500 and silently missed the rest. About 214 items could not be
  found by typing at all, and the category filter did not help because they are all the
  same category. Searching now happens on the server, over the whole queue.
- **That search now looks inside each item's details, not just its headline.** You can
  find a pending item by typing part of any of the individual audio files it groups
  together — the most natural thing to search for, and previously the least likely to
  work.
- **The code that reads an author's name out of a folder path existed in three separate
  copies, and everyone believed there were two.** Two of them have now been merged into
  a single shared version. The third was found only during review of that merge.
- **The third copy was handing back the app's own "Unknown Author" placeholder as if it
  were a real person's name** — and doing so with high confidence. Every book the app
  files without a known author is stored in a folder literally named "Unknown Author",
  so this copy read that folder name straight back out and treated it as the author.
- **The safety check that should have caught this was skipped precisely because the value
  was wrong.** The check asked "is the author missing?" and only then tried to recover.
  A wrong author is not a missing one, so the recovery step — which would have thrown the
  placeholder away — never ran.
- **A duplicated helper for tracking where each piece of metadata came from was also
  merged into one copy**, along with a small helper it depended on that would otherwise
  have quietly become a *third* copy of itself.
- **Two claims written during this work turned out to be false and were corrected.** One
  was a comment stating that no duplicated copies remained; the other was a test whose
  description promised it would catch a failure it demonstrably could not.

Verified against production: the new whole-queue search returns the correct single match
for a term that appears only inside an item's file list, and correctly returns nothing
for a term that only appears as an internal field label.

---

## Searching the review queue only covered the visible page

**What it was.** The review queue is the list of books the app wants a human decision on.
The screen fetched a batch of them — up to 500 — and the search box filtered that batch in
the browser. The queue currently holds 728 items.

**Why it mattered.** Roughly 214 items were unreachable by typing. There was no error and
no warning; the search simply reported fewer results than existed, which reads as "that
book isn't in the queue" rather than "that book is in the queue but off-screen". The
category dropdown was no help, because 714 of the 728 items share a single category.
Worse, the on-screen count was taken from the fetched batch, so it confidently displayed a
total that was wrong.

**The fix.** Search moved to the server, which now filters the entire queue before
counting or paging. The panel's counts were reworked to stay honest once the number shown
and the number in the queue can differ.

## The search could not see inside an item's details

**What it was.** Each queued item carries a structured record — the folder involved, the
reason it was flagged, and the list of individual audio files it proposes to group
together. Search only looked at the item's one-line headline.

**Why it mattered.** The single most likely thing someone types is part of a filename, and
that was exactly what could not be matched.

**The fix.** Search now reads the whole record, including nested lists. One detail worth
naming: it deliberately matches only the *values* in that record and never the internal
field labels. Matching raw text would have meant that typing a common label like "folder"
returned every item in the queue, since every record contains that label.

## The path-to-author reader existed in three copies

**What it was.** Working out an author's name from a folder path is done by code that had
been copied across the app. Two copies were known about and had visibly drifted apart. A
third — sitting in the same area as one of the two, but written differently enough that
searching by name would never find it — was not known about at all.

**Why it mattered.** A correction applied to one copy is not a correction to the others.
This has already caused a real problem once before: a previous fix to the "Unknown Author"
handling was applied to a single copy and had no effect at all, because a different copy
ran first and had already decided the answer.

**The fix.** The two known copies were merged into one shared version, in the module that
already owns the "Unknown Author" concept. Before merging them, both were run over a
28-path test set and compared: they disagreed on exactly one input, which was far less
divergence than their appearance suggested. That test set is now kept as a permanent test
rather than a one-off measurement.

The third copy's differences in *judgement* — it accepts names up to five words where the
shared version stops at four, and it rejects any name that does not begin with an English
letter — have deliberately **not** been changed yet. Aligning them would change real
answers for real libraries, including non-English author names, so it needs its own
careful comparison first and is recorded as follow-up work.

## The placeholder was being stored as a real author

**What it was.** When the app cannot determine an author, it files the book under a folder
named "Unknown Author". The third copy of the path reader had no rule excluding that name,
so it read it back out and reported it as the author — with its highest confidence rating.

**Why it mattered.** The app then created or attached a genuine author record named
"Unknown Author" and assigned the book to it, exactly as if a person had been identified.
Production already carries two such records, one with over two thousand books attached.
Books in this state were still offered to the automatic re-reading step, so this cost
accuracy and cleanliness of the author list rather than losing anything outright.

**The fix.** The placeholder name was added to that copy's exclusion list, and a test now
drives the real reader over the app's own folder layout to confirm it. The test is paired
with a second one confirming a genuine author is still read correctly, so it cannot pass
by simply returning nothing.

## A safety check that a wrong value walks straight past

**What it was.** After reading metadata, the scanner runs a recovery step for books whose
author is still blank. The step that discards the "Unknown Author" placeholder lives
*inside* that recovery step.

**Why it mattered.** This is the mechanism that let the problem above survive. If the
author is blank, recovery runs and the placeholder is discarded. If the author is the
placeholder — that is, present but wrong — the recovery step is skipped, and so is the
discard. Being wrong in one specific way allowed a value to avoid the check for being
wrong. No test detected this, because every test that reached the check supplied a blank
author, and a blank author cannot demonstrate the problem.

**The fix.** The placeholder is now rejected earlier, at the point the folder name is
read, so it never reaches the check in the first place. The broader pattern — that a
"is it missing?" test is not a substitute for "is it correct?" — has been recorded so the
same shape is recognised next time.

## Two statements that were written and were not true

**What it was.** During this work, a comment was added asserting that no duplicated copies
of the path reader remained, and a test was added whose description listed three kinds of
future change it would catch.

**Why it mattered.** The comment was written before the third copy was found, and a
confident statement that a problem is solved is the single most effective way to stop the
next person from checking. The test's description was worse: of the three failures it
claimed to catch, the one this very change introduced was the one it could not see, which
was confirmed by deliberately breaking the code and watching the test pass anyway.

**The fix.** Both were corrected to describe what is actually true, including the limits.
The test's real value — confirming the placeholder never reaches the caller, regardless of
which internal layer prevents it — is now stated accurately, and the check it cannot see
is covered by a separate test that can.

## Duplicated metadata-provenance code

**What it was.** The code that records where each piece of metadata came from — a manual
override, a stored value, or an online lookup — existed as two identical copies. One of
them had no callers at all and was kept alive only by its own test.

**Why it mattered.** Low immediate risk, but it is the condition that produces every
problem described above: two copies stay identical only until someone edits one.

**The fix.** Merged into one shared version. Notably, the merge nearly created a *new*
duplicate: the code depended on a small helper that also existed twice, and moving the
main function without noticing would have produced a third copy of the helper — the exact
outcome the work existed to prevent. Behaviour was confirmed unchanged by capturing the
function's output across six scenarios before the move and comparing byte-for-byte after.
