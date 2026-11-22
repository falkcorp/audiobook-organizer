### Added

#### Copy-on-write config prototype (`config.Get()`), codemod and design

Prototype for replacing the mutable package-global `config.AppConfig` with an immutable snapshot behind an atomic pointer. Today a background reader that reads `config.AppConfig` without the lock while a settings save runs is a data race. The design is in `docs/plans/platform-core/config-cow.md`.

`config.Get()` returns the published snapshot. Writers copy it, change the copy and publish the copy, so readers never take a lock and never see a half-written config. Under `go test`, a test-time seal panics if anything writes into a published snapshot in place. `scripts/config_cow_codemod.py` rewrites reads mechanically and can be re-run. It refuses pointer hand-offs and writes, and lists them for a human. For now it has been applied to `internal/reconcile`, `internal/dedup` and `internal/scanner`. A new guard test refuses writes through `config.Get()`.

### Fixed

#### `Config.Validate()` no longer rewrites the config it checks

`Validate` used to fill defaults in its receiver: the iTunes path shims, `embedding.vector_backend`, and the zero-means-default retention and size fields. Called at boot as `config.AppConfig.Validate()`, it therefore wrote the live config field by field without the lock. `Normalized()` now returns a normalised copy, `NormalizeAndValidate()` publishes it in one step, and both boot sites use it. The settings save normalises its candidate explicitly, so what gets stored is unchanged.
