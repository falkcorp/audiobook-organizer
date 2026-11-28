- [ ] **OFFLINE-IDENTITY-CLI-ABO** Let core's offline identity commands run on ABO after the identity flip (due before R2).
      `books.OfflineOptions` (internal/books/options.go) never sets `IdentityBoot`, so the
      offline open's owner list lacks `core/auth`, `core/rbac` and `core/backup`, and core's
      offline identity commands (`users reset-password`, `storage journal-queue`) refuse on a
      post-flip ABO store. Lockout recovery does not depend on them: the bootstrap token
      (core's `POST /api/core/v1/auth/bootstrap`) mints an administrator key on a post-M3
      store, pinned end to end by `TestBootstrapToken_RedeemsOnceAtCoreAndNotAtBooks`
      (token file, exchange, key lists every account), and `make rollback-restore` is the
      fallback. Fix: declare `IdentityBoot` in `OfflineOptions` with cells for each offline
      identity command on a migrated store. Separate PR, not a deploy gate.
