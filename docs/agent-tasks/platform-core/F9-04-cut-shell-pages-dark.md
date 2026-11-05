<!-- file: docs/agent-tasks/platform-core/F9-04-cut-shell-pages-dark.md -->
<!-- version: 1.0.4 -->
<!-- guid: 7e3b5f23-bec9-40f3-bf21-45877bf529e2 -->
<!-- last-edited: 2026-11-05 -->

# F9-04 — Cut the shell-classified pages, dark: Login, AcceptInvite, Setup, NotFound, Users, System with its Storage, Quota and SystemInfo tabs and the split MaintenanceTab, the Settings frame, Dashboard, Diagnostics and the WelcomeWizard frame; their audiobook parts become books widgets, setup and userPageExtensions

| Field | Value |
|---|---|
| ID | F9-04 |
| Release | R3 (dark; merges after OWNER-05), flip label F9; the shell ships in R3 by default (plan §(l) item 7) |
| Tier | **T3** (Opus / high): a cut of shell-classified pages into the shell and books' slots; the import boundary must prove the cut and the SPA must render the same |
| Wave | W42 (beside F9-02, R3-05; file-disjoint) |
| Depends on | F9-01 (the `shell` and `ui-kit` packages), F8-04 (F4-08 through F8-04 edit these pages under `web/src/`), F9-05 (the pages import its providers and utils) |
| Precondition | 07 §7.19 step 4: every R2 flip whose native routes the pages call has merged (enforced by `Depends on`: F8-04) |
| Spec | 07 §7.11 (the classification table: the `pages/Login.tsx …`, `pages/Users.tsx`, `pages/System.tsx`, `components/system/MaintenanceTab.tsx`, `pages/Settings.tsx`, `pages/Dashboard.tsx`, `pages/Diagnostics.tsx` and `components/wizard/WelcomeWizard.tsx` rows), §7.4.2 (`WidgetContribution`, `SetupContribution`, `UserPageExtensions`, `SettingsPanelContribution`), §7.4.3 (shell routes), §7.9.1 (permission atoms; `/setup` and the first-run gate order), §7.3.1 (import boundary), §7.19 step 4 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's SPA looks and behaves the
same after this PR: same pages at the same URLs, same tabs, same widgets. ABO still boots on `<BrowserRouter>` with its
own `App.tsx`, which renders the moved pages from `@mediacore/shell` through their old paths (below), with the books
parts passed in as slot contributions. `main.tsx` on `createShell` and the new page paths (`/`, `/system/diagnostics`,
`/books/...`) are F9-03's.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f9-04-shell-pages" -b agent/platform-core-f9-04-shell-pages origin/main
cd "$REPO/.worktrees/pc-f9-04-shell-pages"
git rebase origin/main
npm ci --prefix web
```

## Precondition: the R2 flips and F9-05 have merged

07 §7.19 step 4: "Preconditions: steps 1–2 and every R2 flip whose native routes the shell pages call". These pages
call the identity routes (Login, AcceptInvite, Setup, Users: F4-08), the tasks routes (Dashboard, MaintenanceTab's
shell half: F5-18), the integrations routes (the Settings frame's generic tabs: F7-09) and the system, backup, update
and health routes (System and its tabs: F8-04), and those flips edit these files under `web/src/`. The pages also import
F9-05's providers and utils (`AuthContext`, `apiFetch`, `apiResponse`, `safeReturn`, `ServerFileBrowser`,
`AnnouncementBanner`, `useOperationsStore`, `storageKeys`), which must be in the shell before a page can move.
`Depends on` F8-04 and F9-05 puts the flips and those pieces in your base, so there is nothing to check here.

## Goal (07 §7.11, one row per page)

| Page | Shell destination | Books part (slot) |
|---|---|---|
| `pages/Login.tsx`, `AcceptInvite.tsx`, `Setup.tsx`, `NotFound.tsx` | shell | none |
| `pages/Users.tsx` | shell `/users`; permissions and titles from `GET /api/core/v1/permissions` (07 §7.9.1) | `GuestPhoneKeys` and `UserMergeDialog` become books `userPageExtensions` |
| `pages/System.tsx` with `components/system/StorageTab.tsx`, `QuotaTab.tsx`, `SystemInfoTab.tsx` | shell `/system` on the native routes of F8 (`/system`, `/backup`, `/update`, `/health`) | `ChapterConsolidationCard`, `ManualFixesCard` become books `widgets` (placement `system`) |
| `components/system/MaintenanceTab.tsx` (split, FE8-6) | the maintenance-window and registered-task controls → shell `/system`, over the native tasks routes | the hash, AcoustID, chapter and duplicate jobs (through `import * as api`) → a books `widgets` entry (placement `system`), keeping its legacy calls; `ThrottlePanel` is a books widget too (07 §7.11) |
| `pages/Settings.tsx` | shell `/settings` frame | Library, iTunes Import, Metadata, Dedup, Paths, OpenLibraryDumps, BlockedHashes, Embedding → books `settingsPanels` |
| `pages/Dashboard.tsx` | shell `/` home | library counts and the scan trigger → a books widget (placement `home`) |
| `pages/Diagnostics.tsx` | shell `/system/diagnostics` frame | AI-dedup diagnostics → a books widget |
| `components/wizard/WelcomeWizard.tsx` | shell first-run frame | books `setup`; the gate `config.root_dir \|\| config.setup_complete` (`App.tsx` at the plan's base) becomes books' `setup.isRequired` |

Moved pages go to `core/web/packages/shell/src/pages/`; the books contributions to `web/src/modules/books/pages/`.
A shell page calls only the generated core client (`useApi()`), the shell's hooks and ui-kit; a call to a books route
is a slot's job. **Old paths:** each moved file's old path keeps one re-export line from `@mediacore/shell`, so its 19
importers elsewhere in `web/src` and `web/tests` at the plan's base (`App.tsx` and the tests among them) stay untouched;
F9-03 re-points them and deletes the re-export files.

## Invariants

- **I1. The cut is proven.** Nothing under `core/web/packages/shell/src/pages/` imports anything outside `core/web`
  (07 §7.3.1, FE-16).
- **I2. Same SPA.** Same pages, tabs, widgets and URLs; the existing vitest and the Playwright specs pass unchanged
  except for import paths.
- **I3. No permission literal.** The Users page reads permissions from `GET /api/core/v1/permissions`; no string literal
  sits in a permission position (07 §7.9.1's AST rule).
- **I4. Gate order kept.** `/setup` and the first-run wizard keep today's gate order (07 §7.9.1); the wizard shows
  exactly when books' `setup.isRequired()` is true.
- **I5.** No shell-classified page moves while it still imports audiobook code (07 §7.11).

## Files

| Action | Path |
|---|---|
| modify, then move (old path keeps a re-export line) | `web/src/pages/Login.tsx`, `AcceptInvite.tsx`, `Setup.tsx`, `NotFound.tsx`, `Users.tsx`, `System.tsx`, `Settings.tsx`, `Dashboard.tsx`, `Diagnostics.tsx`; `web/src/components/system/StorageTab.tsx`, `QuotaTab.tsx`, `SystemInfoTab.tsx`, `MaintenanceTab.tsx`; `web/src/components/wizard/WelcomeWizard.tsx` |
| create | `core/web/packages/shell/src/pages/` (the moved pages and frames, their tests) |
| create | `web/src/modules/books/pages/` (books `widgets`, `settingsPanels`, `setup` and `userPageExtensions` contributions; the books half of `MaintenanceTab`) |
| create | one `changelog.d/` fragment (headerless) |

The books components the slots wrap (`GuestPhoneKeys`, `UserMerge`, `ChapterConsolidationCard`, `ManualFixesCard`,
`ThrottlePanel`, the books settings tabs, `AIJobsPanel`, `CacheStatsPanel`, `ToolsPanel`) stay where they are; the
contributions import them. The Settings frame's generic tabs (Security, API Keys, Plugins → Integrations, AI backends,
Scheduled tasks, Performance, Temp Login, System Info) are classified shell by 07 §7.11 but are not in this row's files
(Planner notes): the frame takes them as panels from ABO in this PR.

## Re-verify before editing (expected results at your base)

```bash
cd web
# The 14 files exist. Expected: 14
ls src/pages/Login.tsx src/pages/AcceptInvite.tsx src/pages/Setup.tsx src/pages/NotFound.tsx src/pages/Users.tsx src/pages/System.tsx \
   src/pages/Settings.tsx src/pages/Dashboard.tsx src/pages/Diagnostics.tsx src/components/system/StorageTab.tsx \
   src/components/system/QuotaTab.tsx src/components/system/SystemInfoTab.tsx src/components/system/MaintenanceTab.tsx \
   src/components/wizard/WelcomeWizard.tsx | wc -l
