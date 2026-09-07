### Fixed

- **"Revert Metadata Fetch" silently reverted nothing and reported success.** You
  give the job a list of bulk-metadata-fetch operations and it rolls the book
  changes those fetches made back to their previous values. It looked each
  operation up in the old operations storage, which has recorded nothing since
  2026-08-23 — so every operation ID you could paste in resolved to nothing, the
  job skipped it, and it finished cleanly having touched no books at all.

  It now finds operations in the current storage, with the old one as a fallback
  for pre-2026-08-23 runs.

  **Two behaviour changes come with it, both deliberate:**

  An operation ID that cannot be found in either storage is now an error that
  stops the job, where it used to be skipped in silence. A revert is destructive
  and the list you pass is what decides its scope, so quietly reverting three of
  the four operations you named — and calling that success — is the wrong answer.
  If you get this error, the ID is either wrong or belongs to a pre-2026-08-23 run
  that the retention sweep has since removed.

  The job also still refuses to run against an operation that is not a metadata
  fetch, which is what stops a revert being pointed at, say, a library scan. That
  check previously only understood the old storage's labels; it now understands
  both, and covers metadata fetches started from the operations screen as well as
  from the maintenance job.
