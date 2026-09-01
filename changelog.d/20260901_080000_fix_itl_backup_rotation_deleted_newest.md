### Fixed

#### iTunes backup rotation deleted the newest backup and kept two-month-old ones

Three code paths wrote backups of the same iTunes library into the same
directory under the same `.bak-` prefix, each with its own timestamp format:

| writer | stamp |
|---|---|
| `internal/itunes/itl_safe_write.go` (the hardened safe-write path) | `2026-09-01T07-14-49.000000000Z-00` |
| `internal/itunes/service/writeback_batcher.go` | `20260801-000000` (and in **local** time) |
| `internal/itunes/service/transfer.go` | `20260701T000000Z` |

Two independent rotators — `rotateBackups` and `pruneITLBackups` — each sorted
the whole set with `sort.Strings` and deleted from the front, on comments
asserting that lexical order equals chronological order. That was true of each
rotator's *own* format and false across the three: the separators differ at the
fifth character, and `-` (0x2D) sorts before every digit, so every
dashed-RFC3339 name sorted as older than every compact name regardless of when
it was written.

The result, with one backup from each writer and a retention of one, is that
the **September** backup is deleted and the **July** one kept. The backups
being destroyed first were the ones from the safe-write path — the only writer
that fsyncs and pins a last-known-good anchor.

Ordering is now by *parsed* timestamp, never by string. One shared
implementation in `internal/itunes/backupname.go` writes a single canonical
name and understands every historical layout, so backups already on disk sort
correctly with no migration step. A name that matches no known layout is kept
rather than rotated away — deleting a file whose age cannot be established is
how a rotation bug turns into data loss.

Two further problems surfaced while fixing this:

- **The canonical layout was emitting a stray `-00`.** `Z07-00` is not a zone
  specifier Go recognises as a unit: it matches `Z07` and treats `-00` as
  literal text, so every backup the safe-write path has ever produced is named
  `...Z-00`. It round-tripped against itself, so nothing noticed for as long as
  the only consumer sorted the names as strings. New names use `Z0700`; the old
  spelling is retained as a parse-only layout.
- **The backups list in the UI was sorted by file modification time.** The
  safe-write path creates its backup by *renaming* the live library, so that
  file's mtime is the library's, not the moment the backup was taken — the
  hardened backups were displayed as far older than they are. The list now uses
  the timestamp in the name, falling back to mtime only for a name that matches
  no known layout.

The `cleanup-backups` maintenance job's `.bak-\d{8}-\d{6}$` pattern was
deliberately left alone: it sweeps the library root, not the iTunes tree, and
widening it to match the canonical layout would let it delete ITL backups if
those trees ever overlap.
