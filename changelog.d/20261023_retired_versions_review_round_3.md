Fixed the review findings on the retired-version readers before anything can
retire a row. A retired version's file path is now one rule everywhere: it is a
claim on that path. The guards in `fs-regroup-xml`, the junk-title fixer, the
consolidation leftovers walk, `fix-version-groups` and the merge planner's
virtual-file attach used lookups that cannot see a retired row, so their
"held by a retired version" checks never fired; they now read through one shared
helper and refuse the path (the merge planner attaches nothing to a path a
retired version holds, and plans nothing when the complete path lookup is
unavailable). The iTunes cross-type audit no longer counts a retired-only PID as
a live-primary hazard. `rewrite-path-prefix` treats each holder's retired rows
as their own all-or-nothing unit, so a retired row can no longer block its
holder's live rows, and its scanned and matched counters are live-only. The
retired-inclusive readers fail closed when the complete reader is unavailable.
`BatchUpsertBookFiles` now takes the file stripe of each row it rewrites, so a
scan's upsert can no longer be undone by a retire committing between the read
and the write, and retire, un-retire and re-home re-check the index keys they
decided on under the owner stripes at commit (this narrows that window; it does
not close it against another writer of the same key). The repoint-book-file-rows
apply path re-checks for a retired row inside the write, a failed segment-hash
lookup during the scan's multi-file vote is now counted and logged, and the
tests that guard these paths now fail without their fixes. Nothing retires a row
yet, so the retired-row rules cannot fire on production; the changes that do run
there are that `BatchUpsertBookFiles` now takes a file stripe per row it
rewrites, the repoint, recover and path-prefix repairs read every row through
the retired-inclusive reader, and `rewrite-path-prefix` counts only live rows.
