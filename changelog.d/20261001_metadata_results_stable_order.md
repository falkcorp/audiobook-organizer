### Fixed

- `GET /api/v1/library/metadata-results` now returns books in book-ID order. It used to build its list from an unordered map, so every request came back in a new random order and `offset` paging returned an arbitrary slice. Pages repeated some books and never reached others: on prod, 12 pages of 1,000 "matched" rows held only 10,350 distinct books out of 42,748. The order is by book ID rather than fetch time so that a background refresh of the cached set during paging does not shift later offsets.
