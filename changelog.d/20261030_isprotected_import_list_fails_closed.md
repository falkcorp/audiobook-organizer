### Fixed

- `Server.isProtectedPath` and `metafetch.Service.isProtectedPath` now fails closed when the import-path list cannot be
  read: every path counts as protected until the list reads again (logged at
  most once a minute). Before, the import paths were silently dropped, so the
  organizer's rename-source guard answered "movable" for a file under an import
  folder and an in-place move or `RenameFiles` could take it out of that folder.
