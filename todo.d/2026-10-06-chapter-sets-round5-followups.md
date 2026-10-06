- [ ] **CHAPTER-SETS-R5a** Chapter-set apply re-check (#3787 round 5): `refreshAudioOf`
      (`internal/plugins/maintenance/fragment_folder_sets.go`) does not see a
      lock-free repoint that moves ANOTHER book's row into the set's folder with
      no book write (no change-log entry, no merge-lock hold, no matching path or
      hash). Either document it next to the size+duration gap in its doc comment,
      or read the folder's rows strictly (a strict by-folder lookup) in the
      re-check.
- [ ] **CHAPTER-SETS-R5b** Chapter-set apply (#3787 round 5): the fragments are not
      re-checked for iTunes after the `afterLockedReplan` seam, only in the locked
      re-plan just before it — a microsecond window in which an iTunes sync could
      tag a fragment before the first write. Re-read the fragments' iTunes state in
      the pre-write check, as the target and version-group checks already do.
