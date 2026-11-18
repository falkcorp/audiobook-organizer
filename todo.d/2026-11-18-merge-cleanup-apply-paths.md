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
- [ ] **TODO-MERGE-CLEANUP-HUMAN-MERGED** PLAN puts `human_merged` in the preview, and only the
      journal half of it is tracked above. The other half is not built: hold a row
      whose pointer a person made, meaning a dedup candidate a person accepted or
      dismissed for this pair. Read the dedup candidate decisions for the pair in the
      preview (a narrow read exposed to the maintenance plugin, like the journal
      one), flag the row, and hold an unmerge or swap that contradicts the person's
      call, before any apply path exists.
- [ ] **TODO-MERGE-CLEANUP-MANUAL-ONLY-GUARDS** PLAN's manual-only skip kinds are not implemented: a
      loser or target whose `itunes_path` is set (the iTunes library is import-only
      and its files are left as they are) and the Doctor Who and Battlestar
      Galactica books that stay manual-only until the census is read. They do not
      matter while the fixer cannot write, but an apply path must not be built
      without them: add the skip kinds, their labels and their tests with the first
      apply.
