Made the first set of readers retired-aware, the second of five stacked
changes for retired book versions, and closed four review gaps in the store
primitives. The single-identity lookups (`GetBookFileByPath`, `ByPID`,
`ByAcoustID` and `GetBookBySegmentFileHash`) now answer the live file only; a
new `GetBookFileByPIDIncludingRetired` returns a retired holder, and
`BookFilesAtPath`, `BookFilesWithHash` and the ID-only fingerprint lookups
still return retired rows by design, which the docs now list exactly. The undo
path-key claim and the revert candidate loops never hand a path key to a
retired row. `RetireBookFiles`, `UnretireBookFiles` and `RehomeRetiredVersion`
hold the version stripe and each row's stripe from read to commit, so a
concurrent fingerprint or transcript write is no longer undone, and
`UpdateBookVersion` and `DeleteBookVersion` take the version stripe so a stale
version can no longer point a retired version back at its old holder.
`CreateBookVersion` refuses a retired version without its provenance and a
re-home refuses the shell the version was retired out of. The maintenance
repairs that claim, move or repoint file rows now see retired rows (a retired
row is refused by name, never moved or repointed), and the iTunes PID audits
count retired rows apart from live duplicates so a retired row that keeps its
PID is neither a duplicate nor an orphan. Nothing retires a row yet, so no
retired row exists for these readers to see on production; the changes that do
run there are the stripe locks the retire primitives and the version updates
now take, and the repairs and iTunes audits reading through the new
retired-inclusive readers.
