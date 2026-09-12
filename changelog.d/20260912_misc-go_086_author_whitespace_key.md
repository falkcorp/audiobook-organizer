### Fixed

#### Author names that differ only by internal whitespace now resolve to one author

`util.NormalizeAuthor` now collapses runs of internal whitespace (tabs, double
spaces, NBSP and other Unicode spaces) as well as trimming and lowercasing, so
"Raymond  L.  Weil" and "Raymond L. Weil" no longer mint separate author rows.
The same key backs the author, author-alias, narrator and series name indexes.
Index entries written before this change stay reachable: lookups try the new key
and then the old one, and index deletes now remove an entry only while it still
points at the row being deleted, so deleting or renaming one of two colliding
rows can no longer strip the other's entry. Role and playlist name lookups now
use their writers' own normalizer and are unaffected. Existing duplicate rows are
not merged and legacy index entries are not re-keyed; the new REPORT-ONLY
`maintenance.author-whitespace-collision-report` op (manual trigger, no
schedule, writes nothing) lists the colliding author groups with IDs, quoted
names, book and reference counts, and the ID a name lookup resolves to.
