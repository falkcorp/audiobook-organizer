### Fixed

#### The `create_backups` setting now keeps a backup before each tag write

The setting was read nowhere, so turning it on did nothing. With it on (it
defaults to on), a tag or cover write that goes through
`fileops.WriteTagsSafe` now keeps the pre-write file beside it as
`<file>.bak-<unix seconds>`. A second write in the same second gets a `-1`,
`-2` suffix instead of overwriting the first backup.

The backup is a hardlink to the original inode, made after the tag write
succeeded and before the tagged copy is renamed over the path. It costs no
extra disk until the next write replaces the path. Where a hardlink is not
possible (`EXDEV`, `EPERM`, `ENOTSUP`) it falls back to a full exclusive copy.
If the rename fails, the backup is removed and the original is left as it
was.

Bulk writes keep no backup (owner decision D69). Their safety net is the
provenance ledger or the operation journal, and a sibling per file would
double the library on disk until the sweep runs. These paths opt out through
the context (`tagger.WithoutBackup`):

- `library.bulk-write-back`, `maintenance.bulk-write-back` and the write-back maintenance plugin
- `metadata.batch-save`
- the `scan-composer-tags` fix
- the startup movement-atom cleanup
- batch metadata apply, apply-when-scanned and the file-I/O pool's replays
- the iTunes import's metadata enrichment
- `RevertOperation`'s tag writes. A revert writes one tag row at a time, so it
  used to make one full backup per row. The revert journal is its safety net.

Single-book edits keep a backup when the setting is on: a book edit's
write-back, a per-book write-back, fetch or apply, and rename tag writes.

The two backup sweeps (`maintenance.cleanup-old-backups` and the scheduler's
`cleanup-old-backups`) remove `.bak-*` siblings older than
`backup_retention_days`. They decide age by the backup's mtime, so the writer
stamps a hardlinked backup's mtime to the time of the write. A hardlink
otherwise keeps the original's mtime and would be swept on the next run. The
sweeps walk `RootDir` only: a backup beside a file outside the library root
is never removed automatically.

### Removed

#### `write_backup_before` retired

`metadata_scoring.write_backup_before` (flat key `write_backup_before_tag_write`)
made its own full `.bak-YYYYMMDD-HHMMSS` copy before a metadata write-back. That
duplicated `create_backups`, so it is gone from config, the defaults, the
Settings page and the config API docs. A stored value is ignored with a warning
on load, and a `PUT` that sets it is rejected. Old `.bak-YYYYMMDD-HHMMSS` files are still
removed by the backup cleanup sweep.
