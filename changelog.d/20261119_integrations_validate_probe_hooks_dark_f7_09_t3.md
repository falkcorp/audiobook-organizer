### Added

- Core integrations gain a per-kind settings validator (`Registry.SetValidator`), run on Put, Test and import (`ImportValidator`) and never on the serve path, and an `Options.Probed` callback fired on every health probe. The ai Router can install its endpoint rule as that validator (`RouterOptions.ValidateEndpoints`, off by default) and `Router.ObserveProbe` clears a failure bench on a `HealthOK` probe. Dark: nothing in the application installs either until the F7-09 flip.
- `core/storage` registration now rejects an invalid or duplicate `Spec.Derived` entry and any `Derived` declaration on a core subsystem, with a `SpecError` at open.

### Changed

- `ai.Router.ClearBench` leaves a quota bench that is still in force alone (a health probe succeeds on a spent balance); a real successful call still ends every bench. A benching failure that lands while a quota bench is in force keeps it a quota bench with the later end time, so a probe cannot clear it and the quota window is not shortened.
- The settings validator is handed what the row serves (stored layer plus operator overlay) as its stored layer, so a value an operator locks is grandfathered like a stored one.
- `integrationhttp.ErrHoldChanged` is removed; the hold-release route recognises `providerhttp.ErrHoldChanged` directly, so the real provider registry serves it without a wrapper.
