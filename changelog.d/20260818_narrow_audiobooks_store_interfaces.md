### Changed

- Narrowed the store interfaces in `internal/audiobooks` to what a compiler probe
  measured they actually use. `metadataStateStore` went from two full `database.*`
  surfaces to the single method it calls (`RecordMetadataChange`), and
  `authorSeriesStore` from `AuthorReader`+`SeriesReader` wholesale to the 9 methods
  it reads, grouped by entity. No behaviour change: no function bodies were edited.
- Deleted `audiobookUpdateStore`, which restated `audiobookStore` under a second
  name. Its own direct-call set was one method (`GetBookByID`), and
  `NewAudiobookUpdateService` forwards straight into `NewAudiobookService`.
  This drops the tree's `interfacebloat` count from 5 to 4.

### Fixed

- `scripts/check-interface-width.sh` reported a finding count of `0` when
  `golangci-lint` failed to run at all — most often a stale v1 binary earlier on
  `PATH`, which exits 3 without linting because this repo's config is v2 format.
  The `0` was then diagnosed as "interface width went DOWN", a confident and
  entirely wrong message. The script now inspects the exit code and treats
  anything other than 0 or 1 as a run failure with its own message.
