- [ ] **Organize callers that move files but never rewrite `book_file` rows** — three
  pre-existing gaps surfaced by the #3046 review, deliberately left out of that PR:
  - `internal/server/folder_autoscan_op.go` repoints `book.FilePath` at the target
    directory and discards `landing.Files`, so every `book_file` row still names the
    source file (the same defect `server.go` documents as the reason `AutoOrganizeFn`
    was rerouted through `PerformOrganize` — this third copy was not).
  - `internal/server/batch_save_op.go` with `Organize: true` copies files into RootDir
    and writes nothing to the DB: library files with no row, which the next organize
    collides with (`_copy1`).
  - The exported `organizer.OrganizeBookDirectory` wrapper returns only the path map,
    dropping `Landing.Created`; `metafetch/service_apply.go` (`ensureLibraryCopy`) and
    `itunes/service/importer.go` therefore cannot roll back what they created, and
    `ensureLibraryCopy` still demotes the original on a failed `CreateBookFile`.
  Fix shape: route all three through `CreateOrganizedVersion` (or `PerformOrganize`)
  with the `*Landing`, and export the `*Landing` form of the directory organize. Add a
  test per caller that asserts the rows after the move, not just the files.
