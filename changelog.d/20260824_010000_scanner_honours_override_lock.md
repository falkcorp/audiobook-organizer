### Fixed

- **A library scan no longer overwrites metadata you curated.** The scanner
  overlays values read from a file's own tags onto the existing row on every
  rescan, and it did so unconditionally — so if you corrected a title, author,
  series, narrator, language or publisher in the UI while the file's embedded
  tags still held the old value, the next scheduled scan silently wrote the old
  value back. The scan now checks each field's provenance first and leaves
  anything you locked or explicitly set alone. If that provenance cannot be
  read, the scan skips the overlay rather than risk overwriting an edit it
  could not verify.
