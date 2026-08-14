### Added

#### `skip_author_ids` on the stranded-ampersand repair

`maintenance.author-conjunction-repair` now accepts `skip_author_ids`, excluding
specific author rows from a run. Excluded rows stay in the matched total and are
reported as `skip_explicitly_excluded`, so a partial run cannot be mistaken for a
complete one.

This exists because two dry runs of the same op against the same prod data
returned different numbers. The first ran four seconds after a service restart,
fell through to the Pebble junction scan, and reported `books_relinked=86`. The
second ran against a warm memdb and reported `84`. Row counts were identical in
both (`authors_matched=46`, 31 merge, 15 rename); the whole difference was author
46627, `& Nicholas Courtney`, where Pebble holds two book links memdb does not.
memdb had been freshly loaded, so its loader drops those links rather than
lagging a write.

That matters because the merge path relinks what it can see and then **deletes**
the author row. Run through memdb it would relink zero books for 46627 and delete
the author anyway, leaving two Pebble junction rows pointing at an author id that
no longer exists — the orphaning hazard H8 documents on
`maintenance.author-split-scan`.

Excluding by id, rather than by a heuristic like "skip rows that report zero
books", keeps the exclusion visible in the op's params and in its summary. The
underlying divergence is filed separately; once it is understood the flag comes
off and that row gets repaired with the rest.
