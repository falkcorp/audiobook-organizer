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
under a single key other than `results` when at least one element is a
metadata object, and a bare object when the batch has one filename. Neither
shape has been observed yet.

Error payloads are never taken as "nothing found". `results` is accepted only
when it is the reply's sole key, so `{"results": [], "error": "context length
exceeded"}` is an error instead of an empty parse for every book in the batch.
Each result must be `null`, `{}`, or an object with at least one metadata
field. An object whose keys are all foreign, such as `{"error": "x"}` or
`{"code": 500, "message": "overloaded"}`, fails the reply. An empty parse is
saved as the book's AI result and recorded in the scan cache, so a book that
got one would never be sent to the model again.

The single-book parser (`ParseFilename`, `ParseAudiobook`, `ParseCoverArt`)
used to decode a wrapped reply or `null` into empty metadata with no error. It
now unwraps a single-result wrapper, checks what the wrapper holds the same
way, and rejects everything else.

Both parsers return reply failures as `*ai.ReplyParseError`. The error
includes a truncated excerpt of the reply so the operation log shows what the
model sent. The excerpt escapes control characters, Unicode format characters
(bidi overrides and isolates, zero-width characters), and U+2028/U+2029. The
scanner's permanent-failure check now returns false for a `ReplyParseError`
before it matches any text. It used to search the whole message for provider
markers such as `permission_error` or `invalid_api_key`. A book title echoed
into a malformed reply could then abort the AI phase and stop the parser chain
from trying the next backend. A malformed reply now counts toward the phase's
ordinary failure threshold, and the chain falls through to the next backend.
