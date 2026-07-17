<!-- file: docs/agent-tasks/library-ui/TASK-03-tag-filter-cloud.md -->
<!-- version: 1.0.0 -->
<!-- guid: 43030f97-1136-44dc-a954-189059bdacc2 -->
<!-- last-edited: 2026-07-01 -->

# TASK-03 — "Has tag X" filter + browsable tag cloud on Library (TAG-SEARCH)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · **Depends on:** TASK-02 (same file `web/src/pages/Library.tsx` — do not start until TASK-02 is merged to `origin/main` and this worktree is rebased on top of it)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/lu-tag-filter-cloud" -b agent/lu-tag-filter-cloud origin/main
cd "$REPO/.worktrees/lu-tag-filter-cloud"
git rebase origin/main
```

## Goal

Give users a prominent, browsable way to filter the Library by tag, sized by
how common each tag is (a real "tag cloud" — bigger text for more-used tags),
without requiring them to open the filter drawer first.

## ⚠️ Correcting a likely wrong assumption

A basic tag filter **already exists**, but it is nested inside the filter
drawer (`FilterSidebar`), not on the main Library page. Verify before
assuming there's nothing here:
```bash
grep -n "availableTags\|selectedTags\|onTagsChange" web/src/components/audiobooks/FilterSidebar.tsx
```
As of this writing, `web/src/components/audiobooks/FilterSidebar.tsx` (around
line 197–283) already renders:
- A flex-wrapped list of `Chip`s, one per `availableTags` entry, labeled
  `"${tag} (${count})"`, clickable to toggle membership in `selectedTags` —
  all chips render at the same `size="small"`, i.e. **not** sized by
  frequency, so it does not read as a "cloud."
- An `Autocomplete` multi-select for the same `selectedTags`/`onTagsChange`.
- A caption "Books must have ALL selected tags" — i.e. multi-tag selection is
  **AND-only** today; there is no OR mode. **Do not add OR-mode filtering in
  this task** — that requires a backend query-semantics change and is out of
  scope for this frontend-only task; leave the AND semantics as-is.

This existing UI is only reachable by opening the filter drawer
(`filterOpen`/`setFilterOpen` in `Library.tsx`), and gives no visual signal of
which tags are common vs. rare. The actual gap this task closes:

1. A **new, prominent tag-cloud section on the main Library page** (not
   nested inside the filter drawer) where tag font-size scales with
   `availableTags[i].count`, so heavily-used tags are visually larger.
2. Clicking a tag in this new cloud toggles it in the **existing**
   `selectedTags` state via the **existing** `handleTagFilterChange` callback
   — reuse the filter plumbing that already exists, do not create a second,
   parallel tag-filter state.

## Background (verify before editing)

- Tag data flow (all pre-existing, reuse as-is):
  - `availableTags: Array<{ tag: string; count: number }>` is computed in
    `web/src/hooks/useLibraryFilters.ts` and flows into `Library.tsx` (verify:
    `grep -n "availableTags" web/src/hooks/useLibraryFilters.ts web/src/pages/Library.tsx`).
  - `Library.tsx` passes `availableTags`, `selectedTags`, and
    `handleTagFilterChange` into `web/src/components/library/LibraryBookGrid.tsx`
    (verify: `grep -n "availableTags\|selectedTags\|handleTagFilterChange" web/src/pages/Library.tsx web/src/components/library/LibraryBookGrid.tsx`),
    which currently forwards them only to `FilterSidebar` (line ~422–432 as of
    this writing).
  - Tag CRUD (adding/removing tags on individual books) lives in
    `internal/server/handlers/audiobooks/handler_tags.go` — this task does
    NOT touch tag CRUD, only the read-side filter/browse UI. Do not edit any
    Go files for this task.
- `web/src/hooks/useLibraryQuery.ts` already applies `selectedTags` as a
  `tags` query param when fetching (verify:
  `grep -n "selectedTags\|tagsParam" web/src/hooks/useLibraryQuery.ts`) — the
  new tag cloud must feed the same `selectedTags` state so it automatically
  benefits from this existing fetch wiring; do not write a new fetch path.

- **Re-verify all anchors before editing** — line numbers drift:
  ```bash
  grep -n "availableTags\|selectedTags\|handleTagFilterChange" web/src/pages/Library.tsx web/src/components/library/LibraryBookGrid.tsx web/src/components/audiobooks/FilterSidebar.tsx
  ```

## Step-by-step

1. Create a new component, e.g. `web/src/components/library/TagCloud.tsx`,
   that accepts `availableTags: Array<{ tag: string; count: number }>`,
   `selectedTags: string[]`, and `onTagsChange: (tags: string[]) => void` and
   renders each tag as a clickable MUI `Chip`/label whose `fontSize` (or MUI
   `Chip` `size`/inline `sx` font-size) scales between a min and max based on
   that tag's `count` relative to the max count in the list (simple linear or
   log scale — pick whichever avoids one outlier tag dwarfing everything;
   clamp to a sane min/max font size so the layout doesn't break). Selected
   tags should be visually distinguished (e.g. `color="primary"`/`variant="filled"`,
   matching the existing selected-chip styling already used in
   `FilterSidebar.tsx` line ~214). Clicking a tag toggles it in `selectedTags`
   via `onTagsChange`, using the identical toggle logic already used in
   `FilterSidebar.tsx` (`exists ? remove : add`) — do not diverge from that
   toggle behavior.
2. Render this new `TagCloud` component directly on the Library page's main
   content area in `web/src/components/library/LibraryBookGrid.tsx` (or
   `Library.tsx`, whichever keeps consistent with where other "always
   visible" controls already live — check how the existing header/toolbar is
   composed and follow that pattern), wired to the same `availableTags` /
   `selectedTags` / `handleTagFilterChange` already passed down. Make it
   collapsible/dismissible if screen space is a concern, matching the
   existing collapse pattern used by `LibrarySoftDeletedSection` (verify:
   `grep -n "softDeletedExpanded\|onToggleSoftDeletedExpanded" web/src/components/library/LibraryBookGrid.tsx`)
   — reuse that expand/collapse convention rather than inventing a new one.
3. Do not remove or alter the existing tag chip list/Autocomplete inside
   `FilterSidebar.tsx` — the new main-page tag cloud is additive; the filter
   drawer's tag controls remain as a secondary/detailed way to filter.
4. Do not add OR-mode tag matching, do not touch tag CRUD, and do not touch
   any Go/backend files.
5. Bump the file header on every file you touch (version bump + `last-edited`
   date) per `.standards/instructions/file-headers.md`. New files need a
   fresh header per the same standard.

## How to test

```bash
cd web && npm install && npm run build && npm test
```

## Acceptance criteria

- [ ] A tag cloud is visible on the main Library page content area without
      opening the filter drawer.
- [ ] Tag font/chip size visibly scales with `count` (verify by rendering
      tags with clearly different counts in a test and asserting a
      size/style difference, not just presence).
- [ ] Clicking a tag in the cloud toggles it into the existing
      `selectedTags` state (verify the same books get filtered as when using
      the existing `FilterSidebar` tag chips — i.e. both UIs stay in sync
      because they share state).
- [ ] The existing `FilterSidebar` tag chip list and Autocomplete are
      unchanged and still functional.
- [ ] No Go files are modified; no OR-mode tag matching is introduced.
- [ ] A new test (e.g. `web/src/components/library/TagCloud.test.tsx`) covers:
      rendering with varied counts produces differently-sized chips, clicking
      a tag calls `onTagsChange` with the correct toggled array, and a
      selected tag renders in its selected visual state.
- [ ] `npm run build` and `npm test` are green.
- [ ] File headers bumped/added on every changed or new file.

## Commit message

```
feat(library): add browsable tag cloud with size-by-frequency to Library page (TAG-SEARCH)

Tag filtering existed only inside the filter drawer as a uniform-size chip
list, giving no visual signal of which tags were common. Add a new TagCloud
component on the main Library page, sized by tag frequency, sharing the
existing selectedTags/handleTagFilterChange state so it stays in sync with
the filter drawer's tag controls.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/lu-tag-filter-cloud
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a size-scaled tag cloud already renders on the main Library page content
area (outside the filter drawer) and shares `selectedTags`/
`handleTagFilterChange` with the existing filter UI, this task is done —
verify with `grep -rn "TagCloud" web/src/components/library/ web/src/pages/Library.tsx`.
Rollback = revert the commit; the pre-existing `FilterSidebar` tag chip list
and Autocomplete are untouched and remain the sole tag-filter UI, matching
pre-task behavior exactly.
