<!-- file: changelog.d/feat-itunes-cleanup-sample.md -->
<!-- version: 0.1.0 -->
<!-- guid: d4a90c17-6e28-4b53-9f01-7c2b5e8a1d64 -->
<!-- last-edited: 2026-07-22 -->

### Added

#### `/itunes/cleanup-merged` dry-run now returns a sample of the removal set

The merged-track cleanup preview now includes a `sample` array (up to 40 of the
to-remove tracks) with each track's PID, book id, title, author, file path, and
`merged_into_book_id`, so an operator can eyeball that the removal set is genuinely
merged duplicates before applying a large, destructive removal.
