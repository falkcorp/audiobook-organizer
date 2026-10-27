- [ ] **CREDITS-PR3-RUN** Run `maintenance.credits-backfill` on prod: the dry run
      first (`{}`), owner reads the classes, samples and `filing_author_changes`,
      then the owner runs the apply in `limit` cohorts (each write rewrites the
      book row: a `book_ver` snapshot and a search reindex, up to ~55k books).
      Re-run `maintenance.credit-census` after it. The fixed classes drop to the
      books the run held or left for review (iTunes without the opt-in,
      owner-manual, user-locked, split-name or self-read narrators, books past
      `limit`), each listed in the run's result; the census-only review classes (dangling/tombstoned/combined ids, `author_id_not_primary`,
      narrator column vs junction disagreements) need owner decisions. Decide
      `allow: narrator_new_from_split` / `narrator_self_read` / `itunes` from the
      dry run. Gate for credits plan PR 4.
