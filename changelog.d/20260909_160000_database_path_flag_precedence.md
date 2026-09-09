### Fixed

- The `--db` flag and the `DATABASE_PATH` environment variable are now honoured
  again. A database path saved in the config blob months earlier silently
  outranked both on every start, so moving the database required editing the
  database you were trying to move. On 2026-09-09 this took production down: the
  server opened the new database correctly, then restored the old path string
  from that database's own config blob and refused to start because the old
  directory no longer existed.
- Configuration errors about a database path now report what actually went wrong.
  Every failure to inspect the parent directory was reported as "does not exist",
  so a permissions problem, a path whose parent is a file, and a genuinely
  missing directory were indistinguishable.
