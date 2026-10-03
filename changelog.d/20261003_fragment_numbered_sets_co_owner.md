### Added

#### Fragment consolidation — numbered chapter sets

A serial's chapters each carry their own title (`070 - Skating`, `047 - Core`),
so the chapter key kept them apart and no group ever formed: 4,966 such books
were left on prod on 2026-10-03. A folder's leading-numbered fragments now form
one no-parent row when there are at least three, every number is different and
at least two chapter keys occur among them. The set takes the whole folder's
numbered files, including any that would have grouped among themselves (three
`Interlude` chapters belong to the serial). Two files at one position mean the
folder holds more than one work, and the key groups decide as before. The
duration gate, the track-order check and the survivor rule are unchanged, and
the row stays review-risk: it is planned for the owner to approve.

### Changed

#### Fragment consolidation — a file another live book also owns is held at plan

A row whose fragment file is also a row of a live book outside the row was
planned applicable and then refused at apply ("also owned by book X"), with
the reason visible only in the apply result; five rows (about 400 fragment
books) did so on every apply of 2026-10-03. Such a row is now listed held
(`skipped_co_owner`) with the co-owner named as a member, for the owner to
decide which book keeps the file.
