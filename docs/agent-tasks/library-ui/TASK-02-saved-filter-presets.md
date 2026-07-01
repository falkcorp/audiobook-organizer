<!-- file: docs/agent-tasks/library-ui/TASK-02-saved-filter-presets.md -->
<!-- version: 1.0.0 -->
<!-- guid: aa72b1d8-eacd-4f03-a6a6-26e30bb89ed8 -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Save current filter set as a named preset (USER-QUICK-FILTERS)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · **Depends on:** TASK-04 (same file `web/src/pages/Library.tsx` — do not start until TASK-04 is merged to `origin/main` and this worktree is rebased on top of it)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/lu-saved-filter-presets" -b agent/lu-saved-filter-presets origin/main
cd "$REPO/.worktrees/lu-saved-filter-presets"
git rebase origin/main
```

## Goal

Let a user save their currently-applied Library filter set as a named
preset, persisted per-user, and reapply/edit/delete it later. There are
already six **built-in** quick-filter presets computed server-side (missing
covers, no ISBN, etc. — see below), but no way for a user to save their own
**custom** named filter combinations.

## ⚠️ Correcting a likely wrong assumption

There is a `web/src/components/audiobooks/AudiobookList.tsx` component with a
"Quick Filters" kebab menu that renders the six built-in presets from
`GET /library/quick-queries`. **This component is dead code — it is not
imported or rendered anywhere in the live app** (verify:
`grep -rn "AudiobookList" web/src/App.tsx web/src/pages/` returns nothing).
The live Library page (`web/src/pages/Library.tsx`, routed at `/library` and
`/fingerprints` in `web/src/App.tsx`) does **not** currently have a
quick-filter kebab menu at all. Do not build on top of `AudiobookList.tsx` or
assume its menu is live UI — build the new "save as preset" feature into the
actual live filter UI described below.

## Background (verify before editing)

- Filter state on the live Library page is owned by
  `web/src/hooks/useLibraryFilters.ts`, which exposes `filters: FilterOptions`,
  `setFilters`, `handleFiltersChange`, `filterOpen`/`setFilterOpen`, and
  `selectedTags`. `Library.tsx` destructures these (verify with
  `grep -n "useLibraryFilters(" web/src/pages/Library.tsx`) and passes them
  down to `web/src/components/library/LibraryBookGrid.tsx`, which renders
  `web/src/components/FilterPanel.tsx` (the actual filter UI — a dialog/panel
  keyed off `filterOpen`/`onFiltersChange`).
  ```bash
  grep -n "filterOpen\|handleFiltersChange\|FilterPanel" web/src/pages/Library.tsx web/src/components/library/LibraryBookGrid.tsx web/src/components/FilterPanel.tsx
  ```
- The six **built-in** presets are computed server-side by
  `GetQuickQueryCounts` / served at `GET /library/quick-queries`
  (`internal/server/handlers/system/handler.go`, search for
  `func (h *Handler) GetQuickQueries`) — these are unrelated read-only counts,
  not user-editable, and out of scope for this task. Do not modify that
  endpoint or its six presets.
- Per-user JSON-blob persistence already exists and has a working precedent
  you should copy: `web/src/services/api.ts` has `getUserColumnConfig`,
  `saveUserColumnConfig`, `deleteUserColumnConfig`, which PUT/GET/DELETE a
  JSON-serialized blob under a fixed key via the generic preference endpoints
  `GET/PUT/DELETE /preferences/:key` (backed by
  `internal/server/handlers/system/handler.go`'s `GetUserPreference` /
  `SetUserPreference` / `DeleteUserPreference`, wired in
  `internal/server/wire_system_routes.go`). Re-verify:
  ```bash
  grep -n "COLUMN_CONFIG_KEY\|getUserColumnConfig\|saveUserColumnConfig\|deleteUserColumnConfig" web/src/services/api.ts
  grep -n "GetUserPreference\|SetUserPreference\|DeleteUserPreference" internal/server/handlers/system/handler.go internal/server/wire_system_routes.go
  ```
  This task should follow the **exact same pattern** (a new preference key,
  e.g. `library_filter_presets`, storing a JSON array of named presets) rather
  than adding a new backend endpoint — do not add new Go routes/handlers
  unless the generic preference endpoints genuinely cannot express this (they
  can: it's a JSON blob under a key, same shape as column config).
- `FilterOptions` (in `web/src/types`, check with
  `grep -n "interface FilterOptions" web/src/types/index.ts`) is the shape of
  a single filter set — a saved preset is `{ id: string, name: string,
  filters: FilterOptions, selectedTags?: string[] }`.

## Step-by-step

1. In `web/src/services/api.ts`, add a new section mirroring "Column config
   preferences" (do not touch the existing column-config functions):
   - `const FILTER_PRESETS_KEY = 'library_filter_presets';`
   - `export interface SavedFilterPreset { id: string; name: string; filters: FilterOptions; selectedTags?: string[]; }`
   - `getSavedFilterPresets(): Promise<SavedFilterPreset[]>` — GET the key,
     `JSON.parse` the value, default to `[]` on missing/parse failure (mirror
     `getUserColumnConfig`'s null-on-failure pattern but return `[]` instead
     of `null` since this is a list).
   - `saveSavedFilterPresets(presets: SavedFilterPreset[]): Promise<void>` —
     PUT the full array back (mirror `saveUserColumnConfig`).
   - You do not need a delete-all function; delete-one is handled by
     save-with-the-item-removed.
2. In `web/src/pages/Library.tsx`:
   - Load saved presets once on mount (mirror how `availableTags` or similar
     lists are loaded via `useEffect`) into local state,
     `savedPresets: SavedFilterPreset[]`.
   - Add a "Save current filters as preset..." action, reachable from
     wherever the filter UI already exposes actions (check `FilterPanel.tsx`
     for an existing actions row/footer to extend, or add a small button next
     to the existing filter-open control in `Library.tsx`/`LibraryBookGrid.tsx`
     — match whichever location keeps parity with the existing MUI layout, do
     not introduce a new toolbar). Clicking it opens a small MUI `Dialog` with
     a name `TextField` and a Save button; on save, build a
     `SavedFilterPreset` from the current `filters`/`selectedTags`, append to
     `savedPresets`, call `saveSavedFilterPresets`, update local state.
   - Add a "Manage presets" submenu (an MUI `Menu`/`MenuItem` list, reachable
     from the same place as the save action) listing each saved preset by
     name with:
     - Click-to-apply: calls `handleFiltersChange` (and `setSelectedTags` if
       the preset stored tags) with the preset's stored filter values.
     - An edit affordance (rename — reopen the save dialog pre-filled, PUT
       the updated array) and a delete affordance (remove from the array,
       PUT the updated array) per preset row.
   - Do NOT touch the six built-in quick-query presets or
     `GET /library/quick-queries` — this task only adds user-saved custom
     presets, it does not change or duplicate the built-ins.
3. Do not modify any mutation handler's cache-invalidation logic (that's
   TASK-04's scope, already merged before this task starts) — only touch
   filter-preset code paths.
4. Bump the file header on every file you touch (version bump + `last-edited`
   date) per `.standards/instructions/file-headers.md`.

## How to test

```bash
cd web && npm install && npm run build && npm test
```

## Acceptance criteria

- [ ] `web/src/services/api.ts` has `getSavedFilterPresets` /
      `saveSavedFilterPresets` using the `library_filter_presets` preference
      key, mirroring the column-config pattern exactly (no new backend
      routes added).
- [ ] The live Library page (`/library` route) has a working "save current
      filters as a named preset" action.
- [ ] Saved presets persist across a reload (backed by the `/preferences/:key`
      endpoint, not local-only state).
- [ ] Saved presets are listed in a "Manage" UI with apply / rename / delete
      actions.
- [ ] The six built-in quick-query presets and `GET /library/quick-queries`
      are unchanged.
- [ ] A new or extended test in a `Library.*.test.tsx` file covers: saving a
      preset persists it (mocked `apiFetch`/`api.ts` call), applying a saved
      preset updates the active filters, and deleting a preset removes it
      from the list.
- [ ] `npm run build` and `npm test` are green.
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(library): add save/apply/manage named filter presets (USER-QUICK-FILTERS)

Users could only use the six server-computed quick-filter presets; there was
no way to save a custom combination of filters for reuse. Add per-user
persistence of named filter presets via the existing generic /preferences/:key
endpoint (same pattern as column config), with save/apply/rename/delete UI on
the Library page.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/lu-saved-filter-presets
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `getSavedFilterPresets`/`saveSavedFilterPresets` already exist in
`web/src/services/api.ts` and the Library page already has a working
save/manage UI wired to them, this task is done — verify with
`grep -n "library_filter_presets\|SavedFilterPreset" web/src/services/api.ts web/src/pages/Library.tsx`.
Rollback = revert the commit; existing saved presets remain harmlessly stored
under the `library_filter_presets` preference key (orphaned but inert) until
the feature is reintroduced or the key is manually deleted via
`DELETE /preferences/library_filter_presets`.
