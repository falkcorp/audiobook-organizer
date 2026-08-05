<!-- file: changelog.d/20260805_030000_regroup_series_guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: a2f464e2-f0d6-478e-a1f5-e3831ba95b30 -->
<!-- last-edited: 2026-08-05 -->

### Fixed

- **The shattered-book classifier would have merged whole series into single books.**
  A production dry-run on 2026-08-05 produced 930 regroup candidates; sampling the
  `regroup.multidisc` ones — the *confident* kind, the only one with an apply
  handler — found **41 of 43 were not chapter sets at all**:

  ```
  Super Sales on Super Heroes.m4b / 2 / 3 / 4 / 5      ← five separate novels
  He Who Fights with Monsters 10 / 12 / Book 01        ← separate novels
  Path Of The Voidwalker - BK01 / BK02 / BK03          ← "BK" = Book
  ```

  One grouped two entirely different titles (*Isekai Cheat Appraiser* with
  *My Cottage Was Transferred to Another World*).

  **Root cause:** every one of the 43 was an iTunes **author-level** folder —
  `iTunes Media/Audiobooks/<Author>/`. The classifier's founding assumption, that
  files sharing a folder are tracks of one book, holds in the organized tree
  (`<Author>/<Title>/files`) and is false in the iTunes tree, where one folder holds
  an author's entire catalogue.

  The existing over-merge guard could not catch it. That guard keys on distinct
  title *stems*, and numbered sequels all strip to one stem, so `manyDistinctTitles`
  stayed false and the collapse was judged confident.

  🔑 **Runtime is the discriminator the name cannot provide.** Six two-hour files are
  six books; six three-minute files are six chapters. `ShatterBook` now carries
  `DurationSec`, and a confident flat collapse is vetoed when a strict majority of
  members are individually ≥90 minutes — a threshold in the empty band between
  chapters and novels.

  Deliberately conservative in both directions: a lone long member cannot veto a
  real chapter set, and **unknown duration counts as not-book-length**, so a library
  with missing durations does not have every collapse silently blocked. A vetoed
  group falls through to `ambiguous` and is held for review rather than merged —
  matching the existing rule that this classifier errs toward *not* grouping,
  because leaving a book shattered is recoverable and merging distinct books is not
  (the apply path hard-deletes absorbed rows).

  This was only measurable because the duration data became trustworthy the day
  before — the millisecond purge and duplicate-row cleanup are what make runtime a
  usable signal here. Per-row values are normalised through `NormalizeDurationSec`
  when summed, so a stale millisecond row cannot masquerade as book-length.

  6 tests, including the production shape, a real chapter set that must still
  collapse, and a check that the guard is load-bearing — removing it makes the
  series case plan "a CONFIDENT collapse of 6 full-length books into one".
