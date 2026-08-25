### Fixed

- **An organized copy could become the version group's primary while owning none
  of the audio.** When `CreateOrganizedVersion` copied a book's per-file rows to
  the organized copy, a read failure was discarded by an `err == nil` guard and a
  write failure by `_ =`. Both fell through to the version-group handover, which
  demotes the original to `organized_source` and marks it non-primary. The result
  was a version group whose primary row had no files and whose superseded row held
  the only audio — reported as a successful organize, with nothing logged.

  Both errors are now handled. The per-file copy goes through
  `BatchCreateBookFiles`, which is atomic, so a failure writes no rows at all and
  transfers no iTunes persistent ID off the original's file rows. On failure the
  half-built organized copy is rolled back — author links cleared, book row
  deleted, copied files removed under the library root — and the error is returned
  before the original is touched, so the original keeps its audio *and* stays
  primary.

  A book with zero file rows is still legitimate and is unaffected; that is the
  case `ensureSingleFileBookFile` backfills.
