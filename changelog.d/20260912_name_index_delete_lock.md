### Fixed

- **Deleting or renaming an author, alias, narrator or series can no longer
  erase another row's name-index entry.** The ownership-checked delete read
  the entry's owner and then committed the delete later with no lock held, so
  a concurrent rename that took the key over in between still lost its entry:
  that row stopped resolving by name and the next import created a duplicate
  of it. Every writer of a name-index family (author, alias, narrator, series)
  now holds one per-family lock from its row read through its commit.
  `CreateSeries` and `CreateAuthorAlias`, which checked for an existing name
  and then created with no lock at all, are serialized by the same locks, so
  concurrent creates of one name no longer mint several rows.
