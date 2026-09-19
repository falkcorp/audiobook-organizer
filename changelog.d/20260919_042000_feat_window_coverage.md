### Added

- The signal-coverage report (`GET /api/v1/signals/coverage?windows=true`) now counts windowed fingerprints: how many present files have one, how many have a recorded failure, and how many have neither, split into files of books with missing parts versus the rest. It is a full count of every file, not a sample. Add `deep=true` to also tell current windows from ones made by an older pipeline or tool version. Splitting the older opening-credits fingerprints by era waits on a later change.
