### Fixed

- Server startup no longer waits on the activity database. The "Server started" activity
  entry used to be written synchronously inside `NewServer`; on 2026-09-14 that write landed
  on a multi-GB SQLite WAL left by an interrupted activity compaction, ran the whole
  checkpoint inline, and kept the HTTP listener closed for about six minutes. The entry is
  now queued (`activity.Service.RecordDeferred`) and written in the background once the
  listener has been started. Shutdown closes the activity store through
  `activity.Service.Close`, which writes anything still queued first and never closes the
  store under an in-flight write. If its 5s budget runs out, the store is left open and the
  log line counts the entries not yet confirmed written.
- The SQLite activity store no longer lets a foreground write run a WAL checkpoint.
  `wal_autocheckpoint` is 0 on every connection. A background checkpointer on its own
  connection runs PASSIVE every 30s, then TRUNCATE when the store is idle, and
  `journal_size_limit` caps the reset WAL file at 64 MiB.
- Activity compaction checkpoints after every deleted chunk instead of once per day, so a
  multi-million-row day can no longer build a multi-GB WAL, and interrupting it no longer
  leaves one behind.
- Closing the SQLite activity store is now bounded. The final checkpoint gets 5s. If it
  cannot finish, the writer handle is left open so SQLite does not run an unbounded
  close-time checkpoint past systemd's stop timeout. Nothing is lost, because the WAL is
  recovered on the next open.
