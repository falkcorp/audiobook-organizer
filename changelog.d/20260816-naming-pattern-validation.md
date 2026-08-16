### Fixed

- **A naming pattern that silently strands files is now rejected at the point
  it is set.** Two shapes are refused: a path separator in
  `file_naming_pattern` (it names one file, not a directory path), and a file
  pattern with no `{track}` / `{track:02d}` / `{track_title}` placeholder. The
  second looks entirely reasonable — `"{title} - {author} - read by {narrator}"`
  was once a shipped default — and is catastrophic for multi-file books,
  because every track expands to the same name and all but the first are left
  behind as `.tmp-rename`.

- **Path components are normalized to NFC.** macOS produces NFD and Linux
  produces NFC, so the same title arrived as two different byte strings and
  created two directories that render identically. Korean is the sharp case:
  NFD decomposes a Hangul syllable into jamo, so `해리` is 6 bytes composed and
  12 decomposed with no visual difference.

- **Component truncation is rune-aware.** The 200-byte cap was a byte slice, so
  any Japanese, Korean or Chinese title over ~67 characters was cut mid-rune
  and the result was not valid UTF-8. The filesystem rejects that outright with
  `EILSEQ`, meaning such a book could not be organized at all.

- **Invisible and filesystem-hostile characters are stripped or escaped:** C1
  controls, zero-width space, BOM, bidi overrides and line/paragraph
  separators; Windows reserved device names (`NUL`, `COM1`, and `NUL.m4b` too)
  and trailing dots/spaces, which NTFS silently strips. Zero-width
  joiners/non-joiners are deliberately preserved — they are meaningful in
  Devanagari conjuncts and bind emoji sequences.

### Changed

- `folder_naming_pattern` and `file_naming_pattern` defaults are now declared
  once as `DefaultFolderNamingPattern` / `DefaultFileNamingPattern` instead of
  being hand-copied into two places.
