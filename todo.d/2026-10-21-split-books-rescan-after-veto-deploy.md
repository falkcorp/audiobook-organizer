- [ ] **SPLIT-BOOKS-RESCAN** After the split-books distinct-works veto and the
      merge-time recheck deploy, re-run `dedup.split-book-scan` so the Split
      Books tab and the Repairs lane are rebuilt without the vetoed clusters
      (stored candidates keep their old contents until a scan replaces them;
      the merge paths already refuse them). Then spot-check the 15 large
      clusters (33+ members) that could not be classified offline: open each
      in Repairs, confirm it is held or applicable for the right reason, and
      confirm the bulk-merge dry run reports no unexpected refusals. Offline
      estimate for the 49 checkable rows: 35 not applicable (the 29 wrong
      rows plus about 6 genuine chapter splits), 14 applicable. The 6 genuine
      rows are held by the 2h gate or unknown durations and the owner reviews
      them by hand; the counts are an estimate, not a promise. Known
      fail-safe false positives that also land here: a genuine split whose
      folder is named "Series"/"Collection", whose chapter titles read "Name
      N: Subtitle" without Chapter/Part, whose parts are labelled "Section N:"
      or "Episode #N" in one work, whose sections are "Book One/Two" inside
      one novel, whose rows carry different ASINs, or that has a chapter of
      1900 or more. Known miss: short (under 2h) ASIN-less novellas with bare
      numbered file names can still pass, so read each row before applying.
      Done when the Split Books tab holds no cluster that a veto rule would
      hold.
