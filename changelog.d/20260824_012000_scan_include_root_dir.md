### Added

- **A library scan can now cover the whole library without re-hashing all of
  it.** The new `include_root_dir` scan parameter folds the organized library
  root into a scan while keeping the incremental skip, so unchanged files are
  still passed over. Previously the only way to reach the library root was
  `force_update`, which also switched the skip off — so "scan everything" and
  "re-read every byte of everything" could not be asked for separately. On a
  library of ~154,000 files that is the difference between minutes and hours.
  The default is unchanged: a plain scan still skips the library root.
