### Added

#### Window prints are compared: `fpwinidx:` band index, `window_acoustid` dedup signal, fragment containment evidence

The window fingerprints the Macs write (`fpwin:`) had no reader. Each banded
window (kind `window`, tracked files only) now gets 16 position-independent
MinHash bands over its set of 2-frame shingles, stored as an inverted index
(`fpwinidx:` rows plus an `fpwinidx_meta:` member list per file) in the same
batch as every window write, replace, delete, failure tombstone, carry-over
and `book_file` delete cascade. On synthetic prints the scheme finds 99.4% of
copies with 1% of their bits flipped and 0% of unrelated windows.

- `dedup.window-index-build` backfills the index for windows written before
  this change. Preview by default; `dry_run=false` builds it on a worker per
  CPU, resumes where a canceled run stopped, checks the run's write fence per
  file, and sets `system:flag:fpwinidx_v1_done` only after a complete pass.
  Not scheduled. Its preview prints the size estimate (742,000 files with
  three windows each: 1.75 GB at the planned 40 bytes per key; a compacted
  4,000-file synthetic database measured 35.5 bytes per key and 469 bytes per
  member list, which scales to 1.6 GB).
- New SUPPORTING dedup signal `window_acoustid`: the index proposes candidate
  files, `WindowSetSimilarity` verifies them, one signal per candidate book,
  +6 boost (`dedup.signals.window_acoustid.boost: 0` turns it off). Prints
  from another pipeline or tool version are counted as incomparable, not as
  disagreement. Silent until the index flag is set. The review UI labels it
  "Window AcoustID" with its own colour and treats it as supporting, so the
  Duplicates row chips never show it as the reason a pair exists.
- The chapter-fragment fixer shows "window containment 0.NN of the fragment's
  bands appear in the parent" on fragment-to-parent rows when both files are
  indexed (`fragment_window_containment_min`, default 0.5, labels it
  supporting or weak). Informational only: it never makes a row applicable.

### Changed

#### Dedup score formula is `noisy-or-v2`

The signal set changed, so new scores carry `noisy-or-v2`. Nothing rescans on
its own: stored pairs keep their `noisy-or-v1` scores until the owner runs
`dedup.rescore` in a window with no library scan.
