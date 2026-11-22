### Changed

- The scanner package tests now set up and read configuration through the copy-on-write config API (`config.Get`, `config.SetForTest`, `config.ReplaceForTest`) instead of assigning to `config.AppConfig` directly. Test set-up only; no behaviour change.
