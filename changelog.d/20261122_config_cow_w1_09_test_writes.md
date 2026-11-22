### Changed

- Test set-up in `internal/{ai,appdirs,batch,books,catalog,decisions,deluge,importer,itunes,transcribe,versions,remux}` and `internal/plugins/{acoustid,deluge}` now changes the process configuration through `config.SetForTest` and `config.ReplaceForTest`, and reads it through `config.Get()` and `config.Snapshot()`, instead of assigning to `config.AppConfig` directly (config-cow slice W1-09, 198 sites). Test-only; no production behaviour changes.
