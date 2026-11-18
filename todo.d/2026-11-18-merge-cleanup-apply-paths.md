- [ ] **TODO-MERGE-CLEANUP-APPLY-PATHS** The `maintenance.merge-cleanup` preview (PR 1) lists
      unmerge, swap-keeper and repoint-chain rows but has no apply path. Each apply
      is its own change: the unmerge needs the primary hand-off for rows held as
      `group_needs_handoff` and a decision on `sync_redirect_present` rows (an
      Audiobookshelf redirect that sends clients to the other book); the swap needs
      the retired-versions write path; the repoint is a one-field write. Run the
      preview on production first and read the census (counts per class, skip kind
      and flag, and the `origin_match4` split) before choosing which apply to build.
- [ ] **TODO-MERGE-CLEANUP-AUTOMERGE-OWNERSHIP** The preview cannot yet tell a pointer written by the
      dedup auto-merge from one a person made: the auto-merge journal
      (`dedup:automerge:`) is not exposed to plugin deps, so `owned_by_automerge_journal`
      is not implemented and only the MATCH-4 origin evidence is reported. Expose a
      narrow read of the journal to the maintenance plugin, then hold rows whose
      pointer a live journal entry owns (an undo of that merge is the right repair).
- [ ] **TODO-MERGE-CLEANUP-REJECT-FAMILY** The preview reads the owner's `same_work` and `same_copy` pair
      rejects, and `mergeShapedRepairs` gives a merge-cleanup reject the split-books
      reasons, but nothing yet checks which op kind and decision family a reject of a
      merge-cleanup row is stored under, or that the next plan reads it back. Verify
      that end to end (reject a row, re-plan, see the row held as
      `owner_says_distinct`) before an apply path is built.
