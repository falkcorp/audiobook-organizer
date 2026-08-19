### Changed

#### `metafetch.Service` narrowed from `database.Store` to 64 measured methods

`metafetch.Service` held its store as `database.Store` — the 398-method union — in
both its struct field and its constructor. It now declares the **64 methods** a
compiler probe measured: 36 direct calls plus five forwarding constraints, in seven
declared entries.

Two things had to be narrowed first, because both took `database.Store` themselves
and so re-imposed the union on anything that forwarded a store into them:

- **`database.EnsureSingletonBookTag`** and its two siblings for authors and
  series. Each took the full union to call **three** methods on one entity. They
  now take `BookTagSingletonStore` / `AuthorTagSingletonStore` /
  `SeriesTagSingletonStore` — exported, so callers can embed them by name rather
  than restate the three methods.
- **The three checkpoint helpers** in `internal/metafetch/pipeline_checkpoint.go`.
  These forward to `organizer`, where the same three functions *already* declared
  `database.UserPreferenceStore` — and `internal/server` has an identical copy that
  was *also* already narrowed. The metafetch copy was an unnarrowed duplicate of an
  already-narrowed twin.

No function body changed and no call site changed.
