### Fixed

- **A folder of tracks named `Name 001`…`Name 080` was imported as one book per
  track instead of one audiobook.** The multi-file detector's sequence-number
  patterns covered `Chapter NN`, `Part N of M`, `Track NN`, `Disc NN`,
  `(76 of 85)`, a LEADING `01 - `, and a bare `01` — but nothing matched a
  **trailing** number, which is one of the most common ways a ripped audiobook
  names its tracks.

  With no number extracted from any file, the detector's pattern quorum failed and
  the folder was never grouped, so the scan wrote a separate book row per file —
  each titled with its own file stem, each taking the folder name as its author,
  each in its own version group. Measured on the production library: an 80-file
  folder became 80 books.

  Note that the tag requirement was never the obstacle. The detector already
  groups untagged tracks on sequential filenames alone (AP-5); it simply could not
  read the filenames.

  Fixed by one pattern, ordered last so keyword forms still win — `Part 1 of 8`
  must keep extracting 1, not the total. A folder of unrelated titles that happen
  to end in a year is still rejected by the existing density check.

  **This prevents new bad rows. It does not repair rows already written that way.**
