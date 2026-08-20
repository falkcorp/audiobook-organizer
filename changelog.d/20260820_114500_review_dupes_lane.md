<!-- file: changelog.d/20260820_114500_review_dupes_lane.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3f7b0c95-8d42-4a16-b73e-5c1d9e2a8047 -->
<!-- last-edited: 2026-08-20 -->

### Added

#### Duplicate review moved into the unified workspace

`/review` now has a working duplicates lane: a filter rail, a book-against-book
comparison, bulk merge and dismiss, the compare drawer, and the full keyboard
path (`j`/`k` to move, `m` to merge, `d` to dismiss, `s` to select, `Shift+A`
for the page, `Enter` for the drawer, `?` for the list). Two of the three lanes
now render in one place; the review queue is the last one still living on its
old screen.

The recommended-keep decision moved with it, into a module both the ★ chip and
the `m` shortcut import. It was previously a private function whose comment
promised the two "can never drift" — true while they sat in one file, and no
longer guaranteed once a port split them across a hook and a view.

### Fixed

#### Bulk merge could act on more duplicates than were on screen

The control reads "Merge everything matching this filter", but the endpoint
behind it never accepted a band — and band is the primary filter on that
screen. Narrowing to one band and pressing it merged every pending candidate in
the library. Merges are the hardest operation here to undo, so this could not
be discovered safely.

Band and book now travel with the request. The "both unmatched" filter cannot
be expressed by that endpoint at all, because it describes the two books rather
than the pair, so the action is refused outright while that filter is on rather
than quietly sent without it. Now that everything else narrows correctly, the
filter looks more trustworthy than it did, which makes the one field that
cannot travel more dangerous rather than less.

#### "Show duplicates of this book" found nothing unless it was on the first page

Opening a book's duplicates from the fingerprint column filtered the rows
already loaded rather than asking the server, so a book whose duplicate sat
further down the list showed an empty result underneath a banner naming that
book. The filter is now applied server-side, which also makes the count beside
it honest and the empty state truthful — it can finally say "no duplicate
candidates for this book" and mean it.

#### A `?book=` deep link fetched the whole library before narrowing

Arriving at the dupes lane for a specific book rendered once with no filter applied,
requested every pending candidate in the library, then threw that response away and
asked again with the filter. The URL's two filters are now read once, above the lane,
and passed in — so the first render is already correct and a deep link costs one
request instead of two. The larger the library, the more the discarded one cost.
