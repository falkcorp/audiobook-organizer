### Make the credential directory a real config option, not a hardcoded constant

`config.DefaultSecureStateDir` is the Linux-only literal `/var/lib/audiobook-organizer`
with a single escape hatch (`ABK_STATE_DIR`) that exists so tests and containers can
write anywhere at all. That was deliberate and it is not the end state — it was taken
as a stopgap to get past a broken bootstrap runbook, and it deliberately bypasses the
config system every other setting in this codebase goes through.

Do it properly: `state_dir` as an ordinary setting, resolved the same way the rest are.

**1. Wire it through cobra + viper like every other path setting.**

- A `--state-dir` persistent flag on `rootCmd`, registered in
  `persistentFlagConfigKeys` so it is bound by the same loop as `--db`/`--dir` and
  captured by the `Changed()` pass that feeds `config.MarkFlagExplicit`.
- `StateDir string` on `config.Config` with the `state_dir` mapstructure key, plus a
  `viper.SetDefault`.
- Env-authoritative treatment in `applyEnvAuthoritativeConfig`, exactly as
  `database_path` got: `if flagSupplied("state_dir") || envSupplied("STATE_DIR")`.
  Without this the persisted config blob overwrites the operator's value on every
  start-up, which is the bug #3168 was about — and this setting is *more* dangerous to
  get wrong than `database_path`, because the blob lives in the database whose secrets
  the setting governs.
- An entry in `envLockedSettings`/`flagLockedSettings` so `SettingLocks()` reports it
  and the UI can grey it out with a reason.
- Keep `ABK_STATE_DIR` working as an alias, or migrate it and say so in the changelog.

**2. Resolve the default per-platform** instead of one Linux literal, with an explicit
value beating all of it:

| OS | Directory | Notes |
|---|---|---|
| Linux | `$STATE_DIRECTORY` if systemd set it, else `$XDG_STATE_HOME/audiobook-organizer`, else `/var/lib/audiobook-organizer` | systemd exports `STATE_DIRECTORY` when the unit declares `StateDirectory=`, and it is already exactly this directory |
| macOS | `~/Library/Application Support/audiobook-organizer` | the documented location; **not** `~/Documents`, which is user-visible and iCloud-synced |
| Windows | `%LOCALAPPDATA%\audiobook-organizer` | `%APPDATA%` roams — a machine-local secret must not follow the user to another machine |

**3. Decide whether it is UI-settable, and write down the answer.** It is currently
kept out of Settings > Paths on purpose: a text box that relocates `.encryption_key`
is a text box that deletes secrets, because the UI cannot move the file. If it is
surfaced, it needs to be read-only-with-explanation, or paired with a real "move the
key for me" action that is transactional.

**Four things this must not break**, all of which have already bitten:

- **Never relocate the key without moving the file.** `database.InitEncryption`
  generates a fresh key when it cannot read one, and `config/persistence.go` then
  re-encrypts the four secrets recoverable from the config file and `DeleteSetting`s
  every other secret. So changing the resolved directory in a release silently destroys
  secrets on upgrade. Keep the legacy-directory fallback read, and keep
  `guardAgainstKeyRegeneration` in front of it.
- **The write side and the consume side must not re-derive independently.** They did,
  as two copies of `filepath.Dir(database_path)`, and that is a 401 with the token file
  sitting right there. `internal/server/bootstrap_state_dir_test.go` pins it; keep that
  test meaningful.
- **Windows has no `0600`.** `os.WriteFile(path, key, 0600)` is not enforced on NTFS.
  Either set a real ACL or document the Windows path as unprotected.
- **`os.UserHomeDir` fails in a service context.** A Windows service or a launchd daemon
  can run with no usable home; the fallback must be a real path, not `""`, or every
  derived path collapses to a relative one next to the working directory.

Groundwork already in place: one resolution function (`config.SecureStateDir`), one
creation helper (`config.EnsureSecureStateDir`), and one call site per consumer — so
this is a change to those two functions plus their tests, not a sweep.
