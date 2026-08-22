### Changed

#### Removed the now-redundant `react-is` npm override from `web/package.json`

The `"react-is": "^19.0.0"` entry in `web/package.json`'s `overrides` object was added
by the MUI upgrade prep commit (`chore(web): MUI upgrade step 0 prep — normalize icon
imports, pin react-is`) to force a React-19-compatible `react-is` while the tree was
still mid-upgrade. That upgrade is finished: `react` is on `^19.2.8` and
`@mui/material` on `^9.3.1`, and `@mui/material`/`@mui/utils` now declare
`react-is: ^19.2.8` as a direct dependency of their own, so the root `react-is`
resolves to 19.2.8 with or without the override. Keeping it only hid the fact that
MUI no longer needs the help.

Dropping the override lets three long-tail transitive consumers resolve `react-is`
honestly instead of being force-upgraded across two major versions: `prop-types`
(`^16.13.1`) and `hoist-non-react-statics` (`^16.7.0`) now get a nested 16.13.1, and
the dev-only `pretty-format` (`^17.0.1`) gets a nested 17.0.2. That is three added
lockfile entries and is correct semver behavior, not a regression — the root
`react-is` that MUI and application code resolve is unchanged at 19.2.8, and nothing
under `web/src` imports `react-is` directly. `npm run build`, `npm run lint`,
`npx tsc --noEmit`, and all 752 tests across 91 files stay green, and `npm audit`
still reports 0 vulnerabilities. The two remaining overrides (`minimatch`,
`brace-expansion`) are security pins and were deliberately left alone.
