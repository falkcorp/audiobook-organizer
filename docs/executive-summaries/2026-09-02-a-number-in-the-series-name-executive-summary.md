<!-- file: docs/executive-summaries/2026-09-02-a-number-in-the-series-name-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7a3c9e15-2d84-4b06-9f71-c8e5b0a24d39 -->
<!-- last-edited: 2026-09-02 -->

# A number in the series name

## What was wrong

A series name should name the series. "Which book is this?" is a separate fact,
and the library already has a field for it.

Those two had been quietly running together. Books were arriving with series
names like `Discworld 05`, `Nameless Sovereign #5` and `Dragon Born [04]` --
the series and the book's place in it mashed into one piece of text. Out of
42,495 series in the library, **7,814 are contaminated this way**: 955 carry an
explicit marker like "Book 3" or "#5", and another 6,859 simply end in a bare
number. (Those two shapes are what was counted; names with the number in
brackets were counted separately, at 198, and other shapes were not counted at
all -- so 7,814 is a floor, not a total.)

The visible damage is that the library stops behaving like a library. Every
volume of a series looks like a *different* series, so a shelf that should hold
one entry holding twelve books instead holds twelve entries holding one book
each. Sorting a series into reading order cannot work, because the field that
says what order the books go in was left empty while the number sat in the
title text instead.

There was already a cleanup tool that could recognise these names, but it had
to be run by hand, it defaulted to reporting rather than changing anything, and
nothing stopped fresh contamination arriving behind it. Meanwhile the check
that *did* run automatically on every save recognised only three narrow shapes
and missed the rest -- including `#5`, the example that prompted this work.

## What changed

There is now one shared piece of logic that recognises a position in a series
name, and everything uses it -- the automatic check on save and the manual
cleanup tool alike. Previously there were two, and the weaker one was the one
that ran all the time.

It now recognises the numbering styles actually present in the library:
trailing markers (`Book 3`, `Vol 9`, `#5`), bare trailing numbers
(`Discworld 05`), bracketed ones (`The Hollows (7)`), and embedded ones where a
keyword vouches for the number (`Evil Genius: Book 4: Becoming the Apex
Supervillain`).

**The number is moved, not deleted.** This is the part that mattered most. Each
of the four places that saves a series name now writes the number it removed
into the book's position field. If the book already states its position, that
value is left alone -- a cleanup pass must never overwrite something a person
set deliberately.

**Uncertain cases are flagged, not changed.** Some names begin with or contain
a number that is genuinely part of the name. `86—EIGHTY-SIX` is a real title;
"correcting" it produces `—EIGHTY-SIX`, which is worse than the problem. Where
nothing vouches for the number, the system now records what it *would* have
done and leaves the name untouched, for a person to confirm.

Two guards worth naming, because both prevent damage that would have been hard
to undo. Names like `Chapter 12` and `Disc 3` look exactly like a numbered
series to a pattern matcher; stripping them would have collapsed hundreds of
unrelated books into one invented series called "Chapter". Those are held back.
And when a book exists in more than one edition, the number is now recorded on
*every* edition rather than only the main one -- the earlier version of this
fix would have silently dropped it for the others.

Every change is written to the log with the book, the old and new name, the
number recovered, and the rule that fired, so a rewrite of someone's data is
never silent.

## One case where the number is removed but not kept

Names with the number in brackets -- `Dragon Born [04]`, `The Hollows (7)` --
are handled differently on purpose, and it is worth knowing why.

When the existing cleanup tool looked at those 198 names, about 180 of them
turned out not to be series positions at all. They were pieces of a single
audiobook that had been split into separate rows, each piece numbered. Treating
that number as "this is book 4 of the series" would file the pieces as four
different books in a series that does not exist.

So for bracketed names only, the brackets are removed from the name and the
number is **not** recorded as the book's position. The position is simply left
blank. A blank position is easy to spot and easy to fill in; a confidently wrong
one looks correct and tends to stay. The roughly one-in-ten bracketed names that
really were positions lose their number, and that trade was made deliberately.


## What this does and does not do

This **stops new contamination**. It does not, by itself, clean the 7,814
series already in that state -- that is done by running the existing
`maintenance.series-denumber` job afterwards, and it should be run in reporting
mode first so there is a record to reverse from.
