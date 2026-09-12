### Fixed

- **Metadata apply writes every selected field, and only those.** The apply now
  writes ASIN and genre (the candidate never carried genre), honors
  `series_position`, clears ISBN-10/13 when ISBN is unchecked, and puts subtitle,
  abridged, page count, secondary series and runtime under the field checkboxes.
  One list (`metafetch.ApplyFields`) drives the allowlist, the `fetched_value`
  provenance (now recorded for every written field, not 8) and the web dialogs'
  shared list, and a test fails if the Go and web lists differ.
- **Batch applies download the new cover, and every apply tags files once.** A new
  shared sequel, `FinishApplyFileWork`, runs the cover download, the file I/O and a
  single tag write for the single-book apply, both batch applies and the
  interrupted-apply replay after a restart. Before this, batch-applied books kept
  their old cover, and with `auto_write_tags_on_apply` on each file was tagged twice.
- **An applied cover replaces the old one.** The download used to return any
  cover already on disk without fetching, so a book that had a cover kept it. The
  new image is written to a temp file and renamed over the old one; a failed
  download leaves the old cover in place.
- **The apply rename never moves a file under a protected path.** Each file is
  checked, not just the book, so a library copy with a row still pointing into
  the iTunes tree no longer has that iTunes file moved. Library-root checks now
  compare on a path-separator boundary.
- **Auto-fetch keeps tags in step with the database, without creating copies.**
  Auto-fetch file work (rename, tags, cover embed) now runs through the file-I/O
  pool, only for books that already have a library copy under the library root.
  It never creates one. It no longer writes a series position without a series
  name, a failed cover download keeps the old cover, and a book that already has
  a local cover keeps it (an explicit apply still replaces it).
- **Apply file work locks the files it actually writes.** Every apply's file
  work (single-book, batch, auto-fetch and their restart replays) takes the
  per-path write lock itself: on the library copy's path for a protected book,
  on the files' current path for the cover embed and rename, and on the
  post-rename path for the tag write. An auto-fetch of an iTunes book and a
  manual apply of its library copy no longer write the same files at once.
- **A lost scan stand-down stops apply file work between steps again.** The
  single-book apply and the batch-candidates apply re-check the scan stand-down
  before the cover download, the file I/O and the tag write, as they did before
  the file-side sequel was shared. A scan that resumes mid-apply no longer runs
  alongside the rename or the tag write.

### Removed

- **`auto_fetch_metadata` setting.** Nothing read it. Every auto-fetch caller
  already has its own switch (organize's "fetch metadata first", the iTunes
  import's "fetch metadata", the per-book Fetch button). A stored value now logs
  a removed-setting warning on load.
