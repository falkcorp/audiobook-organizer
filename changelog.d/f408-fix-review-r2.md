### Fixed

- Under core identity, every books store whose claim set retires the drained `u:` family now marks itself in its one constructor, so an offline, standalone or migration-step store answers its account list with `ErrNoUserDirectory` instead of an empty list until core's directory is installed; the server boot refuses a store that did not mark itself (F4-08 fix-forward).
