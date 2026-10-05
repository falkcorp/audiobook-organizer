- [ ] **COMBINED-AUTHOR-SERIES-REPOINT** Re-point the series that combined author
      records own before purging those records. `maintenance.purge-empty-authors`
      now holds any author that is still a `Series.AuthorID` (`held-back(series
      owner)`), because `DeleteAuthor` leaves the series pointing at a deleted id.
      The scanner and metafetch create series under the book's primary author, so
      many combined records the new fixer (`maintenance.repair-combined-author-credits`,
      #3717) empties will stay listed until something re-points their series. The
      obvious target is the record's first part. The owner decides the rule. The
      hold applies to every purge target, not only combined records.
- [ ] **COMBINED-AUTHOR-PATH-LINK-PARTS** `maintenance.author-path-link` now holds a
      folder that names several people (`suspect_composite_credit`) instead of
      linking or minting it. It does not credit the parts, because the op links
      ONE author per book and its half-write resume logic recognises a single
      credit only. Decide whether it should credit every part (resume logic and
      dry-run counts need reworking) or keep holding them.
- [ ] **COMBINED-AUTHOR-TITLE-HOLD-HIDES-PEN-NAMES** The combined-credit fixer
      holds a record as `skipped_names_a_title` when a live book's title equals
      the credit string. Prod has junk book titles of that shape ("Shirtaloon,
      Travis Deverell" is itself a book title), so in the 2026-10-04 offline
      census 92 of the 93 "Shirtaloon, Travis Deverell" books, 44 "J. N.
      Chaney, Terry Maggert" and 11 "Robert Jordan, Brandon Sanderson" books
      are held as titles, not split. Owner decision: exempt a title that is
      the whole credit when every part is an existing author, or repair the
      junk titles first.
- [ ] **COMBINED-AUTHOR-REVIEW-FOLLOWUPS** Follow-ups from the #3717 review,
      deliberately left out of that PR:
      - Undo of a partly applied combined-credit row: `revertJunkAuthorCredits`
        when the credits were written but the primary was not.
      - Rescans: primary versus position 0, and duplicate series rows.
      - The fixer's 2-minute cached author index can resolve a part to a stale
        variant and create a near-duplicate author.
      - The purge's `seriesRefs` guard has no per-item re-check at delete time.
      - Cast-list folders split into 2-3 "authors" at scan time (63 of the 239
        splits in the 2026-10-04 offline run carry "&", mostly Big Finish / Dark
        Shadows casts such as "Lisa Bowerman & Harry Myers"). Their parts are
        narrators. "Full Dark, No Stars" is a book title the library does not
        hold, so the title check misses it.
