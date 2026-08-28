<!-- file: changelog.d/20260828_121000_writeback_queue_coalescing.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9529a060-d452-41b2-a546-bdfbaaaa3e50 -->
<!-- last-edited: 2026-08-28 -->

### Improved

#### Compatible queued metadata write-back selections now coalesce

Queued `Batch Save to Files` and `Bulk Tag Write-back` requests now combine
their selected books into one operation when their behavior options match. A
request with different organize, force, or rename settings remains separate,
and running operations are never changed.
