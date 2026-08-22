### Fixed

- **An interrupted iTunes path-repair preview could come back as a real apply.**
  `POST /operations/itunes-path-repair` defaults to a dry run, but if the server
  restarted mid-run, the legacy resume path re-queued the operation with no
  parameters at all — which decodes to `dry_run: false`. The preview resumed in
  apply mode and rewrote locations in the live iTunes library, with nothing in
  the original request having asked for it. Resume is now explicitly dry-run,
  and new runs no longer take that path at all. The equivalent bug in
  maintenance jobs was fixed earlier; this was its untreated twin.

### Changed

- **iTunes path-reconcile and path-repair no longer create legacy v1 operation
  rows.** Both are now native v2 operations, so what happens to an interrupted
  run is decided solely by the operation's own `ResumePolicy` instead of by two
  mechanisms that disagreed. Neither auto-resumes after a restart — a six-hour
  library-writing operation that restarts itself on every deploy is a known way
  to jam the work queue — so re-trigger by hand if a run is interrupted.
- Both endpoints now return `{operation_id, status}` (repair also echoes
  `dry_run`) instead of a full legacy operation record. No UI consumes these;
  they are operator endpoints.
