<!-- file: changelog.d/abs-sync-syncfile-keyspace.md -->
<!-- version: 1.0.0 -->
<!-- guid: 63f63dc2-4e48-4588-9c47-6ea212aa0ae9 -->
<!-- last-edited: 2026-07-30 -->

### Added

#### `sync_file` keyspace for durable per-file ABS `ino` (ABS-SYNC-ID-2)

Added `internal/database/pebble_store_syncfile.go`: a `sync_file:` Pebble keyspace
giving every `BookFile` a durable identity for the ABS-compatible file-addressing
scheme, `contentUrl: /api/items/{itemId}/file/{ino}`. The captured ABS conformance
fixtures show `ino` is an opaque string (ABS's filesystem inode) that offline clients
cache in downloaded-file URLs. This app's core job is moving and reorganizing files,
so deriving `ino` from a path or inode would break every already-downloaded book after
a reorganization.

`BookFile.ID` is already a stable ULID that survives in-place moves/retags
(`UpdateBookFile` never regenerates it), but a **replacement** (old row deleted, new
row created with a new `BookFile.ID` -- e.g. a remux or quality upgrade) has no
existing seam to keep a cached client URL resolving. `MintOrGetSyncFileID(bookID,
fileID)` mints a `syncFileID` (via the existing `newULID()` helper -- `ino` has no
length constraint, unlike TASK-01's `libraryItemId`) keyed to the `(bookID, fileID)`
pair (mirroring `BookFile`'s own `book_file:<bookID>:<fileID>` primary key), with a
`sync_file:lookup:<bookID>:<fileID>` reverse index for O(1) resolution and an
enumerable `sync_file:book:<bookID>:<syncFileID>` index so a caller can list every
`sync_file` entry for a book without knowing IDs up front. `RepointSyncFile(bookID,
oldFileID, newFileID)` moves the lookup index for a future file-replacement path (no
caller yet -- this is additive-only groundwork, same as TASK-01's `sync_item`
keyspace). A package-level `syncFileMintMu` mutex (separate from TASK-01's
`syncIDMintMu`) guards the check-then-mint race.

New capability interface `SyncFileStore` / `AsSyncFileStore(s any)` follows the
existing type-assertion pattern for adding a capability without touching the `Store`
interface in `store.go`. Nothing reads or writes `sync_file:*` keys yet outside this
task's own tests.
