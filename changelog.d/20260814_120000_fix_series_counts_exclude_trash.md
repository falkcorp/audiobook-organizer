### Fixed

#### Series book and file counts no longer include trashed books on the Pebble path

`GetAllSeriesBookCounts` and `GetAllSeriesFileCounts` have two implementations,
and they disagreed. The memdb implementation has always skipped soft-deleted
books; the Pebble key-scan counted them. A series whose trash held four books
reported those four in its book count, and their audio files in its file count.

memdb is the production default, so the visible effect was confined to cold
start — the window after process start but before warmup publishes the in-memory
index — and to any deployment running with `UseMemDB=false`. In that window a
series' counts read high and then silently dropped to the correct value once
warmup completed, with nothing logged to explain the change.

The fix adds the same `bookIsSoftDeleted` skip both scans were missing. In
`GetAllSeriesFileCounts` the skip goes in phase 1, where the book-to-series map
is built, rather than in the phase-2 file loop: a book excluded from that map
never has its `BookFile` rows attributed to the series, so one check covers both.

A new cross-backend conformance test (`TestSeriesCounts_ExcludeTrashOnBothPaths`)
runs the same fixture through both implementations with `UseMemDB` flipped and
asserts they agree. Both halves of the fix were mutation-verified — reverting
either one turns the test red on the specific count it governs.
