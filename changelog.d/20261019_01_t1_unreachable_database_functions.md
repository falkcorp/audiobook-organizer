Deleted production functions in `internal/database` that no binary could
reach, along with the tests that covered only them: the unused Argon2 password
key-derivation helpers and `GetDecryptedSetting` in `settings.go`,
`SubstringSearchMatches` (the ranking predicate `SubstringSearchRank` is the
one the stores call), `GetMigrationHistory`, `InvalidateCachedMetadataFetch`,
`MetadataCacheChangedSinceOf`, the author and series `EnsureSingleton*Tag`
helpers and their two narrow store interfaces, two unused memdb indexer types
(`nullableBoolFieldIndex`, `effectiveIntFieldIndex`), and a handful of small
helpers (`MetaBool`, `MetaInt`, `SortFileEvents`, `AnyProvisionalCore`,
`OwnFolderSplit.Spans`, `StorageMigrationCheckpointPath` and its suffix
constant, and a no-op LSH index-delete placeholder). Five helpers that only
tests call (`encodeVectorV0`, `PathWindowRef`, `familyForKey`, `recordMigration`,
`undecodableMarkerCount`) and the flat `getAllAuthorBookRefCountsPebble` wrapper
moved into `_test.go` files. The three `CatalogStore` methods the brief listed
(`GetEntryByProviderID`, `GetRaw`, `CountEntries`) stay: the first has a
production caller in `internal/catalog`, the other two are still reachable
from `deadcode`. `JoinCreditNames` / `JoinCreditNamesABS` stay too: they are
the owner-approved tag write-back join ("A, B and C") that the credit-lists
plan's write-back PR will call. Running behavior does not change.
