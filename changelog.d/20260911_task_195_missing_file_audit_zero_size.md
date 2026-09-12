### Added

- **`maintenance.missing-file-audit` now reports a `zero_size` bucket.** The
  stat sweep previously counted any `book_file` row whose path resolved as
  `Present`, even when the file on disk held zero bytes — indistinguishable
  from a healthy file in the report. `os.Stat`'s result is now inspected: a
  path that resolves but is empty is counted in a new `fileZeroSize` /
  `report.ZeroSize` bucket (with its own bounded `ZeroSizeSample`), separate
  from `Present`, `Missing`, and `Unreadable`, so a truncated/corrupt file is
  never silently counted as healthy. The op remains report-only — it takes
  no repair action on any bucket.
