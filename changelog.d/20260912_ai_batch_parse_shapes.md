### Fixed

#### AI filename parsing no longer aborts on folders like "Disc 1" or "Season 2"

The `library.ai-parse` operation failed whole batches with `json: cannot
unmarshal object into Go value of type []*ai.ParsedMetadata`. For
filenames that carry no metadata at all (`Disc 1`..`Disc 8`, `Season 2`) the
local model answers `{"results": []}`. The batch parser rejected an empty
`results` list and then tried the reply as a bare array, which fails on any
object. Three such batches tripped the phase's failure threshold, and one run
aborted with 74 of 106 books unparsed.

`{"results": []}` now means "nothing found": every book in the batch is saved
unchanged and marked as attempted. That is the same outcome as the model's
other rendering of the same answer (one entry per filename with every field
null). `ParseBatch` now always returns exactly one entry per filename or an
error. A results list that is shorter or longer than the batch is an error,
because results are matched to books by position and a dropped entry would
put one book's title on another. The parser also accepts an array wrapped
under a single key other than `results`, and a bare object when the batch has
one filename. Neither shape has been observed yet.

The single-book parser (`ParseFilename`, `ParseAudiobook`, `ParseCoverArt`)
used to decode a wrapped reply or `null` into empty metadata with no error. It
now unwraps a single-result wrapper and rejects everything else. Both parsers
include a truncated, log-sanitized excerpt of the reply in their errors, so
the operation log shows what the model sent.
