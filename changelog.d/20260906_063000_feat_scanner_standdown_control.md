### Added

- **A maintenance op can now ask the library scanner to stand down while it
  works.** The operations registry gained a scan stand-down control:
  `AcquireScanStandDown` cooperatively quiesces any running `library.scan`, waits
  until that scan has actually parked, and holds a refcounted gate so no new scan
  dispatches until the op releases — at which point the scan resumes from its
  checkpoint. This replaces the previous "don't run an apply while a scan is
  active" documented precondition with a real runtime control, so a filesystem
  apply and the scanner can no longer clobber each other.

  Correctness details worth calling out: the quiesced scan is recorded
  `interrupted_quiesced` (a resumable status), not `canceled`, so it comes back;
  resume re-walks and re-reads current rows (it reuses the existing
  checkpoint→fresh-walk path), so an op's writes are never overwritten from a
  stale in-memory batch; a crash-safe **lease** means a dead holder cannot wedge
  the scanner paused, and a holder must treat a lapsed lease as a hard abort; and
  a **persisted marker** makes a reboot-while-held safe — the boot resume sweep
  leaves the scan stopped and warns for reconcile rather than restarting it over a
  dead op's half-applied work. This ships the control only; the maintenance ops
  that use it follow in a subsequent change.
