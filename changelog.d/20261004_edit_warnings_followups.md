### Fixed

#### Single-book edit: the last log-only partial saves are reported too

`PUT /audiobooks/:id` already returned `warnings` when the field locks, the
change history or the author credits of a landed edit were not saved (#3711).
Two more failures were still only logged: the history row of a case-only
series rename, and the narrator list (`book_narrators`) — an unreadable
author list, a narrator that could not be created, or a failed junction
write. Each now adds a warning to the response.

#### Library: an organize rollback reports books that were not saved completely

The organize rollback restores each book with `PUT /audiobooks/:id` and showed
"Rollback complete." even when a book's response carried warnings. It now
lists those books and their warnings in a warning toast.

#### Swapped title/author fixer: the continuation fallback reads each journal once per plan

When the undecodable-row gate fails `GetBookChanges` for every book, the
continuation check falls back to the repair operation's own journal. One plan
now reads each operation's journal once (grouped by book) instead of once per
repair-locked candidate; a failed read is remembered too, so every candidate
of that operation fails closed alike. A re-plan before apply still reads
fresh.
