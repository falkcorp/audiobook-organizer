### Added

#### Fragment consolidation — numbered chapter sets

A serial's chapters each carry their own title (`070 - Skating`, `047 - Core`),
so the chapter key kept them apart and no group ever formed: 4,966 such books
were left on prod on 2026-10-03. A folder's leading-numbered fragments now form
one no-parent row when there are at least three with at least two chapter keys
among them, and the set takes the folder's whole numbered run (three
`Interlude` chapters spread through a serial stay with it).

Numbers alone do not make a serial, so a set must also pass these tests, each
written against a shape that merged different works in review: the folder is
not a library root or import path, does not sit directly under the library
root, and is not named for the files' author (`J. Author`, `Author, Jane`,
`Jane Author Collection`); no file sits in a disc folder; no two files claim
one position; all files have the same author and series; the numbers run from
0 or 1 without large gaps (years and title numbers are not chapters); the
folder does not hold works side by side (one name's files as a block of half
the folder, or every name on two or more files); no file carries its own ASIN
or a title that is not its file name; there are at least eight files; and the
folder gives the work a title.

A folder found to hold works side by side is left to the key groups, as before
the rule existed, and each key row says how many other numbered files the
folder holds. Every other set stays ONE row whatever its state, held with the
reason (`skipped_numbered_set_unsure`, or the existing duration, missing-file
and track-order skips): three same-named chapters of a numbered run are no
longer handed to a key group to become a partial book under the folder's name,
which the key-group rule did before wherever a folder's numbered files carried
two or more names. The duration gate still applies, so a serial with chapters
of ten minutes or more is listed held, not applied. Rows are review-risk and
show the first twelve file names in order.

### Changed

#### Fragment consolidation — a file another live book also owns is held at plan

A row whose fragment file is also a row of a live book outside the row was
planned applicable and then refused at apply ("also owned by book X"), with
the reason visible only in the apply result; five rows (about 400 fragment
books) did so on every apply of 2026-10-03. Such a row is now held
(`skipped_co_owner`) with the co-owner named as a member, for the owner to
decide which book keeps the file. The row keeps its class.
