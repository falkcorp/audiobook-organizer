<!-- file: changelog.d/abs-sync-syncitem-keyspace.md -->
<!-- version: 1.0.0 -->
<!-- guid: 791ba524-e776-4bee-861d-7df49c26b297 -->
<!-- last-edited: 2026-07-30 -->

### Added

#### `sync_item` keyspace — durable identity for the upcoming ABS sync API (ABS-SYNC-ID-1)

Added a new Pebble keyspace, `sync_item:<syncID>` plus reverse index
`sync_item:book:<bookID>`, that will back the `libraryItemId` every
Audiobookshelf-compatible client (Absorb, AudioBooth, etc.) stores progress and
bookmarks against.

- **Why a separate identity from the Book ULID:** this app's core loop is moving,
  retagging, and merging books. If the client-visible id were the raw ULID, every
  dedup merge and every untagged move (which mints a new ULID via version-linking)
  would silently orphan a device's saved place.
- **Why a 36-char UUID, not our 26-char ULID:** Absorb (source-audited) splits
  compound podcast keys by fixed byte offset — `substring(0, 36)` / `substring(37)` —
  at multiple call sites. A 26-char ULID breaks episode splitting; anything longer
  than 36 chars is mis-truncated into the wrong `/api/me/progress/...` path. The
  minted id is a canonical, hyphenated, lowercase UUIDv4, minted by hand via
  `crypto/rand` (16 lines of stdlib) rather than adding `google/uuid` as a direct
  dependency.
- New store methods on `*PebbleStore` (`internal/database/pebble_store_syncid.go`):
  `MintOrGetSyncID`, `GetSyncIDForBook`, `ResolveSyncItem` (follows merge-redirect
  chains, capped at 10 hops with cycle detection), `RepointSyncItem` (untagged-move
  support), and `RecordSyncMerge` (idempotent merge-loser → merge-winner redirect,
  used by a later merge-hook task). Exposed via a `SyncIdentityStore` capability
  interface + `AsSyncIdentityStore` type-assertion helper, following this repo's
  established "don't touch `store.go`" pattern.
- This PR is additive-only: nothing reads or writes these keys yet. No existing
  behavior changes. The scanner's merge/move paths, and the ABS HTTP handlers
  themselves, are wired up by follow-up tasks.
