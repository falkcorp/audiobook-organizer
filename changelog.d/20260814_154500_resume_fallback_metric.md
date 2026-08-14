### Added

- New Prometheus counter `audiobook_organizer_maintenance_resume_params_fallback_total{job_id, reason}`:
  fires when an interrupted maintenance job resumes WITHOUT the operator's saved
  params, falling back to the job's advertised `dry_run` default
  (`reason=no_saved_params` or `load_error`). Since #2419 persists resolved params
  on every enqueue, any fire after pre-#2419 rows age out means a params save
  silently failed — an alertable condition instead of a log line that ages out of
  journald. (C511)
