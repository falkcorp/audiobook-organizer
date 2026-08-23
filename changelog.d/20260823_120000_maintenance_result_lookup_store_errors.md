### Fixed

- Maintenance result lookups no longer report a database failure as a missing
  operation. The two operator-facing result routes
  (`/api/maintenance/composer-scan/:id` and the missing-file repair route) read
  two operations keyspaces, and a failed read was being folded into "operation
  not found" — answering `404` and logging nothing, which pointed the operator at
  a bad id when the database was the thing that had broken. A store failure now
  logs at `error` and answers `500`; a genuinely unknown id still answers `404`.
- A maintenance run that cannot obtain an operation id now says so at `warn`
  before the id is used, rather than only when the activity summary is skipped.
  An empty id also disables per-item result writes and any resume skip-set keyed
  off them, none of which surfaced as an error anywhere.
- Five maintenance jobs no longer declare a resume policy that contradicts
  themselves. `bulk-deluge-import`, `cleanup-empty-folders`,
  `refetch-missing-authors`, `repair-missing-files` and `scan-composer-tags` all
  report that they can be resumed while declaring `ResumeDrop`; they had been
  resuming only through a legacy startup branch removed when the last v1 operation
  minter was retired, which left them silently never resuming. They now declare
  `ResumeRestart`, which resumes the existing run in place with its original
  `dry_run` preserved.

  **Scope:** this restores resume for a run interrupted by a hard kill, where the
  row is left `running`. It does not restore it after an ordinary shutdown — a
  separate, pre-existing gap means the startup sweep cannot see a run that shut
  down cleanly, whatever its policy says. See the TODO entry on
  `resumeAfterStartup`.
- An interrupted `bulk-fetch-metadata` run no longer discards its record of
  completed books. It tracks them against its operation id, and its previous
  `ResumeRequeue` policy issued a new id on every resume, so a resumed run
  re-fetched the entire library over the network. It now resumes in place, subject
  to the same scope note above.
