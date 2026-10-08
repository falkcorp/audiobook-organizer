### Changed

- **Repairs "Chapter fragments": fragments iTunes knows about are now combined
  like any other.** iTunes is import-only now, so a fragment with an iTunes id,
  an iTunes path, or a file under `books/itunes/**` is no longer listed for
  manual action only (`skipped_itunes`). It is planned and applied like every
  other fragment: moved, copy, ghost, no-parent, folder chapter set and
  existing-book rows alike. Only database rows change. No file is moved,
  renamed or deleted, the iTunes library is never written, and every file row
  keeps its iTunes id and iTunes path, so the next import still finds each
  track on the book it now belongs to. Every step is undoable from the apply
  operation. Doctor Who / Big Finish / Torchwood stay manual-only. The
  owner-only "owner:" row for iTunes twins and the "fragment-only" retire into
  an iTunes-linked parent are gone, since neither case is special any more.
  The `itunes-chapter-set` class is gone too: those sets now appear as
  ordinary folder or parent chapter sets.
