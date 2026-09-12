### Fixed

#### Organize resolves an occupied destination for books already in the library instead of failing every scan

Re-organizing a book that already lives under the library root used to stop with
"destination already exists — refusing to overwrite", record nothing, and retry
the same pair on every scan. Now the occupant is classified with the same helper
the folder landing uses. A byte-identical occupant, or the same recording with
rewritten tags (the Chromaprint fingerprints match and durations agree within
max(2%, 60s)), is adopted: the book becomes a non-primary version in the
occupant's version group, and nothing moves or is deleted. A different file gets
organize's `_copyN` name. Pairs that cannot be decided are left untouched and
recorded as durable skips: same audio with no fingerprint, chapter fragments
collapsing onto one path, and directory conflicts. A skipped pair is retried when
either file's size or mtime changes, or when the database facts the decision read
change (a fingerprint backfill, a new owner row, a version-group change).

A book laid out one chapter per folder (`<Book>/<Book> - N/file`) is never moved
by organize: its folder name is the only place the chapter number lives, and
moving it would have renamed chapters 2..N to `_copyN` and deleted their folders.
Those chapters are declined as `fragment_collapse` until the layout is merged.
Books with an empty or placeholder title are no longer organized. Organize inside
a library scan now records its change rows under the scan's operation ID, and the
outcome counts appear in the `library.scan` result as `organize_outcomes`.
