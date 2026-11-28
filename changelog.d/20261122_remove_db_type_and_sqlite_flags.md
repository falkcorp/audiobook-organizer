### Removed

- The `--db-type` and `--enable-sqlite3-i-know-the-risks` command-line flags are gone. Both did nothing (PebbleDB is the only backend); the second was kept hidden and deprecated so an old unit file or script that still passed it kept starting. A command line that carries either now fails with `unknown flag`, so remove them from any systemd unit, launchd plist, script or alias before upgrading. The `database_type` setting and its removal message are unchanged.