# Their relative imports: the audiobook parts to cut and F9-05's pieces (now re-export lines). Read every hit
for f in src/pages/{Login,AcceptInvite,Setup,NotFound,Users,System,Settings,Dashboard,Diagnostics}.tsx \
         src/components/system/{StorageTab,QuotaTab,SystemInfoTab,MaintenanceTab}.tsx src/components/wizard/WelcomeWizard.tsx; do
  echo "== $f"; grep -E "from '\.\.?/" "$f" | sed -E "s/.*from '([^']+)'.*/\1/"; done
# Importers of the 14 files elsewhere (plan base: 19). Left untouched (re-export lines)
for b in Login AcceptInvite Setup NotFound Users System Settings Dashboard Diagnostics StorageTab QuotaTab SystemInfoTab MaintenanceTab WelcomeWizard; do
  grep -rlE "from '([^']*/)?$b'" src tests | grep -vE "/$b\.(ts|tsx)$"; done | sort -u | wc -l
cd ..
# Slot types in the shell (F9-01). Expected: >= 4
grep -rn 'export interface \(WidgetContribution\|SetupContribution\|UserPageExtensions\|SettingsPanelContribution\)' core/web/packages/shell/src/ | wc -l
# Nothing of this PR exists yet; F9-05 left only the empty entry file. Expected: 0, then 1
ls -d web/src/modules/books/pages 2>/dev/null | wc -l
ls core/web/packages/shell/src/pages/ | wc -l
```

STOP if a listed file is missing, a page still calls a core route by a HEAD path that its flip should
have switched (a flip left a caller: report it, do not switch it here), or a page imports something 07 §7.11
classifies as neither shell, ui-kit nor a slot.

## Steps

1. Take a vitest and Playwright baseline, and a snapshot of each page's rendered tabs and widgets.
2. Write the tests first (below).
3. Cut and move in this order: `NotFound`, `Setup`, `AcceptInvite`, `Login` (no books part) → `Users` (books
   `userPageExtensions`) → `StorageTab`, `QuotaTab`, `SystemInfoTab`, then `MaintenanceTab`'s split, then `System`
   (books `widgets`, placement `system`) → `Diagnostics`, `Dashboard` (books widgets) → `WelcomeWizard` (books `setup`)
   → the `Settings` frame (books `settingsPanels`; the generic tabs passed in as panels). For each: cut the books part
   into its contribution in `web/src/modules/books/pages/`, `git mv` the page into
   `core/web/packages/shell/src/pages/`, leave the one-line re-export at the old path, export it from `core/web/packages/shell/src/pages/index.ts` (F9-05
   created it and already re-exports it from the shell's root `src/index.ts`; do not edit the root file), run vitest.
4. ABO's existing `App.tsx` keeps importing the old paths; pass the books contributions where the moved pages take
   them (through the old-path re-export module, which may also export a thin ABO wrapper that binds the contributions;
   name the pattern in the PR).
5. ESLint import boundary in `core/web`, vitest in both workspaces, e2e. Headers, fragment.

## Code contract

Slot types are 07 §7.4.2's (`WidgetContribution` with its placement, `SetupContribution` with `isRequired` and
`steps`, `UserPageExtensions`, `SettingsPanelContribution`); copy them from `@mediacore/shell`, never redefine them in
ABO. Books' `setup.isRequired` is exactly the gate `!config.root_dir && !config.setup_complete` (07 §7.4.2 comment on
`SetupContribution`).

## Tests (write first)

- The pages' existing tests move with them and pass from `core/web/packages/shell/src/pages/` with shell fakes.
- `nav.permissions.test.tsx` (07 §7.18, F9-01's) still passes.
- `TestInviteLink_FragmentEndToEnd` and `TestTempLoginLink_FragmentEndToEnd` (07 §7.18; ABO e2e) still pass on the
  moved `AcceptInvite` and `Login`.
- `shellPages.equivalence.test.tsx` (name coined here) in `web/`: each moved page renders the same tabs, widgets and
  actions as the step-1 snapshot.
- `booksSetup.isRequired.test.ts` (name coined here): the wizard shows exactly when `!config.root_dir &&
  !config.setup_complete`.
- `maintenanceSplit.test.tsx` (name coined here): the shell half shows only the maintenance-window and registered-task
  controls; the books widget shows the hash, AcoustID, chapter and duplicate jobs.

## Acceptance (commands and expected output)

```bash
# The cut is proven. Expected: lint exit 0 and 0 hits
(cd core/web && npm run lint)
grep -rn "web/src\|@abo/" core/web/packages/shell/src/pages/ | wc -l

