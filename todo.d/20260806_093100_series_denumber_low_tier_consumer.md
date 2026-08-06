<!-- file: todo.d/20260806_093100_series_denumber_low_tier_consumer.md -->
<!-- version: 1.0.0 -->
<!-- guid: c1e84b76-3d29-4f05-a917-6b2508fd3e14 -->
<!-- last-edited: 2026-08-06 -->

- [ ] **Give the 466 low-confidence series positions somewhere to go** — deferred
  deliberately on 2026-08-06 (owner decision), revisit after owner items 1 and 2.

  `maintenance.series-denumber` now reports 466 series names carrying a bare
  number (`08. Battle for the Abyss` → position 8, `Station 64: The Doll Dungeon`
  → position 64) covering ~580 books. They are correct often enough to be worth
  surfacing and wrong often enough that they can never apply themselves —
  `86—EIGHTY-SIX` is a real series name in this library with the identical shape.

  Today they exist only in the `reportPath` TSV. Nothing consumes them.

  🔑 **Why no review-queue Kind was built yet:** a new Kind needs frontend
  mapping, and `review_apply_enabled` is OFF in production, so approving such a
  hold would mutate nothing. Wiring these in only makes sense once holds have
  real per-item actions — i.e. after owner item 1 (recommendations) and item 2
  (overrides). Doing it in that order avoids building a producer for a consumer
  that cannot act.

  Note for whoever picks this up: for `08. Battle for the Abyss` the parsed base
  is `Battle for the Abyss` — the BOOK's title. The real series (`Horus Heresy`)
  is not present in the string at all. So the low tier cannot be resolved by
  better parsing; it needs evidence from outside the name (sibling books sharing
  a folder/author with different leading numbers — spec D4, unbuilt), or a human.

  Design: [`docs/specs/2026-08-06-series-embedded-positions-design.md`].
