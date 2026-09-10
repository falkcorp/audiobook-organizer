<!-- file: docs/agent-tasks/todo-completion-2026-09/web/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5465f752-e483-4383-8d1d-06bf0cd692d0 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — web (todo-completion-2026-09)

22 tasks: 16 carried forward from the 2026-08-21 package (ids kept), 6 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-158](TASK-158-add-a-settings-panel-section-to-edit-path-aliase.md) | carried | hygiene | P2 | M | Add a Settings panel section to edit path_aliases | No PathAliasesSection.tsx/.test.tsx exists (find = 0). grep -c 'path_aliases' in web/src/h |
| [TASK-160](TASK-160-move-openai-api-key-validation-server-side-curre.md) | carried | security | P1 | M | Move OpenAI API key validation server-side (currently sent from the browser) | web/src/components/wizard/WelcomeWizard.tsx:160 still `fetch('https://api.openai.com/v1/mo |
| [TASK-161](TASK-161-strip-dedup-and-metadata-source-namespaces-from-.md) | carried | hygiene | P2 | S | Strip dedup:* and metadata:source:* namespaces from Browse by Tag widget | web/src/components/library/TagCloud.tsx:120 still a single `label={`${t.tag} (${t.count})` |
| [TASK-162](TASK-162-reformat-metadata-tags-in-browse-by-tag-strip-pr.md) | carried | hygiene | P2 | S | Reformat metadata:* tags in Browse by Tag: strip prefix, 'key: value' spacing | Same single label={} line (TagCloud.tsx:120) as TASK-161, still the raw tag with no format |
| [TASK-165](TASK-165-review-the-17-apifetch-callers-catch-handlers-fo.md) | carried | hygiene | P2 | M | Review the 17 apiFetch-callers' catch handlers for session-expiry messaging | isAuthRedirectError (defined web/src/utils/apiFetch.ts:70) is used in exactly one non-test |
| [TASK-166](TASK-166-make-the-book-detail-page-s-author-field-s-link-.md) | carried | hygiene | P2 | S | Make the book-detail page's Author field(s) link to a library view filtered by t | grep 'author_id' in web/src/hooks/useLibraryQuery.ts, web/src/pages/Library.tsx = 0 hits.  |
| [TASK-167](TASK-167-make-the-book-detail-page-s-series-field-link-to.md) | carried | hygiene | P2 | S | Make the book-detail page's Series field link to a library view filtered by that | grep 'series_id' in the same three files = 0 hits. BookDetailInfoTab.tsx:272-273 still a p |
| [TASK-168](TASK-168-make-narrator-publisher-genre-and-release-year-f.md) | carried | hygiene | P2 | M | Make Narrator, Publisher, Genre, and Release Year fields link to filtered librar | grep "searchParams.get('filters')" across web/src = 0 hits (the prerequisite ?filters= par |
| [TASK-169](TASK-169-link-version-group-id-to-a-filtered-library-view.md) | carried | hygiene | P2 | S | Link version_group_id to a filtered library view (now unblocked — the filter wor | grep 'version_group_id' in web/src/components/bookdetail/BookDetailVersionGroup.tsx = 0 hi |
| [TASK-170](TASK-170-retarget-dedup-operations-spec-ts-and-dedup-spec.md) | carried | hygiene | P2 | S | Retarget dedup-operations.spec.ts and dedup.spec.ts resolve-production status mo | web/tests/e2e/dedup-operations.spec.ts:118 and web/tests/e2e/dedup.spec.ts:163 both still  |
| [TASK-171](TASK-171-retarget-diagnostics-spec-ts-ai-submit-and-expor.md) | carried | hygiene | P2 | S | Retarget diagnostics.spec.ts AI-submit and export status mocks to v2 | web/tests/e2e/diagnostics.spec.ts:183 op-1 mock now targets '**/api/v1/operations/v2/op-1' |
| [TASK-173](TASK-173-add-resizable-sortable-columns-to-the-acoustic-d.md) | carried | hygiene | P2 | M | Add resizable/sortable columns to the acoustic dedup candidates table | grep 'useConfigurableTable' web/src/components/dedup/DedupAcousticTab.tsx = 0 hits; raw <T |
| [TASK-174](TASK-174-add-resizable-sortable-columns-to-the-activity-l.md) | carried | hygiene | P2 | M | Add resizable/sortable columns to the Activity Log table | grep 'useConfigurableTable' web/src/pages/ActivityLog.tsx = 0 hits; raw <TableContainer> a |
| [TASK-175](TASK-175-add-resizable-sortable-columns-to-the-split-book.md) | carried | hygiene | P2 | M | Add resizable/sortable columns to the split-book dedup candidates table | grep 'useConfigurableTable' web/src/components/dedup/DedupSplitBookTab.tsx = 0 hits; Candi |
| [TASK-189](TASK-189-play-the-first-2-minutes-of-part-1-s-audio-direc.md) | carried | correctness | P2 | M | Play the first ~2 minutes of part 1's audio directly from the review metadata pa | internal/server/audio_sample.go:47 still `context.WithTimeout(c.Request.Context(), 120)` ( |
| [TASK-217](TASK-217-evidence-panel-explain-a-missing-score-derivatio.md) | carried | hygiene | P2 | M | Evidence panel: explain a missing score derivation in plain language and offer r | web/src/components/review/evidence/adapters.ts:119 wording unchanged from the 09-02 snapsh |
| [TASK-304](TASK-304-author-merge-preview-popover-shows-an-author-s-b.md) | new-finding | data-loss | P1 | S | Author-merge preview popover shows an author's book list as empty on fetch failu | web/src/components/dedup/DedupAuthorTab.tsx:151 |
| [TASK-324](TASK-324-authors-page-fetches-the-entire-authors-table-on.md) | new-finding | perf | P1 | M | Authors page fetches the entire authors table on every mount, no server paginati | web/src/services/api.ts:1893 |
| [TASK-327](TASK-327-series-page-fetches-the-entire-series-table-on-e.md) | new-finding | perf | P2 | M | Series page fetches the entire series table on every mount, no server pagination | web/src/services/api.ts:1827 |
| [TASK-329](TASK-329-dashboard-count-widgets-silently-show-0-when-the.md) | new-finding | ux | P1 | S | Dashboard count widgets silently show 0 when the count API fails -- indistinguis | web/src/pages/Dashboard.tsx:224 |
| [TASK-330](TASK-330-operations-timeline-fetch-swallows-both-network.md) | new-finding | ux | P2 | S | Operations timeline fetch swallows both network errors and non-2xx into an empty | web/src/services/api.ts:589 |
| [TASK-332](TASK-332-no-vitest-or-playwright-coverage-exists-for-the.md) | new-finding | hygiene | P2 | M | No Vitest or Playwright coverage exists for the Authors or Series pages | web/src/pages/__tests__:0 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
