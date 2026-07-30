<!-- file: changelog.d/abs-sync-identity-follow-hooks.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e668999-8957-45fc-b83e-9c65a77a8431 -->
<!-- last-edited: 2026-07-30 -->

### Fixed

#### A device's listening position now survives every merge and untagged move (ABS-SYNC-ID-3 / ID-12)

The durable sync identity added in the `sync_item` keyspace only helps if every
code path that retires a Book ULID carries it forward. Four paths did not, and
each one silently orphaned a client's saved place in a book:

- **`merge.Service.MergeBooks`** (the UI/dedup merge) — the winner keeps its ULID
  and losers are soft-deleted, so this is a loser-only redirect: the winner's
  syncID is minted if absent, and each loser's syncID becomes a redirect record
  pointing at it. A client still holding a loser's `libraryItemId` now resolves
  through to the winner (chains included, e.g. B→A→C).
- **`merge.Service.CombineBooks`** — absorbed shells are **hard-deleted**, so the
  follow runs *before* the delete; there would be no row left to repoint after.
- **`dedup.MergeBooks`** (still live via `internal/reconcile/itunes_heal.go`) —
  also hard-deletes losers. This was the unrecoverable case: without the hook, a
  routine heal run destroyed the only row that could have carried the identity.
- **Untagged move** (`internal/scanner`) — a moved file with no
  `AUDIOBOOK_ORGANIZER_ID` tag cannot be re-linked to its own row, so the scanner
  mints a brand-new ULID and version-links it to the predecessor. The identity now
  follows to the new ULID when the predecessor's file is gone from disk (a real
  move); a second copy whose original still exists leaves the identity alone.

Each user's listening progress moves with the identity, favouring whichever side
is further along (`UserBookState.ProgressPct`, ties broken by the more recent
`LastActivityAt`), and the retired book id is drained so no progress is left
resolvable — or listable by status — under a book that no longer exists. The
losing side is only drained *after* the carry-forward succeeds, so a store failure
mid-way can never destroy the position it was meant to preserve.

All four hooks are best-effort with respect to the merge itself (a sync-identity
hiccup never fails a merge that would otherwise succeed) but never silent: every
failure logs at ERROR with both book IDs. The merge hooks run inside the existing
process-wide `mergeSerializeMu` critical section, which makes them exactly-once
against concurrent merges by construction — no additional partitioning was added.
A 16-goroutine `-race` test against a real PebbleStore asserts a single redirect,
a single `MergedFrom` entry, and the correct surviving progress value after
concurrent identical merges.

**Known gap (follow-up):** the `sync_file` keyspace is keyed `(bookID, fileID)` and
its `RepointSyncFile` primitive cannot move an entry to a different book, so
per-file `ino` ids are not yet carried across `CombineBooks` (files move to the
survivor) or an untagged move. That only affects an offline client's cached
per-file download URLs, not progress or bookmarks.
