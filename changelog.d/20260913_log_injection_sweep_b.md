### Security

- Closed the 81 open `go/log-injection` CodeQL alerts in `internal/server` and
  its handler packages (`handlers`, `handlers/audiobooks`, `handlers/metadata`,
  `handlers/dedup`, `handlers/operations`, `handlers/duplicates`, `absauth`,
  `middleware`); `handlers/abs` was covered by batch A. User-controlled log
  values are wrapped with `logger.SanitizeLogValue` at the sink. Message
  wording and keys are unchanged. Errors and non-string values (bools, ints,
  string slices) pass through `fmt.Sprint` first, so they print as before but
  are logged as strings.
