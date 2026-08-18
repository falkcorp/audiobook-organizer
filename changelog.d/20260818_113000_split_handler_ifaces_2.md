### Changed

#### Four more handler interfaces split

- **`MetadataStore`** (14 methods + an embedded `database.BookStore`) → 7:
  entity resolution, change recording, rejections, copy-on-write snapshots,
  filtered book queries, the legacy operation row, and rating writes.
- **`MetadataFetchService`** (13) → 7: fetch, candidate cache, apply, cover
  download, write-back, no-match marking, history.
- **`VersionsStore`** (11) → 6, including `VersionRawKVDeleter` — a one-method
  declaration for `DeleteRaw`, the most dangerous thing in the set, which is
  precisely why it is now visible on its own rather than buried at position
  eleven of eleven.
- **`AuthStore`** (11) → 4: user reads, user writes, sessions, roles.

Each original name is retained as the composition of its pieces, so every method
set is byte-identical and no consumer moves. `MetadataStore` lands at exactly 8
declared entries (7 groups plus the carried embed), the width limit.
