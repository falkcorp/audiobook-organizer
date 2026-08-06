<!-- file: changelog.d/20260806_frontend_toolchain_eslint10.md -->
<!-- version: 1.2.0 -->
<!-- guid: d0f2337c-43fc-4a24-b9aa-2239c0b2c946 -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **Frontend linting moved to ESLint 10.** `eslint` 9.39.5 → 10.8.0, `@eslint/js`
  9.39.5 → 10.0.1, and `typescript-eslint` 8.65.0 → 8.66.0. Nothing in
  `web/eslint.config.mjs` touched the surface ESLint 10 removed — it has been flat
  config for a while, with no `.eslintrc`, no `.eslintignore`, no custom rules and
  no `RuleTester` — so the config carried over as-is. The upgrade is
  finding-for-finding identical on the current tree: 291 files linted, 0 errors and
  24 warnings both before and after, with no new and no resolved findings. That is
  a real result rather than a vacuous one — `eslint --print-config` confirms the
  three rules ESLint 10 newly enables in `eslint:recommended`
  (`no-unassigned-vars`, `no-useless-assignment`, `preserve-caught-error`) are all
  active at error severity, and that `no-shadow-restricted-names` now runs with
  `reportGlobalThis: true`. This codebase simply has no violations of any of them.
  ESLint 10's other headline change — JSX identifiers now counting as references,
  so `<Card>` marks the imported `Card` as used — produced no delta here either,
  because `no-undef` is already switched off in this config and
  `@typescript-eslint/no-unused-vars` was already JSX-aware.

- **`eslint-plugin-react-hooks` 5.2.0 → 7.1.1, forced by ESLint 10's peer range.**
  This was not optional: 5.2.0, 6.0.0, 6.1.x and 7.0.x all cap their `eslint` peer
  at `^9.0.0`, and `^10.0.0` first appears in 7.1.0, so an ESLint 10 install fails
  to resolve without it. The plugin's 7.x `recommended` preset also pulls in about
  fourteen new React Compiler rules (`immutability`, `purity`, `refs`,
  `static-components`, `set-state-in-effect` and friends) at error severity, which
  is a decision about how this codebase writes React rather than anything to do
  with upgrading ESLint. So the config now pins the exact pair that 5.2.0's
  `recommended` enabled — `react-hooks/rules-of-hooks` at error and
  `react-hooks/exhaustive-deps` at warn — leaving the lint diff attributable to
  ESLint 10 alone. Adopting the compiler rules is a one-line change away and is
  deliberately left as a separate call. `eslint-plugin-react-refresh` needed no
  bump; 0.5.3 already peers `^9 || ^10`.

- **Frontend TypeScript moved to 6.0.3** (from 5.9.3 — deliberately *not* 7, see
  below). `web/tsconfig.json` drops `baseUrl`, which 6.0 removed; its `paths` entry
  was already written in the full-prefix form 6.0 requires (`"./src/*"`), so the
  mapping needed no rewrite. `ignoreDeprecations: "6.0"` is set to stage anything
  else that starts warning. The project turned out to be well positioned for the
  rest of 6.0's default changes: it already pins `types` explicitly, so the new
  `types: []` default — which silently drops the ambient `@types/*` that projects
  relying on the old "load everything" behaviour depend on — does not apply here;
  and it uses `module: ESNext` with `moduleResolution: bundler`, so none of the
  configurations 6.0 deleted outright (`moduleResolution: classic`, `module:
  amd|umd|systemjs|none`, `outFile`) are in play. `tsc --noEmit` is clean over 248
  files, checked with a deliberately broken file to confirm the run was actually
  type-checking rather than silently passing.

  Worth flagging for whoever touches this next: the `@/*` alias that `paths` and
  vite's `resolve.alias` both define is **not used anywhere** in `web/` — zero
  import specifiers reference it. The alias config in both files is dead weight and
  could be deleted, which is also why removing `baseUrl` carried no risk.

- **`@vitejs/plugin-react` 4.7.0 → 5.1.4**, pinned exactly rather than with a caret.
  This is independent of Vite 8: 5.1.4 peers `vite: "^4.2.0 || ^5 || ^6 || ^7"`, so
  it runs on the Vite 7.3.6 this project is held at. The exact pin is deliberate —
  `^5.1.4` floats to 5.2.0, which was published the same day as 6.0.0 as the 5.x
  backport that widens the peer range to include Vite 8. Given this repo has a
  standing incident report against Vite 8, the 5.x release that stays Vite-7-only is
  the one we want, and a caret would silently drift off it. `vite.config.ts` now sets
  `resolve.dedupe: ['react', 'react-dom']` explicitly, because plugin-react 4.x
  added those two for you and 5.0.0 stopped doing so. npm's tree happens to
  hoist a single React 18.3.1 today, so this is belt-and-braces — but a second
  React instance reaching MUI/emotion is precisely the failure that took every page
  down with React error #130 during the abandoned Vite 8 attempt, and the Playwright
  suite that would catch it is currently broken, so the setting is pinned rather
  than left to chance. Verified after the fact: `react-dom` appears in exactly one
  built chunk. The other 5.0.0 breaking changes are inert here — the `exclude`
  default moving from `[]` to `[/\/node_modules\//]` only narrows the transform away
  from dependencies, and `disableOxcRecommendation` was never set.

### Deprecated

- **Vite is deliberately held at 7.x and TypeScript at 6.x.** Vite 8's rolldown
  bundler is still excluded for the reason recorded inline in `web/vite.config.ts`:
  a CJS/ESM interop bug resolved an MUI/emotion import to a namespace object and
  crashed every page with React error #130. The upstream issue (vitejs/vite#22499)
  is closed but no fixing version was ever confirmed. TypeScript 7 is blocked by
  `typescript-eslint@8.66.0`, which peers `typescript: ">=4.8.4 <6.1.0"`; TS 7 is
  the native Go rewrite and exposes no stable programmatic API until 7.1, so
  forcing the install past the peer range crashes at runtime rather than merely
  warning.
