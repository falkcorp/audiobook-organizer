- [ ] **TODO-MUI-3** MUI upgrade Step 3 — React 18 → 19 (OPTIONAL but
      recommended; brief: `docs/plans/2026-08-07-mui-upgrade-path.md`; requires
      TODO-MUI-2 merged — MUI v7 supports React 19, v5/v6 pairings are riskier;
      do NOT combine with the v9 bump in the same session/PR)
  - Why: MUI v9 does NOT require React 19 (peers `^17 || ^18 || ^19`), but
    upgrading first deletes the `react-is` override hack, matches the
    combination MUI tests first-class, and pre-positions for the post-v9
    styling-layer refactor.
  - `cd web && npm install react@19 react-dom@19 && npm install -D @types/react@19 @types/react-dom@19`
  - `npx codemod@latest react/19/migration-recipe` (covers
    `ReactDOM.render` → `createRoot`, `react-dom/test-utils` `act` →
    `react`'s `act`, propTypes/defaultProps removal on function components).
  - Hand-check afterwards: `grep -rn "test-utils" web/src`,
    `grep -rn "defaultProps" web/src`, `grep -rn "useRef()" web/src`
    (React 19 `useRef` requires an argument), and Vitest setup files under
    `web/src/test/`.
  - Remove the `react-is` override added in TODO-MUI-0 from
    `web/package.json` (no longer needed on React 19) and `npm install`.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail, Activity Log, System > Maintenance,
    Dedup tabs; zero new console errors.
  - Rollback: `git revert` of this single PR.
