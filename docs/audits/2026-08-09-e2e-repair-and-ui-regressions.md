<!-- file: docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md -->
<!-- version: 1.4.0 -->
<!-- guid: 7a2f9d41-3e05-4b8c-9160-c4d8b7e35291 -->
<!-- last-edited: 2026-08-09 -->

# E2E repair and the UI regressions it uncovered (2026-08-09)

## Where the suite stands

Measured on merged `main` after all 13 PRs, from a clean worktree:

| project | passed | skipped | **failing** |
|---|---|---|---|
| chromium | 272 | 7 | **0** |
| webkit | 268 | 7 | **4** |

Down from **66 failing specs** at the start of the session. The 7 skipped are `test.fixme`
markers I added for real product bugs (see findings 2, 10 and 11 below) — they are
expected to fail and will report as *unexpected passes* the moment someone fixes them.

The 4 remaining webkit failures are **webkit-only**; chromium passes all of them:

```
 3  library-browser.spec.ts        (pagination — 'jumps to specific page' and siblings)
 1  library-sidebar-filters.spec.ts
```

Re-measure with:

```
cd <a worktree at origin/main>/web
PLAYWRIGHT_JSON_OUTPUT_NAME=/tmp/e2e.json npx playwright test \
  -c tests/e2e/playwright.config.ts --project=chromium --reporter=json > /tmp/e2e-run.log
```

### Webkit — installed now, with two traps

Webkit was **not installed** for most of this session, so the per-file counts in the table
below were measured on chromium alone. It is installed now and the fixes hold: the webkit
run above is within 4 of the chromium one.

`npx playwright install webkit` **from the repo root installs the wrong build** — the root
pins a different Playwright version and fetches `webkit-2248`, while `web/` needs
`webkit-2336`. Run it from `web/`. Running two installs concurrently also leaves a stale
`__dirlock` in `~/Library/Caches/ms-playwright/` that blocks every later install until you
`rm -rf` it.

The stale `scan-button-loading-webkit-darwin.png` golden was regenerated in #2233.

Still open: there are **no linux goldens at all**, so the one visual test cannot pass on a
CI runner regardless. Pre-existing, but it means the nightly e2e workflow has a permanent
red until linux goldens are committed or that test is scoped to a single platform.

## Files fixed (13 PRs — #2224–#2236)

| PR | File | Before → after |
|---|---|---|
| #2224 | `files-history.spec.ts` | 4 → 0 |
| #2225 | `unified-dedup-tab.spec.ts` | 3 → 0 |
| #2226 | `dynamic-ui-interactions.spec.ts` | 6 → 0 |
| #2227 | `batch-operations.spec.ts` | 8 → 0 |
| #2228 | `version-management.spec.ts` | 6 → 0 |
| #2229 | `transcode-and-counting.spec.ts` | 11 → 0 |
| #2230 | `library-browser.spec.ts` + a product fix | 12 → 0 |
| #2232 | `metadata-provenance.spec.ts` + a product fix | 4 → 0 |
| #2233 | webkit visual golden | 1 → 0 (webkit) |
| #2234 | `error-handling` + `scan-import-organize` | 5 → 0 (both browsers) |
| #2235 | `auth-flow` + `search-and-filter` | 3 → 0 (2 fixed, 2 fixme) |
| #2236 | `diagnostics`, `import-paths`, `settings-configuration`, `library-enhancements` | 4 → 0 |

## Product findings — the valuable output of this session

Eleven things the tests uncovered that are **not** test problems. Ordered by user impact.
Each has a `todo.d` fragment; this is the consolidated list.

