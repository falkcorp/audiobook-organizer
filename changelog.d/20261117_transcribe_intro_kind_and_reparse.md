### Added

#### Per-file intro classification and `maintenance.reparse-file-intros`

Each stored per-file intro transcript now carries a classification of what the clip IS: title announcement, credits, chapter marker, part marker, front or back matter (prologue, epilogue, interlude, afterword, foreword, introduction), narration, music or silence, or garbage. Beside the kind it stores the fields read from the text: chapter number, chapter title, part number, the marker word, title, author, narrator, translator, cover artist, publisher, and the first and last book of a "Books N through M" announcement. It is one new additive column on the book file (`intro_parse`, versioned) that nothing reads yet. The `Transcribed*` identity columns and the `ok` / `unparsed` status are untouched and keep exactly the values the transcription run stored, because guards, the metadata search query and the apply gate read them.

The new op `maintenance.reparse-file-intros` fills that column for the rows already stored, from the stored raw text, with no Whisper call. It writes `intro_parse` and nothing else. Dry run is the default and reports, besides before and after counts per kind, a disagreement report that changes nothing: for the rows whose clip is a title announcement, how many agree with the stored identity, how many would fill an empty field and how many conflict (publisher prefix, truncation, a different title, a franchise term lost, another field), the stored titles the grammar itself rejects, and up to ten sample file ids per class (ids only). A live run is resumable by the key of the last completed file, runs on a bounded pool, writes each file through a compare-and-swap, and refuses to start while a scan or bulk apply is running. Rows it cannot re-parse (no transcript, a blank transcript, a parse written by a newer grammar) are counted by class.

### Changed

#### Model and language reach the stored provenance

`BatchResult` and the dispatcher transport now carry the Whisper model and language each file was answered with, so `IntroMeta` records them. Language stays empty until the workers report it, because the worker scripts fix it as `en` and do not yet return it.

#### New transcriptions also store the classification

A new per-file transcription stores its `intro_parse` next to the result. Its title, author, narrator, translator, cover artist and status come from the same grammar as before, unchanged and pinned by a golden over the existing corpus.
