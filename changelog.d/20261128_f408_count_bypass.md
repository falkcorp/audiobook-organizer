### Security

- After the identity switch, several permission checks still asked the old user table whether any account existed, and that table is empty after the upgrade. Each one now answers from the accounts the upgrade moved, or refuses: no check treats an upgraded install as a fresh one with no accounts. The affected checks were the diagnostics export (what a background export may include, and who may download it), permission checks reached outside a normal request, and the first-run settings window.
- `POST /api/v1/auth/setup` answers 410 Gone after the identity switch. The first account is created through core's `POST /api/core/v1/auth/setup`, with the bootstrap token the server writes at start.

### Changed

- After the identity switch the server no longer runs the old API-key expiry stamp, the legacy password count or the old startup read-only key writer at start. Keys and passwords are core's records now, and the startup read-only key is core's `core.auth.bootstrap.readonly_key.enabled` (the upgrade renames `write_startup_readonly_key` to it).
