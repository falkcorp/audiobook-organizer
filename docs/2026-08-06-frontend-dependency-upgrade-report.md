<!-- file: docs/2026-08-06-frontend-dependency-upgrade-report.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e91cf03-7b24-4a86-9e15-0d38c7a24b19 -->
<!-- last-edited: 2026-08-06 -->

# Frontend dependency upgrade report

**Researched 2026-08-06.** Installed versions read from `web/package-lock.json` (resolved,
not the `^` ranges in `package.json`); "latest" verified against `registry.npmjs.org`.

Where a claim was load-bearing, the **published tarball was unpacked and the shipped
`.d.ts` read directly** rather than trusting a migration guide. Those are marked
**[verified in package]**. A migration guide describes intent; a tarball describes what
shipped, and the two disagreed at least once (see MUI §3.3).

Anything that could not be sourced is listed in §8 as unverified rather than asserted.

---

## 0. Executive summary

| Package | Installed | Latest | Verdict |
|---|---|---|---|
| `@testing-library/react` | 14.3.1 | 16.3.2 | ✅ **do first** — the real React 19 gate |
| `zustand` | 4.5.7 | 5.0.14 | ✅ trivial — codebase already v5-clean |
| `jsdom` | 23.2.0 | 30.0.1 | ✅ low — but fix a CI Node pin |
| `eslint` | 9.39.5 | 10.8.0 | ✅ low |
| `typescript` | 5.9.3 | **6.0.3** | ✅ moderate (**not 7 — see below**) |
| `@vitejs/plugin-react` | 4.7.0 | 5.1.4 | ✅ low |
| `react` / `react-dom` | 18.3.1 | 19.2.8 | ✅ small — every landmine is a zero here |
| `react-router` | 7.18.2 | 8.3.0 | ✅ trivial, **gated on React 19** |
| `@mui/material` | 5.18.0 | 9.3.1 | ⚠️ **large — ~75% of total effort** |
| `typescript` → 7.0.2 | — | 7.0.2 | ⛔ **blocked** — tooling will not install |
| `vite` | 7.3.6 | 8.2.1 | ⛔ **blocked** — crashed this app before |

**Total ≈ 2.5–3 weeks**, of which MUI is roughly three quarters.

### The two blocked items

**TypeScript 7 — hard blocked, not merely risky.** TS 7 is the native Go rewrite of the
compiler (Project Corsa), 8–12× faster, GA 2026-07-08. But it exposes **no stable
programmatic API until 7.1**, so every tool that consumes the compiler API is stuck on
TS 6. `typescript-eslint@8.66.0` peers `typescript: ">=4.8.4 <6.1.0"`; `npm install`
fails `ERESOLVE`, and forced installs crash at runtime. The upstream issue was closed
**not planned**. Notably `typescript-eslint@8.65.0` — the version installed here — ships
a warning specifically for detecting TS 7.

**TypeScript 6.0.3 is the ceiling with lint intact**, and it is a real step: TS 6 is the
deliberate transitional release whose deprecation warnings become TS 7's hard errors.

**Vite 8 — this repo already tried it and reverted it.** `CHANGELOG.md` lines 4071–4087,
2026-06-12:

> Revert vite 7→8 bump that crashed the entire web UI … Vite 8's rolldown bundler is
> incompatible with this React 18 + MUI v5 + emotion app: a CJS/ESM interop bug resolved
> an MUI/emotion import to a namespace object, crashing **every** page with React error
> `#130`.

