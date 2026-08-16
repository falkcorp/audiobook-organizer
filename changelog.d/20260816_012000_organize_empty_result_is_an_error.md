### Fixed

#### A multi-file organize that copied nothing no longer reports success

`OrganizeBookDirectory` creates its target directory with `MkdirAll` *before*
copying anything, and skips any source file that has vanished from disk. When
every source was gone it therefore returned an empty directory, an empty
`pathMap`, and a **nil error** — indistinguishable from a clean organize.

This was reachable without any `book_file` row being flagged `Missing`: rows that
look present but whose files have since disappeared all skip silently. The
"every row is flagged missing" case was already rejected; this was the one that
looked like success.

Of the function's three callers, only `OrganizeDirectoryBook` checked for the
empty `pathMap`. The other two took the returned directory at face value:

- `ensureLibraryCopy` (`internal/metafetch/service_apply.go`) created a
  version-linked book record pointing at the empty directory.
- `organizeMultiFileBook` (`internal/itunes/service/importer.go`) assigned it to
  `book.FilePath`.

Both left a book in the library pointing at a directory containing no audio.

The check now lives **inside** `OrganizeBookDirectory`, which returns an error
naming the book and no target directory, so no caller can opt out of it. The
duplicated check in `OrganizeDirectoryBook` is removed; its separate
stat-the-destinations check is kept, because that verifies something different —
`pathMap` records what organize believed it wrote, and the stat confirms the
files are still there.

Filed as F6 in `todo.d/2026-08-15-organize-rename-silent-failures.md`.
