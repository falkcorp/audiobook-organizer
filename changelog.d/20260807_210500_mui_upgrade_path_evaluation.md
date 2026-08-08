<!-- file: changelog.d/20260807_210500_mui_upgrade_path_evaluation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 12b5b80a-63fb-4179-aa40-e0470dd4483f -->
<!-- last-edited: 2026-08-07 -->

### Documentation

- Added `docs/plans/2026-08-07-mui-upgrade-path.md`: MUI 5.14 → 9.x upgrade-path
  evaluation with a measured usage inventory of `web/src` (142 MUI files, 1,726
  `sx=`, 175 `<Grid item`, ~381 system-prop usages, zero `@mui/lab`/`@mui/x-*`),
  per-major breaking-change mapping (5→6, 6→7, 7→9 — there is no Material UI v8),
  risk table, recommended order, verification gates, and rollback plan. Five
  self-contained `todo.d/` agent briefs (TODO-MUI-0 … TODO-MUI-4) cover prep,
  each major, and the optional React 19 hop. Planning only — no packages upgraded.
