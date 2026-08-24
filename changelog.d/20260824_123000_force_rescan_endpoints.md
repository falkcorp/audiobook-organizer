### Added

- **Force a rescan of one book.** `POST /audiobooks/:id/force-rescan` flags a
  single book for a full re-read by the next scan. Unchanged files in the same
  folder are still skipped, so rescanning one book costs one book — not the
  whole folder. In the UI this is the new **Force Rescan** button on a book's
  detail page.

### Changed

- **Renamed the misleading rescan endpoint.** `POST /audiobooks/:id/rescan`
  never caused a rescan: it re-checks file sizes on disk and corrects them,
  without re-reading tags or audio. It is now
  `POST /audiobooks/:id/reconcile-files`, and the UI button is labelled
  **Reconcile File Sizes**. The old `/rescan` path still works and still does
  exactly what it always did, so nothing that calls it changes behaviour.
- The book detail page's folder-rescan button is now labelled **Rescan Whole
  Folder** and warns that some folders hold over a thousand files.
