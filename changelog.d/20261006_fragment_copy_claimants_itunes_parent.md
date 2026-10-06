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
  parent's copy row manual-only.
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
