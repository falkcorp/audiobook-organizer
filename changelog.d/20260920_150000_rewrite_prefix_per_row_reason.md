- `maintenance.rewrite-path-prefix` no longer prints a misleading reason on refused
  rows. Its verdict is decided per book (the check stops at the first failing field)
  but the report has one line per field, so a reason naming a path — "file does not
  exist on disk: .../001.mp3" — was repeated verbatim on the lines for 002, 003 and
  so on. Anyone chasing a genuine per-file refusal was sent to the wrong file. The
  causing row now carries the real reason and every other row of that book points
  at it.
