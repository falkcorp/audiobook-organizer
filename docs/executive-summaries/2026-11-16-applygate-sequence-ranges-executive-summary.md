<!-- file: docs/executive-summaries/2026-11-16-applygate-sequence-ranges-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2c7e5a90-4b13-4d68-9f0a-e81d6b3c7a45 -->
<!-- last-edited: 2026-11-17 -->

# The bulk-apply gate stops refusing some correct matches, without trusting a stale volume number

**Date:** 16 November 2026. Pull request 4682. The effect on a real library is
not measured yet: the numbers that motivated the work came from a preview run
before this change, and nothing here has been re-run against that data.

## Executive Summary

- Before the library copies a match's details (title, series, cover,
  description) onto a book without a person looking, a safety check compares
  the book's volume number with the match's. That check is the reason a
  volume 3 never receives volume 1's details. It was also refusing some right
  matches, so people had to approve them by hand.
- Two kinds of right match were being refused. In the first, a list position
  or track number was read as the volume number: a file named "08 - Title"
  or "02 Chapter 1" looked like volume 8 or volume 2. In the second, a
  collected edition (a box set, an omnibus, "Books 1-3") was treated as if it
  were a single volume, or was marked down in the ranking even when the match
  was that very same collected edition.
- The fix teaches the check what a number is. A list position or track number
  is set aside only when it really is the number that was read, and only when
  something stronger says what the volume is. A range such as "Books 1-3" is
  one thing with a start and an end: it matches only that same range, never a
  single volume inside it, and never a different range.
- A review of the first version found that this was still too trusting. The
  stored volume number on a book is the field most likely to be out of date,
  so a book whose only firm number is that stored one is no longer applied
  just because a weaker number was ignored. It now also needs the recording
  length to be known and to agree, and every word of the book's stored title
  that tells volumes apart (such as "Two" against "One") to appear in the
  match's title or subtitle. A shared start of a title ("Batman") is no longer
  enough, because two volumes of one storyline share it. The stored title,
  the stored volume number and an unmarked file name all count as one source,
  since an earlier apply can have written any of them; only a "Book N" marker
  in a file or folder name is independent of that.
- Collected editions got the same caution. Any collected edition, on either
  side, now needs a known recording length that agrees. If the length is
  unknown or only roughly close, the match goes to a person.
- The reader of ranges was tightened too. A title such as "Book 2 - Three
  Jokers" is volume 2 of a book whose title starts with a number word, not a
  range from 2 to 3. A range followed by more numbers ("Books 1-3 and 5") is
  a different list, not the plain range.
- Outcome: two new refusal reasons were added for the cases above, and each
  is listed on an owner's override record whenever it would have fired, even
  if an earlier check refused first. Neither can be lifted by the review-page
  button that applies rows nobody saw. The tests pin the cases named in this
  summary; they do not prove that every refusal the gate had before is
  unchanged. How many of the earlier false refusals this removes is not
  known until the measurement is run again against the merged change; the new
  rules can hold some of them for a person as well.
- Known limit, accepted: two identical collected-edition titles with no
  number in either ("Knightfall Omnibus" on the book and on the match) are
  applied when the recording length agrees. The title says nothing about
  which omnibus this is, so only the recording length speaks for it.
- The one refusal that is new compared with before this change and remains
  by design: a collected edition (on either side) whose recording length is
  unknown or only roughly close now goes to a person, where two agreeing
  signals used to be enough, even when the two sides are the same range.

## A track or list number was read as the volume number

**What it was.** Audiobook files often start with a position in a list ("08 -
Title") or carry a track or disc number. The safety check read that number as
the volume and then found it disagreed with the stored series position.

**Why it mattered.** A right match was refused, and every such refusal cost a
person a manual approval. The measured share of refusals was large.

**The fix.** The check now sets such a number aside only when it is exactly the
number found next to a track, chapter, disc or part word, or a leading number,
and only when a stronger source remains. A number that merely sits near such a
word is still a volume number. When the stronger source is only the stored
series position, the match must also agree on the title and the recording
length before it is applied unattended.

## A collected edition was treated as a single volume

**What it was.** A box set or a "Books 1-3" edition was read as the last or
first of its numbers, so it was compared as if it were one volume. A match for
the same box set could be marked down in the ranking as a mere compilation.

**Why it mattered.** The same box set could not be applied unattended, and a
box set could not safely be told apart from the single volumes it contains.

**The fix.** A range is now one number with a start and an end. It equals only
the same range. The ranking penalty for compilations is lifted only when the
book is itself a collected edition and the match is demonstrably the same one:
the names are equal once the number wording is set aside, and the range or the
edition wording agrees. Applying any collected edition unattended also needs a
known, agreeing recording length. A single volume that merely sits in a folder
named like a box set is not treated as a collected edition because of the
folder name, and a word that only contains a bundle word ("Recollection") is a
single volume. The same edition worded differently on the two sides
("Books 1-3" against "Omnibus: Books 1-3") is no longer a refusal an owner
cannot override; it goes to the recording-length rule, which an owner review
can lift.

## Titles that merely start with a number word

**What it was.** The first version read "Book 2 - Three Jokers" as the range 2
to 3, because "Three" is a number word. It also read "Book 1 and Two-Face" as 1
to 2, and ignored the end of a list such as "Books 1-3 and 5".

**Why it mattered.** Right single volumes were refused, and a four-book list
could look like the three-book range inside it.

**The fix.** A second end is read only when it is digits, or a roman numeral or
number word directly after a range keyword. A range followed by more numbers is
a list and matches only the same list. A number that starts the title after a
comma ("Batman, Book 2, 10 Nights of the Beast") is not a list member, so the
book stays volume 2.
