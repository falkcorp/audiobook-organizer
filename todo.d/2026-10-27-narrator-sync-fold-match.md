- [ ] **NARRATOR-SYNC-FOLD-MATCH** Narrator sync should fold-match existing
      records instead of minting a second one. `maintenance.credits-backfill`
      links a narrator name that matches a record only by its fold key
      (`authorjunk.FoldKey`: case, accents, punctuation, spaces; "Renee Montoya"
      for the record "Renée Montoya", "Jean Paul Valley" for "Jean-Paul Valley")
      to that record and respells the name inside the Narrator column. A rescan
      with an UNLOCKED narrator field reads the tag's spelling back into the
      column, and `syncNarratorJunctionAfterWrite`
      (`internal/database/narrator_credit_sync.go:124`) resolves each name with
      `resolveNarratorCredit` -> `CreateNarrator(name)`, which is resolve-or-create
      by exact normalized name, so it creates an unaccented duplicate record and
      re-credits the book to it. Fix site: `resolveNarratorCredit` (line 43; its
      `CreateNarrator(name)` call is at line 49).
      Look the name up by fold key first (the store has no fold index; either add
      a `foldKey -> []id` index to the narrator name index next to the exact one,
      kept in `CreateNarrator`, merge and delete, or scan `ListNarrators` once
      per sync, which is O(records) per book write), link to the single fold
      match, and leave an ambiguous fold (several records) to create nothing and
      log, as the backfill's `narrator_name_ambiguous` does. Cost: a new index
      maintained on every narrator write plus its rebuild on boot, and a
      behaviour change for every book write that carries a narrator column
      (CreateBook, UpdateBook, ModifyBook), so it needs its own tests for
      concurrent creates of two spellings. Until then the credit census after a
      rescan is the check, and re-running the backfill finds the book again.
