### Fixed

- **The Mock Freshness check could not see a newly generated mock.** It compared
  with `git diff`, which only looks at tracked files, so adding an interface and
  forgetting to commit its generated mock left an untracked file that the check
  reported as "up to date" — the exact case the job exists to catch. It now uses
  `git status --porcelain`, which sees untracked and modified entries alike.

- **The memory-leak scan filed bug reports when its own scanner crashed.** Any
  non-zero exit was recorded as `has_leaks=true`, and the report job then opened
  and auto-merged a TODO.md entry claiming leaks that were never detected.
  `check-memory-leaks.py` returns 1 for findings, but an uncaught Python
  exception also exits 1, so the exit code alone cannot tell the two apart; the
  job now requires the scanner to have actually reported findings, and fails
  loudly as a scanner failure otherwise.

- **The leak count wrote a malformed line into `$GITHUB_OUTPUT`.** `grep -c`
  prints `0` *and* exits 1 when nothing matches, so `$(grep -c ... || echo "0")`
  captured both its output and the fallback, emitting `leak_count=0` followed by
  a stray `0` line.
