<!-- file: todo.d/20260807_190000_title_credit_verb_welding.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9c1a72-6d84-4b1e-9f30-5c8ab2e74d61 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Strip credit verbs welded onto parsed titles (~44% of transcribed titles).**
      Measured 2026-08-07 on a 360-book / 219-file sample from prod:
      **66 of 149** non-empty `transcribed_title` values end in a credit verb —
      `'The Distance, written'`, `'Empire of Storms, Book 5 in the Throne of
      Glass series, written'`, `'Anna Akmatova, translated'`. That is **44.3%**,
      notably worse than the 24.8% recorded in
      `docs/continuation/2026-08-07-repair-then-backfill-continuation.md`; one of
      the two numbers is wrong, so re-measure before quoting either.
      The transcript text itself is correct — this is purely a parser defect, so
      the fix is reparse-only (no GPU). #2170's reparse guard only ever upgrades,
      so re-running is safe by construction.
      🔴 Tier-0 (`maintenance.intro-migrate-single-file`, applied 2026-08-07)
      copied the parsed fields onto 33,633 `book_file` rows, so the welded titles
      now exist per-file as well — the reparse must cover BOTH levels.
      ⚠️ **CORRECTION (re-measured same day).** An earlier note here claimed
      "author and narrator do NOT have this defect". That was WRONG, and wrong
      for a methodological reason worth remembering: the predicate was anchored
      to the END of the field (`[,\s]+VERB\s*$`), so it could only ever see a
      TRAILING verb. Translator contamination sits MID-field
      (`'Vofon. Translated by CoRansome. Chapter 0'`) and was structurally
      invisible to it. Re-measured over 324 transcripts with a contains-anywhere
      predicate:

        author    4/215 = 1.9%  (all translator credit)
        narrator  4/120 = 3.3%  (chapter headings + Introduction/Foreword)
        title   105/215 = 48.8% (40.9% trailing verb + 9.3% chapter)

      The author figure UNDERSTATES the severity: translations are only ~3.8% of
      the library, so ~half of all TRANSLATED works have a corrupted author. It
      is a near-deterministic failure of a small class, not a low-rate random
      defect — do not dismiss it as "1.9%, basically clean".

- [ ] **Parsed author/narrator overrun the credits block.**
      Re-measured: 4/215 authors and 4/120 narrators run past the credits into
      chapter text or adjacent role credits — `'Vofon. Translated by CoRansome. Chapter 0'`,
      `'Katana Jones, Chapter 24 Kongen Serven'`. ~1-3%, a boundary-detection
      miss rather than a suffix problem, so a trailing-verb strip will NOT fix
      it. Lower priority than the title welding; do not conflate the two.
