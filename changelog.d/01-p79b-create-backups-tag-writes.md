### Fixed

#### The `create_backups` setting now keeps a backup before each single-book tag write

The setting was read nowhere, so turning it on did nothing. With it on (it
defaults to on), a tag or cover write that goes through
`fileops.WriteTagsSafe` now keeps the pre-write file beside it as
`<file>.bak-<unix seconds>`. A second write in the same second gets a `-1`,
`-2` suffix instead of overwriting the first backup.

The backup is a hardlink to the original inode, made after the tag write
succeeded and before the tagged copy is renamed over the path, so taking it
copies no data. The rename then leaves the backup as the only library name of
the old bytes, so each kept backup costs one file's size on disk until the
sweep removes it. (A library file hardlinked from a download costs nothing
more: the download keeps that inode anyway.) Taking the backup never changes
the original inode, so a seeding file the library file shares an inode with
keeps its mtime. Where the hardlink is refused for any reason, or the library
entry is a symlink, the backup is a full exclusive copy. If the rename fails,
the backup is removed and the original is left as it was. When a write falls
back to the AtomicParsley/eyeD3/ffmpeg writers, which rewrite the file in
place, the backup is taken first as a copy. If that fallback fails without
touching the file (for example the tool is not installed), the backup is
removed; if the file changed at all, the backup is kept.

Bulk writes keep no backup (owner decision D69). Their safety net is the
provenance ledger or the operation journal, and a sibling per file would
double the library on disk until the sweep runs. These paths opt out through
the context (`tagger.WithoutBackup`):

- every bulk tag write-back: `library.bulk-write-back`, `maintenance.bulk-write-back`,
  the write-back maintenance plugin and `dedup.series-normalize` (the opt-out
  sits inside the shared write-back loop, so no caller can miss it)
- organize with `fetch_metadata_first`: the cover embed and tag write each
  fetched book queues
- `metadata.batch-save`
- the `scan-composer-tags` fix
- the startup movement-atom cleanup
- batch metadata apply, apply-when-scanned and the file-I/O pool's replays
- the iTunes import's metadata enrichment
- `RevertOperation`'s tag writes. A revert writes one tag row at a time, so it
  used to make one full backup per row. The revert journal is its safety net.

Single-book edits keep a backup when the setting is on: a book edit's
write-back and a per-book write-back, fetch or apply.

The two backup sweeps (`maintenance.cleanup-old-backups` and the scheduler's
`cleanup-old-backups`) remove `.bak-*` siblings older than
`backup_retention_days`. They now decide age by the time in the backup's name
(`<unix>`, `<unix>-N`, or the legacy `YYYYMMDD-HHMMSS` in local time) and use
the file's mtime only for a name without one. A hardlinked backup keeps the
original audio's mtime, which can be years old, so aging by mtime would delete
a fresh backup on the next run. The sweeps walk `RootDir` only: a backup
beside a file outside the library root (for example in an import path) is
never removed automatically. The manual `cleanup-backups` maintenance job
matches only `.backup`, `.bak` and legacy `.bak-YYYYMMDD-HHMMSS` names, so it
does not remove the new `.bak-<unix>` backups.

### Removed

#### `write_backup_before` retired

`metadata_scoring.write_backup_before` (flat key `write_backup_before_tag_write`)
made its own full `.bak-YYYYMMDD-HHMMSS` copy before a metadata write-back. That
duplicated `create_backups`, so it is gone from config, the defaults, the
Settings page and the config API docs. A value in a config file, the
environment or a legacy per-key settings row is ignored with a warning on load.
A value in the stored settings blob is dropped without a warning, because the
app wrote it there itself, and the next settings save rewrites the blob without
it. A `PUT` that sets it, flat or nested under `metadata_scoring`, is rejected
with the removal message. Old `.bak-YYYYMMDD-HHMMSS` files are still removed by
both backup sweeps, aged by the time in their name.
