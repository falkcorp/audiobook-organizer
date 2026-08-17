- [ ] **Store-parameter narrowing: 24 declarations remain** (after PR #2503 narrowed 19).
      Measured 2026-08-17 by AST, not by grep — see
      `docs/audits/2026-08-16-store-interface-decomposition.md` §9.
      - 15 free helpers in `internal/plugins/maintenance`
      - 2 free helpers in `internal/maintenance/jobs` — `vgFixAuthorDirPath`
        (`fix_version_groups.go:276`), `ddMergeDuplicateBook` (`dedup_books.go:329`)
      - 7 non-`Run` methods
      The `Run` methods themselves (37 of them) are interface-constrained and are **not** in
      this count. Pattern choice (B narrow-interface / C split-the-decision / D pass-the-func)
      is awaiting a decision — comparison in
      `.claude/notes/2026-08-17-option-b-vs-c-comparison.md`. Do not sweep C.
