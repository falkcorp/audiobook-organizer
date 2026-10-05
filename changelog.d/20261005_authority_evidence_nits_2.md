### Changed

- **Authority-evidence review nits (review of #3759).**
  - When a repairs plan waits for the authority snapshot load, the log line
    now says why the wait ended: the load finished, the caller's context
    ended, or the wait limit was reached. Each wait logs one line. A wait that
    ends with the lists is an Info line (when it took over a second). A wait
    that ends without them is a single Warn line with the wait's length,
    instead of an Info and a Warn for the same event.
  - The wait bound and log threshold are fields on the authority source,
    defaulting to `authorityAwaitMax` and `authorityAwaitLogAfter`, so a test
    covers the wait log.
  - Comment fixes: `isNilLookup` documents that a nil map, slice, func or
    nil-safe pointer Lookup is read as "no lists", and the combined-author
    fixer names `authorityAwaitMax` instead of hardcoding "two minutes".
