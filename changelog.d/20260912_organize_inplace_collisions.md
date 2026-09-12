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
The chapter-folder check treats letters and digits in any script as part of the
title, so a Cyrillic or CJK book (`Сияние/Сияние - 1/58.MP3`) is recognised the
same way as a Latin one. A folder name with no letters or digits at all
(`-- - 1`) never counts as a chapter folder.

This changes the scanner's shattered-book merge in two deliberate ways compared
with the previous release. Non-Latin chapter folders under a folder named after
the book (`Сияние/Сияние - 1`, `Сияние/Сияние - 2`) still merge into one book.
Non-Latin series volumes under an author folder (`Автор/Сияние - 1`,
`Автор/Сияние - 2`) are no longer merged: the old check reduced every non-Latin
name to an empty string, and an empty string matched any parent folder, so it
wrongly merged separate volumes into one book.

Books with an empty or placeholder title are no longer organized. Organize inside
a tracked operation (`library.scan`, `library.import`, `library.folder-auto-scan`)
now records its change rows under that operation's ID, read from the run context
the operations registry sets up. The outcome counts appear in the `library.scan`
result as `organize_outcomes`.
