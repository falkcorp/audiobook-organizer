### Fixed

- The protected-path check (iTunes library folders, "iTunes Media", import
  paths, `.failed`) now also tests the symlink-resolved spelling of a path and of
  each protected folder. Before, a path spelled through a symlink into a
  protected folder was not protected, so the book purge and the version purge
  could delete a real iTunes file, and the organizer could move one. A path whose
  symlinks cannot be resolved is no longer passed as unprotected: tag writes and
  previews treat it as protected, and a purge keeps the file and retries (a link
  cycle gives up after a few passes like any unresolvable path). The resolver
  moved from the merge guard into `internal/pathutil` so the merge guard and the
  purge, organize and metadata checks share one implementation.
