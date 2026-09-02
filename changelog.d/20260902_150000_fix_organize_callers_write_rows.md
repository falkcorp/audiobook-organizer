### Fixed

- **Four organize callers moved a book's audio into the library and left every
  `book_file` row naming the source.** `library.folder-auto-scan` discarded the
  organizer's path map, `metadata.batch-save` with `organize` stopped at the file
  operation and wrote nothing to the database at all (library files with no row, which
  the next organize collided with as `_copy1`), `ensureLibraryCopy` created the rows one
  at a time with every error only logged, and the import-path fallback repointed
  `Book.FilePath` alone. All four now go through `Service.CommitLanding` /
  `CreateOrganizedVersion`, whose `BatchCreateBookFiles` writes the rows at the paths
  that actually landed, atomically; the registry-less import fallback declines to
  organize instead of doing it wrongly.
- **A failed row write no longer leaves orphan copies in the library or demotes the
  original.** `organizer.OrganizeBookDirectory` now returns the `*Landing`, so a caller
  can see which files this organize created and remove exactly those; `RemoveCreated` is
  exported for the iTunes importer, which commits a landing to the imported book's own
  rows rather than versioning it. `ensureLibraryCopy` used to mark the original
  superseded even when the copy's rows had failed, producing a version group whose
  primary owned no audio.
- **The iTunes import organize phase rolls back instead of hinting.** A `UpdateBook`
  failure after a successful organize used to leave a multi-file book's rows pointing at
  the copies with a "reconcile" message, and a single-file book's copy renamed back over
  its own source. Both now restore the rows to their source paths, remove the created
  copies and reset `FilePath`.
- **The metafetch library copy keeps `work_id`, the Audible/Google/user ratings,
  `quantity` and `version_notes`.** It was built with a full struct copy before this
  change and is now built by `CreateOrganizedVersion`, whose field list omits them.
