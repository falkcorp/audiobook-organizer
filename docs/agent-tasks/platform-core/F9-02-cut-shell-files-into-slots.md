<!-- file: docs/agent-tasks/platform-core/F9-02-cut-shell-files-into-slots.md -->
<!-- version: 1.1.1 -->
<!-- guid: b8dc84ce-aaac-40d9-a6cf-4b8ab601ecf3 -->
<!-- last-edited: 2026-11-05 -->

# F9-02 — Cut the audiobook imports out of the shell-classified layout files into books slots under web/src/modules/books/ and move the layout into core/web, dark

| Field | Value |
|---|---|
| ID | F9-02 |
| Release | R2, flip label F9 (dark PR; R2 if the shell is ready, else R3) |
| Tier | **T3** (Opus / high): a cut of shell-classified files into slots; the import boundary must prove the cut and the SPA must render the same |
| Wave | W38 (beside F9-04; file-disjoint) |
| Depends on | F9-01, F8-04 (F4-08 through F8-04 edit the `web/src/` layout files this row moves), F9-05 (see Goal) |
| Spec | 07 §7.11 (the classification table, rows `components/layout/{MainLayout,TopBar,UserMenu,sidebarSelection}.tsx, Sidebar.tsx minus its arrays` and `pages/ActivityLog.tsx … components/layout/OperationsIndicator.tsx`), §7.3.1 (import boundary), §7.4.2 (slot contributions: `nav`, `banners`, `topBarItems`, `activityPanes`, `taskKinds`), §7.19 step 4 ("cut the audiobook imports out of the shell-classified files into books slots and move them into `core/web`") |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's SPA looks and behaves the
same after this PR: same nav entries and order, same banners, same top-bar items, same URLs (the `/books/` paths
are F9-03's). ABO still boots on `<BrowserRouter>`; it now renders the moved layout from `@mediacore/shell` and fills
its slots from `web/src/modules/books/layout/`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f9-02-shell-cut" -b agent/platform-core-f9-02-shell-cut origin/main
cd "$REPO/.worktrees/pc-f9-02-shell-cut"
git rebase origin/main
npm ci --prefix web
```

## Precondition: the R2 flips have merged

07 §7.19 step 4: "Preconditions: steps 1–2 and every R2 flip whose native routes the shell pages call". The layout
calls the identity routes (`UserMenu`: F4-08), the tasks routes (`OperationsIndicator`: F5-18) and the stream
(`TopBar`: F6-03), and those flips edit these files under `web/src/`. `Depends on` F8-04 puts all of them in your base,
so there is nothing to check here.

## Goal

For each shell-classified file under `web/src/components/layout/` (07 §7.11):

1. Replace every import of audiobook code with a slot the module fills:
   - `MainLayout`: `ReviewBanner` and `usePendingFileOps` become books `banners` (07 §7.11);
   - `Sidebar`: its arrays `librarySubItems` and `menuItems` become books `nav` (with children) plus shell entries;
     the Review badge becomes `useBadge: useReviewCount` with
     `const useReviewCount = () => useReviewStore(s => s.count)` (FE-22);
   - `OperationsIndicator`: it stops importing `operationPolling` and `services/api`/`versionApi`; it gets shell
     state helpers over the generated `TaskRunView` states (FE5-6); audiobook op labels and colours become books
     `taskKinds`; the undo preflight becomes a books `activityPanes`/row action as §7.11 says;
   - `TopBar`, `UserMenu`, `sidebarSelection`: their ABO imports (`services/api`, `AuthContext`, the realtime
     client) are replaced by shell services or by the generated core client.
2. Move the cut files into `core/web/packages/shell/src/layout/` (layout components), with their tests.
3. Put the books side of each slot in `web/src/modules/books/layout/` (07 §7.11: books' UI lives under
   `web/src/modules/books/`), so F9-03 does not move them again.
4. ABO's `App` renders the shell's layout with those slot contributions, still under `<BrowserRouter>`. Each moved
   file's old path under `web/src/components/layout/` keeps one re-export line from `@mediacore/shell` (binding the
   books contributions where a component takes them), so `App.tsx` and the other importers stay untouched; F9-03
   re-points them and deletes the re-export files.

**Order with F9-05.** `OperationsIndicator`, `operationsFormat` and their tests import `stores/useOperationsStore` and
`stores/operationGrouping`, and `UserMenu` imports `contexts/AuthContext`; F9-05 moves those three into the
shell. A layout file cannot move into `core/web` while it imports them from `web/src` (I1), so this row depends on
F9-05 and imports them from their shell location. Do not move or edit F9-05's files.

**Scope.** Only the layout files above. The shell-classified pages (`Login`, `AcceptInvite`, `Setup`, `NotFound`,
`Users`, `System` and its tabs, the `Settings` frame, `Dashboard`, `Diagnostics`, the `WelcomeWizard` frame) are
F9-04's; the providers, stores and presentational pieces (`ActivityLog` with `useOperationsStore`, `operationGrouping`,
`OperationTrackerProvider`, `AuthContext`, `ToastProvider`, `useAppStore`, the `storageKeys` split,
`components/common`, `ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner`, the four ui-kit hooks, the
`apiFetch` group) are F9-05's (07 §7.19 step 4: all of them are cut and moved before `main.tsx` switches at F9-03).

## Invariants

- **I1.** The `core/web` import boundary passes: nothing under `core/web/packages/shell/src/` imports anything
  outside `core/web` (07 §7.3.1, FE-16). This is the proof of the cut.
- **I2.** The SPA renders the same: same nav labels, order, icons, badges, banners and top-bar items; the existing
  vitest and the 39 Playwright specs pass unchanged except for import paths.
- **I3.** No new storage key is read or written by the moved files that the shell's `storageKey()` rule would
  forbid later; the literal keys `ops-indicator-collapse-v1` and the layout keys move with their files (07 §7.11
  `lib/storageKeys.ts` row).
- **I4.** No shell-classified file moves while it still imports audiobook code.

## Files

| Action | Path |
|---|---|
| modify, then move | `web/src/components/layout/` (`MainLayout`, `Sidebar` minus its arrays, `TopBar`, `UserMenu`, `sidebarSelection`, `OperationsIndicator`, `operationsFormat` and their tests) |
| create | `web/src/modules/books/layout/` (books `nav`, `banners`, `topBarItems`, `taskKinds`, `activityPanes` contributions and the `useReviewCount` hook) |
| create | `core/web/packages/shell/src/layout/` (the moved layout files; their tests) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# The layout files. [evidence checkout: MainLayout, Sidebar, TopBar, UserMenu, sidebarSelection, OperationsIndicator,
#  operationsFormat + tests]
ls web/src/components/layout/
# Their audiobook imports at your base (F5 and F6 changed some). Expected at the evidence checkout:
#  MainLayout -> ../ReviewBanner, ../../hooks/usePendingFileOps; Sidebar -> ../../stores/useReviewStore;
#  TopBar -> ../../services/eventSourceManager; UserMenu -> ../../contexts/AuthContext, ../../services/api;
#  OperationsIndicator -> ../../stores/useOperationsStore, operationGrouping, ../../utils/operationPolling,
#  ../../services/api, ../../services/versionApi; operationsFormat -> ../../stores/useOperationsStore
grep -n "^import" web/src/components/layout/*.ts* | grep -v -e "@mui" -e "'react'" -e "react-router" -e "from './"
# F9-05's moves are in your base (see Goal, "Order with F9-05"). Expected: 0 (the three files have left web/src)
ls web/src/stores/useOperationsStore.ts web/src/stores/operationGrouping.ts web/src/contexts/AuthContext.tsx 2>/dev/null | wc -l
# Nothing of this PR exists yet; F9-05 left only the empty entry file. Expected: 0, then 1
ls -d web/src/modules/books/layout 2>/dev/null | wc -l
ls core/web/packages/shell/src/layout/ | wc -l
# The shell package exists (F9-01) and exports slot types. Expected: defineModule and the slot interfaces
grep -rn 'export function defineModule\|export interface BannerContribution\|export interface NavContribution' core/web/packages/shell/src/
# The import-boundary lint runs. Expected: a lint script in core/web
grep -n '"lint' core/web/package.json core/web/packages/shell/package.json
```

STOP if `defineModule` or the slot interfaces are missing (F9-01), or if a layout file imports something whose classification in 07 §7.11 is not shell,
books or ui-kit (report the import).

## Steps

1. Write the tests first: move the existing layout tests and add a slot-equivalence test (below).
2. For each file, in this order: `sidebarSelection` (no ABO imports), `UserMenu`, `TopBar`, `Sidebar`, `MainLayout`,
   `operationsFormat`, `OperationsIndicator`. Cut its audiobook imports into slot props or shell services; keep
   ABO-visible output identical.
3. Write the books contributions in `web/src/modules/books/layout/`.
4. Move each cut file into `core/web/packages/shell/src/layout/` with `git mv` (history), fix imports, and export them
   from `core/web/packages/shell/src/layout/index.ts` (F9-05 created it empty and already re-exports it from the
   shell's root `src/index.ts`; do not edit the root file).
5. Leave the one-line re-export at each old path; ABO's `App` keeps importing the old paths.
6. Run the import-boundary lint, vitest in both workspaces, the e2e suite. Headers, fragment.

## Code contract

Slot types are 07 §7.4.2's (`NavContribution`, `BannerContribution`, `TopBarContribution`,
`ActivityPaneContribution`, `TaskKindPresentation`); copy them, do not redefine them in ABO. The badge hook is
exactly:

```ts
const useReviewCount = () => useReviewStore(s => s.count);
```

## Tests (write first)

- The moved layout tests (`Sidebar.test.tsx`, `sidebarSelection.test.ts`, `operationsFormat.test.ts`, the seven
  `OperationsIndicator.*.test.tsx` files) pass from `core/web/packages/shell/src/layout/` with shell fakes instead of
  ABO stores.
- `layoutSlots.equivalence.test.tsx` (name coined here) in `web/`: ABO's app renders the same nav labels and order,
  the same banners and top-bar items as before the PR (snapshot taken at your base before the first edit).
- `nav.permissions.test.tsx` (07 §7.18, F9-01's) still passes with books' nav entries.

## Acceptance (commands and expected output)

```bash
# The cut is proven: no ABO import inside the shell. Expected: lint exit 0
(cd core/web && npm run lint)
grep -rn "web/src\|@abo/" core/web/packages/shell/src/layout/ | wc -l   # expected: 0

# Only one-line re-exports left at the old layout paths. Expected: no file with more than 3 non-comment lines
for f in web/src/components/layout/*.ts*; do n=$(grep -vc '^//\|^$' "$f"); [ "$n" -gt 3 ] && echo "$f $n"; done

# Tests. Expected: green
make -C core web-test
make web-test
make test-e2e

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- `main.tsx` on `createShell`, the data router, `renderInShell`, the realtime client swap, the `/books/` URLs
  (F9-03).
- The shell-classified pages (F9-04) and providers, stores and presentational pieces (F9-05).

## Rollback

Revert the PR: the files return to `web/src/components/layout/`.

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web` first; headers on every new or changed TS file.
- `git mv` for moves; the PR description lists every `old ==> new` path.
- Conventional commit, for example `refactor(web): cut layout into shell slots and move it into core/web`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
