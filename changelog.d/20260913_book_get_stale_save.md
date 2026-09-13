### Fixed

- **Opening a book no longer reverts a metadata apply that lands at the same
  time.** `GET /api/v1/audiobooks/:id` and the tags endpoint backfilled a
  missing duration / codec / bitrate / sample rate / channels by saving the
  whole book row they had read before running ffprobe and the tag read, so any
  write in between (a metadata apply, an edit) was silently put back. They now
  write only the derived fields that are still empty, through the new
  `Store.FillBookMediaInfo`, which re-reads the row inside the store, writes
  nothing when no field needs filling, and never overwrites a value another
  writer set. The response and the single-book cache now carry the row as
  stored, not the pre-apply copy.
