### Fixed

- The directory holding the database is now excluded from library scans as a
  rule rather than by coincidence. It was kept out only because its name began
  with a dot; a database path without one — now typeable from Settings → Paths —
  would have had every library walk descend into a live database, treating its
  internal files as audiobooks. The same protection is extended to the
  activity-log database, whose location is also settable.
