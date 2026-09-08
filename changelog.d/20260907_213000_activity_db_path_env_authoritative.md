### Fixed

- **The activity-database location setting was destroyed on every boot.** `activity_db_path`
  persists correctly into the DB config blob, but `applyEnvAuthoritativeConfig` — which
  `LoadConfigFromDatabase` runs immediately after restoring that blob — overwrote it with the
  empty default whether or not the operator had actually set `ACTIVITY_DB_PATH`. The setting
  was a dead lever everywhere, not just on hosts that pin the variable in their unit file.

  The guard was `viper.IsSet`, on the strength of a doc comment asserting that IsSet is "true
  only when a real override layer supplied the key, NOT for SetDefault". That is not how viper
  behaves: `IsSet` is `Get(key) != nil` and a registered default *is* a value, so `IsSet` stays
  true against a completely empty environment. `InitConfig` registers
  `SetDefault("activity_db_path", "")`, so the assignment was unconditional. The path key now
  uses an explicit `envSupplied()` check against the bound variable, and the comment has been
  replaced with the measured behaviour.

  `applyEnvAuthoritativeConfig` serves two categories that need opposite tests, which is what
  made this easy to get wrong, so both are now documented at the function:

  - **Auth surfaces** (OAuth / Cloudflare Access / ABS) must let the blob win *never*, even
    with the environment silent — the blob is untrusted input and must not be able to enable
    an auth surface or supply its credentials. For these, `viper.IsSet` is correct precisely
    *because* the registered default makes it unconditional. Unchanged.
  - **Operational keys the UI may also own** (`activity_db_path`) must let the environment win
    only when it genuinely supplies the key. Changed.

  `activity_backend` deliberately stays in the first category despite sitting on the adjacent
  line: it is the OOM rollback lever from the 2026-09-07 SQLite incident, and an operator who
  pulls the variable to stop an OOM loop must not have a restored blob re-engage SQLite
  underneath them. A test now pins that asymmetry so it does not get tidied away.
