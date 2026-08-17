- [ ] **Classify the 71,954 missing `book_file` rows by shape before any
      `missing-file-repair` apply.** Full-population audit
      (`docs/audits/2026-08-17-missing-file-audit-full-population.md`) proved two
      distinct populations: track-slash rows whose bytes are on disk under the
      `{track:02d}` name (repoint, never delete) and vanished-directory rows
      (delete is correct). `missing-file-repair` has no repoint mode and its
      per-book safety rule waves the recoverable rows through.
- [ ] **Decide the 16,265 books with no surviving file** (was believed to be 5,
      from a 120-book sample). Human decision, still open.
- [ ] **`missing-file-repair` dry run hit the 20,000 `max_deletes` cap.** The true
      repairable-row count is unmeasured; a capped apply looks complete but is not.
- [ ] **1,006 missing rows are under the iTunes tree**, contradicting the
      `missing_file_audit.go` header comment that says none are. Investigate
      separately — the iTunes tree is hands-off.
- [ ] **61 rows carry a mangled `/X:/books/itunes/Audiobooks` Windows path.**
