### Fixed

- Batch apply: when an owner-reviewed apply's change history failed to record
  AND the file write-back after it also failed, the write-back error replaced
  the history error, so the op log never said the override had no history.
  The outcome now keeps both (`HistoryErr`, `WriteBackErr`, and a joined
  `Err`), and each op log line reports its own error.
