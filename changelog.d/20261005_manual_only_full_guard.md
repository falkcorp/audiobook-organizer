### Fixed

#### Every op that picks which books it may touch now runs the whole owner-manual check

Doctor Who / Big Finish / Torchwood books are applied by hand (owner rule).
Several whole-library ops decided that from the book row alone (path, title,
narrator, publisher, transcribed fields), or from the path alone, so a book
whose only signal was a book_file path or its transcribed title, an author
credit, a series row or a `franchise:` tag could be touched in bulk. Owner
decision 2026-10-05: they all use the whole-book check now
(`applygate.BookManualOnly`: the row plus every read the bulk-apply guard
makes). The ops are `itunes.regroup` (snapshot and apply-time recheck),
`itunes.clone-into-library`, `maintenance.author-path-link`,
`repoint-version-primary`, `merge-chapter-groups` (preview and apply; the
detection path pre-filter stays as a cheap first pass) and
`author-strip-merge`'s title-as-author relink. A failed read leaves the book
alone and is reported as a failure or `owner_manual_check_failed`, never as an
owner-manual book. The regroup snapshot serves files, series and tags from
bulk reads and runs the per-book credit reads on a NumCPU worker pool.
`applygate.BookRowManualOnly` is now unexported, so no new caller can decide
from the row alone.
