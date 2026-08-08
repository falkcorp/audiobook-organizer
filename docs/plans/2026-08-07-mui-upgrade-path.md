<!-- file: docs/plans/2026-08-07-mui-upgrade-path.md -->
<!-- version: 1.0.0 -->
<!-- guid: ed313f54-6818-4e26-b6ea-282b5cc2156e -->
<!-- last-edited: 2026-08-07 -->

# MUI 5.14 → 9.x Upgrade Path Evaluation

**Status:** Planning deliverable — nothing is upgraded by this document.
**Companion task briefs:** five `todo.d/` fragments (TODO-MUI-0 … TODO-MUI-4)
land in `TODO.md` alongside this doc. Each is a self-contained agent brief; do
**one major per session/PR**, in order.

**Headline finding: there is no Material UI v8.** Material UI jumped from v7
straight to v9 to re-align its major version with MUI X (announced in
[Introducing Material UI and MUI X v9](https://mui.com/blog/introducing-mui-v9/)).
The real path is therefore **5 → 6 → 7 → 9**, three upgrade PRs plus an
optional React-19 PR, not four.

**Second headline finding: React 19 is NOT a hard prerequisite for MUI v9.**
`@mui/material@9` peer-depends on React `^17.0.0 || ^18.0.0 || ^19.0.0`. On
React 18 you must pin a `react-is` override matching your React version
(MUI ships `react-is@19` internally, which changes element identification and
causes runtime prop-type errors on 18 without the override). We still
recommend the React 19 hop before v9 — see "Recommended order".

---

## Part 1 — Measured usage inventory (2026-08-07, `web/src`)

Measured on this repo, not assumed. All commands run from `web/src`.

### Packages and versions

| Package | Version | Notes |
| --- | --- | --- |
| `@mui/material` | ^5.14.20 | only MUI runtime package |
| `@mui/icons-material` | ^5.14.19 | versions in lockstep with material |
| `@emotion/react` / `@emotion/styled` | ^11.11.x | default engine; stays through v9 |
| `@mui/lab` | **not used** | ✅ removes a whole class of migration work |
| `@mui/x-*` (DataGrid, pickers) | **not used** | ✅ no lockstep gate on the upgrade |
| `react` / `react-dom` | ^18.2.0 | React 19 optional for v9 (see above) |
| `typescript` | ^6.0.3 | far above every major's TS floor (4.7 / 4.9) |
| `vite` | ^7.3.6 | current; no legacy-bundler concerns |

### Import surface

| Metric | Count | Command |
| --- | --- | --- |
| Files importing `@mui/*` | **142** | `grep -rl "@mui/" web/src \| wc -l` |
| Named barrel imports `from '@mui/material'` | 141 files | dominant style — codemod-friendly |
| 1-level path imports `@mui/material/X` | 4 total | `styles`, `Rating`, `IconButton`, `Collapse` |
| Deep (>1-level) imports `@mui/material/X/Y` | **0** | ✅ v7 removal of deep imports is a no-op |
| Icon barrel imports `from '@mui/icons-material'` | 37 files | |
| Icon path imports **with `.js` suffix** (`@mui/icons-material/Refresh.js`) | **159** | ⚠️ risk vs v7 package-layout/ESM change |
| Icon path imports without suffix | ~120 | mixed style throughout |
| Distinct `@mui/material` components used | **91** | |

Top components (files importing each): Box 124, Typography 117, Button 91,
Stack 78, Chip 77, Alert 61, CircularProgress 59, TextField 58, Tooltip 56,
IconButton 55, Paper 53, Dialog cluster 40–43, Divider 37, Checkbox 30,
Table cluster 26–28, MenuItem 27, LinearProgress 23, Collapse 23, Grid 22,
Switch/List 20.

### Styling and theme

| Metric | Count | Implication |
| --- | --- | --- |
| `sx=` usages | **1,726** (131 files) | sx survives every major; `v6.0.0/sx-prop` codemod exists if needed |
| `styled(` calls | **0** | ✅ `v6.0.0/styled` codemod is a no-op |
| `createTheme` | 1 (`web/src/theme.ts`) | single small theme; `v6.0.0/theme-v6` trivial |
| `ThemeProvider` | 3 (`main.tsx`, `App.test.tsx`, `test/renderWithProviders.tsx`) | |
| Theme augmentation `.d.ts` | **0** | ✅ no module-augmentation churn |
| `useTheme` | 6 | |
| CSS theme variables (`CssVarsProvider`) | not used | v7 "mode no longer switches under cssVariables" change is a no-op |

### Deprecated-in-5 blockers

| Pattern | Count | Verdict |
| --- | --- | --- |
| `Hidden` | 0 | ✅ (removed in v7) |
| `makeStyles` / `withStyles` / `@mui/styles` / JSS | 0 | ✅ no JSS anywhere |
| Deep imports `@mui/material/X/Y` | 0 | ✅ |

### Grid (the biggest single item)

| Metric | Count |
| --- | --- |
| `<Grid item` | **175** |
| `<Grid container` | **35** |
| Files containing `<Grid` | **23** |
| `Grid2` / `Unstable_Grid2` | 0 (pure legacy Grid) |

### v9-relevant deprecated props (measured now so the v9 brief is concrete)

| Pattern | Count | v9 impact |
| --- | --- | --- |
| System props on `Box/Stack/Typography/Grid/Link` (`mt=`, `p=`, `color=`, `display=`, … as direct props, excluding sx) | **~381** | removed in v9 → sx / codemod |
| `<Typography color=…>` | 405 | palette-color values stay valid as a regular prop from v6; custom CSS values must move to `sx` |
| `InputProps` (capital I, deprecated) | 24 | → `slotProps.input` (removed in v9) |
| `inputProps` (lowercase, htmlInput) | 45 | → `slotProps.htmlInput` |
| `componentsProps` | 4 | → `slotProps` |
| `TransitionComponent` | 0 | ✅ |
| `<ListItem button` | 0 | ✅ v6 list-item codemod not needed |

### Heaviest pages (for the manual smoke list)

Ranked by `sx=` density and component spread:

1. **Library** (`web/src/pages/Library.tsx` + `BookGrid`, `FilterPanel`) — the main page
2. **Book Detail** (`web/src/pages/BookDetail.tsx` + `bookdetail/BookDetailVersionGroup.tsx`, `MetadataReviewDialog.tsx` 76 sx)
3. **Activity Log** (`web/src/pages/ActivityLog.tsx`, 74 sx)
4. **System → Maintenance tab** (`web/src/components/system/MaintenanceTab.tsx`, 64 sx)
5. **Dedup** (`web/src/pages/BookDedup.tsx` + `dedup/Dedup{Embedding,Author,Acoustic,Series}Tab.tsx`, 38–46 sx each)

(6th, optional: Settings → iTunes Import, `web/src/components/settings/ITunesImport.tsx`, 40 sx.)

---

## Part 2 — Per-major mapping, given the measured usage

### Step A — v5.14 → v6 ([guide](https://mui.com/material-ui/migration/upgrade-to-v6/))

What actually applies here:

- **Grid: nothing to do yet.** Legacy Grid remains available (deprecated) in
  v6. Do NOT run `v6.0.0/grid-v2-props` — that codemod targets Grid2 users;
  we have zero Grid2. We convert once, in the v7 step.
- **`react-is` resolution required** on React 18 — add an
  `overrides: { "react-is": "^18.2.0" }` to `web/package.json` (the lockfile
  currently resolves multiple `react-is` copies).
- **Typography `color` is no longer a system prop** — palette-token values
  (405 usages) keep working as a regular prop; audit for non-palette CSS
  values only.
- **Ripple rework breaks interaction tests** — `fireEvent` clicks on
  Button/Checkbox/Chip/Radio/Switch/Tabs may need `await act(async () => …)`.
  With Vitest + heavy dialog/table UI, expect a handful of test fixes. This
  is the main v6 cost.
- **Codemods** (cheap here — styled()=0, one theme file):
  - `npx @mui/codemod@latest v6.0.0/sx-prop web/src`
  - `npx @mui/codemod@latest v6.0.0/theme-v6 web/src/theme.ts`
  - `v6.0.0/list-item-button-prop` — **skip**, 0 usages measured.
- **Pigment CSS: do NOT adopt.** Optional in v6, Emotion stays default. Zero
  benefit for an embedded SPA, real migration cost.
- Floors: TS ≥ 4.7 (we ship 6.0), Node ≥ 14 — all satisfied.

### Step B — v6 → v7 ([guide](https://mui.com/material-ui/migration/upgrade-to-v7/))

- **Grid conversion — the big one.** In v7 the old Grid is renamed
  `GridLegacy` and Grid2 takes the `Grid` name. Two options; we choose
  **convert now** (converting once beats renaming to `GridLegacy` and paying
  again at v9, where `GridLegacy` is removed):
  - `npx @mui/codemod@latest v7.0.0/grid-props web/src`
  - Converts `item xs={12} sm={6}` → `size={{ xs: 12, sm: 6 }}`, drops
    `item`, `xs={true}` → `size="grow"`. 175 `item` / 35 `container` across
    23 files.
  - **Hand-verify layout**: new Grid uses CSS `gap` for spacing (not
    item padding) and containers no longer stretch full-width by default.
    Visual diffs are plausible on all 23 files — smoke every heavy page.
- **Package layout / ESM change**: deep imports >1 level are removed (0 here,
  fine), but our **159 `.js`-suffixed icon path imports**
  (`@mui/icons-material/Refresh.js`) resolve against the *old* file layout.
  Under v7's new exports map these may fail at build. Verify with
  `npm run build`; if broken, normalize all icon imports to suffix-free
  1-level paths first (mechanical sed sweep, see brief).
- `npx @mui/codemod@latest v7.0.0/input-label-size-normal-medium web/src`
  (cheap, idempotent).
- Removed APIs (`Hidden`, `createMuiTheme`, `onBackdropClick`, lab
  re-exports): **all 0 usages** — no-ops.
- CSS-vars mode-switch behavior change: no-op, we don't use `CssVarsProvider`.
- TS floor 4.9 — satisfied.

### Step C (optional but recommended) — React 18 → 19

- **Not required by MUI v9** (peer `^17 || ^18 || ^19`), but doing it before
  v9 (a) deletes the `react-is` override hack, (b) matches the combination
  MUI itself tests first-class ([How we migrated MUI X to React 19](https://mui.com/blog/react-19-update/)),
  and (c) pre-positions us for the post-v9 major, which plans a styling-layer
  refactor.
- Own PR, own smoke pass: `react@19 react-dom@19 @types/react@19
  @types/react-dom@19`, `npx codemod@latest react/19/migration-recipe`,
  fix `ReactDOM.render`/`test-utils` `act` imports, function-component
  `defaultProps` removal, `useRef()` now requires an argument.
- MUI v7 supports React 19, so this slots cleanly between v7 and v9.

### Step D — v7 → v9 ([guide](https://mui.com/material-ui/migration/upgrade-to-v9/))

There is **no v8**; this is one hop. What applies here:

- **System props removed** from Box, Stack, Typography, Grid, Link,
  DialogContentText: **~381 measured usages** → run the v9 system-props
  codemod (listed in the guide; confirm the exact name from the migration
  page or `npx @mui/codemod@latest --help` at execution time), then
  hand-review — `<Box mt={2} color="primary.main">` becomes
  `<Box sx={{ mt: 2, color: 'primary.main' }}>`. This is the largest v9
  mechanical surface in this repo.
- **Slot-prop standardization**: deprecated `InputProps` (24),
  `componentsProps` (4) removed → `slots`/`slotProps`. Per-component
  `deprecations/*` codemods exist (e.g. `npx @mui/codemod@latest
  deprecations/accordion-props web/src`); run the ones for
  TextField/Input and any component the build flags.
- **`GridLegacy` removed** — moot if Step B converted (verify:
  `grep -rn "GridLegacy" web/src` must return 0).
- **New Grid drops `direction="column"`** → use Stack. Check:
  `grep -rnE '<Grid[^>]*direction="column' web/src` at execution time.
- **Emotion remains the default engine in v9.** The styling-layer refactor
  (Emotion independence) is explicitly deferred to the *next* major. No
  Pigment CSS action required or recommended.
- If still on React 18: keep the `react-is` override, pinned to the React 18
  version.

### Risk table

| Area | v6 | v7 | React 19 | v9 |
| --- | --- | --- | --- | --- |
| Grid layout (23 files, 175 item) | LOW (no change) | **HIGH** (convert + visual verify) | — | LOW (already new Grid) |
| Icon imports (159 `.js`-suffixed) | LOW | **MED** (ESM layout change) | — | LOW |
| sx / styled / theme | LOW (0 styled, 1 theme file) | LOW | — | LOW |
| System props (~381) + Typography color (405) | LOW–MED (color semantics) | LOW | — | **HIGH** (removal; codemod + review) |
| Slot props (24 InputProps, 4 componentsProps) | LOW | LOW | — | MED |
| Test suite (ripple/act, react-is) | **MED** | LOW | MED (`act`, types) | LOW |
| Build/embed (`//go:embed web/dist`) | MED gate on every step — a broken `npm run build` ships a broken binary | ← | ← | ← |

### Top-3 risks overall

1. **v7 Grid conversion** — 175 `<Grid item` / 35 containers in 23 files;
   codemod does the props, but CSS-gap spacing and non-stretching containers
   can shift real layouts. Requires the full manual smoke list.
2. **v9 system-prop removal** — ~381 direct-prop usages plus 405
   `Typography color=`; codemod coverage must be hand-reviewed, and misses
   fail silently (prop ignored → styling vanishes, not a compile error, for
   plain-DOM passthroughs).
3. **The 159 `.js`-suffixed icon path imports vs v7's package-layout change**
   — a whole-build failure mode that is cheap to pre-empt (normalize imports
   in Step 0) and expensive to discover mid-upgrade.

---

## Recommended order

| # | PR | Fragment | Why this position |
| --- | --- | --- | --- |
| 0 | Prep: normalize icon imports, add `react-is` override, record smoke baseline | TODO-MUI-0 | removes the v7 build-failure landmine and the v6 react-is landmine before any version changes |
| 1 | `@mui/*` 5.14 → 6.x | TODO-MUI-1 | smallest hop; flushes out test-suite churn (ripple/act) while everything else is unchanged |
| 2 | `@mui/*` 6.x → 7.x + Grid conversion | TODO-MUI-2 | Grid converted exactly once, on the version where the codemod targets our shape |
| 3 | React 18 → 19 (optional, recommended) | TODO-MUI-3 | after v7 (which supports 19), before v9; deletes the react-is hack; isolates React churn from MUI churn |
| 4 | `@mui/*` 7.x → 9.x | TODO-MUI-4 | final hop; system-props + slotProps codemods; no v8 exists |

**One major per session/PR. Never chain two majors in one branch.**

## Verification gate (every step)

1. `make build` — full build; the Go binary embeds `web/dist` via
   `//go:embed` (build tag `embed_frontend`), so `npm run build` failures or
   a broken bundle ship straight to prod. The binary must start and serve
   the UI.
2. `make test-all` — Go + frontend (Vitest) suites.
3. `make test-e2e` — Playwright suite.
4. Manual smoke of the 5 heaviest pages (from the inventory): **Library**,
   **Book Detail** (incl. Metadata Review dialog), **Activity Log**,
   **System → Maintenance tab**, **Dedup tabs**. Check layout/spacing
   specifically after the v7 Grid step.
5. Zero new console errors/warnings on those pages (MUI deprecation
   warnings noted and carried into the next step's brief).

## Rollback (every step)

Each major is a **single PR**. Rollback = `git revert <merge-commit>` of that
one PR (repo is rebase/FF, so revert the commit range of that branch),
rebuild, redeploy. No data migrations are involved — the frontend bundle is
stateless — so revert is always safe. The `package-lock.json` diff rides in
the same PR and reverts with it.

## Sources

- [Upgrade to v6 — Material UI](https://mui.com/material-ui/migration/upgrade-to-v6/)
- [Upgrade to v7 — Material UI](https://mui.com/material-ui/migration/upgrade-to-v7/)
- [Upgrade to v9 — Material UI](https://mui.com/material-ui/migration/upgrade-to-v9/)
- [Introducing Material UI and MUI X v9 — MUI blog](https://mui.com/blog/introducing-mui-v9/)
- [How we migrated MUI X to React 19 — MUI blog](https://mui.com/blog/react-19-update/)
