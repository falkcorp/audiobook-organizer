## P0: `book_file` row creation regressed between 2026-08-11 and 2026-08-14

A book row with no `book_file` rows has no route to any audio. **~13,000 books created
since 2026-08-14 are in this state**, which is the mechanism behind "new books get added
but I can't listen to them". Measurements, eliminated mechanisms and the method traps are
in [`docs/audits/2026-08-25-book-file-creation-regression.md`](../docs/audits/2026-08-25-book-file-creation-regression.md).

Sampled by `created_at` day, n=30/day: 2026-08-11 is **0.0%** over a 16,091-row pool;
2026-08-14 is 93.8%; every day since runs 90-100%. Control 2026-04-04 is 0.0%.
2026-08-12 and 2026-08-13 have no rows at all — a two-day gap right before the collapse,
suggesting a deploy or config change rather than code that rotted.

Three mechanisms are already eliminated and should not be re-proposed: duplicate rows
starving each other (refuted by control — 59/60 pre-boundary duplicate groups have all
rows holding files), the `len(SegmentFiles) > 1` gate at site 1487 (does not apply to
directory books, which reach site 1285 unconditionally), and an outright `book:path:`
index break (would give 0% success; 6 of 43 books succeeded today).

Still unexplained and likely a **second stacked defect**: the successes are partial —
Axiom 52 files on disk -> 42 rows, Foundation 149 -> 76, Flux 59 -> 48.

Any repro must discriminate "no call was made" from "the call was made and returned
early" — both give zero rows, and a test asserting only the end state will pass against
the wrong fix.

Note #2926 fixes the single-file half of this at save time but is **not deployed** —
production ran a binary from 2026-08-24 23:26:31 at measurement time, so nothing is
measurable against prod until it is.
