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

#### Permission bits are a per-caller decision, and the first draft of this change got that wrong

The first version of the shared copy applied the **source's** mode on every
path. That was correct for two of the three operations and actively harmful for
the third, and a review of the change caught it before merge.

Bringing a file in from **outside** the library is not the same operation as
copying a file the library already owns. An ingest source is a download client's
output: a client running `umask 077` produces an `0600` file, and adopting that
mode makes every organized library file owner-only and stops the share serving
them — with every copy reporting success. That is the same failure as the E08
canary, in the change written to prevent it. The paths this replaced
(`os.Create` in the organizer, an `0664` literal in the reflink fallback) were
floored by the service's own umask and were safe for that reason.

There are now three named policies, because none of them can be inferred:

- **`CopyFile` / `CopyFileInto`** take the source's mode — copying a file to
  make a sibling of itself (a backup, or a temp that gets renamed back over it).
- **`CopyFileIngest` / `CopyFileIngestExclusive`** create at the library default
  under the umask and never adopt the source's bits.
- **`CopyFileAtomic`** keeps the **destination's** mode when the destination
  exists. Restoring a library from a backup written months ago under an earlier
  writer's hardcoded `0644` must not stamp that `0644` onto a live `0664`
  group-writable library — at exactly the moment a rollback runs.

The Linux reflink path no longer chmods to the source's mode either; that change
had been justified as making the three code paths agree, and they would have
agreed on the wrong answer. macOS `clonefile(2)` genuinely does copy the
source's mode and cannot be told otherwise, so that platform difference is now
documented rather than papered over.

#### Three more defects the same review surfaced

- **The copy fsynced the data but never the parent directory.** On ext4
  (`data=ordered`) and XFS, `fsync()` on a newly created file does not commit
  the directory entry naming it. A crash could therefore leave a backup's bytes
  on disk under no name at all — which is the very "one crash loses both copies"
  outcome this change claims to eliminate. All four copy entry points that
  create a file now fsync the parent directory, through the same test seam as
  the file fsync; removing it fails a test.
- **A copy of a file onto itself silently emptied it and returned success.**
  `O_TRUNC` on the same inode zeroes the file before `io.Copy` reads a byte, and
  `io.Copy` then reports success having moved nothing. Two of the six replaced
  implementations were immune by accident. `os.SameFile` now refuses it, and
  `FileOperation.Execute` treats a same-file operation as a no-op — its existing
  test passed throughout because it asserted only that the file still *existed*,
  and disabled checksum verification.
- **The unconditional `chmod` was a new failure mode on network shares.**
  `fchmod` returns `EPERM` on CIFS without unix extensions and on vfat/exfat, and
  three of the six replaced implementations never chmodded at all. In the iTunes
  write-back path that turned into "proceeding without a rollback anchor" on a
  perfectly healthy library. The chmod now runs only when the bits on disk are
  not already the ones wanted.
