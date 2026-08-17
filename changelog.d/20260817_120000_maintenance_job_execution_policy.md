### Changed

- Every maintenance job now declares its own execution policy (`ResumePolicy`, `Timeout`,
  `ConcurrencyKey`, `Liveness`, `Capabilities`) via a new `Policy()` method on
  `maintenance.MaintenanceJob`. Nothing reads these yet — the `maintenance.job` bridge still
  supplies its own hardcoded values — so behaviour is unchanged. This is the declaration step
  that lets each job become its own v2 `OperationDef` in a follow-up.

### Fixed

- Corrected the resume policy planned for maintenance jobs that report `CanResume()` but store no
  checkpoint. They were slated to declare `ResumeRestart`, which means *reload the last
  checkpoint*; the value meaning *re-run from zero* is `ResumeRequeue`. The mislabel was not
  cosmetic: the watchdog writes an `uncheckpointed` strike against every `ResumeRestart`
  operation that goes quiet, and a separate guard force-drops one whose progress high-water mark
  never advances — both of which describe a job that never checkpoints.
