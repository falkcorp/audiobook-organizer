<!-- file: docs/executive-summaries/2026-09-01-five-copies-of-one-question-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 1fba1a32-146b-46e2-abcd-19f79b8c1b2e -->
<!-- last-edited: 2026-09-01 -->

# Five copies of one question, and none of them right

**Pull requests:** https://github.com/falkcorp/audiobook-organizer/pull/3029 (merged)
and a follow-up correcting two faults it introduced, described at the end.

## Executive Summary

- When the app has to work out an author's name from a folder or a filename, it asks one
  question: does this text read like a person's name, or is it a title? That question was
  answered by **five separate pieces of code** that had drifted apart over time. No two
  agreed, and **none of them was correct** — each had a flaw the others did not.
- **Authors whose names do not start with an English letter were being thrown away.** Three
  of the five copies tested the first letter against the range A–Z, so `Émile Zola`,
  `Åsa Larsson`, `村上 春樹` and `Александр Пушкин` were judged not to be names at all.
  `José Saramago` survived only because `J` happens to be an English letter. This is the
  headline fix: those books were losing their author entirely.
- **Titles were being filed as authors.** One copy ended in a shortcut that approved any
  text whose first two words were capitalised, so `A Game of Thrones` and
  `The Lord of the Rings` were treated as people's names and stored in the author field.
- **Shelf labels were being filed as authors.** Another copy had no check at all for
  structural text, so `Book 3`, `Chapter 1`, `Volume 2` and `Disc 1` all became authors.
- **Names with lowercase particles were rejected.** `Simone de Beauvoir` and
  `Ludwig van Beethoven` failed because of the lowercase `de` and `van`.
- All five now share one implementation, which combines the checks rather than picking a
  winner. Measured over 51,744 realistic author strings, the app now files **392** distinct
  author names where it previously filed **637** — the 245 it stopped filing are shelf
  labels and title fragments, and it now correctly separates **440** co-author strings it
  previously could not read at all.
- Verified by running the old code and the new code side by side over the same inputs and
  comparing every answer, and then by deliberately breaking each new safeguard one at a
  time to confirm a test catches it — **ten out of ten** were caught.

## 1. Why one question had five answers

The app reads authors from several places: the folder a book sits in, the book's filename,
and the text embedded in the audio files themselves. Each of those paths grew its own copy
of the "is this a person?" check. Because they were copies rather than one shared piece,
a fix applied to one never reached the others, and over time they diverged.

The practical effect is that the **same book could get a different author depending on
which path happened to read it first**. Two of these paths were doing exactly that: given
the same folder name, one would answer `Ludwig van Beethoven` and the other would answer
nothing. Measured over 6,699 folder shapes, the two disagreed on **128** of them. They now
agree on all of them.

## 2. The bug that was losing real authors

The most damaging flaw was also the simplest. Three copies checked whether a name was
properly capitalised by asking whether its first character falls between `A` and `Z`.

That works for English. It fails for every name that begins with an accented or
non-Latin letter, and it fails *silently* — the name is not flagged, it is simply judged
not to be a name, and the author field is left empty.

The replacement asks the question the right way round: the first character must be a
letter, and must not be a lowercase one. It never asks "is it an uppercase letter?",
because in Japanese, Chinese, Korean, Hebrew, Arabic and Thai there is no such thing as an
uppercase letter — asking that question excludes those languages entirely.

## 3. What changed for your library

- Books whose author's name begins with an accented or non-Latin letter will now get their
  author read correctly instead of left blank.
- Books that had a **title** or a **shelf label** stored in the author field will now have
  that field left empty instead. This is the better outcome: an empty author is picked up
  and refilled by the app's other author-finding paths, whereas a confidently wrong one is
  never re-examined, because nothing downstream can tell it is wrong.
- Co-author strings written with accented names — `Émile Zola and Åsa Larsson` — are now
  split into two authors rather than stored as one.

Existing incorrect author records are **not** repaired by this change. It stops new ones
being created and makes the paths agree; cleaning up rows already stored is separate work.

## 4. Two limits recorded rather than papered over

