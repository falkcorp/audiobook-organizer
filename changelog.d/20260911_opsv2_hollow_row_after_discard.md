### Fixed

#### A discarded operation could come back as a blank card on the Activity page

The progress, phase, checkpoint and resume-count writers for operations-v2 rows started from a read that answers "not found" with an empty row, and then wrote that row back. After a run was discarded (`DELETE /operations/v2/:id/record`) while its goroutine was still alive — a `library.scan` the watchdog had abandoned kept reporting for an hour on 2026-09-11 — the next write re-created the row as a shell with no id, name or status, which the timeline counted as in flight and the Activity page rendered as a nameless card with a progress bar. Those writers now refuse a missing row, and migration 62 deletes any shells already on disk at the next start.
