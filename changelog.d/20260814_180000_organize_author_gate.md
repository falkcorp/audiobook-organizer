### Fixed

- Organize no longer bakes "Unknown Author" into library paths: the bulk
  organize loop defers books whose author is unresolved (counted + logged),
  single-book in-place renames return a typed "resolve metadata first" error,
  and the Review Organize preview replaces the rename/copy steps with an
  explicit warning instead of proposing a placeholder path. This closes the
  ordering hole behind the 2026-08-11 mass-reorganize (23,622 entries filed
  under one placeholder directory).
