### Added

- Search result cache: new counter `audiobook_organizer_search_cache_patch_cap_rebuilds_total` (and `Stats.PatchCapRebuilds`) counts lookups that rebuilt because more than `MaxPatchChanged` (2048) books changed since the entry was built.
- `docs/audits/2026-09-26-searchcache-ring-memory.md`: measured memory and lock cost of the search change ring at 8,192 / 65,536 / 262,144 records (the size is unchanged).

### Fixed

- `GET /api/v1/activity?tags=def:<op id>` now returns a renamed operation's whole history by either spelling. The tag is resolved through the operations registry and matches `def:<canonical>` or any `def:<former id>` as one AND term (`ActivityFilter.TagAliases`) in the Nuts, SQLite and Pebble activity stores.
