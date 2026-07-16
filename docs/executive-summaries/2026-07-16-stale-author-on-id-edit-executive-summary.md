<!-- file: docs/executive-summaries/2026-07-16-stale-author-on-id-edit-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b1e7c92-8a53-4f06-b2d9-6e0a1f3c7d84 -->
<!-- last-edited: 2026-07-16 -->

# Executive Summary: Editing a Book's Author by ID Left the Old Author Name Showing

**Shipped:** July 16, 2026. A follow-on to the July 13 author/series data-loss
fix — the same part of the code, but the opposite failure.

## Executive Summary

When you changed a book's author (or series) by picking a different one, the
book's record kept the change to *which* author it pointed at — but the **name it
displayed stayed the old one**. So a book could point at "New Author" internally
while every screen still showed "Old Author," with no error and no sign anything
was off. We fixed it at the point of the edit and added a test that reproduces the
old wrong behavior and proves it's gone.

## What was wrong, in plain terms

Every book stores two things about its author: an internal reference (which author
it belongs to) and a saved copy of the author's name, kept alongside the book so
the library can list thousands of books quickly without looking each author up one
by one. Those two are supposed to always agree.

When an edit changed the author by its internal reference, the code updated the
reference but forgot to refresh the saved name next to it. The save routine has a
safety rule that protects the saved name from being *accidentally blanked out*
(that was the July 13 fix) — but that rule only kicks in when the name is missing
entirely. Here the name wasn't missing; it was simply *the old one*, so the safety
rule left it alone and the stale name was written to storage.

Because the app trusts that saved-alongside name whenever it's present, the book
then showed the previous author everywhere until something else happened to rewrite
it. The same problem applied to a book's series.

## What we changed

The edit now refreshes the saved author and series names to match whichever author
and series were chosen, and it does so **before** the record is written — so the
stored copy is correct, not just the reply the screen happens to show right after
saving. As a bonus, because this runs on every edit, any book that already had a
mismatched name quietly corrects itself the next time it's touched.

## Why it matters

Author and series names are how a library is browsed and searched. A book that
internally belongs to one author but visibly shows another is confusing, looks like
lost data, and is hard to trace — the internal reference and the visible label
disagree with no error to point at the cause. This change keeps the two in step at
the moment of the edit, is covered by a regression test that checks what was
actually saved to the real database (not just what the screen showed), and can be
rolled back as a single change.
