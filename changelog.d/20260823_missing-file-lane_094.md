### Changed

#### Book detail's version-group chip now shows how many versions there are

The chip read "Version Group Linked" — present but silent about size, unlike
the iTunes chip rendered directly beside it, which has always carried its PID
count. It now reads `Version Group (N)`, counting the same `versions` array
that `BookDetailVersionGroup` renders below it rather than a separately
fetched number, so the header and the tray cannot disagree.

The count mirrors that component's own fallback (`versions.length > 0 ?
versions : [book]`): before the array loads, the tray still lists one row, so
the header reads `(1)` rather than `(0)`. A count contradicting the list
underneath it would be worse than no count at all.

The "Primary Version" marker this was grouped with was **not** missing — it has
been present since `c3638b221` and is unchanged here. It now has test coverage,
along with the count: the chip's absence for an ungrouped book, its presence
and value for a grouped one, and the primary/alternate distinction.
