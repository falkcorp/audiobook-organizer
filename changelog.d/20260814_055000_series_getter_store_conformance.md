### Fixed

- **A series listing served during startup showed duplicate versions in arbitrary
  order.** `GetBooksBySeriesIDCore` has two backing implementations and they
  disagreed on two axes at once: the memdb walk excluded non-primary versions and
  sorted by series sequence, while the Pebble scan kept every alternate rip and did
  not sort at all. During the roughly two minutes it takes memdb to warm up after a
  restart, a series therefore came back with its duplicates included and its books
  in storage order rather than reading order. Both paths now apply the same filters
  and share one ordering helper.

  Found by sweeping the other 25 dual-implementation store methods for the defect
  shape fixed in the author getters. Of those, this was the only remaining method
  where one store filtered non-primary versions and the other did not;
  `GetFolderDuplicatesCore` and `GetDuplicateBooksByMetadataCore` were checked and
  already agree.

  The shared ordering helper also drops a comparator that indexed a precomputed
  slice of lowercased titles from inside the sort. Sorting permutes only the slice
  handed to it, so that parallel slice kept its original order and the tiebreaker
  could read a title belonging to a different book. It was not observed producing a
  wrong order, and it is corrected rather than carried forward.
