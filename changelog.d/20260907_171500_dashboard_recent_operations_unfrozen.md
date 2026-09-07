### Fixed

- **The dashboard's "Recent Operations" panel had been frozen since 2026-08-23.**
  It read the retired v1 operations keyspace, which nothing has written to since
  the v1 id minter was retired, so it showed the same five pre-retirement runs to
  everyone who loaded the page — with nothing on screen to say the list was stale.
  On production the newest entry it could offer was from **2026-08-21**. It now
  reads the current operations keyspace and shows the five most recent runs.

- **A canceled or interrupted operation no longer renders as if it were running.**
  The current operations system has states the old one did not — canceled,
  waiting-on-dependencies, and four kinds of interrupted — and the dashboard
  collapsed all of them into "running". Stopped runs now get their own icon and
  colour.

  This also fixes what would have been a new bug: the dashboard polls the server
  every 15 seconds while any operation looks active, and a canceled run that reads
  as "running" is a poll that never stops. Whether to keep polling now comes from
  the operation's completion time rather than from its status name, so a state
  added in the future cannot restart that loop by accident.
