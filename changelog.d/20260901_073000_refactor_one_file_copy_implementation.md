### Fixed

#### Six hand-rolled file copies disagreed on four axes; two of the disagreements were losing iTunes backups

Six functions in this repository copied a file byte-for-byte — three of them
inside `internal/fileops` itself. They disagreed on whether an existing
destination was refused or truncated, what permission bits the destination
ended up with, whether the data was fsynced before the call returned, and
whether a half-written destination was removed when the copy failed.

Two of those disagreements were live defects rather than preferences:

- **Every ITL backup written by the transfer path was owner-only (0600).**
  `internal/itunes/service/transfer.go` copied through `os.CreateTemp`, which
  creates at 0600, and renamed the result into place, so the backup inherited
  the temp file's mode instead of the library's. This is the same failure the
  2026-08-14 E08 canary caught on the tag-write path, where 100 books' files
  went share-unreadable after a rewrite replaced an 0664 file with an 0600 one;
  it had simply never been looked for on the iTunes side.
- **Neither ITL backup writer fsynced.** A backup still sitting in page cache
  when the original is rewritten is not a backup — one crash loses both copies,
  which is the single outcome those backups exist to prevent.

A third defect surfaced while consolidating them: the write-back batcher's
post-rename-validation rollback restored the backup over the **live** iTunes
library with a plain `os.WriteFile`. That is not atomic at any point, so a
crash partway through the rollback left neither a good library nor a good
original.

All six now go through one implementation in `internal/fileops`, split into
four functions named for the question each answers rather than one helper with
flags:

- `CopyFile` — create or truncate, source's mode, fsynced, partial destination
  removed on failure.
- `CopyFileExclusive` — the same, but an existing destination is refused via
  `O_EXCL` rather than a stat-then-create race.
- `CopyFileInto` — copy into a destination the caller already created, keeping
  its inode and hardlinks, and applying the source's mode (this is the E08 fix).
- `CopyFileAtomic` — temp file beside the destination, fsync, rename; the shape
  every "restore the live file from its backup" path needs.

Whether a copy lands via a temp-and-rename stays the caller's policy, so
`internal/organizer` keeps its `safeRename` collision refusal and the iTunes
paths keep plain replacement — only the byte copy is shared.

The Linux reflink path now also applies the source's mode, so the destination's
permissions no longer silently reveal which of the three code paths (Linux
`FICLONE`, macOS `clonefile`, byte-copy fallback) produced it — macOS
`clonefile(2)` had been copying the mode on its own all along.
