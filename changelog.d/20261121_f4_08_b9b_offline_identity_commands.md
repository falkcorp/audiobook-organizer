### Added

- `users reset-password <username>` (offline, service stopped) resets one
  account's password, clears its sign-in restriction and reset-required flag,
  bumps its credential epoch and prints a generated password once to standard
  output. It is journaled and audited and stores no plaintext. A disabled or
  pending account is refused.
- `storage journal-queue list` and `storage journal-queue clear <seq> --confirm`
  (offline, service stopped) show and remove entries of the unapplied-revocation
  queue. Only an entry that does not decode, or one the verified journal proves
  can never apply, can be cleared; an entry the journal still holds is refused.
- `scripts/ci/image_probe.sh` can check, with `PROBE_AUTH_OFF=1`, that an image
  booted with authentication off reports `auth_disabled_remote` on the
  authenticated status route and never on `/health`.
