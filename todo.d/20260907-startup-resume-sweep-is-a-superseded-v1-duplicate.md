### Finish the v1 resume teardown: the `opstate:params` side table and `GetInterruptedOperations`

The startup v1 resume sweep itself is **done** — `resumeInterruptedOperations`,
`resumeV2Op`, `resumeLegacyOp` and `countLegacyV1Ops` were deleted from
`internal/server/server_lifecycle.go` on 2026-09-07 (~280 lines), along with the
three tests that pinned them. `Registry.resumeAfterStartup`
(`internal/operations/registry/resume.go`) was already doing the job, and two
consecutive production boots showed the v1 sweep silent while the registry did
real resume work. Two pieces it exposed are still open.

**1. The `opstate:<id>:params` side table is dead in both directions — delete it.**

Verified 2026-09-07 across `internal/` excluding tests and mocks. There are **zero
matched writer→reader pairs**, and after the sweep deletion there are zero readers
at all:

| dir | site | type | counterpart |
|---|---|---|---|
| write | `organizer/service.go:234` | `operations.OrganizeParams` | no loader anywhere |
| write | `itunes/service/importer.go:397` | `operations.ITunesImportParams` | no loader anywhere |
| read | *(deleted 2026-09-07)* `server_lifecycle.go:212` | `operations.BulkWriteBackParams` | never had a writer — the pre-UOS handler that wrote it was already gone, so the branch could only ever take its own `"no saved params, cannot resume"` path |

The `metabatch/fetch_ops_index.go` legacy arm that used to read this table behind
`if op.Legacy` is **already gone from main** — `CandidateFetchBookIDs` is v2-only
now. Five maintenance jobs carry comments recording their own migration off
`GetOperationParams` (`bulk_deluge_import.go:47`, `bulk_fetch_metadata.go:49`,
`prune_book_snapshots.go:28`, `revert_metadata_fetch.go:40`,
`scan_composer_tags.go:49`). This is the tail of a finished migration: it needs
**no v2 successor**, because nothing reads what it stores.

Scope: `SaveOperationParams` / `GetOperationParams` in `database/iface_ops.go`,
`database/pebble_store_operations.go`, `database/mock_store.go`,
`database/mocks/` (regenerate), `server/server_ops_store.go:194,218`,
`organizer/service.go:84`; the four helpers in `operations/state.go`
(`SaveParams`, `LoadParams`, `LoadRawParams`, `SaveRawParams`) and their two
interface decls; the two write call sites above. Touches ~10 test files that stub
the store methods.

Also orphaned once the helpers go — the param structs in `operations/state.go`
that exist only to be serialized through them: `ITunesImportParams`, `ScanParams`,
`OrganizeParams`, `BulkWriteBackParams`, `IsbnEnrichmentParams`,
`MetadataRefreshParams`, `ComposerScanParams`, `BackfillFileHashesParams`,
`MissingFileRepairParams` (9 types, most already at zero references before this
work). ⚠️ **Keep `BulkMetadataFetchParams`** — it has 3 independent production
uses in `server/metadata_ops.go` and does not route through the helpers.

⚠️ **Do not delete `opstate:` state generally.** The `opstate:<id>` *state* pair is
a separate, live concern — `operations.SaveCheckpoint` is in active use; see the
retention sweep fix of 2026-09-07 (#3108). Only the `:params` half is dead.

**2. `GetInterruptedOperations` now has zero production callers.**

`database/pebble_store_operations.go:474` iterates the v1 bound
`[]byte("operation:") .. []byte("operation:~")`. Its only caller was the deleted
sweep. Left in place deliberately: it should go with the rest of the `operation:`
methods in the final v1 phase rather than as a one-off, since deleting it alone
still touches the store interface and both generated mock sets.
