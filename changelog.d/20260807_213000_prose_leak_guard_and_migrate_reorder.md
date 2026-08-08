<!-- file: changelog.d/20260807_213000_prose_leak_guard_and_migrate_reorder.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f7a2c91-8e5b-4d06-9a3f-1c62b8d47e05 -->
<!-- last-edited: 2026-08-07 -->

### Fixed

- Intro classifier no longer extracts credits from pure narrative prose. A
  mid-sentence bare "by" could split multi-sentence narration into a short
  "title" whose sentence-initial pronouns are capitalized ("…people. We only
  wished…"), evading the deliberately case-sensitive prose-pronoun check — 2 of
  12 sampled prod transcripts stored garbage parsed fields this way (one
  transcribed_title was ~1,000 chars of dialogue). Uncorroborated titles (no
  credit verb) spanning multiple sentences are now rejected (honorifics and
  initials like "Mr. Mercedes" exempted), and a hard 120-char title cap applies
  regardless of word count.
- `maintenance.intro-migrate-single-file` now reports zero-`book_file`-row books
  as `skip_no_book_file_rows` even when the book-level transcript is also
  missing (previously they all landed in `skip_book_has_no_transcript`, so the
  zero-row bucket read 0). Corrected the file comment falsely claiming those
  books are "unlinked, not un-transcribed" — measured 2026-08-07, the ~1,122
  zero-row books have neither file rows nor a transcript, so relinking alone
  will not give them one.
