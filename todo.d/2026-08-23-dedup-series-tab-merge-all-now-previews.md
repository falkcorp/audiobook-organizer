- [ ] **The dedup UI's "Merge All" button now previews instead of merging.**
      TASK-043 made `POST /series/deduplicate` default to `dry_run=true`, and
      `api.deduplicateSeries()` in `web/src/services/api.ts:2821` sends no body,
      so `handleMergeAll` in `web/src/components/dedup/DedupSeriesTab.tsx:232`
      gets a preview after the user confirms the dialog. This is not silent —
      the op's final progress message (which the tab shows as its success
      banner) reads `Series deduplication complete (dry_run=true): WOULD merge
      N duplicates...` — but the button's label no longer matches what it does.
      Deliberately left this way: the API default has to be the safe one, and
      the op should stay preview-only until part 2 of TODO.md's series-dedup
      item (the all-versions getter) and the undo journal land, because both
      change what it deletes. Then give the tab a real two-step — preview,
      show the counts, and a second button that sends `{"dry_run": false}`.
