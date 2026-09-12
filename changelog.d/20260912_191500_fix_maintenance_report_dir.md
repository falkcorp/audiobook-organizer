### Fixed

- Maintenance ops that write a per-row TSV report (`dedupe-book-file-rows`,
  `metadata-cache-reap`, `merge-same-path-dupes`, `missing-file-repoint`,
  `mark-missing-files`, `recover-missing-files`) no longer fall back to a
  relative `reports/` directory when no `reportPath` is given. The fallback
  resolved against the process working directory: in production that was the
  service's state directory, and under `go test` it was the package source tree,
  which left untracked `dedupe-book-file-rows-*.tsv` files in every worktree.
  The default is now `{root_dir}/.reports/`, resolved before the op does any
  work; with no absolute `root_dir` and no `reportPath` the op fails up front
  instead of applying changes it cannot record. The stray committed
  `internal/plugins/maintenance/reports/metadata-cache-reap-unknown-op.tsv`
  (a test run's output) is removed.
