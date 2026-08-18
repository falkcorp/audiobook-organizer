### Changed

#### Five more `internal/database` interfaces split into focused pieces

- **`BookWriter`** 16 → 6: mutations, sync markers, version history, tombstones,
  scan-failure counters, aggregate recomputation.
- **`AuthorReader`** 13 → 4: lookup, aliases, author-book relations, counts.
- **`ExternalIDStore`** 11 → 3: reads, writes, tombstone/reassignment lifecycle.
- **`ActivityStorer`** 10 → 4: writer, reader, retention, lifecycle.
- **`MetadataStore`** 10 → 3: field states, change history, alternative titles.

Each original name is retained as the composition of its pieces, so every method
set is byte-identical and no consumer moves — verified per interface by diffing
method names (16→16, 13→13, 11→11, 10→10, 10→10, all identical) and by the type
checker. `mockery` regenerates to no diff.

With this, every composition in `internal/database` is at or under the width
threshold except `Store` itself. Violations in the package drop from 13 to 8, and
the eight that remain are all 9-method interfaces one method over the line.
