### Added

#### Per-file intro classification and `maintenance.reparse-file-intros`

Each stored per-file intro transcript now carries a classification of what the clip IS: title announcement, credits, chapter marker, part marker, front or back matter (prologue, epilogue, interlude, afterword, foreword, introduction), narration, music or silence, or garbage. Beside the kind it stores the fields read from the text: chapter number, chapter title, part number, the marker word, title, author, narrator, translator, cover artist, publisher, and the first and last book of a "Books N through M" announcement. It is a new additive column on the book file (`intro_parse`, versioned); `ok` and `unparsed` and the `Transcribed*` columns keep their meaning, and chapter continuity stays separate from identity.

The new op `maintenance.reparse-file-intros` re-derives all of this from the stored raw text with no Whisper call, so grammar fixes reach the rows already stored. Dry run is the default and reports before and after counts per kind. A live run is resumable (a contiguous-completion watermark checkpoint, every write idempotent), runs on a bounded pool, writes each file through a compare-and-swap that touches only the classification and the identity and status it names, and refuses to start while a scan or bulk apply is running. It never downgrades identity: identity is written only by a title-announcement parse, a field is never emptied, a stored value is replaced only when it is defective and the new one is not, and an `unparsed` row becomes `ok` only when a title is parsed. Rows it cannot re-parse (no transcript, a blank transcript, a parse written by a newer grammar) are counted by class.

### Changed

#### Intro grammar fixes

Roman numerals, spelled-out numbers, a bare "12." heading, "chapter the Nth" and the part, book, section, disc, track and episode markers are recognised, as are un-numbered prologue, epilogue, interlude, afterword, foreword and introduction. A bare "continued" is narration, not a chapter. "Publisher presents, Title" keeps the credit and removes the publisher wording from the title, including when Whisper punctuates after "presents". "I.", a year, a time, "Part of me" and "Book him" no longer read as markers. A clip with only a sound tag, or only a credit verb, no longer yields an identity. New transcriptions derive their identity with the same grammar the re-parse uses, so a freshly written row is a fixed point of the re-parse.

#### Model and language reach the stored provenance

`BatchResult` and the dispatcher transport now carry the Whisper model and language each file was answered with, so `IntroMeta` records them. Language stays empty until the workers report it, because the worker scripts fix it as `en` and do not yet return it.