# No permission literal in a permission position. Expected: lint exit 0
(cd web && npm run lint)

# Tests. Expected: green
make -C core web-test
make web-test
make test-e2e

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- The layout (F9-02); the providers, stores, common pieces, hooks and `apiFetch` group (F9-05).
- `main.tsx` on `createShell`, the data router, `renderInShell`, the shell realtime client, re-pointing the importers
  and deleting the re-export files, the new page paths and the release-note list of moved paths (F9-03).
- `pages/FileBrowser.tsx` (books page `/books/files`), `pages/UserMerge.tsx` (books row action), every books page.

## Rollback

Revert the PR; the pages return to their old paths.

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web` first; file headers on every created or modified TS file
  (fragments headerless).
- `git mv` for moves; the PR description lists every `old ==> new` path.
- Conventional commit, for example `refactor(web): cut the shell pages into core/web with books slots (dark)`, ending
  with `Co-Authored-By: Claude <noreply@anthropic.com>`; no release or flip trailer; `gh pr merge <n> --rebase`;
  never squash; never `--admin`.

## Planner notes

- **Settings' generic tabs have no row.** 07 §7.11 classifies the Settings frame's generic tabs (Security, API Keys,
  Plugins → Integrations, AI backends, Scheduled tasks, Performance, Temp Login, System Info) as shell, and 07 §7.19
  step 4 has every shell-classified file cut before `main.tsx` switches, but no row lists their files
  (`web/src/components/settings/…`). This brief passes them into the frame from ABO; a row (this one or F9-03) must
  move them.
- **Importers left on re-export lines.** The 19 importers of these pages (`App.tsx` and tests) stay untouched; F9-03
  re-points them and deletes the re-export files. If the planner prefers no re-export files, the row needs them.
