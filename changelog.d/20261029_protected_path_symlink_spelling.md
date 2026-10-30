### Fixed

- The protected-path check (iTunes library folders, "iTunes Media", import
  paths, `.failed`) now also tests the symlink-resolved spelling of a path and of
  each protected folder. Before, a path spelled through a symlink into a
  protected folder was not protected, so the book purge and the version purge
  could delete a real iTunes file, and the organizer could move one. A path whose
  symlinks cannot be resolved is no longer passed as unprotected: tag writes and
  previews treat it as protected, and a purge never deletes the file: a failure
  that may pass (permission denied) keeps the book in the trash and asks again
  on the next pass, while a link cycle will not resolve by retrying, so the book
  is purged and the file is left where it is with a "book purged, file kept"
  line. The resolver
  moved from the merge guard into `internal/pathutil` so the merge guard and the
  purge, organize and metadata checks share one implementation.
- Re-organizing a book in place now refuses a file that is in the iTunes tree or
  a protected folder only through a symlink, and refuses when its symlinks cannot
  be resolved. The protected folders are resolved once per few seconds rather than
  per file, and an unresolvable folder is logged once, not per file.
