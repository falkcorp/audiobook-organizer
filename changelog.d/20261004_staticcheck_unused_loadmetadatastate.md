### Fixed

#### `make ci` staticcheck failure on main (metafetch)

Removed `metafetch.Service.loadMetadataState`, a wrapper with no callers left
after the metadata-state save paths moved to `WithStateSnapshot` and
`LoadStateSnapshot`. staticcheck (run by `make ci`) reported it as unused
(U1000). A repo-wide `staticcheck ./...` is clean after this change.
