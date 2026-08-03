<!-- file: changelog.d/20260802_090000_authors_narrators_decode.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1140b977-2b6b-4e4d-8d5d-603034447a68 -->
<!-- last-edited: 2026-08-02 -->

### Fixed

- **The Authors and Narrators tabs were both blank in the app**, while both endpoints
  answered `200`. Neither was a routing problem — both bodies were unparseable by the
  client, so the failure was invisible in the access log.

  **Authors:** real ABS switches response envelope when the caller paginates, and the
  two shapes share no keys — `{"authors":[…]}` bare, versus
  `{"results":[…],"total":…,"page":…,…}` with `?limit=&page=`. AudioBooth always
  paginates and decodes into `Page<Author>`, whose `total` and `page` are required, so
  the bare shape threw. We now serve whichever envelope the request asks for.

  **Narrators:** every entry needs an `id`, which we never sent. The client's
  `Narrator` declares `id` non-optionally, so one entry without it throws the entire
  list. The id is now derived exactly as real ABS derives it —
  `encodeURIComponent(base64(name))` — because narrators are not entities in ABS and
  the name is the identity; a minted id would change on restart and rot every id the
  client had cached. `numBooks` is omitted rather than sent as `0` (there is no
  reverse narrator→book index, and `0` would render "0 books" beside every narrator).

  **Why the fixtures missed both** — recorded as spec §1.8.11 so it does not recur:
  the authors fixture was captured **without a query string**, so it pinned the shape
  no client ever requests; and the narrators fixture body is `{"narrators": []}`, so
  the conformance diff had **no element to compare** and passed vacuously. An empty
  golden array pins nothing about its elements. A paginated-authors fixture is now
  committed, and both element shapes have hand-written tests.
