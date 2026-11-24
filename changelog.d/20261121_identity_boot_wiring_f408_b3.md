### Added

- An app that sets `app.Options.IdentityBoot` now boots core's identity: the events bus, rbac, auth and the audit logger are built at P5, the revocation journal and the restore hooks run at P4d, each module's `Deps` gain `Auth`, `AuthAdmin`, a module-scoped `Audit` (actions must start with the module's ID) and `Events`, and P7 declares the topics, hands auth the modules' `AuthMethod` specs and commits the seed roles. A module that registers an `AuthMethod` holds an `AuthCompat` that refuses until P7 binds it. The `core.backup` health tokens reach only the authenticated `degraded` field of `GET /api/v1/system/status`. With the switch off, which is the default and what the audiobook app still passes, none of this is built and boot is unchanged.

### Fixed

- In auth-off mode the synthetic principal's audit actor now carries the user ID `auth_disabled`. It had none, and the config committer refuses a commit whose actor has no user ID.
