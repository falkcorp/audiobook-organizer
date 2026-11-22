### Changed

- The tests in `internal/organizer` and `internal/audiobooks` no longer assign to or read the package-global `config.AppConfig` directly. They set configuration through `config.SetForTest` and `config.ReplaceForTest` and read it through `config.Get()`, and the 13 places that handed `&config.AppConfig` to `NewOrganizer` now hand it `config.Get()` after the test has set its configuration. Test-only change; no behaviour change in the application.
