<!-- file: docs/executive-summaries/2026-11-16-applygate-sequence-ranges-executive-summary.md -->
<!-- version: 1.3.1 -->
<!-- guid: 2c7e5a90-4b13-4d68-9f0a-e81d6b3c7a45 -->
<!-- last-edited: 2026-11-17 -->

# The bulk-apply gate reads track numbers and collected editions correctly, and still never trusts a stale volume number

**Date:** 16-17 November 2026. Pull request 4682. The effect on a real library
is not measured yet: the numbers that motivated the work came from a preview
run before this change, and nothing here has been re-run against that data.

## Executive Summary

- Before the library copies a match's details (title, series, cover,
  description) onto a book without a person looking, a safety check compares
  the book's volume number with the match's. That check is the reason a
  volume 3 never receives volume 1's details. It was also refusing some right
  matches, so people had to approve them by hand.
- Two kinds of right match were being refused. In the first (the track-number
  case), a list position or track number was read as the volume number: a file
  named "08 - Title" or "02 Chapter 1" looked like volume 8 or volume 2. In
  the second (collected editions), a box set, an omnibus or "Books 1-3" was
  treated as if it were a single volume, or was marked down in the ranking even
  when the match was that very same collected edition.
- **Track numbers.** The check now knows a leading list number or a track,
  chapter, disc or part number for what it is: a weak reading of the volume.
  It no longer stops the book with a hard conflict. But it does NOT apply
  such a book on its own either. When the track-like number disagrees with the
  stored title, the stored volume number or an unmarked file name, the book
  goes to a person with a reason that says which number was set aside and that
  it looks like a track number. The reason is that every other number on the
  book (the stored title, the stored volume number, the file and folder names,
  even a "Book 2" marker) is something an earlier apply can have written, so
  none of them is independent proof of the volume, and the title and the
  recording length cannot stand in for one. An earlier version of this change
  tried to let a matching title and recording length vouch for the match. A
  review found that it applied the wrong volume of one storyline ("Part 1"
  against "Part 2", "Year Two" against "Year One") because the title check
  agrees whenever two volumes share the start of their title. That path was
  removed; books with a list number in their name were refused before this
  change and go to review now.
- This is a softening only in wording, not in who may override: the earlier
  refusal could already be approved by a person reading the review page, and
  so can the new one. The new one cannot be approved by the review-page button
  that applies rows nobody looked at, which the old one could. A person always
  sees both of the new reasons on an override record, whichever check refused
  first.
- **Collected editions.** A range such as "Books 1-3" is one thing with a
  start and an end: it matches only that same range, never a single volume
  inside it, never a different range, and never the same range worded
  differently as if nothing were different. A collected edition, on either
  side, applies without a person only when the match is the same range and the
  recording length is known and agrees; otherwise it goes to a person, and
  when the two sides use different edition wording the reason says so. The
  ranking penalty for compilations is lifted only when the book is itself a
  collected edition and the match is demonstrably the same one.
- Other fixes: a title that begins with a number word ("Book 2 - Three
  Jokers") is volume 2, not a range from 2 to 3; a range followed by more
  numbers ("Books 1-3 and 5") is a different list; a number that starts the
  title after a comma ("Batman, Book 2, 10 Nights of the Beast") does not join
  the list; a word that merely contains a bundle word ("Recollection") is a
  single volume; a single volume that merely sits in a folder named like a box
  set is not treated as a collected edition because of the folder name, though
  it still goes to a person unless its recording length agrees, as before. The
  text after a leading list number in a title is still read, so adding a list
  number never hides the title's own number.
- Known limits, accepted: two identical collected-edition titles with no
  number in either ("Knightfall Omnibus" on the book and on the match) apply
  when the recording length agrees; only the recording length says which
  omnibus it is. A collected edition whose recording length is unknown or only
  roughly close now goes to a person even for the same range, where two
  agreeing signals used to be enough; that is the one refusal that is stricter
  than before by design.
- Two problems found on the way are not fixed here and are filed for their own
  change: the general title check agrees whenever two titles share a segment
  ("Hush" of "Hush: Part 1" and "Hush: Part 2"), so a book whose stored volume
  number is wrong and whose name carries no list number can be applied with
  another volume's details on the current main (four of five reviewed pairs do);
  and the "already applied" skip exists only on the cached-apply route, not on
  the route that fetches fresh candidates.
- The rule this change keeps for track-like numbers, stated once: a book is
  applied without a person only if every number it carries (stored position,
  title, file and folder names, the track-like ones included) equals the
  match's number. A track-like number changes the reason a book is held (a
  review with a track-number explanation instead of a hard conflict); it never
  turns a refusal into an apply. Three things are not subject to it, and each
  is deliberate. (1) A stored position equal to an end of the title's own
  range is set aside silently: "Batman Books 1-3" at position 3 passes the
  sequence check against a 1-3 candidate, and then still needs the collected-
  edition runtime rule (a known, agreeing runtime) to apply. (2) A book with
  no number at all passes, as on main. (3) A book whose only numbers are part
  suffixes passes against a candidate that names no number, as on main.
- For books whose only odd number is track-like, the same books are refused as
  before, with one exception worth naming: a stored title that has both a
  leading list number and a number of its own at the end ("01 - Batman 451",
  position 1) used to be read as 1 only and could apply; it is now read as 1
  and 451 and held, which is what the gate already did to the same title
  without the list number.
- Collected editions are the separate, intended change in outcome: where main
  refused a box set, an omnibus or a "Books 1-3" edition, it now applies
  without a person on a same-range match with a known, agreeing recording
  length. The recording-length rule also adds refusals: a collected edition
  with an unknown or only roughly agreeing recording length now goes to a
  person even for the same range, where two agreeing signals used to be
  enough.
- Outcome: no book with a track-like number that disagrees with the number the
  rest of its names and fields agree on is applied without a person in this
  change. The tests pin the cases
  named in this summary and compare a title with and without a list number
  added; they do not prove that every refusal the gate had before is
  unchanged. How many of the earlier false refusals this removes is not known
  until the measurement is run again against the merged change: the track-number
  books are now reviewed, not applied, so the gain there is a clearer reason,
  not fewer reviews.

## A track or list number was read as the volume number

**What it was.** Audiobook files often start with a position in a list ("08 -
Title") or carry a track or disc number. The safety check read that number as
the volume and then found it disagreed with the stored series position.

**Why it mattered.** A right match was stopped with a hard conflict, and every
such stop cost a person a manual approval.

**The fix.** The check sets such a number aside only when it is exactly the
number found next to a track, chapter, disc or part word, or a leading number.
If it then disagrees with the other numbers on the book, the book is sent to
review with a plain reason; it is never applied on the strength of a matching
title or recording length. A number that agrees with the stored one is simply
the same number twice and needs nothing more.

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
known, agreeing recording length.

## Titles that merely start with a number word

**What it was.** The first version read "Book 2 - Three Jokers" as the range 2
to 3, because "Three" is a number word. It also read "Book 1 and Two-Face" as 1
to 2, and ignored the end of a list such as "Books 1-3 and 5".

**Why it mattered.** Right single volumes were refused, and a four-book list
could look like the three-book range inside it.

**The fix.** A second end is read only when it is digits, or a roman numeral or
number word directly after a range keyword. A range followed by more numbers is
a list and matches only the same list, and a continuation counts only when it
ends the list.
