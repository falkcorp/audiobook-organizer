### Fixed

#### `make ci` staticcheck failure on main

Removed `PebbleStore.updateBookLocked`, a one-line wrapper with no callers left
after the series-invariant change routed every update through
`updateBookLockedMode`. staticcheck (run by `make ci`) reported it as unused
(U1000). Two comments that named it now name `updateBookLockedMode`.
