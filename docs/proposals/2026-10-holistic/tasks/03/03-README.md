<!-- file: docs/proposals/2026-10-holistic/tasks/03/03-README.md -->
<!-- version: 1.0.0 -->
<!-- guid: cdb39ed7-453e-43b4-a2b7-87f67b2f7642 -->
<!-- last-edited: 2026-10-09 -->

# Workstream 03 task briefs: retire the old /dedup page (wave 2)

Source: `docs/proposals/2026-10-holistic/03-dedup-page-retirement.md` (v1.2.1) with appendices A (endpoint inventory) and B (test disposition). Briefs follow `../00-TEMPLATE.md`. All 16 are wave 2. Every brief names its parity-matrix rows (C01 to C78), the UI labels the owner will see, the test files moved, ported or deleted, and a clickable acceptance checklist.

## Briefs in merge order

| # | Brief | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|---|
| 1 | [03-PR1](03-PR1.md) | Move the compare-drawer closure under review/compare | 2 | sonnet | S | none inside 03 |
| 2 | [03-PR2](03-PR2.md) | Dupes lane: layer filter and chips, bulk keep-older/newer, iTunes and partial-fp chips | 2 | sonnet | M | 03-PR1; 02 PR 18 |
| 3 | [03-PR3](03-PR3.md) | Dupes lane: export duplicates | 2 | sonnet | S | 03-PR2; 01 P7 (`api.ts` order R16) |
| 4 | [03-PR4](03-PR4.md) | AcoustID key in Settings, online lookup and reset commands (D2 Mac-worker warning), audio-match in drawer | 2 | sonnet | S | 03-PR1, 03-PR3 |
| 5 | [03-PR5](03-PR5.md) | Labels and Suspicious sub-views of the Duplicates lane (D20) | 2 | sonnet | M | 03-PR1, 03-PR4 |
| 6 | [03-PR6](03-PR6.md) | Duplicates lane: Clusters view and cluster verbs (D22) | 2 | sonnet | L | 03-PR2 |
| 7 | [03-PR7a](03-PR7a.md) | "Authors & series" lane scaffolding and Authors sub-view (D19) | 2 | sonnet | M | 03-PR6 |
| 8 | [03-PR7b](03-PR7b.md) | Authors & series lane: Series sub-view | 2 | sonnet | S | 03-PR7a |
| 9 | [03-PR8](03-PR8.md) | Authors & series lane: AI scans sub-view | 2 | sonnet | M | 03-PR7a, 03-PR7b |
| 10 | [03-PR9a](03-PR9a.md) | Repairs fixer `dedup.series-prune` (Evaluate-style) | 2 | opus | S | 04 P2, P5, P12 |
| 11 | [03-PR9c](03-PR9c.md) | Repairs fixer `dedup.split-books` (Evaluate-style, D21) | 2 | opus | M | 03-PR9a |
| 12 | [03-PR9b](03-PR9b.md) | Repairs fixer `reconcile.missing-files` (Evaluate-style) | 2 | opus | M | 04 P14c (D27); 03-PR9a, 03-PR9c |
| 13 | [03-PR10](03-PR10.md) | Redirect /dedup to Review, sidebar, announcement link, `?fixer=` seed | 2 | sonnet | S | PRs 2, 5, 7a, 7b, 8, 9a, 9b, 9c |
| 14 | [03-PR11](03-PR11.md) | Delete the old page frontend and its ported tests | 2 | sonnet | M | 03-PR10 live one release; 01 P72; 01 P7 |
| 15 | [03-PR12](03-PR12.md) | Retire the dead /audiobooks/duplicates routes behind a 410 stub, with caller proof | 2 | opus | S | 03-PR11; 01 P72 first |
| 16 | [03-PR13](03-PR13.md) | Docs, changelog and executive summary | 2 | sonnet | S | 03-PR12; 01 P73 |

Model split: 12 sonnet (PRs 1 to 6, 7a, 7b, 8, 10, 11, 13) and 4 opus (9a, 9b, 9c, 12). Sizes: S 8 (PR1, 3, 4, 7b, 9a, 10, 12, 13), M 7 (PR2, 5, 7a, 8, 9b, 9c, 11), L 1 (PR6).

## Ordering rules

- **Roadmap anchors.** `ReviewWorkspace.tsx` is a hotspot (roadmap R24, section 4): 02 PR 18 first, then 03 in the order above, then 06 P10, then 05 PR 10. `api.ts` (R16): 01 P7 -> 03 PR 3 -> ... -> 03 PR 11. `useRepairsLane.ts` (R23): 03 PR 10 -> 07 F3 -> 05 PR 10. `internal/plugins/maintenance/plugin.go`: 04 P2 -> P5 -> P12 -> 03 PR 9a -> 9c -> 9b -> 05 12D. After the 03 chain, roadmap order is 01 P72 -> 03 PR 11 -> 03 PR 12 -> 01 P73 -> 03 PR 13.
- **Serial on `ReviewWorkspace.tsx`:** PRs 2, 3, 4, 5, 6, 7a, 7b, 8, 9b (one description string), 10. The spec says PRs 3, 4, 5 may run in parallel; the briefs serialize them (3, 4, 5) because all three edit that file. Develop in parallel if you like; merge one at a time.
- **The Go PRs (9a, 9c, 9b, 12) can be developed alongside the web PRs** but merge in the order shown. 9b is hard-gated on 04 P14c.
- **PR 10 needs every gap PR merged.** PR 11 needs PR 10 live for a release so bookmarks and the server announcement are proven. PR 12 must prove no caller remains before it retires any route.

## Discrepancies found while verifying against the worktree at HEAD

- The spec's anchor `duplicates/handler.go:738` for the series-prune preview is only the HTTP wrapper; the logic is `internal/server/server_title_helpers.go:16` (`computeSeriesPrunePreview`) and `internal/server/duplicates_helpers.go:142` (`executeSeriesPrune`), in package `server`, which the maintenance plugin cannot import. Brief 9a includes the extraction into `internal/dedup`.
- The spec says apply for split-books runs `dedup.split-book-merge`; at HEAD only `dedup.split-book-bulk-merge` is in `internal/server/testdata/op_ids.golden` (`dedup.split-book-merge` is its ConcurrencyKey). Brief 9c tells the agent to choose and record the apply path.
- The spec says PR 12 deletes routes; D9 and the lead's instruction call for a 410 stub for one release, which brief 12 implements, with the stub's removal filed as a `todo.d` fragment.
- The Dupes lane tab reads "Duplicates" in the UI (`lanes/dupes.ts:19`); the spec's "Dupes lane" is shorthand. The command menu is titled "Dedup" with groups "Find and score", "Collect evidence", "Maintenance"; there is no "Advanced" level at HEAD, so G5's "Advanced -> Maintenance" is placed in the existing Maintenance group (brief 4 tells the agent to report what it finds).
- The spec's PR 10 lists `initialLaneFrom` accepting `layer` and `view`, but PR 2 (`layer`) and PR 5 (`view`) need those seeds earlier; the briefs add each seed in the PR that needs it and PR 10 verifies and adds only `fixer`.
- `web/tests/e2e/review-authors-lane.spec.ts` and `web/src/pages/DedupRedirect.tsx` do not exist yet (created by 7a and 10). The "Scheduler Tasks for Dedup" E2E block is moved to `operation-monitoring.spec.ts` (the spec only says "an operations spec").
