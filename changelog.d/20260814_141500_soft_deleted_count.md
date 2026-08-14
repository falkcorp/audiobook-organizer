### Fixed

#### The soft-deleted total is exact, and a failed count is an error

`GET /audiobooks/soft-deleted` computed its `total` by fetching up to 10,000
rows and taking `len()` — silently wrong above 10,000, a 10,000-row read per
call, and a discarded error that reported `total: 0`, indistinguishable from
an empty trash. A new `CountSoftDeletedBooks` store capability (memdb index
walk / Pebble scan, conformance-tested on both dispatch paths with a
non-vacuous `olderThan` fixture) feeds the handler, which now surfaces count
errors as 500s. The service falls back to paging the listing to exhaustion
for stores without the capability, so no caller can reintroduce a cap.
