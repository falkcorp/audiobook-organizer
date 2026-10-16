Operations now have one catalog, `internal/opscatalog` (05-PR7). It holds `All`,
the list of native ops v3 bundles (empty until the port waves add packages),
the five service-registry plugins that `internal/plugins/plugins.go` used to
list (that file is deleted and `internal/server` imports the catalog instead),
and `Tombstones`, the retired op IDs that moved out of
`internal/server/op_id_aliases_test.go`. `NewServer` registers the maintenance
plugin, the adapted v2 registrars and the catalog's bundles through one
function, in the same order as before.

The server now checks the op-ID ledger when it starts, not only in tests. After
the container has started (so the plugins have registered their ops), a second
gate, `opCatalogGate`, validates the whole catalog and resolves every line of
the embedded `internal/server/testdata/op_ids.golden`. An ID resolves when it is
a live def or alias, a def or former ID of a plugin's dependency-free
`OperationDefs()` list, a native bundle def, or a tombstone, so a server with no
Deluge client still starts. An ID that resolves to none of them stops startup
with the ID named. The check also refuses a scheduled writer that does not say
`.Live()` (owner decision D8).

`oplint` is rewritten as a type-aware `go/analysis` analyzer and is now part of
`make ci`; `make oplint` runs it on `./...` (before, it could not parse its own
argument). It reports a definition not reachable from its package's bundle, a
bundle missing from `opscatalog.All`, a store write that bypasses the Writer
inside `Run`/`Item`/`Apply`/`AfterApply`, `database.Store` in a `Deps` struct or
captured by a definition, `Sequential("")`, any `ops.Deletes` use for owner
review, and, as a warning, a definition with no `Help`. It reports nothing on
the current tree. `TestCatalogLinkedIntoBinary` replaces the old plugin wiring
test and also covers bundle packages.
