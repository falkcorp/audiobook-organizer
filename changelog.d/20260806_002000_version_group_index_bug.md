<!-- file: changelog.d/20260806_002000_version_group_index_bug.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b04f713-25ce-4a09-9d16-4f2703ab85c1 -->
<!-- last-edited: 2026-08-06 -->

### Added

- **Specs + plans for 2 more workstreams** (multidisc apply canary, metadata-results
  cold start), marked UNVERIFIED in-document — the symbol-check pass did not run.

- **Recorded a production bug:** `GetBooksByVersionGroup` silently under-reports
  version-group membership when its index is partially populated, because the
  full-scan fallback only triggers on a ZERO-result index rather than on a
  suspected-incomplete one. This breaks the one-primary-per-group invariant:
  `set-primary` demotes only what the lookup returns, so a group can end up with
  two primaries and show two tiles for one book. The same function backs
  `ApplyVersionGroup`'s stray-primary demotion, so approving a version-group
  review hold is affected too. Found while version-grouping the two copies of
  "The Successors" on production.
