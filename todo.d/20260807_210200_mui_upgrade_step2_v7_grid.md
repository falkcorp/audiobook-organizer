- [ ] **TODO-MUI-2** MUI upgrade Step 2 — `@mui/*` 6.x → 7.x including the
      one-time Grid conversion (brief: `docs/plans/2026-08-07-mui-upgrade-path.md`;
      requires TODO-MUI-1 merged; do NOT continue to v9 in the same session/PR)
  - `cd web && npm install @mui/material@7 @mui/icons-material@7`
  - Grid: convert legacy Grid → new Grid NOW (do not rename to `GridLegacy` —
    it is removed in v9 and we'd pay twice):
    `npx @mui/codemod@latest v7.0.0/grid-props web/src`
    Inventory says 175 `<Grid item` and 35 `<Grid container` across 23 files;
    codemod output is `item xs={12} sm={6}` → `size={{ xs: 12, sm: 6 }}`,
    `xs` → `size="grow"`. After it runs, `grep -rn "<Grid item" web/src`
    must return 0.
  - Hand-verify layout on every Grid file: new Grid spaces with CSS `gap` and
    containers no longer stretch full-width by default — compare against the
    TODO-MUI-0 smoke baseline. Highest-risk files: `web/src/pages/Series.tsx`,
    `web/src/pages/Authors.tsx`, `web/src/pages/Dashboard.tsx`,
    `web/src/components/settings/ITunesImport.tsx`.
  - `npx @mui/codemod@latest v7.0.0/input-label-size-normal-medium web/src`
    (idempotent, cheap).
  - Build must confirm icon path imports still resolve under the v7 package
    layout (TODO-MUI-0 normalized the `.js` suffixes; if `npm run build`
    still errors on `@mui/icons-material/X`, switch those files to named
    barrel imports).
  - Known no-ops for this repo (verified 0 usages 2026-08-07): `Hidden`,
    deep >1-level imports, `createMuiTheme`, `onBackdropClick`, `@mui/lab`,
    `CssVarsProvider` mode behavior.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke with EXTRA attention to spacing/layout on Library, Book
    Detail, Activity Log, System > Maintenance, Dedup tabs.
  - Rollback: `git revert` of this single PR.
