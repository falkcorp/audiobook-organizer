### Fixed

#### Dedup vector index: a crash that kept the row count no longer leaves the index wrong

At startup the server trusted the saved vector index whenever the number of embedding rows still matched the number saved with it. A crash could change rows without changing that number (one book embedded and another deleted, a book re-embedded, a merge that changed which version is primary), and the index then stayed wrong until something rebuilt it. A clean stop now leaves a marker beside the snapshot, only when the snapshot is exactly the index in memory, and every startup removes it. A startup that finds no marker brings the snapshot up to date against the database whatever the counts say. The short-lived worker processes that run a single operation load the snapshot as it is and leave the marker alone, so they do not repeat that check for every operation.

#### Stop timeout: comments and the deploy example

Three code comments said the systemd unit's stop timeout was 30 s. The committed unit sets none, so the deploy drop-in's value (or systemd's default) applies, and production's drop-in sets 90 s. The comments now say where the value comes from, and `deploy/local.conf.example` uses 90 s.
