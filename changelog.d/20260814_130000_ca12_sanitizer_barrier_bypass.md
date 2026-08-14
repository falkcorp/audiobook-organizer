### Fixed

- Log-injection sanitizers (`logging.Sanitize`, `logger.sanitizeLogLine`) had a
  clean-string fast-path that returned before the `strings.ReplaceAll` barrier,
  so CodeQL's path-sensitive analysis saw taint bypassing the sanitizer and
  321 of 322 go/log-injection alerts survived the CA12 wave-1 conduit fix.
  Every path now flows through `ReplaceAll` (which is already allocation-free
  on clean strings); output behavior is unchanged on all inputs.
