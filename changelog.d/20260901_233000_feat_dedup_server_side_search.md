### Added

- The Dupes review panel's search box now searches the whole queue instead of the rows
  already on screen. It previously filtered the loaded page only — 50 of 40,251 candidates
  in production — so typing a title that sat on any other page returned nothing, and "no
  results" was indistinguishable from "not in the queue". The term now round-trips, and the
  count beside it reports matches across the queue rather than matches on the page.
- Dupes search matches a candidate's layer, band and entity IDs, plus the title, author and
  file path of the books on either side of the pair.

### Fixed

- Searching duplicate candidates by author name now works at all. The panel read an
  `author_name` field that the TypeScript type declared but the API never sent, so the
  optional read yielded `undefined`, an empty string took its place, and author never
  matched anything. Author is now resolved through the author table on the server.

### Changed

- The search debounce and the "the server has answered, stop filtering locally" rule are now
  shared by the Dupes and Regroup lanes instead of living in one of them. The second rule is
  a correctness guard, not a tidy-up: the server's search is wider than anything the browser
  can compute, so a local filter left running discards rows the server correctly found.
