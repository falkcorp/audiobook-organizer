### Changed

- **Repair jobs have their own chapter-length limit.** The fragment
  consolidation repair used the import scanner's `chapter_consolidation_threshold_min`
  (10 minutes) to decide whether a file is a chapter or a whole book, so it
  held any chapter set with a file of 10 minutes or more. It now reads a new
  setting, `repair_chapter_max_min`, default 120 minutes. The import scanner
  keeps its own 10-minute setting unchanged, and the folder-books repair keeps
  using the import value for its "too short to be a book" check. A value of 0
  or less means the default, never "off".
