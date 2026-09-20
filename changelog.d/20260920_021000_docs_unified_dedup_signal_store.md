### Added

- Design for the unified dedup signal store: one per-file and per-book record every consumer reads
  from, instead of four independent whole-file similarity call sites, four title normalizers and
  three duration comparators each deriving the same thing. Covers N-way clusters rather than
  pairwise chains, a three-valued agree/disagree/not-comparable outcome so a signal that could not
  be compared stops scoring as a mismatch, partial-book and sibling-book detection shared with the
  apply gate, and a calibration plan with cluster purity, completeness and shatter rate.
  `docs/specs/2026-09-20-unified-dedup-signal-store-design.md`.
