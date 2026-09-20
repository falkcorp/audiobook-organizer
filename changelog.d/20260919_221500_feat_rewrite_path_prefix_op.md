### Added

- **`maintenance.rewrite-path-prefix`** — a new maintenance operation that
  repairs every stored path under a directory that was renamed or moved on disk.
  It substitutes `old_prefix` → `new_prefix` in `book.file_path`,
  `book.source_import_path` and `book_file.file_path`, matching on a
  path-component boundary so `/mnt/x/ab` never sweeps `/mnt/x/abooks/…`.

  Until now a renamed parent directory was unrepairable:
  `maintenance.missing-file-repoint` only derives candidates inside a row's own
  recorded directory, so every row under a renamed parent landed in
  `no-candidate-bytes`, and a library scan — the other thing that re-discovers
  paths — is not run in this deployment.

  Safety: never deletes a row; default **dry run**; refuses a book whose new path
  is already held by another live book (`LiveBookIDsAtPath`) or another
  `book_file` row, and — by default — whose new path does not exist on disk;
  refuses outright when either prefix is inside an iTunes library root. Writes go
  through `ModifyBook` / `ModifyBookFile`, which re-check the substitution
  against the row as read under its own store lock, so a row another writer moved
  in the meantime is skipped rather than clobbered. Each rewritten book gets a
  `prefix-rewrite` entry in its `path_history:` ledger, written after the row
  write it describes, and every run writes a per-field TSV report.

### Changed

- `maintenance`'s internal `opsBookWriter` store interface is now the composition
  of `opsBookMutator` and `opsBookRetirer`, keeping each within the
  `interfacebloat` limit. The exposed method set is unchanged apart from the
  added `RecordPathChange`.
