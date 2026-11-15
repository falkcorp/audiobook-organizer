- [ ] **TODO-F5-07-CHAPTERS-KEY** Decide whether `maintenance.chapters-backfill` should
      share a concurrency key with another whole-library probe op. HEAD declares
      `maintenance.probe-directory-books` for it, and a comment said it shared the
      ffprobe lane with probe-directory-books, but that op's own key is
      `maintenance.relink-unlinked-books`, so the chapters key serialises it with
      nothing. F5-07 maps the key 1:1 and does not change it. Acceptance: either
      chapters-backfill and the other probe op declare one key (and a test pins it),
      or the comments say the lane is chapters-only. Brief: seams note
      `docs/agent-tasks/platform-core/seams.d/F5-07.md`, "Flag for owner".
