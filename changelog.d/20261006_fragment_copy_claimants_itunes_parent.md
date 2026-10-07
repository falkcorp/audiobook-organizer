### Changed

- **Fragment consolidation: several copies of a file the parent still has
  are each a copy, not ambiguous (owner decision 2026-10-06).** Two or more
  unproven fragments claiming one parent row whose file is still on disk
  used to be held `skipped_ambiguous` together ("parent row X is claimed by
  2 fragments"). Each now pairs on its own into the parent's
  `copy-unproven` row (listed for review, never auto-proven). A gone parent
  file, a claimant whose size on disk differs from the parent's file, or
  claimants whose hashes disagree keep the old ambiguity.
- A fragment that is hands-off on its own (under the iTunes library, an
  iTunes id, Doctor Who / Big Finish) is listed manual-only on a
  `manual:<fragment>` row of its own instead of making every sibling in its
  parent's copy row manual-only. A path twin whose donor fragment was split
  off onto its own row is held with it.
- A copy row whose parent is iTunes-linked (book or row iTunes id, row
  `itunes_path`, live itunes external id) is no longer held for its parent.
  It retires the fragments **writing the fragments only**
  (`retireIntoOnly`): no listening state, positions, bookmarks or sync
  redirect follow onto the parent, no external id moves to it, and the
  parent and its version group are untouched. A fragment that would have to
  carry a live external id or listening state onto the parent is held on its
  own row (`skipped_itunes`), and an iTunes book in a fragment's own version
  group holds the row. The mode is in the row's fingerprint (a parent that
  turned iTunes-linked since the plan is `changed_since_plan`), and Apply
  re-checks the fragments' groups, external ids and user state under the
  merge lock before the first write. Moved rows into an iTunes-linked
  parent are still held.

### Fixed

- **Fragment consolidation never retires a fragment whose own row carries an
  iTunes path** (review of the change above). The hands-off test
  (`fragCandidate.itunesWhy`) now counts a row `itunes_path` as an iTunes
  book, as `itunesCopyWhy` does, for copy, moved, ghost and no-parent rows;
  such a fragment is listed manual-only. On prod most library-copy rows carry
  one naming their own file. The fragment fixer's retire also refuses such a
  row under the merge lock (`retireOpts.RefuseITunesPath`; the leftovers
  fixer keeps its own rule that a bare iTunes path reference is not iTunes
  ownership).
- A fragment-only retire into an iTunes-linked parent checks every
  fragment's external ids and listening state before the first write, so a
  late change refuses the row whole instead of after some fragments were
  retired. Such rows are review risk.
- A path twin and its donor are held together whichever of the two is split
  off onto its own row.
