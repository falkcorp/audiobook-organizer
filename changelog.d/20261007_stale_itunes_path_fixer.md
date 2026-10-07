### Added

- **Repairs: new `stale-itunes-path` fixer.** It clears a book's or file row's
  `itunes_path` only when no track at that path exists, in either the imported
  iTunes library or the write-back library, and the book has no iTunes id.
  Both libraries are parsed read-only. If either cannot be read in full,
  nothing is applicable. Every clear is journaled before it is written and can
  be reverted. A path that changed after the plan is refused. The trial can be
  filtered to a list of books.

### Fixed

- **Fragment consolidation: a library copy whose version group holds its
  iTunes source can now be retired by the owner.** Copies made by
  itunes-clone-into-library share a version group with the iTunes book they
  were cloned from, and that "twin" held the copy back permanently. Now the
  copy and its twin go together on the parent's owner row. This needs proof:
  the twin's recorded hash must match the copy's content, and its file must be
  on disk at the same size. The twin must also have no iTunes id, no listening
  state, and no other group members. The owner's apply retires the twin, then
  the copy. Only database rows change: no file is touched and nothing is
  written to iTunes.
- **Fragment consolidation: a copy row held for its version group now says why
  the owner cannot apply it,** instead of dropping that reason.
