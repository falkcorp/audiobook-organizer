### Fixed

#### Duplicate books with non-English titles or authors were being compared wrongly

The duplicate detector compares two books by counting the single-character edits
that separate their titles and authors. That count was measuring **bytes rather
than characters**, and in the encoding this app stores text in, an accented,
Cyrillic, Greek, Japanese or Chinese character takes two or three bytes rather
than one.

Every non-English name therefore looked further apart than it really is:

| | scored before | actually |
|---|---|---|
| "José Saramago" vs "Jose Saramago" | 0.85 | 0.92 |
| "Böll" vs "Boll" | 0.50 | 0.75 |
| "村上春樹" vs "村上春树" | 0.50 | 0.75 |
| "東京" vs "東京都" | **0.00** | 0.67 |

That last row is the clearest case: two titles one character apart were scored
**0.00 — as different as two strings can possibly be.**

Because the detector discards this evidence below a similarity of 0.50, a book
whose title *and* author are both non-Latin could have its strongest metadata
evidence thrown away and never reach the review queue. The comparison now counts
characters on both sides.

**What this changes in the review queue, in both directions.** The old count was
too large for non-English text, and the same count is used two different ways:
as a similarity score, and as a plain "are these titles nearly identical?"
threshold. Correcting it therefore does two things, and only the first is an
unambiguous improvement:

- Non-English pairs that were scored too low, or dropped entirely, now carry
  their real similarity. **Nothing that was already flagged as a duplicate stops
  being flagged.**
- Non-English pairs that the "nearly identical titles" check used to *reject*
  may now be **accepted** and shown to you. The oversized byte count was
  incidentally acting as a stricter filter for non-Latin titles, and that side
  effect is gone. Expect some new same-author CJK or Cyrillic pairs in the
  queue whose titles differ by one character — which in those scripts can mean
  an entirely different word. **These are proposals for review, not automatic
  merges:** nothing is merged without a person, and the automatic-merge path
  does not use title similarity at all.

Two smaller notes. Titles or authors stored in a non-UTF-8 encoding (older
Latin-1 tags, unusual filenames) can now score *lower* rather than higher, which
means evidence discarded rather than a wrong match. And scores already recorded
against existing candidates were computed the old way; they are refreshed the
next time those pairs are scored.

Two related cleanups came with it: the app carried two separate copies of this
comparison, one correct and one not, and they are now a single implementation;
and the author-name cleaner was rebuilding its text patterns on every call,
making it about five times slower and producing twenty times more memory garbage
than necessary during a full library scan.
