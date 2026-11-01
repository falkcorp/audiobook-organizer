### Fixed

- Undoing a combine no longer puts back a repair lock whose repair has since
  been reverted. The lock the merge undo restores for a field (a lock source
  of `repair:<operation id>`) is checked against the operation's journal; when
  that operation's lock row was reverted, the lock is lifted instead of
  re-installed and the undo's warnings (shown on the Activity page) say so.
  The restore already ran under the book's metadata-state lock, so it cannot
  interleave with `ClaimRepairLocks` or `repairs.Writer.LockFields`; a test now
  pins that.
