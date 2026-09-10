<!-- file: docs/executive-summaries/2026-09-10-the-data-we-were-throwing-away-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c7f4a91-6b83-4d05-9e12-7a4c8f3b6a05 -->
<!-- last-edited: 2026-09-10 -->

# The data we were throwing away

**Pull requests:** #3176 (ASIN gate), plus the metadata-signal-widening PR.

## Executive Summary

- We are trying to re-connect tens of thousands of audiobook files that lost track of
  where they live on disk. The plan is to identify each file by its **content** — its
  length, its chapters, what its narration says — instead of just its filename. That
  only works if we actually keep the facts we can find about each book.
- We discovered we were **fetching useful facts from the book databases and then
  throwing them straight in the bin**, because the place we copy them into simply had
  no slot for them. This fixes that.
- What we now keep, that we used to discard:
  - Whether an edition is **abridged or unabridged** — a big deal, because the two are
    different lengths and have different chapters, and telling them apart is exactly the
    kind of thing that makes matching reliable.
  - The book's **runtime** from two of our sources that were reporting it all along
    while we ignored it.
  - **Both** kinds of ISBN (the 10-digit and the 13-digit). We were keeping one and
    dropping the other, even though both are useful identifiers and we had room for both.
  - A book's place in a **second series**, its **subtitle**, its **page count**, and a
    series position like "1.5" that our whole-number field couldn't hold.
- Separately, we fixed a gate that was quietly stopping a background job from looking up
  Audible IDs for the majority of books. It used to decide a book was "done" the moment
  it had any ISBN, so a book with an ISBN but no Audible ID never got one. Now it fetches
  the missing identifier. The Audible ID is the key that unlocks chapter-level data for a
  book, so this directly feeds the matching effort.

## Why it matters

None of this changes what you see on a book today, beyond filling in identifiers that
were blank. It is groundwork: the more true facts we hold about each file, the more of
the disconnected files we can confidently reunite with their books — automatically, and
without guessing.
