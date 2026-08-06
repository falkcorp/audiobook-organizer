<!-- file: changelog.d/20260805_223000_workstream_specs_partial.md -->
<!-- version: 1.0.0 -->
<!-- guid: 71fc3d82-64ab-4e09-b5d7-2039c81ea6f4 -->
<!-- last-edited: 2026-08-05 -->

### Added

- **Design specs + implementation plans for 3 of 11 open workstreams** (~25,600
  words). Documentation only. Covers review-queue recommendations + per-hold
  overrides (owner items 1 and 2), duplicate detection with combine-by-template
  and version-grouping (the "assembled copy already exists" class), and full
  playlist support.

  ⚠️ **Marked UNVERIFIED in-document.** These were authored by agents but the
  adversarial verification pass — which grep-checks that every cited function,
  struct field, op ID and path actually exists — did not run; the workflow was
  halted by API rate limiting (429/529) partway through. Each file carries a
  banner saying so. The design reasoning and the measured production numbers are
  sound; the code citations are what needs checking before execution.

  The remaining 8 workstreams (multidisc canary, series-as-book-numbers,
  metadata-results cold start, First Aid orchestrator, version-group acoustic
  audit, chapters serve + backfill, reading/review status sync, Deluge
  integration) still have their `todo.d/` fragments and are unaffected.
