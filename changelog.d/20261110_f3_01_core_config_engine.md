### Added

- Core: `core/config` is now the full config engine (platform-core F3-01), shipped dark. It provides the store, typed handles with `OnChange` and `AfterCommit`, the operator lock, `Apply` with section permission and protection checks, the boot record (written atomically, a bad file set aside), quarantine of stored values that fail validation, the JSON Schema of every key, the importer, the native override store and committer over `c/config/` (row and audit record in one batch), the `mediacore.config` metrics, and `config.ResolveStateDir`. `core/kernel/config.go` holds the boot wiring but nothing calls it yet. ABO's boot and settings writes are unchanged until F3-07.

### Changed

- Core: `Register` refuses a protect, reload or sensitive tag inside a key's value (a list, map key or value, array, pointer or self-encoding struct) that is stricter than the key itself. It also refuses two fields with one JSON name in one object, and an embedded pointer to a struct, so one setting can never stand for two fields with different protection. `core.auth.oidc.providers` is now owner-protected as a whole list, so an admin can no longer repoint an owner-trusted provider.
- ABO: the credential-directory rule in `internal/config/state_dir.go` now delegates to core's `config.ResolveStateDir`. Behaviour is unchanged.
- Core: `app.ResolveStateDir` now delegates to `config.ResolveStateDir`, so core and ABO pick the state directory by one rule. A padded `<APP>_CORE_PATHS_STATE_DIR` is trimmed and cleaned, and the directory beside the database is used as a fallback only when it can be created.

### Fixed

- Core: every settings audit record names the person who made the change, with their sign-in method and client, including a change that only resets settings to their defaults. The boot-time settings file is always written fresh with owner-only permissions, never through a leftover file or link.
- Core: an invalid value of a sensitive setting no longer appears in errors or logs, whether the schema, the type check, a flag or a section's own validation rejects it. In a section with a sensitive setting, a validation message that is not tied to one plain setting is replaced by a fixed one. A boot-record value that fails to decode now raises a health warning instead of being ignored silently.
