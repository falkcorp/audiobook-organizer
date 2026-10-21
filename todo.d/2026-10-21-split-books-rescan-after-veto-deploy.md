- [ ] **SPLIT-BOOKS-RESCAN** After the split-books distinct-works veto and the
      merge-time recheck deploy, re-run `dedup.split-book-scan` so the Split
      Books tab and the Repairs lane are rebuilt without the vetoed clusters
      (stored candidates keep their old contents until a scan replaces them;
      the merge paths already refuse them). Then spot-check the 15 large
      clusters (33+ members) that could not be classified offline: open each
      in Repairs, confirm it is held or applicable for the right reason, and
      confirm the bulk-merge dry run reports no unexpected refusals. Done when
      the Split Books tab holds no cluster that a veto rule would hold.
