### Documentation

- `docs/audits/2026-08-18-interface-width-shapes.md` gained a Part 2 recording
  what the interface-width sweep measured: the width linter counts declared
  entries and is blind to transitive surface, parameter types are what propagate
  width across packages, per-interface usage figures for the remaining wide
  compositions, the three counting bugs that produced plausible-but-wrong numbers
  along the way, and a per-PR breakdown of the 28 → 5 `interfacebloat` reduction
  with the reason each of the five survivors was parked rather than split.
- `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md` corrected: it no
  longer claims nothing has started, and records the three plan assumptions the
  sweep invalidated.
- August executive-summary roundup gained a plain-language section on the sweep.
