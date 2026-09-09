### Added

- The main database location is now settable from Settings → Paths, alongside the
  activity-log location. When the server's environment or command line is pinning
  it, the field renders read-only and names which one — a disabled control that
  does not say what is disabling it sends the operator to change the wrong thing.
  The field states plainly that a change takes effect on restart, moves no data,
  and never deletes the existing database.
