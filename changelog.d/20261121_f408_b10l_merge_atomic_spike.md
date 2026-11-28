### Added

- Tests pinning that core's `auth.Service.StageUserMerge` stages into a batch taken from a module's `LegacyRange` (core/auth and the module's own rows in one store): the module's rows, guarded by `Batch.Expect`, and the merge land in one Commit or not at all. Covers the happy path, a module-row conflict, a credential-row conflict, and that `AfterCommit` publishes nothing after a failed commit. Test-only; no behaviour changes.