1. **You cannot sort the library.** `SearchBarProps`
   (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange` prop,
   and `LibraryBookGrid.tsx:133` takes the handler as `_handleSortChange` —
   underscore-prefixed to mark it unused. Sort state still works end to end: `Library.tsx`
   holds it, writes `sort`/`order` to the URL, restores on load, sends it to the API.
   Fully functional, completely unreachable except by hand-editing the URL.
   `SearchBar.test.tsx:43` asserts the control is absent, which now passes vacuously.

2. **The "Deleted" library filter did nothing on a warm cache — FIXED in #2230.**
   `useLibraryQuery` sent `undefined` as the state for `deleted` *and* put that same
   `undefined` in the cache key, so `deleted` and `no filter` were indistinguishable to
   the cache; the client-side `marked_for_deletion` filter sits below the cache-hit
   return. Users saw the full library with the Filters chip showing 1. Only ever worked
   from a cold cache, which is why it survived manual testing.

3. **~~The Authors page crashes on any author record without `aliases`.~~ CORRECTED 2026-08-09 — it does not crash; see the note below.**
   `Authors.tsx:89`, `:120`, `:121` read `a.aliases.length` unguarded — one bad row takes
   the whole page to the error boundary, not just a blank column. Reachable from a real
   API response that omits or nulls the field. Check whether the Go handler guarantees it.
   > **Correction.** The unguarded reads are real, but the claim that a real API response
   > could supply a missing/null `aliases` was **wrong** — it was inferred from the
   > frontend without checking the server. `Authors.tsx` fetches only
   > `getAuthorsWithCounts()`, and that handler has coerced nil to `[]` since 2026-03-10
   > (`internal/audiobooks/author_series.go:108`). The page was never crashing. The reads
   > are now guarded anyway, because a TS type is a compile-time claim about runtime HTTP
   > data and validates nothing — but this was a latent fragility, not an outage.


4. **~50 lines of dead bulk-fetch UI.** `LibraryDialogs.tsx:920` renders
   `<Dialog open={bulkFetchDialogOpen}>`, and `setBulkFetchDialogOpen(true)` appears
   **nowhere** in `web/src`. `handleBulkFetchMetadata` (`Library.tsx:1218`) is reachable
   only from it. The flow was replaced: Fetch Selected → `batchFetchCandidates` → Review.

5. **Version-to-version navigation is gone.** `BookDetailVersionGroup.tsx` has no
   `RouterLink`; `VersionManagement.tsx` has no `navigate()`. The only per-version action
   left is **"Move to: \<title\>"** — which moves *files*, a destructive operation, sitting
   where users used to click to browse.

6. **The version-group summary lost its count and current marker.** `Part of version
   group with N books.` and `(Current)` appear nowhere in `web/src`. Only a bare
   "Version Group Linked" chip survives (`BookDetailHeader.tsx:172`).

7. **Per-field "Use File" / "Use Fetched" one-click apply is gone.** Neither string is
   in `web/src`. Fetched values still show as a source label in `MetadataEditDialog`
   (`:188-198`), but accepting a single fetched field now means opening the dialog and
   saving the whole form.

8. **Change Log rows are mouse-only and invisible to assistive tech.**
   `ChangeLog.tsx:135-154` is a `<Box onClick>` with no `role`, `tabIndex`, keyboard
   handler, or label. The visible "Compare snapshot" link was removed. The flow itself
   still works (verified end to end in `files-history.spec.ts`).

9. **The library card's overflow button has no accessible name.**
   `AudiobookCard.tsx:183` — an `IconButton` containing only `<MoreVertIcon/>`. It is now
   the **only** route to Manage Versions, Edit, Fetch Metadata and Parse with AI. The e2e
   suite has to find it via `button:has([data-testid="MoreVertIcon"])`.

10. **Typing in the search box silently drops every active filter and the sort order.**
    `useLibraryQuery.ts:192-193` routes any non-empty search through
    `api.searchBooksPage`, which (`api.ts:1023-1037`) sends only `search`, `limit`,
    `offset`, `is_primary_version` and optionally `show_quarantined` — no
    `library_state`, no `filters`, no `tags`, no `sort_by`. Filter to Organized, search
    an author, get matches from every state, with the Filters chip still showing its
    count.

11. **The library search is not debounced at all.** Typing the ten characters of
    "Foundation" fires ten requests, exactly one per keystroke. The e2e test is named
    "search debounces input to avoid excessive requests" and asserts `<= 3`. Directly
    relevant to the richer-backend-filtering TODO: no server-side improvement helps if
    the client sends ten queries per search.

Also noted: `TagComparison.tsx:69` has dead `expanded` state — `useState(true)` with
`setExpanded` never called.

## Repeatable causes — check these first on the next file

Every real cause this session fell into one of four shapes. None were found by reasoning
about what the page *should* contain; all came from reading
`test-results/*/error-context.md` and looking at what was actually rendered. **Look first.**

1. **Missing `{ data: ... }` envelope.** ~80 call sites in `services/api.ts` read
   `body.data`. A mock returning a bare body yields `undefined`, and the page usually dies
   on `.length` a few lines later — surfacing as an error boundary, not as a mock error.
   Watch for the exceptions: `getBookTags` and `getBookExternalIDs` read the **top-level**
   body.

2. **A specific route branch shadowed by an earlier prefix catch-all.** Three separate
   instances tonight: `/audiobooks/batch` below `startsWith('/api/v1/audiobooks/') && POST`;
   `/audiobooks/<id>/files` falling into the book-detail catch-all; `'/authors*'`
   over-matching `/authors/<id>/books`. **These fail silently** — the wrong branch answers
   with a 200. A `todo.d` fragment asks for one audit pass over the whole dispatcher.

3. **UI relocated rather than removed.** Book Detail lost the version manager (moved to
   the card overflow menu) and the Compare tab. The page renders fine, so DOM snapshots
   look correct and it reads as selector rot. Grep `web/src` for the literal string before
   concluding a feature is gone — and grep for the *handler* too, since #2223 concluded
   linking was gone from Book Detail when `Link Another Version` is still there inline in
   the Files & History tab.

4. **Mock field name ≠ book field name.** `buildFieldFilters()` emits `author`/`series`;
   the book fields are `author_name`/`series_name`. Filtering on a missing key matches
   nothing; sorting on one leaves the order untouched. Both look like a broken page.

## Loose ends

- **`fix10` worktree is still checked out on a merged branch** — it hosts the real
  `node_modules` that every other e2e worktree symlinks to. Working arrangement, but it
  violates the worktree-cleanup rule. Either install `node_modules` properly in the next
  worktree and delete `fix10`, or leave it and remember why it's there.
- **Two stale local branches** with unmerged commits: `fix/e2e-transcode` and
  `fix/e2e-library-browser`. Their useful content (the transcode investigation notes) has
  been superseded by #2229; safe to delete.
- **Backticks in `git commit -m "..."` get command-substituted by zsh** and silently eat
  the quoted word. This mangled two commit messages tonight, both caught after the fact.
  Use `git commit -F <file>` with a heredoc-written file.

## What is left

Nothing on chromium. The 4 webkit-only failures above have **not** been diagnosed — they
are pagination behaviour differences, the first genuinely browser-specific failures this
effort has produced rather than mock or drift problems.

The 11 product findings below are the real backlog.
