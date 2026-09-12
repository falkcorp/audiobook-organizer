### Fixed

#### Author and series listings now page with an exact total

`GET /api/v1/audiobooks?author_id=N` or `?series_id=N` with no other filter used
to ignore `limit`/`offset`, return every book of the author or series in one
response, and report no real total. These listings now page like every other
query (default 50 per page, at most 1000) and report the exact number of books
in the author or series. Quarantined books are left out of both the page and
the total, `sort_by` is applied across the whole set rather than inside each
page, and an author's books come back in a stable order so pages never repeat
or skip a book. A series listing keeps its series order.

The author-merge popover on the dedup page, the one caller that needed every
book, now fetches all pages. It had also been reading the response without its
`data` envelope, so against the real server it always showed an empty list;
it now shows the author's books.
