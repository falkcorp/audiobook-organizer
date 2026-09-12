### Added

#### Test: book counts now pinned to agree with book listings for books with no primary-version flag

`CountAudiobooksFiltered` treats a book with no `is_primary_version` flag as
primary, in the same way the listing does, but no test covered that. Replacing
the count's shared helper with a plain `!= nil && *` check (which treats the
unflagged book as non-primary) left every test in `internal/audiobooks` passing.
`TestIsPrimaryVersion_CountAgreesWithListing` now compares the count with the
listing for the same filters on each count path: the store's count pushdown,
the summary fallback for stores that cannot push the filter down, and the
materialize-and-count fallback at the end of `CountAudiobooksFiltered`. The
test also checks which books the listing returned, so a count and a listing
that are both wrong in the same way still fail.

The materialize-and-count fallback runs only when a tag lookup fails once and
then succeeds, so the test forces that with a store that fails one tag lookup
on request. A store without pushdown does not reach that loop.
