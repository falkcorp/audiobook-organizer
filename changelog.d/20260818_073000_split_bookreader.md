### Changed

#### `BookReader` split from 35 methods into ten focused interfaces

`BookReader` was the widest interface in `internal/database` at 35 methods,
referenced by 13 files. It is now assembled from ten interfaces of 2–8 methods:
`BookByIDReader`, `BookBulkReader`, `BookLookupReader`, `BookDuplicateReader`,
`BookRelationReader`, `BookSearchReader`, `BookCountReader`,
`BookSnapshotReader`, `BookLifecycleReader`, and `BookITunesReader`.

The name `BookReader` is retained as their composition, so the method set is
byte-identical and no consumer moves — verified by diffing the method names
(35 before, 35 after, identical) and independently by the type checker, which
fails every implementation if a method is dropped or re-signatured.

The composition carries an explicit `//nolint:interfacebloat` with a written
reason: at ten embeds it is still over the width threshold, and it exists only
until consumers migrate to the piece each actually uses. That is the intended
use of the override — the directive is greppable and disappears with the alias.
