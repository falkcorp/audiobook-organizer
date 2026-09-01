### Fixed

- **A book whose title names two works no longer has its title recorded as its
  author.** Files like `Jonathan Strange and Mr Norrell - Clarke, Susanna.mp3` or
  `Norse Mythology and Anansi Boys - Neil Gaiman.mp3` were being filed with the
  title in the author field. The rule that caused it asked whether a phrase
  splits into two or more name-shaped parts — but titles do that too, and
  `"Norse Mythology and Anansi Boys"` splits exactly like
  `"Neil Gaiman and Terry Pratchett"`. It is replaced by a much narrower test
  that only looks at `&` and `+`, that runs after the stronger signals (a
  leading "The"/"A", and initials) rather than before them, and that only
  applies when the other half of the filename is a single undivided name.

  That last condition was missing at first and had to be added in review. An
  ampersand tells you a phrase *is* a list of authors; it tells you nothing
  about whether the other half is. Without the condition, a title like
  `Magic Tides & Magic Claims` beat a genuine two-author credit sitting right
  next to it.

  Measured on 68,793 real library paths, split into 40,261 used to choose the
  rule and a held-out 28,532 used only to check it: zero paths where the previous
  behaviour produced a correct or absent author and this produces a wrong one.

  There is a deliberate trade, stated here because it is a real loss and not
  only an improvement. A credit joined by "and" or a comma is no longer
  recognised when it comes *first* in the filename, so
  `Neil Gaiman and Terry Pratchett - Good Omens.mp3` now records the wrong
  author. Recognising that shape is exactly what was filing omnibus titles as
  authors, and nothing in the text separates the two. The loss falls on the
  rarer ordering: this library measures 57 files named "Title - Author" against
  9 named "Author - Title".

  The same trade is sharper on underscore-named files, where declining to answer
  costs the title as well as the author. Every alternative was measured and each
  costs more than it saves — the numbers are recorded in
  `todo.d/20260901_underscore_refusal_falls_through_to_a_worse_answer.md` so they
  are not re-proposed. Neither shape occurs in the 40,261-file production sample.

- **An audiobook named with underscores no longer has "Unknown Author" recorded
  as a real author.** `Mort_Unknown Author.mp3` was stored with the author
  literally set to the placeholder, which then made the book look like it already
  had an author and permanently excluded it from AI author-detection. The
  underscore branch was returning early, before the guard that clears the
  placeholder, the fallback that recovers the author from the folder name, and
  the series-number detection — all of which now run for every branch.

- **A decorated placeholder ("Unknown Author (Unabridged)") is now recognised by
  the gate that decides whether AI author-detection is worth running.** Three
  other places already stripped the edition suffix before that comparison; this
  fourth one did not. No author row in a 60,000-book production sample currently
  hits it, so this closes a hole rather than repairing existing rows.
