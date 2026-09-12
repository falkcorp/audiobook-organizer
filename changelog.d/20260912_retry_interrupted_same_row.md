### Fixed

#### Retry on an interrupted operation resumes that operation instead of starting a copy

Pressing Retry on an interrupted operation now puts that same operation back in
the queue. It leaves the Interrupted list, runs under the same ID, keeps one
continuous log with a "manual retry requested by ..." line where the retry
happened, and picks up from its last checkpoint when it has one. Before, Retry
started a second operation and left the old one in Interrupted, so the logs were
split across two IDs. An interrupted scan left behind that way could also run
again on the next server restart, even after the retry had finished the work.

Retry is refused (409, with the reason) while another run of the same operation
is still queued or running. Manual retries no longer count toward the
repeated-restart guard, so retrying an operation that guard had dropped is not
dropped again as soon as it starts. Failed and canceled operations still start a
new run, as before.
