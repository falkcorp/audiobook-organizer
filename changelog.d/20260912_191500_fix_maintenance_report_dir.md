### Fixed

- Maintenance ops that write a per-row TSV report (`dedupe-book-file-rows`,
  `metadata-cache-reap`, `merge-same-path-dupes`, `missing-file-repoint`,
  `mark-missing-files`, `recover-missing-files`) no longer fall back to a
  relative `reports/` directory when no `reportPath` is given. The fallback
  resolved against the process working directory: in production that was the
  service's state directory, and under `go test` it was the package source tree,
  which left untracked `dedupe-book-file-rows-*.tsv` files in every worktree.
  The default is now `{root_dir}/.reports/`. Before the op does any work, its
  report directory is created and proven writable (default or explicit
  `reportPath`), so a run whose report cannot be written, or that has no
  absolute `root_dir` and no `reportPath`, fails up front instead of applying
  changes it cannot record. The stray committed
  `internal/plugins/maintenance/reports/metadata-cache-reap-unknown-op.tsv`
  (a test run's output) is removed.
- `recover-missing-files` no longer walks application-owned folders under the
  library root (`.reports`, `.wav-cache`, `.activity`, the database directory,
  backups) when building its inventory of unclaimed files. A report or cache
  file whose size matched a missing row could otherwise make that row ambiguous,
  or, with `requireExtMatch` off, become the file the row was repointed to.
