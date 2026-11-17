<!-- file: docs/executive-summaries/2026-11-17-metadata-title-check-executive-summary.md -->
<!-- version: 2.1.0 -->
<!-- guid: 3b9d6e21-8c47-4f05-a1d2-6e0f7a4c9b83 -->
<!-- last-edited: 2026-11-17 -->

# Two parts of one story can no longer be mistaken for each other

**Date:** 17 November 2026. A fix to the automatic metadata apply, found while
reviewing how it compares titles, and rebuilt after review of the first
attempt.

## Executive Summary

- When the library applies information found online to a book (description,
  cover, series, narrator, and so on) without asking, it first checks that the
  found book really is the book in the library. One of those checks compares
  titles. It was too generous: it counted two titles as the same whenever one
  piece of them matched, for example the words before the colon.
- That meant two parts of one story, or two stories of one franchise, could be
  taken for each other. If the recording lengths were close and the author
  matched, the information for "Hush: Part 2" was written onto "Hush: Part 1"
  (and the other way round), and "Batman: The Long Halloween" onto "Batman:
  Dark Victory". The books looked fine in the library but carried the wrong
  description and cover.
- The first fix still let some of these through, because it decided two titles
  were "the same" by removing text it considered unimportant (a series name, a
  credited name, a number written as words) and every removal was a new way for
  two different books to look alike. It is replaced by one rule that removes
  nothing.
- The title check now gives one of three answers:
  - **Same.** The whole title of one side equals the whole title of the other
    after only harmless differences are ignored: capital letters, accents, "and"
    against "&", a leading "The", "(Unabridged)", numbers written as digits,
    words or roman numerals. A series name in front of the title counts when the
    series is known. The file name counts too, because it is how the book is
    stored. Nothing else makes two titles the same: not a series name, not an
    author or narrator name, not a year.
  - **Different.** The titles share a whole piece and each has a piece of its
    own ("Batman: Dark Victory" and "Batman: The Long Halloween"), or they use
    the same words with different numbers ("Part 1" and "Part 2", "Night of 7
    Owls" and "Night of 5 Owls"). The match is refused. A series name, a
    credit, a file name or a year cannot talk the check out of this.
  - **Neutral.** Everything else that is related: one title simply says more
    than the other ("Hush" and "Hush: A Gotham Story",
    "Hush" and "Hush: Part 2", or a stored title that starts with a list number
    such as "08 - The Long Halloween"). A neutral title confirms nothing by itself, so
    it is applied on its own only when the recording lengths also agree, both
    known and within 5%. Without that it waits for review, whatever else
    matches (author, narrator, ASIN or the audio intro). If the author is the
    only other thing that agrees, the book is reported as lacking evidence; if
    two other things agree, it is reported as "title only partly matches and the
    runtime does not confirm it". An owner review can still approve either.
- The same title check also guards the automatic metadata upgrade that runs
  without anyone asking (`metabatch/upgrade.go`), so that path is stricter in
  exactly the same way.
- The same weakness existed in the path for books whose own title is a
  placeholder ("Chapter 3", or empty) and that are found by the audio intro. The
  intro only repeated the library's own search words, yet it was counted as proof
  the title matched, and it could even override a real disagreement between the
  titles. It no longer counts as proof: a book found this way is applied on its
  own only when the recording lengths agree, and a real disagreement between the
  titles is never overridden (an owner can still approve it).
- A second, smaller fix: applying from the results of a metadata search did not
  check whether the book had already been applied some other way, so it could
  apply the same book a second time (rewriting its files and tags again). It
  now skips such books, and a queued apply that is skipped for this reason says
  so in the log. Undoing an apply clears the book's "applied" mark (this is
  tested), so a later run can apply it again on purpose.
- What this costs. Some pairs that used to apply on their own now wait for
  review: any pair where one title only says more than the other and the
  recording length is missing or more than 5% off. How many is not known. It
  depends on the library, and the count has to come from running the read-only
  preview again before the next bulk apply.
- What is still not caught. These pairs are the same as before this change, or
  they are neutral and apply when the lengths agree, so they are open:
  - a sequel whose title only adds a word or a number ("Dark Victory" and "Dark
    Victory Returns", "Hush" and "Hush 2");
  - titles that differ only in a word-form ordinal ("The Second Robin" and "The
    Third Robin");
  - a long title with one word changed;
  - a candidate that is only a franchise name, with no series recorded for it,
    against a book in that franchise;
  - "Year One" against "Grayson: Year One" (a one-sided credit looks exactly like
    a one-sided subtitle);
  - two titles whose numbers are swapped between pieces ("Knightfall 2: Book 3"
    and "Knightfall 3: Book 2"): refused today only by the separate series-number
    check, not by the title check.

  Closing these needs information the title alone does not carry, and each would
  have to be handled without excusing text by what another field says.
