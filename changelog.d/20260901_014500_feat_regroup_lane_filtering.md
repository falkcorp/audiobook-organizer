### Added

#### The review-queue lane can now be filtered, searched and sorted — and says which of its numbers is which

The regroup lane (`/review?lane=regroup`) had no filter, no search and no sort at
all, while the metadata lane beside it carried nine filter switches, a regex box,
a provider picker and a slider. It now has three controls, and they deliberately
do not all work the same way.

**Kind is pushed to the server.** It is the only one of the three that can be,
and the only one that changes which rows exist rather than which loaded rows are
shown. That matters because the lane's fetch is capped at 500: an unfiltered load
of a queue holding 730 holds across kinds spends the whole page budget on a
mixture — on production, ~484 holds of the kind being worked plus 16 of another.
Selecting the kind spends all 500 on the kind being worked. `ReviewItemsFilter`
has carried a `kind` field the whole time; this lane simply never passed it. It
is now passed on BOTH fetch paths — the mount fetch and the reload that runs
after every approve, reject and bulk action, which would otherwise have quietly
repopulated the lane with every kind the moment a reviewer decided one hold.

This raises the cap's usefulness; it does not remove it. A kind holding 714 still
truncates at 500. Paging past that needs `offset`, which the client already
supports and this lane still does not use.

**Search and sort are client-side, and the UI says so.** The endpoint offers
neither, so both act on the loaded page rather than on the queue, and each
control carries the helper text that admits it. Search matches the summary, the
folder path (the item's own and the payload's), the proposed title, the member
file paths, the dedup key, the id and the kind label — built into an index once
per loaded page rather than re-parsing 500 JSON payloads per keystroke, and
debounced 250 ms behind the text field so typing never lags. There is
deliberately no author match: a `ReviewItem` carries no author, and the member
books that do are fetched lazily per row, so matching on one would silently have
meant "matches the rows you already expanded". Sort offers kind (the previous
fixed order, now selectable), newest and oldest by `created_at` — a field that
until now rendered nowhere. The comparator is total, ending on the id: the queue
is written in bulk by a scan, so holds sharing a `created_at` to the second are
the normal case, and a comparator that called them equal would leave their order
to whatever the fetch happened to return.

#### Four counts that used to be two

The lane now keeps apart four numbers that a filter makes genuinely different:
the whole queue, the selected kind's server-side total, what was actually
loaded, and what the search left visible.

The trap this closes is `total`. The store applies `kind` **before** taking the
length (`ListReviewItems`), so under a kind filter the fetched total is that
kind's count and not the queue's — rendering it as "N pending" beside a kind
selector would have understated the queue by everything the other kinds hold. The
all-kinds number now comes from the already-polled `/review/count`, the same
instrument the per-kind totals already trusted, so the two cannot disagree.

The second is truncation. The "your view is partial" warning and the per-bucket
counts are derived from what was LOADED, never from what the search left visible.
Truncation means the lane failed to load rows that exist; a search hiding rows is
the reviewer asking for that. Had the two been merged, every keystroke would have
raised the warning, which is how the one occurrence that matters becomes
unreadable.

### Fixed

#### The regroup lane rendered "Nothing to review 🎉" over a queue holding hundreds of items

The spine had a single empty branch, so a filter that matched nothing was
congratulated as an empty queue — telling a reviewer to go home when the next
step was to widen the filter. Filtered-empty now says what the queue still holds
and offers a clear; genuinely-empty uses the lane descriptor's `emptyMessage`,
which had been carried unused since the lane was ported.

Three more states that were previously indistinguishable or missing: the lane now
shows progress while refetching even when rows are already on screen (a kind
change was otherwise completely silent, since the spine only spins when it has
nothing at all), it drops the previous kind's rows instead of showing them under
the new kind's heading, and its error alert offers a retry. The lane's own fetch
also now carries a 30-second deadline — `apiFetch` supports one per caller and
this lane never asked, so a server that never answered left the spinner turning
with nothing to tell the reviewer.
