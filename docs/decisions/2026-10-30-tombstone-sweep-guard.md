<!-- file: docs/decisions/2026-10-30-tombstone-sweep-guard.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2f8d1c64-a057-4e93-b6c1-83d7e0a94b52 -->
<!-- last-edited: 2026-10-30 -->

# Tombstone sweep: guarded file removal

Task: TODO.md `SWEEP-TOMBSTONES-UNGUARDED-REMOVE`. `sweep.SweepTombstones` unlinked
`tomb.FilePath` with a bare `os.Remove`. Written under the 2026-10-30 autonomy
instruction: where a choice was open, the recommended option was taken and the
alternatives are recorded here for review.

## Decision log

### 1. Reuse the purge's decision by injection, not by moving code

Chosen: `sweep` declares a small `FileGuard` interface (builtin and `database`
types only). `audiobooks.TombstoneSweepGuard` implements it by calling
`planPurgePathIn` (with `purgePathStillReferenced`) and `otherBookRowAt`, the
functions the book purge decides with. The operations handler receives it through
`WithTombstoneSweepGuard`, wired in `wire_handlers.go`. This is the pattern
`VersionPurgeGuard` already uses for `internal/versions`. `audiobooks` does not
import `sweep`, so `TestLayering` gets no new edge.

Rejected:
- Moving `planPurgePathIn` into `pathutil` or a new package. It depends on the
  store, config, `folderCheck`, `dirSet` and the purge's error classes
  (`errPathLookupFailed`, `errPathUnresolvable`, `errPathFolderMissing`); moving
  it would drag all of them and every audiobooks test that names them.
- `sweep` importing `audiobooks`. Legal by layer (both layer 3) and acyclic, but
  it makes the whole audiobooks domain package a dependency of a small sweeper, and the
  guard needs a service built over a wider store than the handler holds.

### 2. The stamp is written only when the purge committed to removing the file

`writeTombstone` stamps only for a plan that is journaled, made for this path, and
found a file to remove. A purge without `delete_files` also writes a tombstone; if
it died before cleanup, the file in it is one the owner chose to keep. Root,
protected and referenced checks would all pass for an ordinary library file, and a
stamp written unconditionally would match it. With no stamp the sweeper keeps the
file. The same rule covers every tombstone written before this change, as the
brief asked.

### 3. Where the stamp lives

Chosen: the same Pebble key and value as the tombstone (`tombstone:<id>`), as a
wrapper that embeds `Book` and adds one `removal_stamp` key
(`CreateStampedBookTombstone`, `GetBookTombstoneStamp`). It is atomic with the
tombstone, `DeleteBookTombstone` removes both, and old rows and old readers
(`GetBookTombstone`, `ListBookTombstones`) are unaffected. Both methods are
capabilities (`StampedTombstoneWriter`, `TombstoneStampReader`) resolved with
`AsCapability`, so `Store` does not widen.

Rejected:
- A `Book` field. It must be mirrored in `BookCore`, `Core()`/`ToBook`, the
  generated TypeScript types and the contract test data, to carry a value only a
  tombstone ever has, on every live row.
- A separate `tombstone_stamp:<id>` key. It needs a key-family registration,
  a second write that is not atomic with the tombstone, and cleanup on every
  failure path.

The stamp type mirrors `audiobooks.fileIdentity` field for field. `audiobooks`
keeps the comparison rule (`fileIdentity.matches`, with its reasons for leaving
the device number out) and converts; `database` only carries the data.
Rejected: moving `fileIdentity` into `pathutil` and renaming its ~40 uses, which
buys a second shared type for no behavioural change.

### 4. Library root is asked once, lazily

`FileGuard.RootReady()` (the version purge's `libraryRootReady`) is called before
the first removal that is needed, once per sweep. An unmounted library makes every
file read as gone, and the bare `os.Stat` the sweeper used before would have
cleaned every tombstone on that evidence. When it fails, every tombstone that
names a file is kept and counted as a refusal.

### 5. A "gone" file still goes through the guard

`planPurgePathIn` runs before the stat, so a tombstone whose path is outside the
root, protected, or still named by a row is kept even when the file is already
gone. That inherits the purge's behaviour on purpose; the cost is that such a
tombstone is reported on every sweep until the cause is fixed.

### 6. Directories are not unlinked by the sweeper

A stamp records whether the purge decided on a file or an empty directory, and the
guard allows either when the stamp matches, as the purge does. The sweeper no
longer removes a directory the purge did not decide on.

### 7. No worker pool

The loop is capped at 1000 tombstones, which are leftovers of purges that did not
finish and are normally none. Two tombstones can name the same path (duplicate
book rows over one file), so a pool would have to partition by path to avoid
racing on one unlink. Sequential is the simpler correct choice.

### 8. Fail closed everywhere

A nil guard, an unset `WithTombstoneSweepGuard`, a nil `audiobookService`, a store
that cannot keep or read a stamp, a stamp read error, and any lookup error all end
in "keep the file and the tombstone". There is no unguarded path left.

### 9. A failed row delete withdraws the stamp

`purgeDeleteRow` writes the (possibly stamped) tombstone, then deletes the row. If
`DeleteBook` fails, the book is still live but a stamped tombstone said "this file
is to go". Found in the automated security review. Chosen: rewrite the tombstone
plain with `CreateBookTombstone` (`withdrawTombstoneStamp`, only when a stamp was
written). The code before stamps left a plain tombstone on exactly this path
("the sweeper will clean up the tombstone"), so this restores that state and keeps
the record the sweep's live-book branch cleans up. The sweep guard already refused
the path because the live row names it; this makes the two records agree. If the
rewrite fails too, the error line says so and the refusal still holds.

Rejected: deleting the tombstone. It would drop the record the existing live-book
branch of the sweep is documented to clean up, and differ from the pre-stamp state.

## Notes for the reviewer

- `errPathLookupFailed` and the other purge sentinels were not renamed or moved.
- `BookFilesAtPath` filters no per-row flag: it returns every row at the path,
  retired rows included. The "deleted" flag lives on the book row, which
  `BookIDsAtPath` returns for soft-deleted books; between them both are covered.
  Both fail closed when memdb is not serving.
- Left alone (adjacent): the branch of `SweepTombstones` that finds a live book
  for a tombstone discards the error from `DeleteBookTombstone` and counts the
  tombstone as cleaned.
