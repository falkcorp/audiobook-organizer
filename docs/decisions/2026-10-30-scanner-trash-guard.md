<!-- file: docs/decisions/2026-10-30-scanner-trash-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f5bc0ca-5ca6-4a5b-8023-27a4d9dc1821 -->
<!-- last-edited: 2026-10-30 -->

# Scanner never relabels a book in the trash

Closes TODO-SCANNER-SUSPICIOUS-UNTRASHES.

## Trace: does a label-only trashed row reach applyScannerFields?

Yes. Nothing between "file found on disk" and "existing row updated" filters
trashed rows. Anchors are at the worktree base (origin/main 9383cb0fb) plus this
change.

1. `classifySkipFile` (`internal/scanner/scanner.go:671`) skips a file only when
   its scan-cache entry is unchanged. A trashed row's file is skipped like any
   other when unchanged, so the exposure is a file the cache does not vouch for:
   a changed or truncated file, a `NeedsRescan` entry, a force-update sweep
   (cache == nil), or `MinBookSizeBytes` raised above an existing file.
2. The suspicious-file branch (`scanner.go:1631`) sets the in-memory
   `Book.LibraryState = "suspicious"` for a single file under the floor, then
   calls `saveBook` -> `saveBookToDatabase` (`scanner.go:3418`).
3. `lookupScanExisting` (`scan_existing.go`, called at `scanner.go:3490`) finds the
   stored row with `store.GetBookByFilePath` (`pebble_store.go:1843`), which has
   no trash filter, and the hash lookups behave the same. `existing := found.byPath`
   at `scanner.go:3694`.
4. The rescan merge (`scanner.go:~4060`) runs `modifyBookRecorded` ->
   `mergeScannedKeepingForeignEdits` (`scan_book_lock.go:248`) ->
   `applyScannerFields` (`scanner.go:~4680`).
5. In `applyScannerFields` the state overlay accepted any scan-derived value
   unless it was the bare "imported" default landing on a row that already had a
   state. "suspicious" is not the default, so it overwrote "deleted".

Confirmed empirically as well, not only by reading: with the guard stubbed to
`return true`, the new rescan tests fail (the label-only trashed row ends as
"suspicious"), so the row does reach the overlay.

Other LibraryState writers in `internal/scanner` (grep of `LibraryState` over
non-test files): `scanner.go:1631` only sets the in-memory scanned book;
`scanner.go:~3598-3622` builds the dbBook for a NEW row (creation default); the
organizer-ID relink only sets `FilePath`. The overlay in `applyScannerFields` was
the only write onto an existing row, so it is the only site needing the guard.

Effect of the bug: a label-only trashed row became "suspicious", i.e. not
`IsInTrash` any more, and reappeared in the library with no restore, no
`library.organize` permission and no audit entry. A flag-trashed row stayed
hidden by its flag but lost the state label a restore reads back.

## Decision log

### 1. Guard with database.IsInTrash, centralised in one helper (taken)

`scannerMayChangeLibraryState(dst)` in `internal/scanner/library_state_guard.go`
is the single test; `applyScannerLibraryState` is the overlay that uses it, and
`applyScannerFields` calls that. A future scan-derived state write calls the same
helper. Rejected: inlining `!database.IsInTrash(dst)` at the one write site (works
today, leaves the next write site to rediscover the rule).

### 2. A trashed row gets NO scan-derived state, including the first-state default

The guard covers the "row has no state yet gets the default" case too. A
flag-trashed row with a nil state therefore stays nil rather than becoming
"imported". Rejected: allowing the default onto a state-less trashed row; the
state is the one column a scan does not own for a row someone removed on
purpose, and a consistent rule is easier to hold than an exception.

### 3. Everything else on a trashed row is still refreshed

Only LibraryState is held. Path, hash, size, duration and tags keep updating, so
a trashed row's file facts stay correct for a restore. Rejected: skipping trashed
rows in the scan entirely (would leave stale hashes/paths on rows a restore later
trusts, and would change what the scan cache stamps). A test pins this.

### 4. One Info line per scan with a count

`scanRunCounters.stateHeldForTrash` (per-run, on the ctx, like the other
end-of-run counters) is bumped once per book where the guard actually withheld a
different state (`libraryStateHeldForTrash`: scan had a state to impose, it
differs from the row's, row is trashed). It is read in the existing scan summary
block as "scan summary: N trashed book(s) kept their library state...". It is
assigned inside the ModifyBook callback and bumped after the write, because
ModifyBook may re-run the callback. Rejected: logging per book (a full-library
rescan of a trash-heavy library would flood the log), and a package-level
counter (two concurrent scans would inflate each other, the reason
`scanRunCounters` exists).

### 5. Not changed

`applyScannerFields` keeps its signature (many tests call it). The suspicious-file
branch at `scanner.go:1631` is unchanged: the in-memory scanned state is the
scan's observation; whether it may land is decided at the single write.
