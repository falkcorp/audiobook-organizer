### Fixed

#### A closing dropdown could leave the page unclickable until reload

Choosing an option from a dropdown — the library filter selects are the ones
this was found on — could leave the menu stuck part-way through closing. When
that happened, every subsequent click anywhere on the page did nothing, and the
only way out was to reload.

Nothing looked wrong when it happened, which is what made it hard to spot. The
menu had already faded to fully transparent, so the screen looked normal. But
MUI's modal layer is a full-viewport element that only stops capturing clicks
once the close has *completely* finished, and in this case it never did. So an
invisible sheet stayed over the whole page and swallowed everything.

The fix closes menus immediately instead of animating them out. Opening is
unchanged — only the closing animation was involved, and only the closing
animation is removed.

For the record, since this was mis-diagnosed twice before: the failure is a
race, not a broken timer. Measured on a 48-core host, running 20 copies of the
affected end-to-end test across 12 workers, the failure rate tracked the length
of the close animation — 0 of 20 passed at the default ~280ms, 8 of 20 at
250ms, and 20 of 20 with the animation removed. Why the animation fails to
report completion is still unexplained; supplying an independent fallback timer
did not help, which rules out the previously-assumed cause. Removing the window
the race needs is what fixes it.

This was not only a test-environment problem. It reproduced on an otherwise
idle machine running a single worker, at roughly 1 occurrence per 282 tests.
