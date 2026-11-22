### Changed

- The internal/server tests now set and read the process configuration through the copy-on-write API (`config.Get`, `config.SetForTest`, `config.ReplaceForTest`) instead of assigning to `config.AppConfig` directly. Test set-up only; no production code changes. The credential-guard route fixture applies its caller's configuration function with `config.SetForTest`, and the scan integration test builds its organizer from `config.Get()`.
