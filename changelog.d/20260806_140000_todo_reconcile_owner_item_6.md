<!-- file: changelog.d/20260806_140000_todo_reconcile_owner_item_6.md -->
<!-- version: 1.0.0 -->
<!-- guid: c81f3d5a-47e9-4b62-9a03-5d7e2b148f60 -->
<!-- last-edited: 2026-08-06 -->

### Fixed

- **`TODO.md` now records owner item 6 (warm the metadata-results build at boot)
  as done.** The warmer shipped as `warmMetadataResultsCache` and is enrolled in
  `startCacheWarmers`, but the checkbox was never ticked, so the item read as
  outstanding. Documentation only — no behaviour change.

  The entry now also distinguishes the warmer from the stale-while-revalidate
  work that merged the same night (#2153/#2154). They address the same 34 s
  symptom by different means — SWR keeps a warm cache warm under load, the
  warmer covers the first request after a restart — and conflating them is what
  made the item look already-closed to one reader and still-open to another.
