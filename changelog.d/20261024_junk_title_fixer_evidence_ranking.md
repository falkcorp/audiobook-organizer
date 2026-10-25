### Fixed

#### `maintenance.repair-junk-titles`: rank provider and stripped evidence first, filter announcer prose, strip the book's own series, gate fragments by duration

A review of the fixer's 2026-10-23 plan found 513 of 624 reviewed proposals wrong
(plus 39 unsure and 72 accepted). The transcription source carried most of the
error: 261 rejected against 64 accepted. A second review of the first fix found
ten more problems, fixed here. Nothing is written without the usual review and
apply; the change is in the plan step.

- Source priority is now the title without its prefix, the metadata candidate,
  the provider's recorded title, the transcription, the folder or filename, the
  series and number. The transcription used to come first. `title_prefix_stripped`
  behaves as before.
- A transcription loses its series or genre tail before it is judged ("..., Book
  Two of X", "..., a drowsy Quillfeather tale"). A bare ", Book 9" is kept: it
  can be part of a real title. The book's own recorded series is stripped too,
  before or after the title ("X, Series Book 3" and "Series, Book 3, X"), but
  only for a series the book is recorded in and only when what is left is a
  title of its own.
- Evidence agreement (`junkEvidenceAgrees`) no longer lets a comma open a
  subtitle, drops series tails on both sides, and treats two different explicit
  book numbers as two books. The transcription probe alone keeps the comma
  (`junkEvidenceAgreesSeps`), so "X, The Second Tide" still agrees with "X".
  `titleAgreesWithAny`, which the swapped title/author fixer reads, is unchanged.
- A transcription is refused when it is an announcer phrase ("an unabridged
  recording of", "X presents", "you're listening to", a bare "Book 4"), carries a
  format word, starts in lowercase, or reads as a sentence (pronoun-led opening,
  a name and a past-tense verb, a trailing full stop, quotation marks).
- A folder or filename title is refused when it carries a chapter, track, credit
  or placeholder marker (a roman "Chapter XIII", "Part (16)", "(1 of 5)", "[tag]",
  a trailing "(2020)"), is a bare section name, or starts or ends with a person.
  The markers are also looked for in the file name before an author segment was
  stripped, so a chapter label stored as the author is not stripped into a clean
  title. A name that is only in the library is stripped from a file name when a
  "read by" credit follows it and never when a live book holds it as its title;
  an author row that no other book is credited to (counting co-authors linked
  through the book_authors table, and leaving out the book being judged) is not
  an author of the library. A head before " - " that is only a library name must
  also be shaped like a person.
- A filename strips the stored author only when it is not itself junk. The
  stored author and the book's other people go behind a "read by" credit, or
  without one only when the name has evidence beyond the file name: another book
  of the library is credited to it, or it is the book's narrator. With no credit
  and no such evidence the segment is kept (a "Title - Name" stem is exactly
  what the scanner reads as a title and an author, so stripping the stored author
  back off it proves nothing). At most one library name goes.
- `series_and_number` needs a recorded series position equal to the number in
  the title, a real series name (an "of" inside it is fine), and no disc, track
  or part word in the title ("CD_01" included).
- A proposal found anywhere but the stripped title is not made for a book that
  runs under 30 minutes or whose library state is `suspicious`; it is listed as a
  possible fragment. The runtime is the book's duration, or the sum of its files
  when every file has one. A book whose runtime is unknown is listed for a person
  ("runtime unknown"), not proposed: the gate fails closed.
- With a junk stored author, a provider title is accepted when it agrees with
  the path (folder, work folder or the file name's title), the stripped title or
  the transcription, and otherwise the row is listed for a person. A provider
  title that only differs from the path's by punctuation keeps the path's
  spelling. A catalog title that is the stripped or spoken title plus a tagline
  subtitle is not proposed.
- The duplicate-title guard compares owners, not only the proposal: a real-author
  book and a junk-author book proposed the same title are both held when the
  junk one's owner (provider's or folder's author) can be the same person or is
  unknown, and one proposal for two different owners stays applicable. A
  proposal for a real author is held by a live book of the same title under the
  same rule, so a duplicate author row of the same person, or a book with no
  author, holds it too. The author a provider recorded names the owner only when
  the provider's title was not refused.
- The book's own series is cut off a title only after an explicit separator
  ("Harbor Lights, Ember Wars, Book 3"). A series that follows the title after a
  plain space ("Second Quillmoor Book 3" in series "Quillmoor") may be the end of
  the title, and the book being judged has a junk title, so no live book can say
  so: the text is left whole and then refused, so the row is listed for a person.
  Where a separator is there, the book's own transcription or file name spelling
  the whole title also stops the cut.
- A narrator counts as evidence for stripping a name from a file name only when
  it narrates another live book that has a real title and is not itself a live
  title. The narrator field is filled from the same file name and tags (the AI
  filename parse, the artist-tag fallback), so on its own it proves nothing.
- A series named like a disc, side, tape or volume marker is refused as
  "series and number" whatever separates the word from its number ("CD-2",
  "Disc-02", "Side A", "Vol.III"), while a series that merely contains one of the
  words ("Side Effects", "Part-Time Tides") still is a series.
- The junk-title tests and the comments in the fixer no longer use the titles,
  authors and library paths of published works that earlier revisions of the
  fixtures carried, nor the same titles with a word swapped. The replacements
  are made-up word combinations that keep only the shape a test needs ("Series:
  Title, Book 2", a bare number, a comma title). Only the names the code itself
  matches on stay: publisher idents, the Big Finish, Doctor Who and Torchwood
  owner-manual brands (with invented episode names), and Bible book names.

Checked offline by replaying the 2026-10-23 plan's rows through the new string
filters (no live library, so the library-author rule had no live-title guard and
its refusals are an upper bound). The string rules leave every accepted
transcription, filename and folder row (64 + 3 + 3) as it was; the replay does
not cover the rules that read the library, and those change the plan: the
fragment gate, the library-author and own-person strips, and the unified
duplicate guard can each hold a row the reviewer accepted, and a book with an
unknown runtime is now listed for a person where it was proposed. Of the 452
rejected transcription, filename and folder rows, 207 are now refused or cut to
the reviewer's own better title (115 of 261, 49 of 134, 43 of 57), against 164
(96, 30, 38) with the first fix. The rest still slip or change to another wrong
title: 146 of the transcription rows (misheard words and prose that no string
rule can tell from a title), 85 filename and 14 folder rows. The rows that depend
on the ranking, the provider's recorded title, the series and the duration cannot
be replayed from the plan alone; those rules are covered by plan-level tests on
invented libraries, including the shapes of the accepted provider rows, and the
recorded-series strip is not measured per row.
