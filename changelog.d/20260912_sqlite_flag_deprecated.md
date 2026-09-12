### Fixed

#### The removed `--enable-sqlite3-i-know-the-risks` flag no longer breaks startup

Deleting the inert flag outright (#3268) turned it into a startup failure: cobra
rejects an unknown flag, so any systemd unit, script or alias outside the repo
that still passed it stopped the server from starting. The flag is registered
again as hidden and deprecated, bound to nothing. Passing it prints
`Flag --enable-sqlite3-i-know-the-risks has been deprecated, it was removed and
does nothing: SQLite is no longer selectable and PebbleDB is the only database
backend; remove it from your command line` to stderr (the journal under the
systemd unit) and startup continues. It does not appear in `--help`.
