### Changed

#### Activity page: only running work is open by default, and finished runs get a Retry button instead of Refresh

On the Activity page's operations panel, only the Active section is expanded when the page loads; Pending, Completed, Failed, Canceled and Interrupted start collapsed, with their counts still shown in the headings. Collapse All and Expand All work as before. The per-row Refresh icon now appears only on rows that can still change. Completed rows have no per-row action. Failed, Canceled and Interrupted rows get a Retry button that requeues the operation as a new run (`POST /api/v1/operations/v2/{id}/retry`) and reports the new id; Interrupted rows also get a Discard button that cancels the operation so it is never resumed.
