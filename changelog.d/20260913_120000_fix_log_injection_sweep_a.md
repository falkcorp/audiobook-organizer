### Security

- Closed 122 of the 307 open `go/log-injection` CodeQL alerts. The fix wraps
  user-controlled log values with `logger.SanitizeLogValue` in
  `internal/metafetch`, `internal/operations/registry`,
  `internal/server/handlers/abs` and `internal/fileops`. Message wording and
  keys are unchanged.
- Added `TestGuard_NoDirectSlogCalls`, a source-parsing CI guard. It fails on a
  new direct `log/slog` call under `internal/` or `cmd/`. A ratchet records the
  328 files and 1,974 calls that exist today, and both may only shrink. The
  plan for the remaining alerts is in
  `docs/audits/2026-09-13-log-injection-sweep.md`.
