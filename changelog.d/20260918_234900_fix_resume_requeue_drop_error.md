- Fixed a startup-resume path that could run an operation twice. When a `ResumeRequeue` operation was
  resumed after a restart, the registry inserted the replacement operation before checking whether the
  original had actually been retired. If that retire write failed — the disk-pressure / compaction-stall
  shape — the original stayed in a resumable status, so the replacement ran immediately and the next
  startup resumed the original alongside it. The registry now retires the original first and inserts no
  replacement if that write fails, recording the failure in `op_errors_v2` and leaving the row for the
  next startup sweep to retry.
