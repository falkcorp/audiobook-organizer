### Changed

- `core.realtime.ring_size` defaults to 4096 (was 1024), in the section default and the 05-realtime settings table. The kernel's own ring floor (`realtimeRingFloor`, `realtimeRingDefault` and the log line that reported it) is gone, so `ring_size` reaches the realtime hub as configured. Dark: the realtime hub is not built yet.
- `books.book.relocated.v1` carries `book_id` only. `from_path` and `to_path` are dropped: the consumer acts on the book's path as stored when it runs and read neither. Nothing has staged the topic yet, so no stored message changes.
- A path that goes to empty and back (`path`, `""`, `path`) is documented and pinned as no relocation, because no file moved, as in HEAD's `NotifyDelugeAfterOrganize`.
- The task executor's `StartIn` reads the queued runs once per batch and computes each queued run's dedupe key once, so a bulk start of N runs over M queued ones decodes M runs instead of N times M. Two merges into the same queued run inside one batch now build on each other instead of the second overwriting the first.
