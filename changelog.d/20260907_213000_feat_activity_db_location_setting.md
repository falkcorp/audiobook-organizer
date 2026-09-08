### Added

- **The activity database's location is now a setting, and moving it is safe.**
  Settings → Paths gained an **Activity Database** section: a path field and a
  "Move the existing database when this path changes" toggle. Leaving the path
  empty puts the database at `{library}/.activity/activity.sqlite` — a
  dot-directory that every library walk already skips, so the multi-gigabyte
  SQLite file and its WAL never get scanned as content.

  With the toggle on (the default), changing the path **copies** the existing
  database to the new location, verifies it by comparing row counts, and only
  then removes the original. It is never a rename: the new location is usually on
  a different filesystem, where a rename fails outright. Every failure path leaves
  the source completely intact and logs loudly, and a relocation that cannot be
  completed never stops the server from starting — the fallback is to open the
  configured path and keep logging, which beats refusing to boot over the location
  of a log file. The copy also refuses to start while another connection holds the
  database, rather than copying a file being written underneath it.

  With the toggle off, a path change starts an empty database at the new location
  and leaves the old file where it is; the UI says so in those words, because
  "the activity log started over" and "the history was deleted" look identical
  from the dashboard.

- **Settings controls that the server's environment overrides now say so.** The
  config API reports which settings are pinned by environment variables, and the
  matching controls render disabled with the responsible variable named. Without
  this, a field pinned by the service configuration looked ordinary, accepted an
  edit, saved it — and was overwritten from the environment on the next boot. This
  is live in production today, where `ACTIVITY_DB_PATH` is set in the systemd
  unit, so the activity-database path field correctly shows as read-only there
  until that line is removed.

  Settings **export/import** deliberately excludes the activity-database path. It
  is a per-host location, and importing another machine's settings file would
  otherwise queue a relocation of a multi-gigabyte database on the next start,
  possibly to a path that does not exist on that host.

### Changed

- `SettingsGeneral` no longer keeps its own 44-field structural copy of
  `SettingsState`; it imports the canonical type. The duplicate had to be updated
  in lockstep by hand, and adding a field to the real one broke the component at
  a prop boundary with an error naming neither file.
