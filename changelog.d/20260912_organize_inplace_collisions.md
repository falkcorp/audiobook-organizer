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
recorded as durable skips: same audio with no fingerprint, chapter fragments from
one folder collapsing onto one path, and directory conflicts. A skipped pair is
retried only when either file's size or mtime changes. Books with an empty or
placeholder title are no longer organized. Outcome counts appear in the
`library.scan` result as `organize_outcomes`.
