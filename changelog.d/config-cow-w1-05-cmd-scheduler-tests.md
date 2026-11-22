### Changed

- The `cmd` and `internal/scheduler` tests now set up and read configuration through the copy-on-write API (`config.Get`, `config.SetForTest`, `config.ReplaceForTest`) instead of writing `config.AppConfig` directly (config-cow slice W1-05). Test set-up only; no behaviour change.
