- [ ] **AUTHOR-LEDGER-RESTORE (owner decision)** `purge-empty-authors`, `author-duplicate-merge`
      and (once #3305 merges) `purge-empty-narrators` journal every delete as an
      `operation_changes` row: `author_delete` or `narrator_delete`, old value `"<id>:<name>"`.
      Nothing can replay them. Undo does not restore the rows; a fix is in flight so it at least
      stops reporting them as reverted. Decide whether an op should recreate a deleted author or
      narrator from its ledger row, and whether it may reuse the original id (the id counter
      has moved on) or must mint a new one and remap references. 1,742+ purge rows written on
      2026-09-12 are the first real input.
