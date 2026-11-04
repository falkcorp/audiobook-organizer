### Changed

- A file's runtime is now stored as "measured" or "not measured" instead of a
  number where 0 meant both "never checked" and "checked and found nothing".
  Internally `book_file.duration` is a pointer: absent means not measured, and a
  stored value is always more than 0 seconds. Existing rows need no migration,
  because a 0 was never written to disk, so every file that was never checked
  now reads as not measured. Nothing a user sees changes yet: totals, holds and
  the API still treat an unmeasured file as contributing no time, exactly as
  before. This is the groundwork for telling "not probed yet" apart from
  "possible bad file" in a later change.
