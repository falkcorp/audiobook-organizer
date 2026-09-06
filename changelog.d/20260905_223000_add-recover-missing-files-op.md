### Added

- **New maintenance operation `maintenance.recover-missing-files`** recovers book
  files whose recorded path is gone but whose bytes still exist on disk under a name
  the shape-based `missing-file-repoint` op cannot derive. It builds an inventory of
  every unclaimed file on disk keyed by size and matches each missing row's recorded
  file size against it, but repoints a row **only when the match is unambiguous** —
  exactly one unclaimed in-tree file of that size and extension, wanted by exactly one
  missing row. Anything less certain (two candidate files, two rows wanting one file,
  an extension mismatch) is refused and reported rather than guessed. It rewrites only
  `FilePath` (never moves or deletes), defaults to a dry run that writes a full per-row
  report, re-stats each candidate immediately before writing so a file that changed
  underneath it is skipped, and censuses the rest — files that exist only outside the
  library tree (reflink candidates) versus files that exist nowhere. Run
  `missing-file-repoint` first; this op handles the residue it leaves behind.
