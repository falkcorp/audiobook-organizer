### Fixed

#### The retention sweep re-scanned every operation once per page, and could report deleting more than it did

The maintenance job that deletes operation records older than the retention
window collected them by walking the operation listing in 500-row pages. The
method it paged over, `PebbleStore.ListOperations`, reads the *entire*
`operation:` keyspace into memory, unmarshals every row and sorts the whole set,
and only then slices out the requested page. Paging over a method shaped like
that pays for a full scan on every page.

Production held 10,163 operations when this was found, so a single retention run
did 21 complete scans — roughly 213,000 unmarshals to gather a set that one pass
yields. The cost grows quadratically with the number of operations, so it gets
worse exactly as retention becomes more necessary.

Paging also made the job's own count untrustworthy. The listing is newest-first,
so an operation created by anything else while the scan was running pushed every
existing row to a higher index; reading a fixed, increasing sequence of offsets
across a shifting list re-read rows it had already seen and put the same ID in
the delete list twice. Nothing was lost — deleting an already-deleted record does
nothing — but the job counted the repeat, so the number it reported was larger
than the number of operations it actually removed.

Phase one now takes the whole listing in a single call, which is what the
function's comment had claimed all along while the code did otherwise.
`ListOperations` gained the "no limit" sentinel this needs: a limit of zero or
less returns everything from the offset onward, matching `SearchBooks`, which
already documents the same convention. Previously a limit of zero computed an
empty page and returned no rows — a trap set for precisely the caller that wants
them all.

That sentinel is covered by a test against the real store rather than against a
test double. The retention job is exercised entirely through a fake, so removing
the sentinel from the real implementation left every test in that package green;
had it shipped that way, retention would have collected nothing, deleted nothing
and reported success. The new test fails with "should have 1200 item(s), but has
0" when the sentinel is reverted.
