### Fixed

- The Deluge protected-path cache (seeding folders and `protected_paths`) now
  recognises a path reached through a symlink: a link into a protected folder, a
  file under a symlinked parent, a dangling link into the folder, and a
  protected folder configured through a link. Paths are compared as spelled and
  with symlinks resolved, against the prefixes as spelled and resolved; the
  prefixes are resolved once per refresh. A path whose links cannot be resolved
  counts as protected.
- The frozen iTunes tree (any `books/itunes/` segment, case-insensitive) is now
  protected by the Deluge cache and by the shared protected-path check
  (`pathutil.UnderProtectedPath`, used by the purge, organizer, metadata
  write-back, server middleware and metafetch) whatever the configuration says,
  including for a link into the tree.