- **Georgian names are still dropped.** Georgian's everyday alphabet is classified as
  lowercase by international standards, so the "must not start with a lowercase letter"
  rule refuses it. This is not new — the old code dropped Georgian too — and the obvious
  fix was tested and does not work. It is written down with the evidence rather than left
  as a plausible-sounding suggestion for someone to waste a day on.
- **Two-letter surnames written in Latin letters** — `Ng`, `Wu`, `Ho`, `Li` — are refused
  when they appear in a run-together co-author string, because in Latin script two letters
  followed by a space is almost always an abbreviation. The same two-letter surname in
  Japanese, Chinese or Korean characters is accepted. The cost is a co-author string left
  unsplit, which is visible and repairable, rather than split at the wrong place, which is
  not.

## 5. How it was checked

The old and new code were both run over the same 51,744 generated author strings and every
answer compared one to one, rather than trusting that the new code "should" behave the
same. That comparison is what caught two regressions the change had introduced along the
way — one that turned `Ludwig van Beethoven` into `Ludwig van` plus `Beethoven`, and one
that split `Ursula Le Guin` after `Le`.

It also caught a mistake in the *test* rather than the code: a safeguard meant to catch
short surnames slipping through was counting a name's length in storage units rather than
in characters, so the Arabic word `بن` — two characters, four storage units — passed a
check written to stop exactly that. The safeguard was rewritten and the flaw it was meant
to catch was then caught.

## The follow-up: the change introduced two faults of its own

Reviewing the merged change turned up two new problems, both of the kind it existed to
prevent — recording a *wrong* author rather than leaving the field empty. An empty author
gets picked up later by automatic detection; a wrong one never does, because nothing knows
to look at it again.

**A title that names two books was being stored as the author.** The new code had a rule
along the lines of "a phrase listing two or more names is more likely to be the author
than a single name." That is a reasonable-sounding rule and it is wrong, because titles
list things too. `Norse Mythology and Anansi Boys` is built exactly like
`Neil Gaiman and Terry Pratchett`, and nothing in the text tells them apart. So for a file
named `Jonathan Strange and Mr Norrell - Clarke, Susanna.mp3`, the app stored *Jonathan
Strange and Mr Norrell* as the author.

Simply deleting the rule turned out to be wrong as well, and only measuring showed it: it
fixed 8 real files and broke 4 others, such as
`Elora Bishop & Bridget Essex - Under Her Spell.mp3`, where the two names really are the
authors. What separates the two cases is the punctuation — a title happily uses the word
"and" or a comma, but almost never an ampersand to join two names. The rule was rewritten
to look only at `&` and `+`, and to be consulted last rather than first, after the more
reliable clues.

**A book named with underscores kept the words "Unknown Author" as its author.** For a
file called `Mort_Unknown Author.mp3`, the app stored the phrase "Unknown Author" as
though it were a person. That is worse than storing nothing: the book then looks like it
already has an author, so the automatic author-detection skips it permanently. The cause
was that the underscore-handling branch finished early and returned before the checks that
clean this up. Those checks now run for every branch, so no future shortcut can skip them.

## How much of this was guesswork

None of the three choices above was made on judgement. Each was measured against 68,793
real file paths taken from the live library.

That number is split deliberately. The rule was chosen by looking at 40,261 of them, and
then checked against the remaining 28,532 — which it had never seen — with no further
adjustment. A rule invented to explain the cases in front of you will always explain those
cases; the only way to find out whether it is real is to try it on data it was not built
from. It held: across all 68,793 paths there is no file where the previous behaviour got
the author right, or sensibly left it blank, and the new behaviour gets it wrong.

One alternative was rejected on the evidence rather than on taste. The safest-sounding
option was "when in doubt, record nothing." Measured, that was much the worst of the
three: 491 files got a *wrong* author, because declining to answer does not stop the app
— it hands the decision to older, less careful code further down, which then fills in
something worse. Refusing to answer is only safe if somebody is listening for the refusal.

## Something found but not fixed

The two parts of the app that read authors out of filenames still disagree with each other
on 1,110 of 40,261 real files. The earlier work had reported them as identical, and they
were — on the 1,232 invented test cases used to check them, which happened to contain none
of the shapes they differ on. They disagree on real filenames like
`1-01 Zero History - 001.mp3`. This is long-standing, not newly caused, and it is recorded
as separate work rather than quietly folded into this change.
