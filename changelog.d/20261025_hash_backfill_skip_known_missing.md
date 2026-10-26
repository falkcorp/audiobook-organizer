### Fixed

The `backfill-file-hashes` maintenance op no longer retries, and warns about,
every file that is gone from disk on every run. A prod preview found about 515k
rows with no hash, and every sampled failure was a file missing on the server
rather than a read error.

When the hash attempt fails with not-found (ENOENT or ENOTDIR) the op records
that on the row in a new, separate field, `book_file.hash_absent_at`. It does
not touch `Missing`: that flag also means "superseded tombstone" in the
consolidation code and the version-primary election excludes flagged rows, so
setting it for a hash attempt could have let a version with absent chapters win.
`hash_absent_at` is read only by this op (and is cleared by a successful scanner
stat like `Missing` is). Later runs stat a row that is known absent (this marker
or `Missing`) and skip it when the file is still gone; a file that reappears is
hashed and its marker cleared. Read errors of any other kind are still retried
every run and never recorded, and no `book_file` row is ever deleted.

Recording is guarded. The op refuses to start while the library root is unset,
unreadable or empty (an unmounted share answers not-found for every path),
checks again before each chunk's writes, never records an absence for a path
outside the library root, skips rows with no path, and refuses to start a
marker write while a library scan runs. Each row is also re-checked under its own
lock before it is marked. Writes are grouped per book through a new narrow store
capability, `database.BookFileBatchModifier` (kept off `Store`), so a book's
aggregates are recomputed once per book rather than once per file, and a
recompute failure is reported apart from a failed write.

Every run that records or previews markers writes a per-row TSV report to
`{root_dir}/.reports/backfill-file-hashes-<op id>.tsv`, dry runs included, so the
preview can be read before an apply and an apply audited after it. The result
reports `hashed`, `skipped_known_missing`, `newly_missing`, `read_errors`,
`write_errors`, `skipped_no_path`, `unverified_absent`, `skipped_changed`,
`marker_errors`, `markers_cleared`, `clear_errors` and `recompute_errors`; a dry
run reports what an apply would do without writing anything.

New params: `mark_absent` (default true; false makes a pure hash run that counts
absences but records none) and `clear_markers` (clears every in-scope marker,
the undo for a recording pass). The one-click Fingerprint and metadata-hash
buttons now send `mark_absent: false`, so no click records absences without the
preview-then-apply flow. The checkpoint phase was renamed to `scanning-by-book`
because the run order changed to (book, file), so an older checkpoint is ignored.

### Changed

`merge-chapter-groups` and `repoint-version-primary` no longer name
"merge-chapter-groups" in the error they return when a library scan is running;
the shared scan guard now names the calling op.
