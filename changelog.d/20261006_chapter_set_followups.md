### Fixed

- **Chapter-set rows are checked against the whole library again at apply
  (#3774 review follow-ups).** A folder or parent chapter set's plan-time
  tests read the whole library, but the apply's re-plan read only the row's
  books. Before a set's first write, the apply now decides the row again over
  a fresh, complete listing with the same code the plan ran. It refuses as
  changed since the plan, writing nothing, when any of these appeared after
  the plan: a second live book holding the same audio, a join target that an
  earlier no-parent apply assembled, a version group, a book in the set's
  folder, or a better-ranked book of the title. A run that was already cut
  off resumes as before.
  - A set joined by its title is now held, as an audio join already was, for
    two known authors among its files, for a member's version group holding
    a live book other than the target, and for its audio held by a live book
    other than the target (chapter fragments elsewhere do not count).
  - Two files are no longer the same audio on size and duration alone when
    both carry a hash and the hashes differ.
  - A joined set's listening state is placed by the set's own chapter
    numbering. Before this, a name like "Some Work - 04 1" read as chapter 1,
    so every fragment landed at the target's first track.
  - The version-group test builds its index once per plan, not once per row.
