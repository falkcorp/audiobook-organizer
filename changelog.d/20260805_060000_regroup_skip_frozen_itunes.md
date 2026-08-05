<!-- file: changelog.d/20260805_060000_regroup_skip_frozen_itunes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b1f8c47-2a93-4e06-bd75-8f2c4a1e903d -->
<!-- last-edited: 2026-08-05 -->

### Fixed

- **The regroup producer no longer proposes changes inside the frozen iTunes tree.**
  `books/itunes/**` is the externally-managed Original library — the config layer
  already marks it `Frozen` and read-only — but the shattered-book scan never
  consulted that, and generated holds for it anyway.

  The result dominated the review queue: **561 of 777 ambiguous holds (72%) were
  iTunes AUTHOR folders**, `iTunes Media/Audiobooks/<Author>/`. That layout puts an
  author's whole catalogue in one directory, and a folder-grouping classifier reads a
  shared folder as a shared book.

  Every one of those proposals was wrong twice over:

  - **the books were not shattered.** Sampling their members shows complete, correct
    audiobooks — 11.79 h, 25.30 h, 8.86 h, one file each, real titles. Combining them
    would have merged distinct novels; and
  - **the tree may not be reorganised anyway**, so the proposal was unactionable even
    if it had been right.

  Excluded at the source, using the existing `UnderFrozenITunesTree` policy helper
  (newly exported) rather than a fresh heuristic, so no downstream classifier has to
  re-derive the rule. The count is reported as `skipped-frozen-itunes=N` in the run
  summary, so the queue shrinking is visibly a policy decision rather than a silent
  bug.

  Both `FilePath` and `ITunesPath` are checked: the classifier folds the original
  iTunes album path in as a second grouping signal, so testing only one would let a
  book back in through the other.

  🔑 **This removes noise, not work.** The excluded books are already correct as
  separate books. What IS wrong with them is metadata — four different novels all
  titled `Super Sales on Super Heroes, Book 2`, two titled
  `He Who Fights with Monsters 4`, one titled `Herald of Shalia 1/?` — which is a
  matching problem the regroup queue was never going to solve.
