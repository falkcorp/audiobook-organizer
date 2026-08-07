<!-- file: todo.d/20260807_192000_chapter_terminator_only_matches_chapter_one.md -->
<!-- version: 1.0.0 -->
<!-- guid: d41f8a63-2b95-4e07-8c1d-93b6f0a2e578 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **`nameBoundaryRe` only terminates on "chapter one"/"chapter 1" — every
      other chapter number leaks into author/narrator.**
      `internal/transcribe/parse.go:46` enumerates
      `chapter one|chapter 1|prologue|part one|part 1`. Verified 2026-08-07
      against the real corrupted values from prod:

        LEAKS       'Vofon. Translated by CoRansome. Chapter 0'
        LEAKS       'Katana Jones, Chapter 24 Kongen Serven'
        LEAKS       'Victor Baveen. Chapter 12 Trickster Teeth'
        TERMINATES  'Eric Rounds Chapter 1 Catalyst'

      Second-order quirk: `chapter 1` is followed by `\b`, so **chapters 10-19
      also leak** ("chapter 12" does not match "chapter 1" + boundary). In
      practice ONLY literal "chapter one" and "chapter 1" ever terminate.

      Fix — generalise instead of enumerating:

        chapter\s+[\w.]+|prologue|epilogue|part\s+[\w.]+|
        book\s+[\w.]+|volume\s+[\w.]+

      🔴 **SEQUENCING: fix this BEFORE running tiers 1/1b and 2/3.** Tier 0 was
      single-file books whose clip opens at Chapter 1 — the one case that works,
      which is why the measured leak rate was only ~3.4%. Tiers 1-3 are
      MULTI-FILE books whose clips come from files opening at Chapter 7, 12,
      24 — exactly the leaking values. Running those tiers first would corrupt
      author/narrator across the long tail and require a second reparse to undo.

      Belongs in the same PR as
      `todo.d/20260807_191500_anchor_based_credit_parsing.md` — same code path,
      same boundary logic.
