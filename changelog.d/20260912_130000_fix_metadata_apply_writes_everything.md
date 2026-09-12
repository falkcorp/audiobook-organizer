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
  single tag write for the single-book apply, both batch applies and auto-fetch.
  Before this, batch-applied books kept their old cover, and with
  `auto_write_tags_on_apply` on each file was tagged twice.
- **Auto-fetch keeps tags in step with the database.** It used to write tags only
  under `write_back_metadata` (off in production); it now follows the same
  apply settings as a manual apply.

### Removed

- **`auto_fetch_metadata` setting.** Nothing read it. Every auto-fetch caller
  already has its own switch (organize's "fetch metadata first", the iTunes
  import's "fetch metadata", the per-book Fetch button). A stored value is
  ignored on load.
