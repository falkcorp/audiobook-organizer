### Changed

- The memdb book listing no longer copies every matching book to serve a sort that
  has no index. For created_at, updated_at, year, duration, bitrate, file_size,
  narrator and the other sort keys that read a field directly, it now ranks the
  shared read-only rows through a bounded top-(offset+limit) heap and projects only
  the page. The ABS Recently Added shelf and the Library tab's `sort=addedAt&desc=1`
  on a 60k-row library drop from about 78.5 MB and 57 ms per call to about 20 KB and
  3 ms, with no `enabled_sort_indexes` change. Page contents and ordering, ties
  included, are identical to before. The author and series sorts still copy, because
  they write the resolved name onto the row.
