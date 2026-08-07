<!-- file: todo.d/20260807_190500_zero_bookfile_books_lack_transcripts.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c2e5b04-71af-4d93-a6c8-1e7f40b95d2a -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Correct the claim that zero-`book_file` books are "unlinked, not
      un-transcribed".** `internal/plugins/maintenance/intro_migrate_single_file.go:52`
      asserts this; the tier-0 dry-run on 2026-08-07 disproves it.
      `skip_no_book_file_rows` came back **0**, not the expected ~1,122, because
      the transcript check at `:225` runs BEFORE the row check at `:237`. The
      arithmetic places those books in `skip_book_has_no_transcript` instead:
      1,415 − 147 (single-file, no transcript) − 145 (multi-file, no transcript)
      = **1,123** ≈ the 1,122 zero-row books.
      They therefore have **neither** file rows **nor** a book-level transcript.
      🔴 Impact on **#6 follow-through**: relinking them will NOT hand them a
      transcript. They need real GPU transcription afterwards, so budget for it.
      Fix the comment, and consider ordering the two checks so the row-shape
      reason is reported even when the transcript is also missing.
