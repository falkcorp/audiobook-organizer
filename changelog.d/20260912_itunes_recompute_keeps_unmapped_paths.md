### Fixed

- The nightly **Recompute iTunes Paths** job no longer writes an empty iTunes
  path over a stored one when no path mapping covers a book file. Before, a
  file outside every mapped root (or any file, with no mapping configured) had
  its stored iTunes path blanked, which stopped that book being written back
  to iTunes. Since the boundary fix for iTunes path matching, that also
  included files in a sibling directory of a mapped root (`/lib2` next to
  `/lib`). Such rows are now kept and listed as warnings in the job's log (up
  to 200 per run), and the job ends with a summary of rows updated and kept.
  `path_reconcile` already skipped them; the two jobs now agree.
