### Changed

- An identity boot that applies a restore now walks the restore journal the way the backup spec orders it (opened, restore_line, hooks_done, marker_written, applied) instead of journaling the hooks as owed at P4b before they ran. A crash anywhere in the hooks leaves the restore in flight, and the next boot resumes the same restore: one restore line, the pre-swap view and the source's boundary (now journaled before the re-apply moves the store's journal head). A boot that did not declare the identity flip, and an identity boot whose store rewrite runs after the restore, keep the owed edge exactly as before.
- `auth.RestrictLastAdmins` is idempotent across a crash. Every line it journals carries the restore run's key, and a line the journal already holds for that run is not appended again; every store effect (sign-in restricted, keys needing confirmation, identity links removed, password blanked) is still applied from the store's own state.

### Added

- The crash-matrix cell `TestIdentityRestore_KilledThenBootedEqualsFaultFree` kills an identity restore at every durable operation of the boot that applies it and requires the killed-then-booted restore to end as a fault-free one does in admins, users, keys, the journal's lines, head and queue, the restore report and the health tokens, with exactly one restore line that restore_hooked names. It runs with and without a pre-swap store.
