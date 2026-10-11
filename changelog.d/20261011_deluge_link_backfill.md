### Added

#### `deluge.link-backfill` — link library book files to the torrents they came from

New manual operation (permission `integrations.manage`, write set `book_files`) for library files organized before Deluge imports recorded their torrent. For each torrent under the discovery label (or `label`), it hashes every audio file with `filehash.BookFileHash` (head and tail above 100 MB, whole file below; no audio decoding) and looks for the library row in this order: `DelugeOriginalPath` equal to the torrent file's path, rows with that content hash, the file provenance chain (skipped and reported as `provenance_unavailable` when the store does not expose it), then same basename and size under the library root. A candidate is linked only when the hash equals its `FileHash`, its sampled `OriginalFileHash`, or the digest of its earliest provenance event with the same size. A path match alone never writes. Only rows inside the library root are linked (a row still naming the seeding copy is counted `outside_library`). A row two torrents resolve to (cross-seeded) is skipped for both and counted `ambiguous`. A row already holding another torrent's hash is counted `conflict` and left alone. The op writes exactly one field, `BookFile.DelugeHash`, through `ModifyBookFile` with an under-lock re-check, and never removes, moves or repoints anything. It previews unless `dry_run=false`; params `limit` and `workers` (default CPU count, max 8, run through `registry.RunItems` with `Concurrency` set). Its result reports `torrents`, `files`, `linked` / `would_link`, `already_linked`, `unverified`, `ambiguous`, `conflict`, `non_audio`, `errors`, `provenance_unavailable`, `unmatched` and `outside_library`.

A linked row has `ImportedFromDelugeAt` unset, so it becomes a bulk-import candidate (`GetBookFilesNeedingDelugeImportCore`) for `/discovery/import`, `deluge.centralize` and `maintenance.bulk-deluge-import`. Because every linked row is already inside the library root, those resolve its destination to its own path and copy nothing; a test pins this.

### Removed

#### Deluge plugin: dead `importToLibrary`

`internal/plugins/deluge/import.go` had no callers (it carried a `lint:ignore U1000`) and was kept only until this change (owner decision D55). The live importer, `internal/deluge/importer_adapter.go`, is unchanged. `MarkFileImportedFromDeluge` left the plugin's store interface with it.
