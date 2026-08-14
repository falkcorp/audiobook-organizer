### Security

- Log-injection sweep wave 1 (CA12): new `logging.Sanitize`/`SanitizeErr`
  escape CR/LF in user-controlled strings; the shared logging conduits
  (`logging.Info/Warn/Error/Debug`, `httputil.InternalError`, the http-request
  error logger) now sanitize messages and string attribute values for every
  caller at once. Also restored the machine-recognizable `strings.ReplaceAll`
  barrier in `internal/logger`'s sanitizer — a comment claimed it existed but
  a refactor had replaced it with a builder loop CodeQL cannot model, leaving
  322 downstream alerts open.
