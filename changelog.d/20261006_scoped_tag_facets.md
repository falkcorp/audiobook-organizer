### Added

#### Library Browse-by-Tag chips are scoped to the current results

The tag cloud used to list every tag in the library with library-wide counts
(`language: en (28697)`) no matter what was searched or filtered, so it could
not help narrow a result set. It now lists only the tags carried by the books
the current request matches, with counts over those books. `GET
/api/v1/audiobooks/facets?scoped=1` takes the list's own parameters (search,
filters, tags[], library_state, is_primary_version, show_quarantined, ...) and
parses them with the list handler's own parser (`parseListRequest`), and the
match set comes from the list pipeline itself (the search result cache entry
for a cacheable search, else `queryAudiobooks` uncapped), so a chip count
always agrees with what clicking it shows. Answers are cached per request and
store change-log generation; tag writes invalidate them.

Clicking a chip appends `tag:"value"` to the search box; clicking an active
chip removes it. A results set with no tags says so instead of hiding the
panel.

### Fixed

#### A second `tag:` term or tag chip did not narrow the Library

The list request sent only the first `tag:` term from the search box, and none
at all once a tag was selected in the sidebar. All non-negated `tag:` terms
and selected tags are now sent together (ANDed).

### Changed

#### `/audiobooks/facets` cold path no longer decodes every book row

`GetGenreCounts` and `GetDistinctLanguages` walked every Pebble book row with a
JSON decode each — the cold cost behind a 5.8 s mean on the facets endpoint.
They now walk memdb's in-memory rows when memdb is serving (same rows, same
answer; Pebble remains the pre-warmup fallback). ABS `/filterdata` uses the
same two functions and benefits too.
