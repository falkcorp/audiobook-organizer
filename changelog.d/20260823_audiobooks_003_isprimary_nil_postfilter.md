### Fixed

#### `is_primary_version` post-filters now treat a nil flag as primary, matching storage's default

`GetAudiobooks`' author/series/search post-filter (`internal/audiobooks/service_query.go`)
classified a book with `IsPrimaryVersion == nil` as **non-primary**
(`b.IsPrimaryVersion != nil && *b.IsPrimaryVersion`). Both storage layers
disagree: `pebble_store.go`'s own `IsPrimaryVersion` filter and the memdb
`is_primary_version` index (`memdb_schema.go`, `Default: true`) both treat a
nil flag as primary. The mismatch meant a nil-flagged book fetched via
`GetBooksByAuthorIDCore` (which itself counts nil as primary) could then be
silently dropped from `?is_primary_version=true` results, or wrongly
included in `?is_primary_version=false` results, on the exact same request
that the library-wide pushdown path answered correctly.

The post-filter now reads `b.IsPrimaryVersion == nil || *b.IsPrimaryVersion`,
so nil books are classified the same way everywhere. The same fix was
applied to the identical expression in `CountAudiobooksFiltered`'s legacy
`GetAllBooksCore` fallback — a defensive, currently-unreachable branch (the
pushdown path added for fingerprint filters now covers every case that used
to fall through), but it carries the exact same faulty comparison and would
misclassify the same rows if it were ever exercised, so left inconsistent it
would silently reintroduce the bug for whatever future filter combination
finally reaches it.

See `TODO.md`'s `is_primary_version` investigation for the fuller context on
where nil-vs-explicit-false is handled across the codebase.
