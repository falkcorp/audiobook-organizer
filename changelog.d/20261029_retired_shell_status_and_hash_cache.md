### Fixed

#### A book whose retired versions were all un-retired is no longer treated as a retired shell

The scanner asked "was a retired version ever made from this book" and counted
the versions. A version that `UnretireBookFiles` emptied stays `Status=retired`
with no rows (the retire revert deletes it through a check that needs the
status, and a retire apply adopts an empty retired version of its own op), so
the book stayed a shell for ever and every scan lookup on it paid the extra
reads and took the shell branch. `isRetiredShell` now counts the retired rows
the book's versions still hold (`database.RetiredRowsHeldFor`, the check the
restore and purge guards already make), so a shell with nothing left to hold is
an ordinary book again. The now-unused `database.RetiredVersionsFromBook`
helper is removed.

### Changed

#### Retired-content hash lookups are cached for the length of a scan

While memdb is not serving and retired versions exist, every hash lookup the
scanner made re-enumerated every retired version and read each one's rows. A
scan (`ProcessBooksParallel`, so also `ProcessBooks`, the library scan's chunks
and the folder auto-scan) now opens `database.BeginRetiredHashScope`, and
`RetiredBookFilesWithHash` reads the retired rows once into a map keyed by hash.
A generation counter, bumped after every retire, un-retire, rehome and rewrite
of a retired row's hashes, makes the next lookup rebuild, so a concurrent
change is never answered from a stale map. Only the retired side is cached: the
live side (a row the scan itself just created) is read on every call. Overlapping
scans share one cache and the last to finish drops it.
