### Added

- The revocation journal's file writes can now be labelled, killed and error-injected by the crash-matrix kit. The journal takes a filesystem seam (`journal.FS`, passed with `journal.WithFS` or `ComponentOptions.FS`/`FileOptions.FS`) with 19 stable writer labels, and a bridge package maps the boot's in-memory test filesystem onto it. Production is unchanged: with no seam the journal uses the real filesystem with the same file modes, fsync points and atomic renames as before. Nothing in the running server opens the journal through the seam yet; that is wired in the boot work that follows.
- The writer table gains an `Unbound` marker for writers whose code exists but that no boot reaches yet, and lists the journal's and the restore set-aside's file writes under it.
