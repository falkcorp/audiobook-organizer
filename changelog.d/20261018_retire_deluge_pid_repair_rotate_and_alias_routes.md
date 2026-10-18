### Removed

- Six more endpoints with no caller in the repo now answer `410 Gone` (they still check sign-in and permissions first, so an unauthorized caller gets 401 or 403, and are removed entirely in a later release). Each names its replacement in the 410 message:
  - `DELETE /api/v1/books/:id/versions/:vid`, `POST /api/v1/books/:id/versions/:vid/restore` and `POST /api/v1/books/:id/versions/:vid/purge-now`: use the same paths under `/api/v1/audiobooks/:id/versions/...`, which were always the live routes.
  - `POST /api/v1/deluge/discover/import`: use `POST /api/v1/import/file`, which imports one file by path but does not link the torrent. `GET /deluge/discover` and `GET /deluge/labels` are unchanged.
  - `POST /api/v1/itunes/pid-repair`: no replacement. It cleared duplicate iTunes PIDs on database rows only (never the iTunes library); the last census found nothing left for it to clear. `GET /itunes/pid-integrity` and the `pid-census --repair` plan preview are unchanged.
  - `POST /api/v1/auth/api-keys/:id/rotate`: create a new key with `POST /api/v1/auth/api-keys`, then revoke the old one with `DELETE /api/v1/auth/api-keys/:id`. The new key no longer inherits the old key's scopes and lifetime, and the old key no longer gets a one-hour grace window, so create the new key and switch clients over before revoking. An API key still cannot act on another user's keys. Creating, listing, reading, disabling and revoking keys are unchanged.
- Code that only these routes used is deleted: the Deluge discover-import handler, `itunes.ApplyPIDRepairPlan` (the PID repair apply) and its `UpdateBookFile` dependency, the API-key rotate handler with its lifetime and grace-window helpers, and the handler store's `SetAPIKeyExpiry` method. No database store methods were removed: `SetAPIKeyExpiry` is still used by the one-time key expiry stamp.

### Changed

- Startup and expiry-warning log lines about API keys without an expiry now say to create a replacement key and revoke the old one, instead of telling the operator to rotate it.
