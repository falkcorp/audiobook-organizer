### Added

- System > Maintenance has a Provider Throttles panel. It lists the metadata
  providers currently on hold (reason, detail, time left), refreshes on mount,
  on demand and every 4 hours, and shows an empty state when nothing is held.
  Each row has Reset now, and there is a Reset all; both start
  `maintenance.throttle-reset` with `dry_run=false` through the ops-v3 trigger,
  so the run appears in the operations bell, and stay disabled until it settles.
  Buttons are disabled for callers without `scan.trigger`.

### Fixed

- Provider throttle holds no longer expose credentials. A hold's detail comes
  from the raw provider error, which for a transport failure includes the
  request URL and its query string (some providers put the API key there).
  Details are now redacted when recorded (URL userinfo dropped, every query
  value replaced by `***`, bare `key=`/`token=`/`secret=`-style pairs masked),
  redacted again when a persisted hold is loaded, and redacted in
  GET /api/v1/metadata/providers/throttles, so holds saved earlier are scrubbed too.
- The Google Books API key is now sent in the `X-Goog-Api-Key` request header
  instead of a `key=` query parameter, so a transport error no longer carries it
  in the request URL. Errors leaving a chain-built provider source are wrapped
  in a redacted error (original still reachable through `errors.As`/`errors.Is`,
  so throttle classification is unchanged), and the search `sources_failed`
  text is redacted too. URL redaction is stricter: userinfo (even with `/`, `?`,
  `#` or `@` in the password) becomes `***@`, and the whole query becomes `?***`.
