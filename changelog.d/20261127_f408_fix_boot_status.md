### Fixed

- After the identity switch, `GET /api/v1/auth/status` told every upgraded install it was a fresh one (no users, setup offered, sign-in not required) and `GET /api/v1/users` returned an empty list, because both still read the old user table that the upgrade empties. Both now read the accounts the upgrade moved: the status answers as it did before the upgrade for the same people, and the list shows every account, the built-in `_system` account included as before. `_system` alone does not count as a user, so an install with no person yet still offers setup.
- The server no longer tries to re-create the old built-in roles and system user at every start after the identity switch. Both writes were refused (the upgrade retires those tables) and logged a warning on each start.
