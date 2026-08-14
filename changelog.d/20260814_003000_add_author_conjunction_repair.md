### Added

#### Maintenance op to repair author rows with a stranded ampersand

`maintenance.author-conjunction-repair` cleans up the 48 author rows the
Oxford-comma splitting bug created — `& Conrad Westmaas`, `& Lisa Bowerman`,
`& India Fisher` and the rest. The forward fix stops new ones appearing but does
nothing to existing rows, because nothing re-normalizes an author name after
creation.

The existing `maintenance.author-split-scan` cannot reach these rows:
`SplitCompositeAuthorName("& Conrad Westmaas")` returns nil (three words, no
delimiter), so the split scan skips them entirely.

Two repair paths, chosen per row:

- **Merge** when a correctly-named author already exists — 31 of the 48. Book
  links move to the existing author and the stranded row is deleted.
- **Rename in place** when no such author exists — the remaining 17. The row
  keeps its id, so no book row is touched at all.

The link that gets rewritten is the book↔author **join slice**, not
`Book.AuthorID`. Every stranded row sits at position 1+ of a credit list, which
is why all 48 report `file_count: 0` while carrying books between them; a merge
that only rewrote the denormalized primary would report success and move
nothing. The tests assert on `SetBookAuthors` for exactly this reason.

Defaults to `dry_run=true`. Merging deletes author rows, so the per-row plan is
meant to be read before anything is written.

The op matches `&` only, deliberately narrower than the forward fix's pattern.
Three rows begin with `and ` — `and Thanks for All the Fish`, `and the Farm Boy
(DBY)`, `and Make Better Decisions` — and those are book *titles* that reached
an artist tag, not stranded conjunctions. Renaming them would produce
`Thanks for All the Fish`: still not an author, but no longer obviously broken.
They are left visibly wrong on purpose, and filed for the fix that addresses the
real defect (the comma branch cannot tell a person from a title clause).

The loop is deliberately sequential rather than pooled: the merge path is a
read-modify-write of a book's author slice, and two workers repairing two
different rows that appear on the same book would lose one another's update.
Partitioning by author does not make the work disjoint, because the unit
actually mutated is the book.
