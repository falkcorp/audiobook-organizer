### Changed

- Four plugins dropped `database.Store`: `acoustid` (7 methods), `deluge` (6),
  `metafetch` (2), and `itunes` — whose store field was **deleted**, not
  narrowed, because it was never read.

### Fixed

- `acoustid`'s reset-all op resolved its batched fast path with a bare
  `p.store.(*database.PebbleStore)`. That fails through the `indexedStore`
  decorator the server installs at startup, so in production the op silently
  took the per-row fallback — roughly 100× slower, with nothing logged to say
  the fast path had been skipped. It now uses `database.AsPebbleStore`.
