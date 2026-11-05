<!-- file: docs/agent-tasks/platform-core/F9-05-cut-shell-providers-stores-dark.md -->
<!-- version: 1.0.3 -->
<!-- guid: 63f1b27d-a358-4726-9d66-3132e9ec5db8 -->
<!-- last-edited: 2026-11-05 -->

# F9-05 — Cut the shell-classified providers, stores and presentational pieces, dark: the Activity page with `useOperationsStore`, `operationGrouping` and `OperationTrackerProvider`, `AuthContext`, `ToastProvider`, `useAppStore`, the `storageKeys` split, `components/common`, `ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner`, the four ui-kit hooks and the `apiFetch` group

| Field | Value |
|---|---|
| ID | F9-05 |
| Release | R2, flip label F9 (dark PR; R2 if the shell is ready, else R3 unchanged) |
| Tier | **T3** (Opus / high): a cut of shell-classified providers, stores and presentational pieces, the Activity page among them; the import boundary must prove the cut and the SPA must behave the same |
| Wave | W39 (alone). This row is the **first** of the three F9 cut rows: F9-02 and F9-04 depend on it and follow in W40 |
| Depends on | F9-01 (the `ui-kit`, `shell` and `api-client` packages), F8-04 (the last R2 flip: F4-08 through F8-04 edit the `web/src/` files this row moves) |
| Precondition | 07 §7.19 step 4: every R2 flip whose native routes the cut files call has merged (enforced by `Depends on`: F8-04) |
| Spec | 07 §7.11 (the classification table: the rows named in the title), §7.9.1 (`AuthProvider`, the 401 rule, `safeReturn`), §7.3.1 (import boundary; ui-kit's graph), §7.4.2 (`TaskKindPresentation`, `ActivityPaneContribution`, `storageKey()`, the provider order), §7.18 (`shellStorageKeys.test.tsx`, `shortcuts.test.tsx`, `auth.flow.test.tsx`), §7.19 step 4 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's SPA looks and behaves the
same after this PR. It still boots on `<BrowserRouter>` with its own `App.tsx`; the moved pieces are imported from
`@mediacore/shell`, `@mediacore/ui-kit` and `@mediacore/api-client` through their old paths (below). `main.tsx` on
`createShell`, `renderInShell` and the `/books/` URLs are F9-03's.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f9-05-shell-providers" -b agent/platform-core-f9-05-shell-providers origin/main
cd "$REPO/.worktrees/pc-f9-05-shell-providers"
git rebase origin/main
npm ci --prefix web
```

## Precondition: the R2 flips have merged

07 §7.19 step 4 lists as preconditions "steps 1–2 and every R2 flip whose native routes the shell pages call". The
pieces this row moves call the identity routes (`AuthContext`, F4-08), the tasks routes (the Activity page,
`useOperationsStore`, `OperationTrackerProvider`, F5-18), the stream (F6-03) and the system routes (F8-04), and those
flips edit these files under `web/src/` in their own PRs. `Depends on` F8-04 puts all of them in your base, so there is
nothing to check here.

## Goal (07 §7.11, one row of the table per item)

1. **Activity** (`pages/ActivityLog.tsx` + `stores/useOperationsStore.ts`, `stores/operationGrouping.ts`): the page
   becomes the shell's tasks pane over `/api/core/v1/tasks` (the generated core client; it imports neither
   `generated/ops.ts` nor `utils/operationPolling`, which stays books', and gets shell state helpers over the
   generated `TaskRunView` states, FE5-6). Audiobook op labels and colours become books `taskKinds`
   (`TaskKindPresentation`); the book feed, the `versionApi` undo preflight and `PendingFileOpsBanner` become a books
   `activityPanes` entry in `web/src/modules/books/activity/`. The page, store and grouping move to
   `core/web/packages/shell/src/providers/` (or a sibling the shell's layout expects; keep them under that directory).
2. **`OperationTrackerProvider`**: split into the shell's `TaskTrackerProvider` (follow a run app-wide) and a books
   provider for the merge, combine and purge locks in `web/src/modules/books/activity/`, which keeps
   `utils/followOperation.ts` and `utils/trackedOperations.ts` (books, 07 §7.11).
3. **`AuthContext`, `ToastProvider`, `useAppStore`** → the shell. `AuthContext`'s `* as api` import is rewired to the
   generated core client before the move (07 §7.11); `AuthProvider` calls `/api/core/v1/auth/{status,login,logout,
   setup,bootstrap}` and `/api/core/v1/me`; `usePermission` fails closed on unknown or null (07 §7.9.1).
4. **`lib/storageKeys.ts` split**: the books keys (`itunes_import_settings`, the `library*`, `metadata-review-*` and
   `dedup-*` keys) move to `web/src/modules/books/storageKeys.ts` with their literal values unchanged; the keys of
   shell- and ui-kit-bound files (`activity-source-prefs`, `activity-ops-pinned`, the `table_config` prefix,
   `dismissed_announcements`, `welcome_wizard_completed`, and the literal `ops-indicator-collapse-v1` and
   `settings.showAdvanced` where their files move) become `storageKey()` names; `app-theme-mode` stays as it is for
   F9-03 to pass as `themeStorageKey` (07 §7.7, §7.10). No audiobook key name enters `core/web` (03 R1). The one-time
   reset of those preferences is the spec's, "at shell adoption" (07 §7.11); the F9 cut rows and F9-03 ship in the same
   release (R2, or all in R3), so no release sees a partial reset. Until F9-03 passes the app ID through
   `createShell`, give `storageKey()` ABO's app ID the way F9-01's shell exposes for a non-shell host, and say how in
   the PR.
5. **`components/common`**: `ConfigurableTable`, `BulkConfirmDialog`, `FilterTagBar` → ui-kit (presentational).
   `ServerFileBrowser` is cut first: it takes `{ browse, include?, exclude? }` props instead of calling
   `api.browseFilesystem`, `api.includeFilesystemPath` and `api.excludeFilesystemPath`, the context menu hides without
   `include`/`exclude`, and ui-kit declares its own `BrowseEntry`/`BrowseResult` types, structurally assignable from the
   generated core types. ui-kit imports neither `@mediacore/api-client` nor `@mediacore/shell` (07 §7.3.1).
6. **`ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner`** → ui-kit (presentational) or the shell
   (stateful), per 07 §7.11.
7. **Hooks** `useAsyncAction`, `useKeyboardShortcuts`, `useDebouncedSearch`, `useRowSelection` → ui-kit.
   `useKeyboardShortcuts` names no route: its table comes from the shell, built from shell routes plus each nav entry's
   `goKey` (07 §7.11, §7.4.2; `shortcuts.test.tsx`).
8. **`utils/apiFetch.ts`, `apiResponse.ts`, `permissionError.ts`, `apiKeyExpiry.ts`, `safeReturn.ts`,
   `settingLocks.ts`** → the api-client runtime (`core/web/packages/api-client/src/runtime/`) or the shell
   (`safeReturn` moves to the shell, 07 §7.9.1).

**Old paths.** Each moved file's old path under `web/src/` keeps one re-export line from its new package (for
example `export { AuthProvider, useAuth } from '@mediacore/shell';`), so the 148 importers elsewhere in `web/src` and
`web/tests` at the plan's base stay untouched and file-disjoint from F9-02 and F9-04. F9-03, which owns all of `web/`,
re-points every importer and deletes these re-export files (Planner notes).

## Invariants

- **I1. The cut is proven.** Nothing under `core/web/packages/shell/src/providers/`,
  `core/web/packages/ui-kit/src/components/`, `core/web/packages/ui-kit/src/hooks/` or
  `core/web/packages/api-client/src/runtime/` imports anything outside `core/web` (07 §7.3.1, FE-16); ui-kit imports
  neither `@mediacore/api-client` nor `@mediacore/shell`.
- **I2. Same SPA.** The existing vitest and Playwright suites pass unchanged except for import paths; the Activity
  page shows the same runs, labels, colours and actions.
- **I3. Storage keys.** Every key the moved shell and ui-kit code reads or writes is a `storageKey()` name, and no
  pre-shell key (for example `table_config`, `dismissed_announcements`) is read (`shellStorageKeys.test.tsx`); the
  books keys keep their literal values. The seven reset preferences go into F9-03's release-note list (record them in
  the PR description).
- **I4.** No permission string literal in a permission position (07 §7.9.1's AST rule).
- **I5.** No shell-classified file moves while it still imports audiobook code (07 §7.11).

## Files

| Action | Path |
|---|---|
| modify, then move (old path keeps a re-export line) | `web/src/pages/ActivityLog.tsx`, `web/src/stores/useOperationsStore.ts`, `web/src/stores/operationGrouping.ts` (+ `operationGrouping.test.ts`), `web/src/components/operations/OperationTrackerProvider.tsx`, `web/src/contexts/AuthContext.tsx`, `web/src/components/toast/ToastProvider.tsx`, `web/src/stores/useAppStore.ts`, `web/src/components/common/ConfigurableTable.tsx`, `ServerFileBrowser.tsx`, `BulkConfirmDialog.tsx`, `FilterTagBar.tsx` (+ `FilterTagBar.test.tsx`), `web/src/components/ErrorBoundary.tsx`, `KeyboardShortcutsDialog.tsx`, `AnnouncementBanner.tsx` (+ `AnnouncementBanner.test.tsx`), `web/src/hooks/useAsyncAction.ts`, `useKeyboardShortcuts.ts`, `useDebouncedSearch.ts` (+ `__tests__/useDebouncedSearch.test.ts`), `useRowSelection.ts` (+ `useRowSelection.test.ts`), `web/src/utils/apiFetch.ts`, `apiResponse.ts`, `permissionError.ts`, `apiKeyExpiry.ts`, `safeReturn.ts`, `settingLocks.ts` |
| split | `web/src/lib/storageKeys.ts` (keeps the shell-bound names until F9-03) → `web/src/modules/books/storageKeys.ts` |
| create | `core/web/packages/shell/src/providers/`, `core/web/packages/ui-kit/src/components/`, `core/web/packages/ui-kit/src/hooks/`, `core/web/packages/api-client/src/runtime/` (the moved files and their tests) |
| create | `web/src/modules/books/activity/` (books `taskKinds`, the `activityPanes` entry, the merge/combine/purge provider) |
| modify | `core/web/packages/shell/src/index.ts` (the shell's root entry file, owned by this row: the re-export lines for `providers/`, `layout/` and `pages/`) |
| create | `core/web/packages/shell/src/layout/index.ts`, `core/web/packages/shell/src/pages/index.ts` (empty entry files, `export {};`; F9-02 and F9-04 fill them) |
| modify | `core/web/packages/ui-kit/src/index.ts`, `core/web/packages/api-client/src/index.ts` (one re-export line each for this row's additions) |
| create | one `changelog.d/` fragment (headerless) |

Touch nothing else. `components/common/PathLinks.tsx` with `utils/pathAliases.ts` and `utils/formatPath.ts` stay books
(07 §7.11) and are not this row's; `components/layout/` is F9-02's and the pages are F9-04's.

## Re-verify before editing (expected results at your base)

```bash
cd web
# Every listed file exists at your base. Expected: 30
ls src/pages/ActivityLog.tsx src/stores/useOperationsStore.ts src/stores/operationGrouping.ts src/stores/operationGrouping.test.ts \
   src/components/operations/OperationTrackerProvider.tsx src/contexts/AuthContext.tsx src/components/toast/ToastProvider.tsx \
   src/stores/useAppStore.ts src/lib/storageKeys.ts src/components/common/ConfigurableTable.tsx src/components/common/ServerFileBrowser.tsx \
   src/components/common/BulkConfirmDialog.tsx src/components/common/FilterTagBar.tsx src/components/common/FilterTagBar.test.tsx \
   src/components/ErrorBoundary.tsx src/components/KeyboardShortcutsDialog.tsx src/components/AnnouncementBanner.tsx \
   src/components/AnnouncementBanner.test.tsx src/hooks/useAsyncAction.ts src/hooks/useKeyboardShortcuts.ts src/hooks/useDebouncedSearch.ts \
   src/hooks/__tests__/useDebouncedSearch.test.ts src/hooks/useRowSelection.ts src/hooks/useRowSelection.test.ts src/utils/apiFetch.ts \
   src/utils/apiResponse.ts src/utils/permissionError.ts src/utils/apiKeyExpiry.ts src/utils/safeReturn.ts src/utils/settingLocks.ts | wc -l
# Importers of the moved files elsewhere (plan base: 148 files). They are left untouched (re-export lines).
for b in AuthContext ToastProvider useAppStore storageKeys useOperationsStore operationGrouping OperationTrackerProvider apiFetch apiResponse \
         permissionError apiKeyExpiry safeReturn settingLocks useAsyncAction useKeyboardShortcuts useDebouncedSearch useRowSelection \
         ConfigurableTable ServerFileBrowser BulkConfirmDialog FilterTagBar ErrorBoundary KeyboardShortcutsDialog AnnouncementBanner ActivityLog; do
  grep -rlE "from '([^']*/)?$b'" src tests | grep -vE "/$b\.(ts|tsx)$"; done | sort -u | wc -l
# The audiobook imports to cut. Read them; each must become a slot, a prop or a generated-client call
grep -nE "^import .* from '\.\.?/" src/pages/ActivityLog.tsx src/contexts/AuthContext.tsx src/components/operations/OperationTrackerProvider.tsx \
   src/components/common/ServerFileBrowser.tsx src/stores/useOperationsStore.ts
cd ..
# F9-01's packages. Expected: three package.json files and defineModule
ls core/web/packages/ui-kit/package.json core/web/packages/shell/package.json core/web/packages/api-client/package.json
grep -rn 'export function defineModule' core/web/packages/shell/src/ | wc -l
# Nothing of this PR exists yet. Expected: 0
ls -d core/web/packages/shell/src/providers core/web/packages/ui-kit/src/hooks web/src/modules/books/activity 2>/dev/null | wc -l
```

STOP if a listed file is missing (report which; a flip may have renamed it), or a moved file imports
something 07 §7.11 classifies as neither shell, ui-kit, api-client nor a slot (report the import).

## Steps

1. Take a vitest and Playwright baseline at your base.
2. Write the tests first (below).
3. Cut and move in dependency order: the `apiFetch` group (leaves) → hooks → `components/common`, `ErrorBoundary`,
   `KeyboardShortcutsDialog`, `AnnouncementBanner` → `ToastProvider`, `useAppStore`, `AuthContext` → `storageKeys`
   split → `useOperationsStore`, `operationGrouping`, `OperationTrackerProvider` → the Activity page. For each file: cut
   its audiobook imports into slots, props or generated-client calls; `git mv` it to its package; leave the one-line
   re-export at the old path; run vitest.
4. Write the books side in `web/src/modules/books/activity/` and `web/src/modules/books/storageKeys.ts`.
5. ESLint import boundary in `core/web`, vitest in both workspaces, e2e. Headers, fragment.

## Code contract

Slot and helper types are 07 §7.4.2's (`TaskKindPresentation`, `ActivityPaneContribution`, `storageKey(scope, name)`,
the provider order `ClientProvider` → `AuthProvider` → `RealtimeProvider` → `ToastProvider` → `TaskTrackerProvider`);
copy them from `@mediacore/shell`, never redefine them in ABO. `ServerFileBrowser`'s props are exactly
`{ browse, include?, exclude? }` (07 §7.11).

## Tests (write first)

- The moved tests (`operationGrouping.test.ts`, `FilterTagBar.test.tsx`, `AnnouncementBanner.test.tsx`,
  `useDebouncedSearch.test.ts`, `useRowSelection.test.ts`) pass from their new packages.
- `shellStorageKeys.test.tsx` (07 §7.18, spec name) in the shell: every key the moved shell and ui-kit code reads or
  writes is a `storageKey()` name; no pre-shell key is read.
- `shortcuts.test.tsx` (07 §7.18): ui-kit's `useKeyboardShortcuts` names no route (the shell-side cases run at F9-03
  when nav exists; write the ui-kit half here).
- `auth.flow.test.tsx` (07 §7.18) for the moved `AuthProvider`: session-ending 401 redirects; 401 on login, setup or
  bootstrap shows the error in place; 503 `auth_unavailable` keeps the session.
- `serverFileBrowser.props.test.tsx` (name coined here): with `browse` only, no context menu; with `include`/`exclude`,
  the menu calls them.
- `activityPage.equivalence.test.tsx` (name coined here) in `web/`: the Activity page renders the same runs, labels and
  actions as the baseline.
- `booksStorageKeys.literal.test.ts` (name coined here): every books key in `web/src/modules/books/storageKeys.ts` keeps
  its literal value from your base.

## Acceptance (commands and expected output)

```bash
# The cut is proven. Expected: lint exit 0 and 0 hits
(cd core/web && npm run lint)
grep -rn "web/src\|@abo/" core/web/packages/shell/src/providers/ core/web/packages/ui-kit/src/ core/web/packages/api-client/src/runtime/ | wc -l
grep -rn "@mediacore/api-client\|@mediacore/shell" core/web/packages/ui-kit/src/ | wc -l

# Old paths are one-line re-exports. Expected: each file 1-3 lines (header aside), only export statements
for f in web/src/contexts/AuthContext.tsx web/src/utils/apiFetch.ts web/src/pages/ActivityLog.tsx; do grep -vc '^<!--\|^//\|^$' "$f"; done

# Tests. Expected: green
make -C core web-test
make web-test
make test-e2e

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- The layout (F9-02) and the shell-classified pages (F9-04).
- `main.tsx` on `createShell`, `renderInShell` replacing `renderWithProviders`, the shell realtime client, re-pointing
  the importers and deleting the re-export files, `themeStorageKey`, the `/books/` URLs (F9-03).
- `utils/operationPolling.ts`, `followOperation.ts`, `trackedOperations.ts` (books; R3-02 moves `operationPolling` onto
  `TaskRunView`), `PathLinks` and its utils (books).

## Rollback

Revert the PR; the files return to their old paths.

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web` first; file headers on every created or modified TS file
  (fragments headerless).
- `git mv` for moves; the PR description lists every `old ==> new` path and the seven reset preferences.
- Conventional commit, for example `refactor(web): cut providers, stores and common pieces into the shell (dark)`,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; no release or flip trailer; `gh pr merge <n> --rebase`;
  never squash; never `--admin`.

## Planner notes

- **Importers left on re-export lines.** The 148 importer files of the moved pieces are not in the row, and many are
  F9-02's or F9-04's files. The brief keeps them untouched by leaving a one-line re-export at each old
  path, which F9-03 (owner of all of `web/`) re-points and deletes. If the planner prefers no re-export files, the row
  needs those 148 files.
