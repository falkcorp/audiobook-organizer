### Performance

#### Author deduplication runs 8.8x faster

The most expensive loop in author dedup compared every pair of distinct author
last names with Jaro-Winkler similarity: 26,357,430 pairs on the production
library's 7,261 distinct surnames, on a single core, each comparison allocating
two rune slices and two match bitmaps.

Two changes, neither of which alters which authors get grouped:

- **A provable length screen.** Because matches cannot exceed the shorter
  string and the Winkler prefix boost is capped at 4 characters, a Jaro-Winkler
  score of at least `t` requires the two strings' lengths to be within `5t-4` of
  each other — at the 0.95 threshold used here, within a factor of 4/3. An
  integer rune-count comparison therefore rules out 61% of pairs before any
  string work happens. The bound is conservative by construction, so it can only
  reduce how many comparisons run, never which pairs are accepted.
- **Sharding the scan.** Deciding which names are similar reads shared state and
  writes none, so it now runs across all cores, while the order-dependent
  grouping it feeds stays serial. Workers pull outer indices from an atomic
  counter rather than claiming fixed ranges, because the inner loop shrinks as
  the outer index advances and a static split would leave one worker holding
  most of the work.

Measured at production shape on 10 cores: **4.62s to 0.53s**. Output verified
byte-identical to the previous implementation, pinned by a golden test that
records the full grouping rather than a group count.

This site was missed by the 2026-07-05 concurrency audit because it iterates
derived last-name strings rather than books or authors, so it did not match the
collection shapes that sweep looked for. That audit has been updated: all five
of its priority items were already complete, and it now warns against being read
as a live TODO list.
