### Changed

- `audiobooks.NewOrganizeService` no longer takes `database.Store`. It takes
  `organizer.Store` composed with `metafetch.Store` — the two services it
  actually forwards its store into. `metafetch`'s consumer interface is exported
  for this, matching the `organizer.Store` precedent. This was the last
  `database.Store` constructor parameter in `internal/audiobooks`.
