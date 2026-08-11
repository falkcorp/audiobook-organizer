### Fixed

#### The library page no longer freezes the browser on load

Opening `/library` could lock the browser for seconds before a single book card
became clickable. It happened at the default page size, with no filters, on a
first visit — nothing the user had configured made any difference.

The cause was work the user could not see. `Library.tsx` called
`loadSoftDeleted()` unconditionally on mount, which requested up to **10,000**
soft-deleted books, and `LibrarySoftDeletedSection` rendered every one of those
rows inside a MUI `<Collapse>`. `Collapse` animates height; it does **not**
unmount its children. The panel is collapsed on every load, so all 10,000 rows
were fetched, built, styled and inserted into the document on a page where
nobody could ever look at them.

Measured on chromium before any code changed, page size pinned at the default
20, soft-deleted rows varying:

| soft-deleted rows | DOM nodes | blocked main thread |
|---|---|---|
| 0 | 1,064 | 0ms |
| 1,000 | 15,061 | 679ms |
| 5,000 | 71,061 | 4,050ms |
| 10,000 | 141,061 | 10,813ms |

(Two independent runs on separate harnesses; node counts were identical in both
and blocking times agreed within noise except the 10,000 case, which measured
8,034ms and 10,813ms. Apple-silicon laptop, no CPU throttling — a slower
machine is worse, not better.)

That "collapsed does not mean unrendered" was confirmed rather than assumed:
expanding the panel afterwards changed the document's node count by exactly
**zero**.

Three changes, in the order they matter:

- **The mount fetch asks for the count only** (`limit=1` instead of
  `limit=10000`). The server computes the total independently of the page
  window, so the "N items" chip in the header is exactly as correct as before.
  To be precise about which layer this fixes: the handler still reads up to
  10,000 rows internally to compute that total, so the server-side read is
  unchanged. What goes away is the multi-megabyte response, the client-side
  parse, and the 140,017 DOM nodes — which is where the freeze was. The
  server-side read, and the fact that the total silently saturates at 10,000,
  are tracked separately in
  `todo.d/20260811-soft-deleted-total-capped-at-10000.md`.
- **The collapsed panel renders nothing** (`unmountOnExit`). Rows are fetched
  when the panel is actually opened.
- **An opened panel loads at most 500 rows, and says so on screen.** A user
  with 3,000 soft-deleted books sees "Showing the first 500 of 3,000" rather
  than a short list sitting under a large count, silently disagreeing with it.

#### A wasted 1,000-row library query on every page load

Separately, the initial page size was read from `?limit=` *or* `localStorage`
as one fallback chain. A user who once picked 1,000 from the items-per-page
dropdown therefore seeded state with 1,000 and fired a 1,000-book query on
every subsequent visit — against a library of 366,922 books.

This was reported as "1,000 cards render on every visit, forever". It does not,
and the difference is worth recording. The URL-sync effect that runs on the
first commit reads `searchParams.get('limit') || '20'` with no localStorage
fallback of its own, so it overwrote the remembered value with 20 before
anything painted. Traced 3/3 on unmodified code, the app requested limits
`["1000", "20"]` and rendered 20 cards. The restore path had been dead for as
long as that effect existed; its only surviving effect was the discarded query.

The localStorage read is gone, which removes the wasted query and changes no
rendered output. The 1,000 option is **not** removed: picking it writes
`?limit=1000` and renders 1,000 cards, as before.

#### `?limit=` is now clamped at both ends

The URL-sync effect's limit parser had a lower bound but no upper one, so
`?limit=50000` on a hand-edited or shared link walked straight past the ceiling
the initial-state clamp exists to enforce — and it was reachable on load, not
only on later navigation. Both parsers now share one `clampItemsPerPage`
helper.

All four behaviours are pinned by `web/tests/e2e/library-load-freeze.spec.ts`,
whose assertions are structural (node counts, requested parameters, card
counts) rather than wall-clock, so it can gate for years without becoming a
flake. It was observed failing 4/5 against the unfixed build before it was
believed: 42,017 nodes in the collapsed panel, requested limits
`["1000", "20"]`, and 1,200 cards for `?limit=50000`. The wall-clock numbers
above live in `web/tests/e2e/library-load-perf.spec.ts`, which is skipped
unless `E2E_PERF=1`.
