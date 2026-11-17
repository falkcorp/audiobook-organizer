<!-- file: docs/executive-summaries/2026-11-17-metadata-title-check-executive-summary.md -->
<!-- version: 2.2.0 -->
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
- The title check now gives one of three answers, and it asks them in this
  order:
  - **Same.** The whole title of one side equals the whole title of the other
    after only harmless differences are ignored: capital letters, accents, "and"
    against "&", a leading "The", "(Unabridged)", numbers written as digits,
    words or roman numerals. A series name in front of the title counts when the
    series is known. When the found book has no subtitle, its series and number,
    or its author's or narrator's name, written beside its title can also make
    the whole title (as a tagger often writes it), but a name is never allowed to
    stand in for a series name, and a found book that has a real subtitle never
    gets these extra spellings, so words added as a subtitle cannot be skipped
    over. A subtitle such as "A Novel", "Unabridged" or "Series, Book 2" (the
    book's own series and number) is not counted as a real subtitle. The file name counts too, but only
    for a library book whose own title is empty or a placeholder ("Chapter 3");
    for a book with a real title the file name neither agrees nor disagrees.
    Nothing else makes two titles the same: not a series name on its own, not a
    year.
  - **Different.** The titles share a whole piece and each has a piece of its
    own ("Batman: Dark Victory" and "Batman: The Long Halloween"), or both state a
    number of the same kind (a volume, or a part) and the numbers differ ("Part
    1" and "Part 2", "Hush 1" and "Hush 2", "Part 1 of 3" and "Part 3"). A
    number only one side states is not a difference ("Who Am I" and "Who Am I
    2"). The match is refused. A series name, a credit, a file name or a year
    cannot talk the check out of this. Numbers swapped between the pieces
    ("Knightfall 2: Book 3" and "Knightfall 3: Book 2") now count as different;
    an earlier version of this change let them through.
  - **Neutral.** Everything else that is related: one title simply says more
    than the other ("Hush" and "Hush: A Gotham Story", or a stored title that
    starts with a list number such as "08 - The Long Halloween"). A neutral title confirms
    nothing by itself, so it is applied on its own only when the recording
    lengths also agree, both known and within 5%. Without that it waits for
    review, whatever else matches (author, narrator, ASIN or the audio intro). If
    the author is the only other thing that agrees, the book is reported as
    lacking evidence; if two other things agree, it is reported as "title only
    partly matches and the runtime does not confirm it". An owner review can
    still approve either.
- A part is checked on its own as well. If the found book is "Part 2" of a
  title and the library book is not marked as a part, the match is refused even
  when the recording lengths agree, because two parts of one book are often
  within 5% of each other's length. Before this change such a pair was applied
  when the lengths agreed. In the other direction, an old "Disc 2" folder name
  no longer refuses a book whose recording length already agrees.
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
    a one-sided subtitle).
  Closing these needs information the title alone does not carry, and each would
  have to be handled without excusing text by what another field says.
- Left for the owner to decide, and not changed here: a neutral title that is
  applied because the recording lengths agree. In a replay over 4000 books, 49 of
  the 61 applications that rest on a neutral title rest on the length alone.
  Today's behaviour is pinned by tests marked "under owner review", and the open
  question is on the TODO list.
