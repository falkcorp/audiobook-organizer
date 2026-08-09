<!-- file: changelog.d/20260809_290000_library_duplicate_mount_fetch.md -->
<!-- version: 1.0.0 -->
<!-- guid: e2b71c68-40d5-4a93-8f17-c5904ab3e2d1 -->
<!-- last-edited: 2026-08-09 -->

### Fixed

- **The Library asked the server for the same page of books twice every time you
  opened it.** The search box re-parsed its (empty) contents on load and reported
  a result that was identical to the one already held, which was enough to make
  the page think its filters had changed and fetch everything a second time. On a
  large library that is a full duplicate query on every visit. It now fetches
  once.
