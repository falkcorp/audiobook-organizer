### Changed

- Chapter fragments fixer (`fragment-consolidation`): a numbered chapter set
  sitting directly in an author folder (for example
  `iTunes Media/Audiobooks/<Author>/01 ....mp3`) now takes the combined book's
  title from the album tag every file shares, read from the stored file tags
  (`book_file.raw_tags`, keys `album`/`ALBUM`/`TALB`/`TAL`/`©alb`). A set whose
  files do not all share one album, or whose album is generic ("Audiobooks",
  "Unknown Album") or just the author folder's or author's name, stays held for
  its title as before. All other checks are unchanged. Owner decision
  2026-10-08.
