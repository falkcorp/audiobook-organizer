### Fixed

- Under core identity, every books store whose claim set retires the drained `u:` family now marks itself in its one constructor, so an offline, standalone or migration-step store answers its account list with `ErrNoUserDirectory` instead of an empty list until core's directory is installed; the server boot refuses a store that did not mark itself (F4-08 fix-forward).
- Under core identity, `GET /api/v1/auth/sessions` answers 410 Gone and names `GET /api/core/v1/me/sessions` instead of an empty list read from the drained session rows; a caller with no user is refused as before (F4-08 fix-forward).
