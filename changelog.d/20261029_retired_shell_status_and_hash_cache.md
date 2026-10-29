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
library scan (`PerformScan`) now opens `database.BeginRetiredHashScope` once for
the whole run, across all of its chunks; `ProcessBooksParallel` opens a scope of
its own for the entry points that do not come through `PerformScan`
(`ProcessBooks`, the folder auto-scan), and the store counts overlapping scopes
so the nested ones share the cache. Inside a scope `RetiredBookFilesWithHash`
reads the retired rows once into a map keyed by hash. A generation counter,
bumped after every retire, un-retire and rehome and after any rewrite that
changes a retired row's hashes, holder, version or retired flag, makes the next
lookup rebuild, so a concurrent change is never answered from a stale map. Only
the retired side is cached: the live side (a row the scan itself just created)
is read on every call.

The lookup returns `database.RetiredRowRef` (row, holder and version ids, all the
scanner reads) in a scope and out of one, so the answer has one shape and the
cache keeps three ids per hash instead of a decoded row. Building the map now
scans each holder's rows once, however many retired versions it holds, and
decodes only the fields it indexes, skipping the raw fingerprint that made a
row up to 230 KB.

#### The retired-shell check decodes only what it counts

`isRetiredShell` is asked up to three times for each scanned shell. On a Pebble
store the count now reads one snapshot: the shell's retired versions are listed
once, and each holder's rows are decoded into just the retired flag and version
id, not as whole rows with their fingerprints.
