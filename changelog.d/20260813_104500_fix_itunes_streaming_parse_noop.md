### Fixed

- The iTunes streaming library parser read zero tracks from a normally
  formatted `Library.xml` and reported success. Apple writes the opening tag of
  a section on the line after its key, and the parser looked for that tag in the
  very next piece of the file rather than the next tag — so it found a line
  break, gave up, and returned an empty library with no error. The track-PID
  backfill built on it therefore did nothing on every run, and then recorded
  itself as complete, so it would never try again. The parser now reads such
  files correctly, and a file with no track section at all is reported as an
  error instead of an empty success.
