### Changed

- Four more packages dropped `database.Store` for measured slices:
  `internal/organizer`'s two free functions (2 methods), `internal/deluge` (2),
  `internal/metabatch` (4) and `internal/diagnostics` (12). None had a
  forwarding constraint, so each is a leaf that had been carrying all 398
  methods to use a handful.
