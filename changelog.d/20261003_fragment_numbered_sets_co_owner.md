### Added

#### Fragment consolidation — numbered chapter sets

A serial's chapters each carry their own title (`070 - Skating`, `047 - Core`),
so the chapter key kept them apart and no group ever formed: 4,966 such books
were left on prod on 2026-10-03. A folder's leading-numbered fragments now form
one no-parent row when there are at least three with at least two chapter keys
among them, and the set takes the folder's whole numbered run (three
`Interlude` chapters spread through a serial stay with it).

Numbers alone do not make a serial, so a set must also pass these tests, each
written against a shape that would have merged different works: the folder is
not a library root or import path; no file sits in a disc folder; no two files
claim one position; all files have the same author and series; the numbers run
from 0 or 1 without large gaps (years and title numbers are not chapters); no
chapter key with three or more files sits together as one block (`01-03 - Book
A`, then `04 - Book B`); the folder is not named like the files' author; and
the folder gives the work a title. A set that fails is dropped in favour of the
key groups, as before the rule existed; when no key group forms from its files
either, it is listed held (`skipped_numbered_set_unsure`) with the reason, so
the cluster is visible. The duration gate, the track-order check and the
survivor rule are unchanged, and the row is review-risk with the first twelve
file names in its evidence: it is planned for the owner to approve.

### Changed

#### Fragment consolidation — a file another live book also owns is held at plan

A row whose fragment file is also a row of a live book outside the row was
planned applicable and then refused at apply ("also owned by book X"), with
the reason visible only in the apply result; five rows (about 400 fragment
books) did so on every apply of 2026-10-03. Such a row is now held
(`skipped_co_owner`) with the co-owner named as a member, for the owner to
decide which book keeps the file. The row keeps its class.
