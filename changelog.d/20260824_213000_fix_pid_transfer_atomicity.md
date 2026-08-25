### Fixed

- **iTunes PID transfers now commit with the row that claims them.** Creating a
  `book_file` that carries an iTunes persistent ID already held by another row
  transfers ownership by clearing the PID from the prior owner. That clear went
  through `ClearITunesPID`, which commits a database batch of its own, while the
  row taking the PID was written in a separate, later batch. Any failure in
  between — a marshal error, an index write, or the commit itself — left the PID
  erased from the old row and never written to the new one, so it belonged to
  nobody and nothing reported an error. The transfer is now staged into the same
  batch as the row claiming the PID, so it lands with that row or not at all.
  Affects `CreateBookFile` (reachable today: the version-split copies in the
  organizer and metafetch apply paths carry PIDs onto new rows) and
  `BatchCreateBookFiles` (not reachable before this change — its only caller,
  the maintenance relink repair, builds rows without PIDs — so no stored data
  was affected there).
