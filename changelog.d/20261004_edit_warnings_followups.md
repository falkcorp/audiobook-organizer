### Fixed

#### Single-book edit: the last log-only partial saves are reported too

`PUT /audiobooks/:id` already returned `warnings` when the field locks, the
change history or the author credits of a landed edit were not saved (#3711).
Two more failures were still only logged: the history row of a case-only
series rename, and the narrator list (`book_narrators`) — an unreadable
author list, a narrator that could not be created, or a failed junction
write. Each now adds a warning to the response, with the underlying error
(the history writer now returns its error instead of only logging it).

#### Library: an organize rollback reports what it did

The organize rollback restores each book with `PUT /audiobooks/:id` and showed
"Rollback complete." even when a book's response carried warnings, and on a
failure it showed only "Rollback failed.", dropped the warnings of the books
already restored and skipped the cache clear and reload. It now stops at the
failed book and says "Rolled back k of N; failed at <title>: <error>" with the
warnings so far, lists warned books (the first three, then "and N more") on
success, and clears the cache and reloads either way.

#### Swapped title/author fixer: the continuation fallback reads each journal once per plan

When the undecodable-row gate fails `GetBookChanges` for every book, the
continuation check falls back to the repair operation's own journal. One plan
now reads each operation's journal once (grouped by book) instead of once per
repair-locked candidate; a failed read is remembered too, so every candidate
of that operation fails closed alike. A re-plan before apply still reads
fresh.
