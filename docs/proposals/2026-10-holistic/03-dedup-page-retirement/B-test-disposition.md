<!-- file: docs/proposals/2026-10-holistic/03-dedup-page-retirement/B-test-disposition.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8a1f4d37-c25e-4b90-9e6a-2d7b0c58e913 -->
<!-- last-edited: 2026-10-09 -->

# Appendix B: what happens to every test that targets the old page

Parent: [`../03-dedup-page-retirement.md`](../03-dedup-page-retirement.md).

**Round-2 (r1, 2026-10-09).** PR 7 was split into 7a (Authors sub-view) and 7b (Series sub-view) and PR 9 into 9a/9b/9c; the PR column follows. `BookDedup.validation.test.tsx` stays **dropped**; the main doc's PR 7 no longer claims to port it.

**Rule.** No test is deleted without a recorded destination or reason.

- A test **moves** when its subject moves (PR 1).
- A test is **ported** when its behaviour is re-created in a new lane. The port must be watched failing before it passes.
- A test is **dropped** only when its subject is obsolete; the reason is recorded here.

The `it`/`test` counts below come from `grep -cE "^\s*(it|test)\("`.

## B.1 Vitest unit tests

| File | Tests | Disposition | PR |
|---|---|---|---|
| `components/dedup/__tests__/BandFilterBar.test.tsx` | 5 | **move** to `review/compare/__tests__/` | 1 |
| `components/dedup/__tests__/CandidateCompareDrawer.test.tsx` | 5 | **move**, then extend it with the audio-match section (G5) | 1, 4 |
| `components/dedup/__tests__/FolderFilesChip.test.tsx` | 3 | **move** | 1 |
| `components/dedup/FolderFilesChip.test.tsx` | 3 | **move**, and merge it with the file above. They are two test files for one component, so check whether they duplicate each other before merging. | 1 |
| `components/dedup/__tests__/LabelToggle.test.tsx` | 4 | **move** | 1 |
| `components/dedup/__tests__/BulkActionBar.test.tsx` | 5 | **drop**: the subject has no production importer (dead, F4) | 11 |
| `components/dedup/__tests__/DedupAcousticTab.selectAll.test.tsx` | 6 | **port** to `review/lanes/useDupesLane.selection.test.tsx`, covering cross-page selection under `layer=acoustid` and bulk keep-A, keep-B and dismiss-with-undo | 2 |
| `components/dedup/__tests__/DedupAIReviewTab.test.tsx` | 2 | **port** the "superseded scan" and "apply refused because superseded" cases to the G7 AI-scans sub-view | 8 |
| `components/dedup/__tests__/DedupAuthorTab.test.tsx` | 3 | **port** the books-popover cases to `AuthorsPanel.test.tsx` | 7a |
| `components/dedup/__tests__/DedupBookTab.selectAll.test.tsx` | 3 | **port** selection behaviour to the G3 cluster spine. Its subject (the Version Groups list) is obsolete, but cross-page selection over groups is the same contract. | 6 |
| `components/dedup/__tests__/DedupBookTab.test.tsx` | 7 | **port** "bulk merge outcome reporting", which preserves partial-failure reports after a refetch, to `useDupesLane.test.ts` for `mergeSelected` and `linkCluster`. Keep that defect fixed in the new verbs. | 2, 6 |
| `components/dedup/__tests__/DedupEmbeddingTab.test.tsx` | 2 | **port** the "book-sig coverage badge" cases to `spine/DupesSpine.test.tsx` (C78) | 2 |
| `components/dedup/__tests__/dedupTabs.selectAll.test.tsx` | 5 | **port**: Author part to `AuthorsPanel.test.tsx` (7a); Series part (7b); AI part to G7 | 7a, 7b, 8 |
| `pages/__tests__/DedupLabels.test.tsx` | 5 | **port** to `review/lanes/useDupesLabels.test.tsx` and `DupesLabelsView.test.tsx` | 5 |
| `pages/__tests__/BookDedup.validation.test.tsx` | 13 | **drop**. It tests a *local copy* of `validateBookID` declared inside the test file (`:9-21`), not the product function, so it would stay green whatever the product did. Its subject (typing two ids into the compare panel) is replaced by G5, which takes the ids from the open pair. | 11 |

Totals: 15 files and 71 tests.

- move: 5 files, 20 tests;
- port: 8 files, 33 tests;
- drop: 2 files, 18 tests.

## B.2 Playwright e2e

| Spec / block | Tests | Disposition | PR |
|---|---|---|---|
| `tests/e2e/dedup.spec.ts`, "Author Dedup" (`:99-243`) | 11 | **port** to `review-authors-lane.spec.ts` against `/review?lane=authors` | 7a |
| `dedup.spec.ts`, "Book Preview Popover" (`:245-317`) | 6 | **port** to the same spec | 7a |
| `dedup.spec.ts`, "Series Dedup" (`:319-338`) | 2 | **port**, with `view=series` | 7b |
| `dedup.spec.ts`, "Dedup Tab Navigation" (`:340-383`) | 3 | **replace** with redirect assertions in `review-dupes-lane.spec.ts`: one per row of the G9 table | 10 |
| `dedup.spec.ts`, "Dedup Refresh Operations" (`:384-397`) | 1 | **port**: the refresh command in the authors lane | 7a |
| `dedup.spec.ts`, "Dedup Pagination" (`:398-420`) | 1 | **port**: author-group pagination in the authors lane | 7a |
| `dedup.spec.ts`, "Dedup Bulk Actions" (`:421-440`) | 2 | **port** "merge all asks first" to the authors spec | 7a |
| `dedup.spec.ts`, "Dedup AI Review" (`:442-450`) | 1 | **port** to the authors spec, AI sub-view | 8 |
| `tests/e2e/dedup-operations.spec.ts`, "Production Company Resolution" (`:78-157`) | 3 | **port** to the authors spec | 7a |
| `dedup-operations.spec.ts`, "Dedup Operation Progress" (`:159-185`) | 2 | **port**: progress now comes from the bell, so assert the toast and the bell entry | 7a |
| `dedup-operations.spec.ts`, "Scheduler Tasks for Dedup" (`:187-220`) | 2 | **move** to an operations spec. It drives `/operations` and the tasks API, not the page. Its "manual trigger" test starts on `/dedup?tab=authors`; change that to `/review?lane=authors`. | 10 |
| `dedup-operations.spec.ts`, "Dedup Error Handling" (`:222-283`) | 3 | **port** to the authors spec. These are the error-state assertions the four-state rule requires. | 7a |
| `tests/e2e/utils/test-helpers.ts:271-274,946-952` | — | **keep**. The mocks serve `/review` too. Update the comment "the Dedup page fetches this on mount" to name the Dupes lane. | 11 |

Totals: 2 specs, 12 blocks, 37 tests.

- **port: 32** (11 + 6 + 2 + 1 + 1 + 2 + 1 + 3 + 2 + 3);
- **replace with redirect assertions: 3**;
- **move to an operations spec: 2**.

Not affected:

- `tests/e2e/review-dupes-lane.spec.ts` and `benchmark-review-lanes.spec.ts` already target `/review`. They gain cases in PRs 2, 6 and 10.
