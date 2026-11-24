### Added

- `users reset-password <username>` (offline, service stopped) resets one
  account's password, clears its sign-in restriction and reset-required flag,
  bumps its credential epoch and prints a generated password once to standard
  output. It is journaled and audited and stores no plaintext. A disabled or
  pending account is refused.
- `storage journal-queue list` and `storage journal-queue clear <seq> --confirm`
  (offline, service stopped) show and clear entries of the unapplied-revocation
  queue. An entry the verified journal proves can never apply is removed; an
  entry the journal still holds is refused. A row that does not decode is never
  just deleted: it is rebuilt from the journal line it named, or, when the
  journal cannot name it, the store fails closed first (every API key needs
  confirmation again, every credential epoch is bumped) and the row is removed.
- `scripts/ci/image_probe.sh` can check, with `PROBE_AUTH_OFF=1`, that an image
  booted with authentication off reports `auth_disabled_remote` on the
  authenticated status route and never on `/health`.
