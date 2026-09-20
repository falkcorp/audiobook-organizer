### Fixed

- **`maintenance.duration-reextract` silently previewed when asked to apply.** It
  declared its flag as `dryRun` only, while the maintenance ops beside it declare
  `dry_run`. `encoding/json` drops an unrecognised field without a word, so a run
  started with `{"dry_run": false}` fell back to the struct default — dry run —
  and previewed instead of applying. Caught in production on 2026-09-20. The
  summary itself is correct and does say `would correct` rather than
  `corrected N`, which is precisely what made it easy to miss: it was a truthful
  dry-run line for a run the caller believed was applying.
  Both spellings are now accepted, absent still means dry run, and sending both
  with different values is refused rather than guessed — the pattern
  `maintenance.author-path-link` and `maintenance.author-id-repair` already use.
