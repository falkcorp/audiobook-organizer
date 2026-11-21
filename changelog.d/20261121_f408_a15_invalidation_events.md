### Added

- core/auth: every revoking write calls the new `Deps.OnInvalidated` after its batch commits, and never for a batch
  that does not, so realtime can end a revoked credential's open stream at once instead of on its next heartbeat
  (F4-08 A15, dark until the flip binds the callback to the kernel's bus). Events name only what the write revoked:
  the key or session itself, the whole user for a disable, delete or merge, and each other session for a credential
  epoch bump, so the session that changed its own password and the user's API keys keep their streams.

### Changed

- core/auth: `Admin.StageUserMerge` returns an `AfterCommit` step that the caller runs once its batch has committed.
  The step publishes the merged-away user's invalidation, and publishes nothing if the commit did not land.
