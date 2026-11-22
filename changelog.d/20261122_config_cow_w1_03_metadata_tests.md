### Changed

#### Metadata fetch, batch and metadata-source tests use the copy-on-write config API

The `_test.go` files of `internal/metafetch`, `internal/plugins/metafetch`, `internal/metabatch` and `internal/metadata` set up and read their configuration through `config.Get`, `config.SetForTest` and `config.ReplaceForTest` instead of assigning to or reading the package-global `config.AppConfig`. This is test set-up only (215 sites in 32 files) and changes no runtime behaviour.
