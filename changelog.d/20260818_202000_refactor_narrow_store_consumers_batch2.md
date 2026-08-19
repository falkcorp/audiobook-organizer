### Changed

#### Eight more store parameters narrowed in `internal/server`

| site | was | now |
| --- | --- | --- |
| `server.maintenanceStore` | 131 | 12 |
| `handlers/operations.OperationsStore` | 115 | 17 + `database.SettingsStore` |
| `duplicates_helpers.seriesPruneStore` | 111 | 6 |
| `duplicates_helpers.seriesMergeStore` (2 sites) | 60 | 2 |
| `handlers/ai.aiHandlerStore` | 51 | 3 |
| `middleware.authSessionStore` (2 sites) | 25 | 7 |
| `middleware.authKeyStore` | 20 | 4 |

`effectivePermissionsFor` took `database.RoleStore` — six methods — for a single
`GetRoleByID` lookup. That one parameter is what forced `RoleStore` into both auth
composites; narrowing it released them.

`OperationsStore` had embedded `database.BookStore` with the note "structural
satisfaction requires the full". That was accurate when written and stopped being
true when the sweep/audit parameters were narrowed. Its constraint is now
`sweep.fileAuditor` (one method) and `sweep.tombstoneSweeper` (three).

`database.SettingsStore` stays embedded rather than method-listed: it is four
methods and already the right size. Using the domain pieces is the goal — the
problem was never that the pieces existed, it was that consumers took the union.

Both `maintenanceStore` and `OperationsStore` are kept as compositions of focused
interfaces rather than flat method lists. `interfacebloat` counts declared entries,
so a flat list of the methods actually used trades a smaller method set for a wider
declaration — narrowing one axis while regressing the other.
