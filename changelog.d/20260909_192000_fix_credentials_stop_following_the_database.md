### Fixed

- **Credentials no longer move when the database moves.** The settings encryption
  key, the emergency `.bootstrap-token` and the startup `.readonly-key` now live in
  a fixed directory — `/var/lib/audiobook-organizer`, overridable with
  `ABK_STATE_DIR` for tests and containers — instead of wherever
  `filepath.Dir(database_path)` happened to point.

  Three separate call sites each re-derived that directory. When production moved
  its database to `<root_dir>/.appdata` on a different pool, the bootstrap token
  went with it: into a mode-0700 directory that no `sudoers` rule named, so the
  `sudo cat` step in the bootstrap runbook could not read it. Worse, the token file
  from *before* the move stayed readable at the old path and kept answering, so
  following the runbook returned a well-formed, ten-minutes-expired token and a
  `401 invalid bootstrap token` with nothing pointing at the cause. Two of those
  derivations were the write side and the consume side of the same token, so
  updating either one alone would have written the token to one directory and
  deleted it from another.

- **`InitEncryption` no longer overwrites a key it failed to read.** Every
  `os.ReadFile` error fell through to "generate a new key", and generating ends in
  `os.WriteFile`, which truncates. For a key that was writable but unreadable (mode
  `0200`, or a transient I/O error) the real key was silently replaced. Any read
  error that is not "file does not exist" is now fatal. A wrong-length key is also
  rejected before it reaches the package global rather than after.

- **Startup now refuses to generate an encryption key over the top of existing
  secrets.** When no key exists in either location but the database already holds
  encrypted settings, the server stops with a message naming the file to restore.
  Previously it generated a fresh key and `LoadConfigFromDatabase` then re-encrypted
  the four secrets recoverable from the config file and **deleted every other
  one** — exiting 0 and looking healthy, with the loss only noticed later as a
  credential that needed re-entering. `ABK_ALLOW_ENCRYPTION_KEY_REGEN=1` overrides
  it for an operator who has accepted the loss.

  An install whose key is still beside its database keeps working: that location is
  still read as a fallback, with a warning naming where to move the file. The key is
  deliberately **not** copied — one secret in two places is a second thing to leak.
