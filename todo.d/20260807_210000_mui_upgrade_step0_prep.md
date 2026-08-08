- [ ] **TODO-MUI-0** MUI upgrade Step 0 — prep PR before any version bump (brief:
      `docs/plans/2026-08-07-mui-upgrade-path.md`; do NOT bump any `@mui/*` version
      in this PR, and do not proceed to Step 1 in the same session)
  - Normalize the 159 `.js`-suffixed icon imports so v7's ESM package-layout
    change can't break the build later:
    `grep -rlE "from '@mui/icons-material/[A-Za-z]+\.js'" web/src | xargs gsed -i -E "s|(from '@mui/icons-material/[A-Za-z]+)\.js'|\1'|g"`
    then verify `grep -rn "icons-material/.*\.js'" web/src` returns 0.
  - Add a `react-is` override to `web/package.json` (needed by MUI v6+ on
    React 18): `"overrides": { "react-is": "^18.2.0" }`, then
    `cd web && npm install` to refresh the lockfile.
  - Record the smoke baseline: run `make build`, start the binary, and
    screenshot/manually check the 5 heaviest pages (Library, Book Detail +
    Metadata Review dialog, Activity Log, System > Maintenance tab, Dedup
    tabs) so later Grid diffs have a reference.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    zero new console errors on the 5 smoke pages.
  - Rollback: `git revert` of this single PR.
