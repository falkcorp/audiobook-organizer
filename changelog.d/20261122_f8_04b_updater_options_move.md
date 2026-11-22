### Changed

- internal/books: the core updater's options constructors (`CoreOptions`, `FetchOptions`, `ReleasedAsset`) and their tests moved out of `internal/updater` into `internal/books`, behaviour-neutral, so deleting the updater's gin half does not take them along. `updater fetch-release` reads the same options as before.
