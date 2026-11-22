### Changed

- Tests in `internal/dedup`, `internal/plugins/dedup`, `internal/reconcile` and `internal/merge` now set up and read configuration through the copy-on-write API (`config.Get`, `config.SetForTest`, `config.ReplaceForTest`) instead of assigning to `config.AppConfig` directly (config-cow slice W1-08, 204 sites). Test set-up only, no behaviour change.
