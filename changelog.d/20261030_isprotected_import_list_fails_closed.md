### Fixed

- `Server.isProtectedPath` and `metafetch.Service.isProtectedPath` now fails closed when the import-path list cannot be
  read: every path counts as protected until the list reads again (logged at
  most once a minute). Before, the import paths were silently dropped, so the
  organizer's rename-source guard answered "movable" for a file under an import
  folder and an in-place move or `RenameFiles` could take it out of that folder.
- The metafetch library-copy path now refuses with the protected-list-not-loaded
  error while the import-path list is unreadable, instead of copying a book that
  may need no copy (every book outside the library folder reads as protected then).
- The library-copy decision in metafetch reads the import-path list once and
  decides the lookup, the sibling checks and the loaded check from that read.
  Before, a read that failed and a second that succeeded could let a copy be made
  of a book that was never protected.
