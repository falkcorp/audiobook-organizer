### Added

#### iTunes smart playlists now import from the XML export

The importer previously read only the binary `iTunes Library.itl`, which
extracts **zero** smart playlists from real iTunes 12.13.10.3 libraries. The XML
export of the *same* library carries **292**, each with an intact Smart Criteria
blob. iTunes maintains both files, so this reads the one that is legible.

`maintenance.itunes-playlist-import` now takes `libraryPath` and picks the
reader by extension, defaulting to the configured `itunes.library_read_path`
(usually the XML). The old `itlPath` parameter still works. Reading the binary
`.itl` now logs a warning pointing at the XML.

Measured on the live 153 MB export: **351 playlists, 292 smart**, parsed in
under four seconds.

### Fixed

#### An import will no longer create hundreds of silently-empty playlists

The Smart Criteria translator currently yields empty queries, because
`ParseSmartCriteria` misreads the format and returns rules with no field,
operator or operands *without* erroring. The op now always evaluates a dry run
first and **refuses to apply** when playlists would be created with an empty
query, naming how many. `allowEmptyQueries=true` overrides it — the raw criteria
blob is stored on every imported row, so shells imported that way can be
re-translated once the parser is fixed.
