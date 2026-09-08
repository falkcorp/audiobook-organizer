### Fixed

- **The left navigation sidebar can be scrolled again.** Its nav list was styled
  `overflow: 'hidden'`, and because the sidebar's inner container is `height: 100%` the
  Drawer paper itself never overflows either — so no scrollbar could appear anywhere and
  the mouse wheel did nothing. Every nav item past the fold was simply unreachable on a
  short window. Measured in a real browser at a 400px-tall viewport: 928px of nav in a
  336px box, wheel scroll moved it `0px`, and the last item (Settings) was off-screen with
  no way to reach it. The list is now the scroll container (`overflowY: auto`, with
  `overflowX: hidden` so the collapsed-drawer width transition can't produce a horizontal
  bar, and `scrollbarGutter: stable` so the icon-only layout doesn't shift). Same
  measurement after: wheel scrolls 592px and Settings is reachable.

- **"Expand All" and "Collapse All" on the Activity page do something.** They drove only
  the operation parent/child nesting, and that nesting is never populated — the only thing
  that sets a parent is `registry.WithParent`, which has no production callers, so every
  operation the API returns has `parent_id: null` (confirmed against production: 40
  operations over a 6-hour window, all parentless). Both buttons recomputed a set that no
  rendered row consulted, so clicking either did nothing at all.

  They now collapse and expand the Pending / Active / Completed groups, and each group
  heading is individually clickable with a chevron. This is the useful behaviour in
  practice: a typical window is dominated by dozens of finished `library.ai-parse` rows
  under Completed, which can now be rolled up on their own. The buttons still clear and
  refill the parent set, so they stay correct if operation lineage is ever wired up.
