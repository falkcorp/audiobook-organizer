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
- Five maintenance jobs resume again. `bulk-deluge-import`, `cleanup-empty-folders`,
  `refetch-missing-authors`, `repair-missing-files` and `scan-composer-tags` all
  advertise that they can be resumed, but declared `ResumeDrop`; they had been
  resuming only through a legacy startup branch that was removed when the last v1
  operation minter was retired, which left them silently never resuming. They now
  declare `ResumeRestart`, which resumes the existing run in place with its
  original `dry_run` setting preserved.
- An interrupted `bulk-fetch-metadata` run no longer re-fetches the whole library.
  It tracks completed books against its operation id, and its previous
  `ResumeRequeue` policy issued a new id on every resume, discarding that record.
  It now resumes in place and picks up where it stopped.
