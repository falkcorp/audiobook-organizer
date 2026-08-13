### Fixed

- Filtered library queries no longer return almost the entire library when the
  in-memory index is unavailable. During the ~2 minute startup warmup (and
  permanently when the in-memory index is disabled or has been abandoned after a
  write-buffer overflow), searching or filtering the library fell back to a path
  that applied only two of its eight filters — so a title search returned every
  book, with a matching total to go with it. The fallback now applies every
  filter, and the total is computed by the same code that selects the rows, so
  the two cannot disagree. As a side effect the fallback no longer loads the
  whole library into memory before filtering; it only builds the rows that match.
