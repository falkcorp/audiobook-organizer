### Fixed

#### Duplicate books with non-English titles or authors were being scored as unrelated

The duplicate detector compares two books by measuring how many single-character
edits separate their titles and authors. That measurement was counting **bytes
rather than characters**, and in the encoding this app stores text in, an
accented, Cyrillic, Greek, Japanese or Chinese character occupies two or three
bytes rather than one.

The effect was that every non-English name looked far more different than it
actually is:

| | scored before | actually |
|---|---|---|
| "José Saramago" vs "Jose Saramago" | 0.85 | 0.92 |
| "Böll" vs "Boll" | 0.50 | 0.75 |
| "村上春樹" vs "村上春树" | 0.50 | 0.75 |
| "東京" vs "東京都" | **0.00** | 0.67 |

That last row is the clearest case: two titles one character apart were scored
**0.00 — as different as two strings can possibly be.**

Because the detector discards this evidence entirely below a similarity of 0.50,
a book whose title *and* author are both non-Latin could have its strongest
metadata evidence thrown away, and a genuine duplicate would never reach the
review queue. Books with an accented author but an English title were
under-scored rather than dropped.

The comparison now counts characters on both sides. Two consequences worth
knowing:

- **This can only ever raise a similarity score, never lower one**, so no pair
  that was previously flagged as a duplicate stops being one.
- Scores already recorded against existing candidates were computed the old way
  and are not rewritten by this change; they are refreshed the next time those
  pairs are scored.

Two related cleanups came with it: the app carried two separate copies of this
comparison, one correct and one not, and they are now a single implementation;
and the author-name cleaner was rebuilding its text patterns on every single
call, which made it about five times slower and produced twenty times more
memory garbage than necessary during a full library scan.
