### Added

- server, scheduler: the 45 remaining T7 operation definitions (library scan, import, organize and transcode, the iTunes and Open Library imports, the metadata fetch, apply and save family, the author and series merge and dedup operations, the AI author scans, reconcile scan and apply, and the scheduler's metadata upgrade) now have native `books.<id>` task definitions built dark (F5-13), plus `books.library.scan-full` beside `books.library.scan`. Each runs the same core as its existing shell and keeps its title, help, priority, timeout and permission. Writes the core makes go through the run's gated writer; every kind that writes refuses `dry_run` and `dryRun` params, and a kind with no preview of its own refuses a preview run. Nothing registers them yet, so behaviour is unchanged.

### Changed

- server: the bodies of those definitions moved into shared cores that take their store and writer through narrow interfaces; the existing shells call them. `sdk.NewProgress` takes a two-method `ProgressSink` instead of the full reporter interface.
