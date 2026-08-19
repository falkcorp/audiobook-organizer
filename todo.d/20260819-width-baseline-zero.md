- [ ] **`database.Store` is grouped, not yet unreachable.** `.interface-width-baseline`
      is at 0 and `Store` declares six domain composites instead of forty embeds, but it
      still transitively carries all 398 methods and the six composites are only a
      relabelling. The actual split is still the plan of record —
      `docs/plans/2026-08-19-split-the-pebblestore-surface.md`. Do not read the 0 as
      that job being done.
