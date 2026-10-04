### Fixed

#### Pebble metrics scrape can no longer hang or break a store close

The OpenLibrary Pebble sample now uses `TryLock` on the service mutex, so a scrape that lands during an OpenLibrary data delete or store open drops only that store's series instead of hanging all of `/metrics`. The closed-store check no longer opens a Pebble snapshot (which could make a concurrent `Close` fail with "leaked snapshots"); each store sets its own closed flag instead. The `pebble_*` sources are released at server shutdown, only if a newer server has not replaced them.
