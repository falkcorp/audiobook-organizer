<!-- file: docs/executive-summaries/2026-11-17-metadata-title-check-executive-summary.md -->
<!-- version: 2.4.0 -->
<!-- guid: 3b9d6e21-8c47-4f05-a1d2-6e0f7a4c9b83 -->
<!-- last-edited: 2026-11-18 -->

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
    2"). The match is refused. A series name, a credit or a file name cannot
    talk the check out of this, and neither can a year that is written as part
    of the title: "Gotham 2033" and "Gotham 2034" are different books, and so
    are "Gotham 2033" and "Gotham 2". (A year in brackets next to a title that
    has no year of its own is only a release date and is ignored.) Numbers swapped between the pieces
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
  when the lengths agreed. When the library book's own title or file name says which part it is, that is
  what counts; the folder name is used only when they say nothing, so an old
  "Part 1" folder over a file called "Part 2" can no longer let a "Part 1" through.
  When the found book names no part, an old "Disc 2"
  folder name does not refuse a book whose recording length agrees (as before);
  a part marker left in the library book's own title or its single file's name
  ("Hush CD1" against the whole book "Hush") does now refuse it, whatever the
  lengths say. That last case is a new refusal, and in the replay over 4000
  pairs it changed none.
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
- A third and a fourth review of the rule tightened it in seven places, each started from a
  failing test:
  - A found title that is only a few words of a longer library title ("Gotham"
    against "Gotham Knights Detective Tales") is no longer taken as related
    unless it shares at least half the words. A found title that adds words to
    the library title is unchanged.
  - A library title that ends in one capital letter ("Robin X") is treated as a
    whole title, so the folder name "Robin" no longer stands in for it. The same
    holds when recording details are tacked on ("Robin X [64kbps]"): the details
    are set aside and what is left is judged.
  - A volume written in the library book's subtitle ("Batman, Book 2") is now
    compared with the found book's number, so "Book 2" is not applied from a
    "Book 3".
  - A found book whose title is just its own series name ("Batman" in the series
    "Batman") no longer counts as the same title as a library book called
    "Batman"; it waits for a matching recording length. The same goes for a library
    title that is "Batman" with the author's name beside it ("Batman - Bruce
    Wayne"), and for a file name used in place of an empty title.
  - A list number in front ("08 - ") never makes two titles different, and a
    year in brackets does not either unless the other title carries a year too.
    A year written plainly in a title ("Gotham 2033") is part of its name: it
    differs from any other year and from any other number ("Gotham 2034",
    "Gotham 2"). It is ignored only against a title that states no number at all.
    A fourth review found that the first version of this rule was too generous
    ("Gotham (2033)" and "Gotham (2034)" were treated as the same book); it now
    refuses them, at the price of waiting for review on a few titles that carry
    a plain year, such as "Knightfall 1993" against "Knightfall: Book 1".
  - "Hush I" is now different from "Hush, Book 2", "Hush, Book #2", "Hush #2",
    "Hush (Book 2)", "Hush Volume II" and "Hush II (Unabridged)", as it already
    was from "Hush II".
  - "Third Book" is read as "Book 3", so it matches "Book 3" and differs from
    "Book 4".
- What this costs, measured. A read-only replay over 4000 stored book and
  candidate pairs compared three versions. The check as it was applied 276 of
  them. The first rebuilt check applied 203, so it lost too many pairs that are
  plainly the same book. This version applies 247: 34 that used to apply now wait
  for review (31 of those 34 were read by hand as the same book; 3 could not be
  told), and 5 apply that did not before, each a pair the old check refused as
  "title disagrees" and this one applies, all five with recording lengths within
  5% (they are listed for review in the PR, not judged here). Of the 34, 19 are now refused as "title disagrees" and 15 wait
  because the title is only partly matched and the recording length does not
  confirm it.
  The third round changed none of those totals: the same 247 apply, with the
  same 34 lost and the same 5 gained against the check as it was, and two
  pairs changed only the reason they wait. The count for a real library still
  has to come from running the read-only preview again before the next bulk
  apply.
- What is still not caught. These pairs are the same as before this change, or
  they are neutral and apply when the lengths agree, so they are open:
  - a sequel whose title only adds a word or a number ("Dark Victory" and "Dark
    Victory Returns", "Hush" and "Hush 2");
  - titles that differ only in an ordinal that is no volume's number ("The
    Second Robin" and "The Third Robin");
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
