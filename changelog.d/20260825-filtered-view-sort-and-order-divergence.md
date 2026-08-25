### Fixed

- **Sorting inside a series, author, or narrator view did nothing.** Those
  drill-downs never read the `sort` query parameter at all, so every option in
  the Sort By menu — including Title — returned the group's own order. They now
  sort, and they sort the whole filtered set before cutting out the requested
  page rather than reordering the rows already on screen.

- **The same sort could return two different orders depending on config.**
  Unknown values were ranked at opposite ends by the two code paths that order
  a library page: the memdb sorted indexes put a missing value *last*
  ascending, while the materialise-and-sort fallback ran it through
  `derefInt`/`derefStr` first, turning a nil year into the year 0 and a nil
  narrator into `""` — both of which sort *first*. Which order a request got
  depended on whether its sort could be pushed down, i.e. on
  `enabled_sort_indexes`, not on anything the caller asked for. `title` diverged
  in three further ways: the index normalises the title, falls back to the
  original filename, and pushes empties to the end; the fallback did none of
  those.

  There is now one ordering rule (`database.SortBooks`) that both paths call.

### Changed

- **Books with no value for the sorted field now sort last in ascending order
  on every field**, matching what the indexed path always did. This is a
  visible reordering for the ~19 fields that have no index and therefore always
  took the fallback — genre, publisher, language, format, codec, quality,
  edition, library state, sample rate, and the duration/bitrate/file-size
  aliases. Descending reverses it, because the store serves descending as a
  full reversal of the key order.

- `sample_rate` is now sortable. The API accepted it as a valid sort field and
  then silently ignored it, because only the `sample_rate_hz` spelling had a
  comparator.
