- [ ] **USER-STATE-UNLISTED** Decide what to do with listening state held only by user ids the store does not list.
      A read-only census of the production copy (2026-11-28, counts only) found 3,632
      distinct books with listening-state rows (`ubs:` / `upos:`), of which only 299 have
      a row from a user the store lists; about 3,333 books carry state only under user ids
      with no user record. The keep-progress gates (`merge.BookHasUserProgress`,
      `merge.UserStateProbe` / `BookHasCarryableUserState`, used by hard deletes, purges,
      dedup merges and the iTunes fixers) check listed users only, so for those books
      every gate reads "no progress". If those rows are someone's real progress (for
      example under an id an earlier merge or migration re-keyed), those books can be
      deleted with it; if they are leftovers of deleted accounts, there is nothing to keep.
      This predates the F4-08 identity flip (M3 does not touch these families) and the
      decision is the owner's. Next step: the read-only census of the unlisted ids (shapes
      and counts only, never ids) the F3-07 lead is running, then an owner ruling. Do not
      change gate behaviour before that ruling.
