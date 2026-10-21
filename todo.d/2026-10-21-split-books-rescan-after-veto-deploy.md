- [ ] **SPLIT-BOOKS-RESCAN** After the split-books distinct-works veto and the
      merge-time recheck deploy, re-run `dedup.split-book-scan` so the Split
      Books tab and the Repairs lane are rebuilt without the vetoed clusters
      (stored candidates keep their old contents until a scan replaces them;
      the merge paths already refuse them). Then spot-check the 15 large
      clusters (33+ members) that could not be classified offline: open each
      in Repairs, confirm it is held or applicable for the right reason, and
      confirm the bulk-merge dry run reports no unexpected refusals. Expect
      most of the wrong rows held, plus roughly 6 genuine chapter splits held
      by the 2h gate or unknown durations: the owner reviews those by hand
      (the counts are an offline estimate, not a promise). Known fail-safe
      false positives that also land here: a genuine split whose folder is
      named "Series"/"Collection", whose chapter titles read "Name N: Subtitle"
      without Chapter/Part, whose sections are "Book One/Two" inside one novel,
      whose rows carry different ASINs, or that has a chapter of 1900 or more.
      Done when the Split Books tab holds no cluster that a veto rule would hold.
