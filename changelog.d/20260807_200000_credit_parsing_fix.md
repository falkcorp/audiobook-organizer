<!-- file: changelog.d/20260807_200000_credit_parsing_fix.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0cab59d5-584b-4c66-b696-42680b634c16 -->
<!-- last-edited: 2026-08-07 -->

### Fixed

- **Credit parsing no longer contaminates title, author or narrator.** Chapter
  headings, translator credits and cover-art credits leaked into the adjacent
  name field because the boundary list enumerated `chapter one|chapter 1` and
  had no anchor for the other roles. Replaying 346 production transcripts, the
  number of contaminated fields drops from **103 to 0**.

### Added

- **`transcribed_translator` and `transcribed_cover_artist`** on both books and
  book files, with anchors that find each role wherever it appears in the credit
  run, so credit order does not affect the parse.
- **Combined "written and narrated by X" credits** are now recognised, which also
  recovers the narrator instead of leaving it empty.
