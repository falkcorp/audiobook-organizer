### Fixed

- The four whole-library offset-pagination loops in `internal/reconcile`
  (AssignOrphanVGs, ElectMissingPrimaries, version-group fetch, book-load) now
  enumerate with ONE store call reading a single consistent snapshot. Offset
  pages over the async memdb snapshot could silently skip or repeat rows when
  the reconciler swapped snapshots between pages (~13 swap windows per prod
  run) — and report success. Five more walkers filed for the same treatment;
  the original CI flake re-diagnosed as a separate warmup-publish race.
