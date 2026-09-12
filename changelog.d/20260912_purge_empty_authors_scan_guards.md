### Fixed

- **`maintenance.purge-empty-authors` no longer races a running library scan
  on apply.** It counted author references once for the whole library and then
  deleted the eligible authors one by one, with no scan interlock. A scan that
  linked a book to one of those authors in between (for example, an import
  resolving an existing author by name) left that book pointing at a deleted
  author, and the author's name was lost with the row. Apply now:
  - re-checks each author with a point lookup immediately before deleting it,
    and holds it (new report bucket "held (linked during run)") if a book now
    links to it; a lookup that fails holds the author too;
  - takes the scan stand-down once for the apply and renews it per author,
    aborting the remaining deletes if the lease is lost (a dry run takes no
    gate);
  - writes an `author_delete` undo-ledger row (author id and name) before each
    delete, and skips the delete if that row cannot be written.