There is an inline warning at `web/vite.config.ts:32-35`. The upstream issue
([vitejs/vite#22499](https://github.com/vitejs/vite/issues/22499)) is closed, but **which
version fixed it was never confirmed** — that is the single most important open question
in this report. Root cause is documented at
[rolldown.rs/in-depth/bundling-cjs](https://rolldown.rs/in-depth/bundling-cjs):
Babel's and Node's interpretations of a CJS `default` import are irreconcilable, and
Rolldown picks between them heuristically.

**A plausible unlock:** modern MUI ships ESM, so completing the MUI upgrade may sidestep
the interop ambiguity entirely. Unresearched — flagged as a possibility, not a plan.

---

## 1. React 18.3.1 → 19.2.8

**Ladder:** 18.3.1 → **19.0.0** (2024-12-05, *all* breaking changes) → 19.1.0 → 19.2.0
(2025-10-01) → 19.2.8. Types: `@types/react` 18.3.31 → 19.2.18, `@types/react-dom`
18.3.7 → 19.2.4.

### Breaking changes, and their count in this codebase

| API | Status | Migration | **Hits here** |
|---|---|---|---|
| `defaultProps` on function components | Removed | default params | **0** |
| `propTypes` on function components | Removed | TS types | **0** |
| String refs | Removed | callback refs | **0** |
| `createFactory`, module-pattern factories | Removed | JSX | **0** |
| Legacy context (`contextTypes`) | Removed | `createContext` | **0** |
| `ReactDOM.render` / `hydrate` / `unmountComponentAtNode` | Removed | `createRoot` | **0** — already `createRoot` at `main.tsx:44` |
| `findDOMNode` | Removed | refs | **0** |
| `react-dom/test-utils` | Removed except `act`, which moved to `react` | — | **0** — all 29 `act` uses come from RTL |
| `element.ref` | Deprecated (warns) | `element.props.ref` | **0** |
| UMD builds | Removed | ESM CDN | n/a |
| `react-test-renderer` | Deprecated; `/shallow` removed | RTL | **0** |

`forwardRef` is **neither removed nor deprecated** at 19.2.8 — react.dev says it *"will
be"* deprecated in a future release. There is correspondingly no `remove-forward-ref`
codemod.

### `@types/react` 19 — the mass-compile-error risk **[verified in package]**

- **`useRef` now requires an argument.** All three surviving overloads take an initial
  value. Types-only; runtime `useRef()` still works. **0 no-arg sites** — all 71 uses
  pass one.
- **`MutableRefObject` still exists** as a deprecated alias (`index.d.ts:1670`). The 7
  uses here survive with a JSDoc deprecation, not an error.
- **JSX namespace moved to `React.JSX`.** Breaks `declare global { namespace JSX {…} }`.
  **0 hits** — no `declare global` anywhere.
- `ReactElement["props"]` default `any` → `unknown`. The 3 uses are plain annotations.
- Ref callbacks may no longer implicitly return a value. The 1 site
  (`UnifiedDedupTab.tsx:1030`) already uses a block body.
- Removed types `ReactChild`, `ReactFragment`, `ReactNodeArray`, `ReactText`, `VFC`:
  **0 hits**.

**Every React 19 landmine is a confirmed zero in this codebase.**

### Codemods

```bash
npx codemod@latest react/19/migration-recipe          # runtime APIs
npx types-react-codemod@latest preset-19 ./src        # @types/react 19
```

Both expected to be near-no-ops. Run them anyway as a cheap net.

### The actual blocker: `@testing-library/react`

| RTL | `react` peer | React 19? |
|---|---|---|
| **14.3.1 (installed)** | `^18.0.0` | ❌ |
| 16.0.0 | `^18.0.0` | ❌ |
| 16.1.0 | — | ✅ first release supporting React 19 |
| **16.3.2 (latest)** | `^18.0.0 \|\| ^19.0.0` | ✅ |

There is no RTL v17. RTL 16 moved `@testing-library/dom` and `@types/react-dom` from
bundled deps to **peer** deps, so both must be added explicitly:

```jsonc
"@testing-library/react": "^16.3.2",
"@testing-library/dom":   "^10.4.1",   // NEW
"@types/react-dom":       "^19.2.4"    // NEW
```

**Because 16.3.2 also accepts React 18, this lands first, alone, on the current React.**
If RTL 16 breaks tests you learn that without React 19 as a confounding variable.

### Effort: small — 1–2 days

---

## 2. react-router 7.18.2 → 8.3.0

**Hard gates, from the shipped `react-router@8.3.0` package.json:**

```jsonc
"peerDependencies": { "react": ">=19.2.7", "react-dom": ">=19.2.7" },
"engines": { "node": ">=22.22.0" }
```

plus **ESM-only**. React 19.2.7+ is a hard prerequisite — npm will refuse to resolve
otherwise.

**`react-router-dom` no longer exists at v8.** Its last published version is 7.18.2.
Official: *"Removed `react-router-dom`. It was just a mirror of `react-router`."*

**[verified in package]** all 13 declarative symbols this codebase uses —
`BrowserRouter`, `MemoryRouter`, `HashRouter`, `Routes`, `Route`, `Link`, `NavLink`,
`Navigate`, `Outlet`, `useNavigate`, `useLocation`, `useParams`, `useSearchParams` — are
exported from the **main** `react-router` entry under the **same names**. Only
`RouterProvider` and `HydratedRouter` need `react-router/dom`, and neither is used here.

Everything else removed at v8 is data-router / Framework-Mode only: `meta({data})`,
`matches[i].data`, `MiddlewareEnabled`, `cloudflareDevProxy`, and the five
now-default-on `v8_*` future flags. **Zero apply to a declarative SPA.**

**No official codemod.** It is a pure module-specifier swap:

```bash
cd web
grep -rl "from ['\"]react-router-dom['\"]" src tests \
  | xargs sed -i '' "s/from ['\"]react-router-dom['\"]/from 'react-router'/g"
npm uninstall react-router-dom && npm i react-router@8.3.0
```

50 import statements across ~52 files. Usage tally: `useNavigate` 67, `MemoryRouter` 58,
`Link` 46, `Navigate` 43, `Route` 37, `BrowserRouter` 21, `Routes` 17, `useSearchParams`
12, `useLocation` 10, `useParams` 5. No `Outlet`, no `NavLink`, no data-router APIs.

**8.3.0 behaviour note:** path-parameter encoding now follows RFC 3986 rather than
ad-hoc percent-encoding. Not classed as breaking; sanity-check any route params carrying
exotic characters.

### Effort: trivial (~2h) — all the risk is in the React 19 prerequisite

---

## 3. MUI 5.18.0 → 9.3.1 — the big one

**There is no MUI v8.** The ladder is **5 → 6 → 7 → 9**. Material UI moved from v7
straight to v9 in step with MUI X v9. Verified independently: `@mui/x-data-grid` has 74
8.x releases; `@mui/material`, `@mui/system` and `@mui/utils` have **zero**.

**MUI never gates on React.** Peers are `react ^17 || ^18 || ^19` at 6.0.0, 7.0.0 *and*
9.0.0. This chain is fully independent of the React upgrade.

### 3.1 Hop 5 → 6 (2024-08-27)

- `Unstable_Grid2` → stable `Grid2`. **Classic `Grid` with `item`/`xs`/`md` is untouched
  at this hop** — the 175 `item` sites do not break yet.
- `Typography` `color` is no longer a system prop (use `sx`).
- `AccordionSummary` wraps content in a default `<h3>`; `Divider` renders `<div>` not
  `<hr>`; `Chip` retains focus on Escape.
- `LoadingButton` removed from Lab as of 6.4.0. UMD bundle removed.
- Floors: TS 3.5 → **4.7**, Node 12 → **14**. React floor unchanged.
- `InputProps` / `componentsProps` / `TransitionComponent` are **deprecated but fully
  functional**. The guide is explicit that you need not address deprecations to use v6.
- **Emotion remains the default engine.** Pigment CSS is opt-in and experimental.

**Codemods** **[verified — `@mui/codemod@9.3.1` unpacked and listed]**:
`npx @mui/codemod@latest v6.0.0/{all,grid-v2-props,styled,sx-prop,system-props,theme-v6,list-item-button-prop}`.
Note `grid-v2-props` targets `Grid2`/Joy/System Grid only — **not** classic `Grid`.

**New:** CSS theme variables (`cssVariables`), `colorSchemes` for one-line dark mode,
`theme.applyStyles('dark', …)`, container queries.

**Effort: low (~0.5 day).** Check `<Divider>`-as-`<hr>` assumptions and
`AccordionSummary` snapshots.

### 3.2 Hop 6 → 7 (2025-03-26) — the Grid hop

**This is where the 175 `<Grid item>` sites break.** The deprecated `Grid` was renamed
`GridLegacy`, and `Grid2` moved into the `Grid` namespace:

```diff
-<Grid container spacing={2}><Grid item xs={12} md={6}>…</Grid></Grid>
+<Grid container spacing={2}><Grid size={{ xs: 12, md: 6 }}>…</Grid></Grid>
```

`import Grid from '@mui/material/Grid'` **silently starts resolving to the new API**.
Under TypeScript that is a hard compile error, so the compiler is the safety net.
`container` and `spacing` are unchanged. New Grid does not grow to full container width
by default — some flex-parent cases need `sx={{ width: '100%' }}`.

**Removed:** `Hidden`, `createMuiTheme`, `experimentalStyled`, Dialog/Modal
`onBackdropClick`, `Rating`'s `MuiRating-readOnly` class. `StyledEngineProvider` moves to
`@mui/material/styles`. **Deep imports beyond one level removed** (they were private
API) — single-level `@mui/material/Button` and `@mui/icons-material/Icon` are unaffected.

- Floors: TS 4.7 → **4.9**. React unchanged.
- ⚠️ **`react-is@19` gotcha:** v7 uses `react-is@19` internally. If MUI 7 lands while
  React is still 18, add a `react-is` override pinned to 18.3.1. **Doing React 19 first
  makes this unnecessary** — the only real argument for sequencing React ahead of MUI.
- `@mui/icons-material` went **ESM-only at v7.0.0**, not v9.

**Codemods** **[verified in package]**:
`npx @mui/codemod@latest v7.0.0/{all,grid-props,input-label-size-normal-medium,lab-removed-components,theme-color-functions}`.
`grid-props` converts breakpoint props to `size={{…}}` and auto-removes `item`. Its
documented limitation is Grids wrapped in `styled()` — **this codebase has zero
`styled()` calls, so Grid migration is effectively codemod-complete**, with `tsc`
catching stragglers.

**Effort: moderate (~2–3 days).**

### 3.3 Hop 7 → 9 (9.0.0 on 2026-04-07; 9.3.1 on 2026-08-06) — the removals hop

**Emotion: no action required.** At 9.x, `@emotion/react`, `@emotion/styled` and
`@mui/material-pigment-css` are **all `optional: true`** peers. Pigment CSS's own repo
says *"⚠️ Alpha phase, currently, on hold"*, and "independence from Emotion" is listed
under **What's next** — i.e. after v9. Emotion 11.14 (already installed, already latest)
carries straight through, and **1,727 `sx=` props need zero changes**. `sx` gains a
claimed ~30% perf improvement for heavy use.

**A. System props removed from Box / Stack / Typography / Link / Grid.**
**[verified in shipped `@mui/material@9.3.1` `.d.ts`]** — the surviving prop lists are:

```
Stack:      children component direction divider spacing sx useFlexGap
Box:        component (+ sx)
Typography: align children classes color component gutterBottom noWrap sx variant variantMapping
Grid:       children columns columnSpacing component container direction offset rowSpacing size spacing wrap
```

Everything else moves into `sx`. **Measured: 480 removed system-prop JSX attributes** —
`Stack alignItems` 167, `Typography fontWeight` 78, `Typography display` 39, `Box
display` 38, `Stack justifyContent` 37, `Box alignItems` 26, `Stack mb` 24, `Box
justifyContent` 22, `Box gap` 18, plus a tail.

> **Not counted, because they survive:** `Typography color` (the 906 repo-wide `color=`
> hits are overwhelmingly legitimate component props) and `Stack direction`/`spacing`.

Codemod: **`npx @mui/codemod@latest v9.0.0/system-props`** — **[verified: `v9.0.0/`
contains exactly one transform. There is no `v9.0.0/all` and no `preset-safe`.]**

**B. The `slots`/`slotProps` deprecations are finally removed.** The shipped v9
`TextField` prop list contains **no** `InputProps`, `InputLabelProps`, `SelectProps`,
`FormHelperTextProps` or `inputProps`:

```diff
-<TextField InputProps={{ startAdornment: … }} inputProps={{ step: 300 }} />
+<TextField slotProps={{ input: { startAdornment: … }, htmlInput: { step: 300 } }} />
```

Same across ~15 components. **In this codebase: 24 `InputProps` + 45 `inputProps` + 4
`InputLabelProps` + 1 `SelectProps` + 4 `componentsProps` ≈ 78 sites**, across 258
`TextField` usages. Codemod: `npx @mui/codemod@latest deprecations/all` (59 transforms
available).

> ⚠️ **Several secondary sources claim these were removed at v7. They are wrong.** The
> `v7.0.0`-tagged `TextField.js` still destructures and uses them, and the v7 guide's own
> removals list omits them. Confirmed by reading the shipped v9 types.

**C. `GridLegacy` removed entirely**, and `direction="column"` removed from `Grid` (use
`Stack`). The v7 `size={{…}}` shape is already the stable v9 shape — **no further Grid
work**. 0 `direction="column"` on Grid here.

**D. `disableEscapeKeyDown` removed** from Dialog/Modal — check
`reason === 'escapeKeyDown'` in `onClose` instead. **2 sites.**

**E. Legacy `*Outline` icon exports removed** in favour of `*Outlined`
**[verified by unpacking `@mui/icons-material@9.3.1`]**:

| Import | v9 | Action |
|---|---|---|
| `ErrorOutline` (2 sites) | **missing** | → `ErrorOutlined` |
| `HelpOutline` (3 sites) | **missing** | → `HelpOutlined` |
| `DriveFileRenameOutline` (1 site) | present | none |

**5 concrete sites.**

**F. Behavioural (not removals, still breaking):** `ButtonBase` Enter/Space clicks now
bubble and pass a `MouseEvent`; `Slider` uses pointer events; `Stepper`/`Step` render
`<ol>`/`<li>`; `TablePagination` locale-formats numbers; `Backdrop` no longer defaults
`aria-hidden="true"`. Combinatorial CSS variant classes removed — matters only if theme
`styleOverrides` target them by name. **Browser floor: Chrome 117+, Firefox 121+,
Safari 17+.**

**G. React floor unchanged** (`^17 || ^18 || ^19`). TS min 4.9, Node min 14.

**New:** `NumberField` and `Menubar` (Base-UI-backed), roving-tabindex accessibility
overhaul, `prefers-reduced-motion`, `enhanceHighContrast`.

**Effort: large (~1 week).**

### 3.4 MUI totals

| Hop | Forced work | Codemod coverage | Estimate |
|---|---|---|---|
| 5 → 6 | ~none | n/a | 0.5 d |
| 6 → 7 | 175 Grid `item` sites | effectively complete (0 `styled()`) | 2–3 d |
| 7 → 9 | 480 system props + ~78 slotProps + 5 icons + 2 Dialog | `system-props` + `deprecations/all` | ~1 w |

**Zero usage** of `makeStyles`, `withStyles`, `createStyles`, `@mui/styles`, `@mui/lab`,
`@mui/x-*`, `@mui/base`, `Hidden`, or `styled()` — all confirmed. That absence is what
makes the codemods land cleanly.

**Do the three hops as three separate PRs.** Each hop's codemods assume the previous
hop's state.

---

## 4. zustand 4.5.7 → 5.0.14

**[verified in shipped package]** — `index.d.ts` at v5 is only
`export * from 'zustand/vanilla'; export * from 'zustand/react';`. `create` is a **named
export only**; the default export is a hard removal at 5.0.0.

- **`import create from 'zustand'` is removed.** This codebase already uses
  `import { create }` in all 4 stores. **Zero change.**
- **`equalityFn` removed** as the hook's second argument; moved to `zustand/traditional`.
  **0 sites.**
- **The selector-snapshot rule — the one real landmine.** v5 enforces
  `useSyncExternalStore`'s cached-snapshot contract, so a selector allocating a fresh
  object/array per call throws `Maximum update depth exceeded`:
  ```diff
  -const [v, setV] = useStore((s) => [s.v, s.setV]);
  +const [v, setV] = useStore(useShallow((s) => [s.v, s.setV]));
  ```
  Grep: `grep -rnE "use[A-Za-z]*Store\(\s*\(?[a-z]+\)?\s*=>\s*[\[{]" web/src` —
  **zero hits.** All 21 selectors are single-field atomic. The one non-trivial selector
  returns an existing array element, which is referentially stable. **Already
  v5-hygienic.**
- `setState` `replace` typing tightened. **0 sites.** `zustand/context` removed. **0
  sites.** `zustand/shallow` default export removed. **0 sites.**
- React floor 18, TS floor 4.5 — both satisfied.
- **v5.0.0 ships no new features.** It is a pure cleanup release.

**No codemod exists.** **Effort: ~1 hour.**

---

## 5. jsdom 23.2.0 → 30.0.1

jsdom deleted its `Changelog.md`; notes live only in GitHub Releases.

| Major | Node floor | Headline |
|---|---|---|
| 24.0.0 | ≥18 | selector engine reverted to `nwsapi` — **`:has()` removed again** |
| 25.0.0 | ≥18 | `EventTarget.prototype` proto chain changes under `runScripts: "dangerously"` — **vitest's jsdom env uses `dangerously`** |
| 26.0.0 | ≥18 | `canvas` peer v2→v3 (moot — no `canvas` dep) |
| 27.0.0 | ≥20 | selector engine switched back (`:has()` restored); `sendTo()` → **`forwardTo()`**; **`element.click()` fires `PointerEvent` not `MouseEvent`**; UA stylesheet rebuilt (**shifts default `getComputedStyle()` values**) |
| 28.0.0 | ^20.19 \|\| ^22.12 \|\| ≥24 | resource-loading overhaul; `<iframe>`/`<img>` fire `load` not `error` on non-OK HTTP |
| 29.0.0 | ^20.19 \|\| ^22.13 \|\| ≥24 | **entire CSSOM implementation replaced**; real media-query parsing |
| 30.0.0 | ^22.22.2 \|\| ^24.15.0 \|\| ≥26 | `CSS.escape()`/`supports()`; **`getComputedStyle()` converts lengths to pixels** |
| 30.0.1 | same | **regression fix** — `getComputedStyle()` with `calc()` threw at 30.0.0. **Do not stop at 30.0.0.** |

**What actually breaks a vitest + RTL suite:** `getComputedStyle()` churned across nearly
every major from 27 on — engine swap, `!important` handling, full rewrite, pixel
conversion. That is the #1 risk if any test asserts on computed styles. Second is
`element.click()` firing `PointerEvent` (27.0.0).

**Not affected:** `matchMedia`, `IntersectionObserver`, `ResizeObserver`, `scrollTo` —
jsdom never implemented the latter three; they are polyfilled in `setupFiles`.

**vitest 4.1.10 supports jsdom 30.** Its optional peer is `"jsdom": "*"`, and its shipped
environment loader already contains the jsdom-27 compat branch
(`if ("sendTo" in virtualConsole) … else virtualConsole.forwardTo(…)`). **No vitest
upgrade needed** — vitest is already at latest.

### ⚠️ The one real risk is CI Node pins

- `ci.yml`, `nightly.yml`, `binary-smoke.yml`, `vulnerability-scan.yml`: `node-version:
  '22'` → resolves past 22.22.2. Satisfied, but now **floor-sensitive**.
- **`security.yml` uses `node-version: '20.x'` and runs `npm ci` in `web/`. jsdom 30
  excludes Node 20 entirely.** It does not run vitest so tests will not fail, but expect
  `EBADENGINE`, and a hard failure if `engine-strict` is ever enabled. **Bump that job.**
- `frontend-ci.yml` takes its Node version from a composite action output — **verify
  independently**.

**Effort: ~0.5 day + test triage.** Zero source changes.

---

## 6. Toolchain: TypeScript, Vite, ESLint

### ESLint 9.39.5 → 10.8.0

The 9.x and 10.x lines run in parallel — 9.39.5 shipped the same day as 10.7.0, so this
is not a forced upgrade.

**Node floor raised** to `^20.19.0 || ^22.13.0 || >=24` — the strictest constraint in the
whole set. Local Node 26.5.0 clears it.

**Removed:** eslintrc entirely (`.eslintrc.*`, `.eslintignore`, `--env`,
`ESLINT_USE_FLAT_CONFIG`), `FlatESLint`/`LegacyESLint` classes, `/* eslint-env */`
comments (now errors), and the whole legacy rule-author surface (`context.getCwd()`,
`getFilename()`, `getSourceCode()`, `sourceCode.getTokenOrCommentBefore()`, …).

**This repo touches none of it** — already flat config, no custom rules, no RuleTester.

**Expect churn from three things:**
1. **JSX identifiers are now tracked as references** — `<Card>` counts as a use of the
   imported `Card`. Fixes `no-unused-vars` false positives across ~182 `.tsx` files.
2. `eslint:recommended` gained `no-unassigned-vars`, `no-useless-assignment`,
   `preserve-caught-error`.
3. `no-shadow-restricted-names` now reports `globalThis`.

`typescript-eslint@8.65.0` already peers `^10.0.0`. **Effort: a few hours of triage.**

### TypeScript 5.9.3 → 6.0.3

TS 6 is explicitly *"a bridge between TypeScript 5.9 and 7.0"* and the last release built
on the JavaScript codebase. It emits warnings for everything TS 7 hard-errors on, gated
behind `"ignoreDeprecations": "6.0"`.

**Removed (hard errors):** `moduleResolution: classic`, `module: amd|umd|systemjs|none`,
`outFile`, `amd-module`.

**Deprecated (warns):** `target: es5`, `downlevelIteration`, `moduleResolution: node`,
**`baseUrl`**, `esModuleInterop: false`, the `module` keyword for namespaces, `assert`
import syntax.

**Changed defaults:** `strict` → true, `module` → esnext, **`types` → `[]`**, `rootDir` →
`.`, `noUncheckedSideEffectImports` → true.

**This repo is unusually well-positioned** — it already sets `strict`, `ESNext`,
`bundler`, explicit `target`, and **explicit `types`**. That last one matters: the `types`
default flipping to `[]` silently breaks most projects. The one concrete hit is
`baseUrl: "."`, which must become full-prefix `paths`.

**Effort: ~0.5 day.**

### `@vitejs/plugin-react` 4.7.0 → 5.1.4

Independent of Vite 8 — 5.x still peers `vite ^4.2 || ^5 || ^6 || ^7`. Absorbing 5.0.0's
breaking changes early de-risks the eventual Vite 8 jump.

Watch: **`react`/`react-dom` are no longer auto-added to `resolve.dedupe`** (most likely
surprise — add manually if duplicate-React errors appear); default `exclude` changed `[]`
→ `[/\/node_modules\//]`; `disableOxcRecommendation` removed.

---

## 7. The order to do this in

Derived from `peerDependencies`, not prose. There is exactly **one** hard chain.

```
CHAIN A — strictly sequential
  A1. @testing-library/react 14.3.1 → 16.3.2
      + ADD @testing-library/dom@^10.4.1   (now a peer)
      + ADD @types/react-dom@^19.2.4       (now a peer)
      ↳ RTL 16.3.2 peers react ^18||^19, so this ships TODAY on React 18.
        Standalone PR. This is the de-risker.
                    ↓ hard gate
  A2. react + react-dom 18.3.1 → 19.2.8, @types/react → 19.2.18
      ↳ blocked by A1: RTL 14.3.1 peers react ^18.0.0 only
                    ↓ hard gate
  A3. react-router 7.18.2 → react-router@8.3.0
      + npm uninstall react-router-dom + sed import rewrite
      ↳ blocked by A2: peers react >=19.2.7; npm will refuse

CHAIN B — independent of A, three sequential hops
  B1. @mui/material + @mui/icons-material 5.18.0 → 6.5.0
  B2.                                     6.5.0  → 7.3.11
  B3.                                     7.3.11 → 9.3.1
      ↳ peers react ^17||^18||^19 at EVERY hop. No React gate.

CHAIN C — independent of everything
  C1. zustand 4.5.7 → 5.0.14
  C2. jsdom 23.2.0 → 30.0.1   (+ fix security.yml's Node 20)

CHAIN D — toolchain, independent
  D1. eslint 9 → 10 (+ @eslint/js ^10, typescript-eslint 8.66.0)
  D2. typescript 5.9.3 → 6.0.3
  D3. @vitejs/plugin-react 4.7.0 → 5.1.4
```

**Recommended sequence:**

1. **C1 + C2 + D1 + D2 + D3.** Trivial and independent. jsdom 30 stabilises the test
   environment *before* you change things that need tests to be trustworthy.
2. **A1** — RTL bump alone, on React 18.
3. **A2** — React 19, now a near-pure version bump.
4. **A3** — react-router 8 rides along.
5. **B1 → B2 → B3**, three separate PRs.

**The one cross-chain edge:** MUI 7 pulls `react-is@19` internally, so if B2 lands while
React is still 18 you need a `react-is` override pinned to 18.3.1. Doing A2 before B2
removes the need. That is the only reason to sequence React ahead of MUI — and it is a
good one.

**Do not merge B3 (MUI 9) and A2 (React 19) in the same PR.** They are the two largest
blast radii and you want independent bisectability.

### 🔴 Prerequisite for all of it

The Playwright e2e suite is **broken on `main`** — every test dies at fixture collection
on `unknown parameter "_page"`, ~49 errors, pre-existing and reproducible on the
pre-v7-router tree. **It currently gates nothing**, which is exactly how the react-router
v6→v7 upgrade merged with zero runtime navigation coverage.

`tsc` clean and 402 unit tests passing did not catch that Vite 8 crashed every page in
June. Fix the suite first, or these upgrades ship on the same blind spot.

---

## 8. Explicitly unverified

Listed separately so nothing above reads as more certain than it is.

- **Which Vite version, if any, fixes the MUI/emotion rolldown interop bug** (vite#22499,
  rolldown#9502). The most important open question here.
- Whether upgrading MUI would itself resolve that interop problem. Plausible — modern MUI
  is ESM — but entirely unresearched.
- ESLint 10.1.0's release notes — not findable.
- TypeScript 7.1's stable-API date (~October 2026) — third-party reporting only, no
  Microsoft source.
- Whether `vitest` and `@vitejs/plugin-react` work with TypeScript 7 — neither consumes
  the compiler API for its core path, but that is inference.
- ESLint 10 peer ranges for `eslint-plugin-react-hooks@5.2.0` and
  `eslint-plugin-react-refresh@0.5.3` — not individually checked.
- **Why** react-router 8 pins `react >=19.2.7` specifically. The *requirement* is verified
  from package metadata; no official rationale was found, and the linked discussion
  justifies dropping React 18 on staleness grounds without citing a 19.2 feature.
- react-router 8's TypeScript floor — stated nowhere official.
- Whether MUI v9 flipped `cssVariables`/`colorSchemes` to default-on.
- Whether MUI v9's `styled()` changed behaviour — no breaking-change entry, but no
  positive "unchanged" statement either.
- Whether zustand's `persist` `getStorage`/`serialize`/`deserialize` were removed at v5
  specifically.
- jsdom's `pretendToBeVisual`/`runScripts` defaults being unchanged — verified by absence
  across seven majors, not by a changelog entry (jsdom deleted its changelog).
- `frontend-ci.yml`'s resolved Node version.

---

## 9. Sources

**TypeScript** — [Announcing TypeScript 7.0](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/) ·
[Announcing TypeScript 6.0](https://devblogs.microsoft.com/typescript/announcing-typescript-6-0/)
**Vite** — [migration guide](https://vite.dev/guide/migration) ·
[Announcing Vite 8](https://vite.dev/blog/announcing-vite8) ·
[vite#22499](https://github.com/vitejs/vite/issues/22499) ·
[Rolldown: bundling CJS](https://rolldown.rs/in-depth/bundling-cjs)
**ESLint** — [Migrate to v10](https://eslint.org/docs/latest/use/migrate-to-10.0.0) ·
[v10.0.0 released](https://eslint.org/blog/2026/02/eslint-v10.0.0-released/)
**typescript-eslint** — [#12518](https://github.com/typescript-eslint/typescript-eslint/issues/12518) ·
[#12529](https://github.com/typescript-eslint/typescript-eslint/pull/12529) ·
[#10940](https://github.com/typescript-eslint/typescript-eslint/issues/10940)
**React** — [19 Upgrade Guide](https://react.dev/blog/2024/04/25/react-19-upgrade-guide) ·
[19.2 release](https://react.dev/blog/2025/10/01/react-19-2) ·
[types-react-codemod](https://github.com/eps1lon/types-react-codemod)
**RTL** — [v16.0.0 release](https://github.com/testing-library/react-testing-library/releases/tag/v16.0.0)
**react-router** — [v8 blog](https://remix.run/blog/react-router-v8) ·
[upgrade guide](https://reactrouter.com/upgrading/v7) ·
[discussion #14468](https://github.com/remix-run/react-router/discussions/14468)
**MUI** — [Introducing v9](https://mui.com/blog/introducing-mui-v9/) ·
[upgrade-to-v6](https://mui.com/material-ui/migration/upgrade-to-v6/) ·
[upgrade-to-v7](https://mui.com/material-ui/migration/upgrade-to-v7/) ·
[Pigment CSS](https://github.com/mui/pigment-css)
**zustand** — [v5.0.0 release](https://github.com/pmndrs/zustand/releases/tag/v5.0.0) ·
[migrating-to-v5](https://github.com/pmndrs/zustand/blob/main/docs/reference/migrations/migrating-to-v5.md)
**jsdom** — [releases](https://github.com/jsdom/jsdom/releases)
