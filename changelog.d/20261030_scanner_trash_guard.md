### Fixed

- A rescan no longer relabels a book that is in the trash. A book trashed by its
  `deleted` library-state label alone (the batch `library_state: "deleted"` path
  makes these) was marked `suspicious` when its file was under the minimum book
  size, which took it out of the trash with no restore and no permission check.
  Every scan-derived library-state write now goes through one rule
  (`scannerMayChangeLibraryState`), the scan still refreshes the trashed row's
  file columns, and the scan summary reports how many trashed books it left alone.
