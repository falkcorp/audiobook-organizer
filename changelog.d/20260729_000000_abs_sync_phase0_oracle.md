<!-- file: changelog.d/20260729_000000_abs_sync_phase0_oracle.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4cfcdaaa-a7b2-4189-8aee-ea1bc3b41caa -->
<!-- last-edited: 2026-07-29 -->

### Added

- **Audiobookshelf conformance harness (Phase 0).** Added a pinned ABS 2.36.x
  reference oracle (`testdata/abs-oracle/`), a fixture capture script
  (`scripts/abs_capture_fixtures.py`), golden fixtures (`testdata/abs-fixtures/`),
  and `internal/syncapi/conformance` -- a differ that checks field presence and
  JSON type, not just values, so a response missing a field an ABS client
  hard-requires fails the build instead of failing opaquely on a phone.
