### Fixed

- **Documented that compound narrator names are never split into individual
  narrators.** A book credited to "Michael Kramer & Kate Reading" is stored as one
  narrator record whose name is the whole string, so narrator filtering and
  faceting miss every multi-narrator book. The `BookNarrator` join table already
  supports many-to-many — the rows just never get created, because nothing splits
  on the ingest path. The only splitter in the repo lives inside
  `OptimizeDatabase`, runs only when invoked, and matches `" & "` and nothing else,
  so comma-separated credits stay compound.
