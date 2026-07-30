<!-- file: changelog.d/20260730_abs_sync_chapter_scan_wiring.md -->
<!-- version: 1.0.0 -->
<!-- guid: c88cec5e-61ce-4ece-b355-87fcbf06bef6 -->
<!-- last-edited: 2026-07-30 -->

### Fixed

- **Chapters are now actually extracted during a scan.** `PersistChaptersForBook`
  shipped in the previous change but had no call site, so no scan ever populated
  chapters and item detail would have returned an empty chapter list. Wired into both
  save paths in `internal/scanner/scanner.go` — the directory-based book path and the
  per-file path. The second call site sits deliberately *outside* the
  `SegmentFiles > 1` block so single-file books get their embedded m4b chapter marks
  too. Failures warn and never abort a scan.
