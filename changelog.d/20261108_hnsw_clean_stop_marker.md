### Fixed

#### Dedup vector index: a crash that kept the row count no longer leaves the index wrong

At startup the server trusted the saved vector index whenever the number of embedding rows still matched the number saved with it. A crash could change rows without changing that number (one book embedded and another deleted, a book re-embedded, a merge that changed which version is primary), and the index then stayed wrong until something rebuilt it. A clean stop now leaves a marker beside the snapshot, only when the snapshot is exactly the index in memory, and every startup removes it. A startup that finds no marker brings the snapshot up to date against the database whatever the counts say.
