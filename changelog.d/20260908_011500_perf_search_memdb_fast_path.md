### Fixed

- **Library search no longer scans the whole library from disk on every
  keystroke.** `SearchBooks` iterated every author row to build a name map, then
  every book row, `json.Unmarshal`-ing each one — and it could only stop early
  once it had filled the requested limit. A query that matched *nothing*
  therefore paid for the entire library. Measured on production at ~121K books:
  **5.35 s for a zero-match query** against 0.6 s for a common word that filled
  the limit early. Because the cost was Pebble block reads, it landed on whatever
  else the disk was doing. AudioBooth searches per keystroke, so it hit this on
  the first character and timed out; the retry found the warm cache and returned
  instantly, which is why the failure looked like "timeout, then zero results".
- Search now matches against the in-memory book table when memdb is warm — no
  disk reads and no per-row unmarshal — and falls back to the original scan when
  it is not. This fixes the second half of the AudioBooth search failure; PR
  #3125 fixed the response *size*, but latency was flat across `limit=1` and
  `limit=25`, which is what showed the bottleneck was upstream of truncation.
- **The matching predicate is transcribed from the disk scan quirk-for-quirk**,
  because `SearchBooks` also backs the audiobooks query service and the iTunes
  handler's overfetch window. Author names compare via `NormalizeAuthor` while
  titles and narrators compare via bare `ToLower`; `offset` skips the first N
  *matches* rather than the first N books; soft-deleted books are included. All
  three are pre-existing behaviours, reproduced rather than "cleaned up" so this
  change stays a pure speed-up. A conformance test asserts the two paths return
  equal results — same rows, same order, same truncation.
- **Results are re-read from Pebble rather than returned from memdb.** memdb
  holds a projection with `Description`, `VersionNotes` and the `BookSig*` fields
  stripped, so returning its rows directly would have compiled, passed every
  ID-based test, and silently blanked the description on every search result.
  The hydrating read deliberately skips the `book_sig:` sidecar that
  `GetBookByID` folds in — that would have added ~22 KB of base64 per hit and
  undone the payload reduction from #3125.
