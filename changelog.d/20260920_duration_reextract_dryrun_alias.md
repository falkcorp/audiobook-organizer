### Fixed

- **`maintenance.duration-reextract` silently previewed when asked to apply.** It
  declared its flag as `dryRun` only, while the maintenance ops beside it declare
  `dry_run`. `encoding/json` drops an unrecognised field without a word, so a run
  started with `{"dry_run": false}` fell back to the struct default — dry run —
  and printed the same `examined=76280 ... would-change=17161` summary a real
  apply prints, having written nothing. Caught in production on 2026-09-20 only
  by re-reading a book the summary named as `13s → 2221s`; it was still 13s.
  Both spellings are now accepted, absent still means dry run, and sending both
  with different values is refused rather than guessed — the pattern
  `maintenance.author-path-link` and `maintenance.author-id-repair` already use.
