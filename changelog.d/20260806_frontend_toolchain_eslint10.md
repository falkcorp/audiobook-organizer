<!-- file: changelog.d/20260806_frontend_toolchain_eslint10.md -->
<!-- version: 1.0.0 -->
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
