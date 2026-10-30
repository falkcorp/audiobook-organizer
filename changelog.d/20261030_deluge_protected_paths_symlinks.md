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
- The scheduled `deluge.protected-paths-sync` op now actually fetches the
  torrent save paths (it called `IsProtected("")`, which returns before the
  cache looks at its TTL) and fails when Deluge is unreachable.
- The Deluge cache no longer treats a spelled seeding-folder hit as final: a
  file there that is really in the frozen iTunes tree or under a static prefix
  is reported static. A candidate with a `..` segment is protected, a prefix
  that stops resolving keeps its last resolved form, and an unresolvable prefix
  warns once per refresh interval instead of once per call.
- The Deluge importer's static-prefix check (configured protected paths and the
  iTunes library folder) now follows symlinks too.
