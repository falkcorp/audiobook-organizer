### Fixed

- **A book whose title names two works no longer has its title recorded as its
  author.** Files like `Jonathan Strange and Mr Norrell - Clarke, Susanna.mp3` or
  `Norse Mythology and Anansi Boys - Neil Gaiman.mp3` were being filed with the
  title in the author field. The rule that caused it asked whether a phrase
  splits into two or more name-shaped parts — but titles do that too, and
  `"Norse Mythology and Anansi Boys"` splits exactly like
  `"Neil Gaiman and Terry Pratchett"`. It is replaced by a much narrower test
  that only looks at `&` and `+`, and that runs after the stronger signals (a
  leading "The"/"A", and initials) rather than before them.

  Measured on 68,793 real library paths, split into 40,261 used to choose the
  rule and a held-out 28,532 used only to check it: zero paths where the previous
  behaviour produced a correct or absent author and this produces a wrong one.

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
