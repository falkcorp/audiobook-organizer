### Fixed

- **The window backfill refused most of the library.** `remoteEligible` required
  a file to sit under the `libroot` root specifically, but `pathutil.PathVars`
  has always returned two roots — `libroot` (…/books/audiobook-organizer) and
  `books`, its parent — so every file under the parent was deferred as
  `not_under_libroot` and no worker was ever offered it. Measured in production
  on 2026-09-20: **144,708 of 200,729 files walked in a single run (72%)** were
  refused for this reason, and a re-run refused them again; a 400-book sample of
  the library held 339 under `books` against 60 under `libroot`. Nothing
  technical required the restriction — the worker already accepts a map of roots
  and both production workers can read the parent mount. Files under any known
  root are now eligible, the job names the root its relative path was split
  against instead of hardcoding `libroot`, and a file under no known root is
  still refused (now as `not_under_any_root`, since that is what it means).
