### Changed

- perf(metadata): a metadata apply now writes a book's audio files with up to 4
  writers at once instead of one after another. Extra writers take free slots of
  the process-wide write-back gate without waiting, so concurrent ops still never
  exceed its 8 writers and a book can never deadlock waiting for a slot it
  holds. Applies to both the per-row loop and the "write-back is a directory"
  loop (the prod case: 58 files, ~0.25s each, strictly sequential).
- perf(database): `GetBooksByMetadataSourceHash`, which every apply calls for the
  MATCH-4 duplicate check, now reads a new memdb `metadata_source_hash` index
  instead of JSON-decoding every book row in Pebble. Falls back to the Pebble
  scan when memdb has lost book rows.
- Every apply logs one `apply phase durations` line per book (gate_wait,
  apply_db, lock_wait, cover, copy_lock, embed, rename, tag_prep, tags, db_post,
  total, files), so a slow apply shows where its time went.
