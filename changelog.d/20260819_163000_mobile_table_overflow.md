### Fixed

#### Twelve tables no longer push the whole page sideways on a phone

Twelve tables across seven screens — diagnostics, activity log, metadata history,
book detail, and two dedup tabs — rendered without any horizontal overflow
container. On a narrow screen a table wider than the viewport had nowhere to
scroll, so it stretched the page itself and every other element on it went
off-centre, with the whole body scrolling sideways.

They are now wrapped in `TableContainer`, which scrolls the table on its own
rather than the page. Twenty other tables in the app already did this, either via
`TableContainer` or an equivalent `overflowX` wrapper; these were the ones that
had been missed.

No visual change on a desktop-width screen.
