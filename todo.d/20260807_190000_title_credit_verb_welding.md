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
      **Author and narrator do NOT have this defect** (0 of 149 / 0 of 89).

- [ ] **Parsed author/narrator occasionally overrun the credits block.**
      Same sample: 1/149 authors and 3/89 narrators run past the credits into
      chapter text — `'Vofon. Translated by CoRansome. Chapter 0'`,
      `'Katana Jones, Chapter 24 Kongen Serven'`. ~1-3%, a boundary-detection
      miss rather than a suffix problem, so a trailing-verb strip will NOT fix
      it. Lower priority than the title welding; do not conflate the two.
