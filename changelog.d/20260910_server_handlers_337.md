### Added

#### Dry-run mode for `DELETE /operations/history`

Clearing operation history is irreversible, and until now the endpoint reported how many rows it had removed only after they were already gone — there was no way to see the blast radius first. `DELETE /operations/history?status=...&dry_run=true` now returns the per-status census of rows that *would* be deleted (`would_delete` plus a `counts` breakdown) and deletes nothing, and the real delete returns the same `counts` map taken immediately before it runs, so the response says what was removed and not just how much. Dry run is opt-in rather than the default: this endpoint has always deleted immediately, and quietly turning existing callers into no-ops would leave the UI's clear-history button reporting success while the rows stayed. The census is fail-closed — if counting fails, the request returns an error having deleted nothing.
