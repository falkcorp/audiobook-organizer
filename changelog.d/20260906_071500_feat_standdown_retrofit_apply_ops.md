### Changed

- **The file-repointing maintenance ops now cooperatively pause the library
  scanner while they apply, instead of relying on an operator precondition.**
  `recover-missing-files`, `missing-file-repoint`, and `mark-missing-files` acquire
  the scan stand-down (PR #3080) for their write phase, renew it on every write,
  and abort the rest of the run if the lease is lost — so an apply and a running
  scan can no longer clobber each other's writes to the same `book_file` rows. This
  replaces the previous "don't run an apply while a scan is active" documented
  hazard with a real runtime interlock. Dry runs acquire nothing. A new
  `ScanController` interface on the maintenance plugin's `ServerDeps` carries the
  control across the plugin boundary (no registry types leak in), and the shared
  acquire/renew/abort contract lives in one helper so the ops cannot drift apart on
  it.
