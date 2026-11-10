### Added

- Core: `core/config` is now the full config engine (platform-core F3-01), shipped dark. It provides the store, typed handles with `OnChange` and `AfterCommit`, the operator lock, `Apply` with section permission and protection checks, the boot record (written atomically, a bad file set aside), quarantine of stored values that fail validation, the JSON Schema of every key, the importer, the native override store and committer over `c/config/` (row and audit record in one batch), the `mediacore.config` metrics, and `config.ResolveStateDir`. `core/kernel/config.go` holds the boot wiring but nothing calls it yet. ABO's boot and settings writes are unchanged until F3-07.

### Changed

- Core: `Register` refuses a protect, reload or sensitive tag inside a key's value (a list, map, array, pointer or self-encoding struct) that is stricter than the key itself. `core.auth.oidc.providers` is now owner-protected as a whole list, so an admin can no longer repoint an owner-trusted provider.
- ABO: the credential-directory rule in `internal/config/state_dir.go` now delegates to core's `config.ResolveStateDir`. Behaviour is unchanged.

### Fixed

- Core: an invalid value of a sensitive setting no longer appears in errors or logs. A boot-record value that fails to decode now raises a health warning instead of being ignored silently.
