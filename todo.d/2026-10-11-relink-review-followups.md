- [ ] **RELINK-REVIEW-FOLLOWUPS** Round-3 review of the relink pre-fix imports
      fixer (SHOULD-FIX S1 + nits). S1: once #3957 (14-day trash window counted
      from `trashed_at`, purge-now limited to trashed versions) and the relink
      PR are both on main, reword the purge timing in five places to match:
      the fixer's `Description()` and type comment
      (`internal/plugins/maintenance/relink_pre_fix_imports_fixer.go`), the
      comment in `internal/versions/ingest.go`, the relink executive summary
      and its changelog fragment. N1: three comments and the changelog say a
      row whose class moves between plan and apply is refused; a sibling flip
      (`would_link` -> `same_book_version`) is applied, only a fingerprint
      change is refused. N2: `pfrAdopt` reports "the version changed" when the
      file record left the book; return `ErrBookFileChanged`. N3: relabel
      "dates far apart" and "Same-book link: every check agrees (review)".
      N5: add a test pinning the raw planned-path compare in adopt-only.
