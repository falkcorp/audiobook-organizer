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
