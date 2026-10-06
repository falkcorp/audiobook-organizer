### Fixed

- **Chapter-set rows are checked against the whole library again at apply
  (#3774 review follow-ups).** A folder or parent chapter set's plan-time
  tests read the whole library, but the apply's re-plan read only the row's
  books. Before a set's first write, the apply now decides the row again over
  a fresh, complete listing with the same code the plan ran. It refuses as
  changed since the plan, writing nothing, when any of these appeared after
  the plan: a second live book holding the same audio, a join target that an
  earlier no-parent apply assembled, a version group, a book in the set's
  folder, or a better-ranked book of the title. A run of this fixer that was
  already cut off resumes; a fragment some other writer merged into the
  target is a change, not a cut-off run, and the row is refused.
  - The whole library is listed once per apply run, not twice per row (up to
    four workers did this at once: about 40k books and 742k file rows each).
    Between rows the snapshot reads again only the books written since, by
    anyone, from the book change log, and is listed again when another
    merge-family writer held the merge lock in between. The listing refuses a
    memdb known to be missing book or file rows and falls through to the
    authoritative scan; a listing that cannot be read fails the row.
  - A join (title, audio, or a moved/copy row turned into one) is held when
    the target's version group, or any fragment's, holds a live iTunes copy:
    retiring a fragment hands its group's primary on, and the ranking would
    write the iTunes book. Checked at plan time and again under the merge lock
    before the first write. Moved, copy and ghost rows, which retire their
    fragments into the parent the same way, are held for the same reason
    when the parent's or a fragment's version group holds a live iTunes copy.
  - No row of this fixer writes an iTunes book any more. The book a row
    writes is checked itself, whether or not it is in a version group: a
    moved, copy or ghost row's parent, a no-parent row's survivor (and the
    version groups of its members), and a carry row's terminal. An iTunes id
    on that book, on one of its file rows, as a live external id, or an
    iTunes library path holds the row, at plan and again under the merge lock
    just before the first write.
  - A carry row is also held when a retired book its files leave, or one of
    the moved file rows itself, is an iTunes copy (an iTunes id or path), at
    plan and again under the merge lock before the first move.
  - Between two rows of one apply, the re-check also reads, fresh, every
    book holding the set's own files (by path and by hash). Writers that take
    no merge lock and write no book (repoints, file recoveries, hash
    backfills) are now seen. A row that never re-checks the library no
    longer hides another writer's merge-lock hold from the next one.
  - The apply's re-check treats as "a chapter fragment, not a book" only
    what the plan would: a fragment matched to a parent, or one a run of
    this fixer has written, is a book there too, so the re-check no longer
    passes a set the plan would hold.
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
