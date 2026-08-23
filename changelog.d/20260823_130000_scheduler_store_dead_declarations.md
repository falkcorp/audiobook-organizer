### Changed

- Removed two dead method declarations from the scheduler's store interface.
  `CreateOperation` and `UpdateOperationError` were declared as requirements but
  called from nowhere in the package; with the last v1 operation minter retired,
  re-measuring the interface against the compiler enumerated five methods rather
  than the seven its comment claimed. No behaviour change.
