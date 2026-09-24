### Fixed

- `itunes.sync_enabled=false` now actually disables the iTunes sync. `Importer.Sync` refuses with `ErrSyncDisabled` while the flag is off, and organize's pre-sync skips. Before this, neither the `itunes.sync` op nor organize's `sync_itunes_first` read the flag. So a sync could still point every organized or library-cloned row whose `ITunesPath` differs from the XML Location back at the iTunes file.
