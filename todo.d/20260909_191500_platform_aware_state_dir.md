### Pick the credential directory per-platform instead of hardcoding `/var/lib`

The encryption key, bootstrap token and startup read-only key are pinned to a
Linux-only constant, `/var/lib/audiobook-organizer`, with a single env override
(`ABK_STATE_DIR`) that exists so tests and dev boxes can write somewhere at all.
That is deliberate — it is what makes the prod sudoers rule and the
`server-bootstrap` runbook work without editing them every time the database
moves — but the constant is wrong everywhere except Linux-with-root.

Resolve it from the OS instead, keeping `ABK_STATE_DIR` as the explicit override
that beats all of it:

| OS | Directory | Notes |
|---|---|---|
| Linux | `$STATE_DIRECTORY` if systemd set it, else `$XDG_STATE_HOME/audiobook-organizer`, else `/var/lib/audiobook-organizer` | systemd exports `STATE_DIRECTORY` when the unit declares `StateDirectory=`, which is exactly this directory and already correct |
| macOS | `~/Library/Application Support/audiobook-organizer` | the documented location for app state; **not** `~/Documents`, which is user-visible and iCloud-synced |
| Windows | `%LOCALAPPDATA%\audiobook-organizer` | `%APPDATA%` roams — a machine-local secret should not follow the user to another machine |

Three things this has to get right, all of which have already bitten once:

- **Never relocate the key without moving the file.** `database.InitEncryption`
  generates a fresh key when it cannot read one, and `config/persistence.go`
  then re-encrypts the four secrets it can recover from the config file and
  **`DeleteSetting`s every other secret**. Changing the resolved directory in a
  release therefore silently destroys secrets on upgrade. Whatever this lands
  as, it needs the same old-location fallback read the current code has.
- **Windows has no 0600.** `os.WriteFile(path, key, 0600)` gives no meaningful
  protection on NTFS; the mode bits are ignored. Either set a real ACL or
  document that the Windows path is unprotected.
- **`os.UserHomeDir` fails in a service context.** A Windows service or a
  launchd daemon can run with no usable home; the fallback must be a real path,
  not `""`, or every derived path collapses to a relative one.

Groundwork already present: the resolution is one function with one call site
per consumer, so this is a change to that function plus its tests, not a sweep.
