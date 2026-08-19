### Changed

#### `MigrationFunc` took all 398 store methods; migrations use six

`type MigrationFunc func(store Store) error` handed every migration the whole store.
Measured by swapping the parameter for an empty interface and reading the compiler's
enumeration: **60 of the 61 migration functions touch the store not at all.** The
Pebble schema is created by store initialisation, so most migrations log a line and
return nil.

The one that does use it, `migration007Up`, type-asserts straight past the interface
to `*PebbleStore` for an unexported method — so the width was never what let it work,
and the assertion still compiles against a narrow interface.

`migrationStore` is now six methods: the runner's own version bookkeeping
(`GetUserPreference`, `SetUserPreference`, `GetAllUserPreferences`) plus the three
`migration014UpPebble` uses to rewrite book rows.
