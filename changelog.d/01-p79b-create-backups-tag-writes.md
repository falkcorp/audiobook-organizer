### Fixed

#### The `create_backups` setting now keeps a backup before each tag write

The setting was read nowhere, so turning it on did nothing. With it on
(it defaults to on), every tag or cover write that goes through
`fileops.WriteTagsSafe` now copies the file's pre-write bytes to a sibling
named `<file>.bak-<unix seconds>` before the tagged copy replaces it. The
backup is taken only after the tag write succeeded, it is a full copy (the
original never disappears), and a second write in the same second gets a
`-1`, `-2` suffix instead of overwriting the first backup.

Bulk tag writes keep no sibling (owner decision D69): `library.bulk-write-back`,
`maintenance.bulk-write-back`, `metadata.batch-save`, the `scan-composer-tags`
fix and the startup movement-atom cleanup opt out through the context
(`tagger.WithoutBackup`), because their safety net is the provenance ledger.
Single-book edits (book edit write-back, a per-book write-back or apply, tag
revert, rename tag writes) keep one when the setting is on. The bulk
metadata-apply paths (batch apply, apply-when-scanned, bulk auto-fetch) still
keep one per file for now; whether they opt out too is an open follow-up.

Disk use: each kept backup is a full copy of the audio file. The
`maintenance.cleanup-old-backups` op removes `.bak-*` siblings older than
`backup_retention_days`.
