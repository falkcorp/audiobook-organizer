### Changed

#### Per-file intro results carry their own provenance and survive rescans

`BookFile` gains `IntroSourceHash` (the per-file done marker: a file is done when
it equals `FileHash`), `IntroRunOpID` (the operation that wrote the result) and
`IntroMeta` (model, language, clip start and length). The first two are on
`BookFileCore`; `IntroMeta` is stripped from memdb like `IntroTranscription`.
Each is classified in `bookFileFieldClasses`, so a library rescan keeps them, and
a file whose audio changed drops them together with its transcript.

The "rescans wipe the per-file transcription fields" bug in the design review of
2026-09-13 was already fixed on main by the shared book_file merge
(`bookfile_merge.go`); this change adds a test that names the whole intro group
(`TestRescan_KeepsPerFileIntroGroup`, `TestRescan_ReplacedFileDropsPerFileIntroGroup`)
instead of relying only on the reflection test.
