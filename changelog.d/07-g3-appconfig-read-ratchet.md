### Added

- A new Go test, `TestAppConfigDirectReadRatchet`, counts direct uses of the global `config.AppConfig` outside `internal/config` (625 today) and fails if the number rises, so new code reads `config.Snapshot()` or takes the value as a parameter. The number can only be lowered, by hand, in the change that removes the uses.
