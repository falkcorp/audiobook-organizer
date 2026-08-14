### Fixed

- Collapsed the 7 remaining whole-library offset-pagination walkers to single
  limit-0 snapshot reads (quarantine auto-scan, junk-title repair, title
  backfill/repair, duration backfill, and PebbleStore's folder-dup and
  metadata-dup scans). Offset pages over the async memdb can silently skip or
  repeat rows when the snapshot swaps between calls, and the Pebble pagers
  additionally paid a full prefix scan per page.
