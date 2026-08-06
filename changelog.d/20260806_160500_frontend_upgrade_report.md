<!-- file: changelog.d/20260806_160500_frontend_upgrade_report.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d840b7e-13c5-4f96-a028-6be91537dfc4 -->
<!-- last-edited: 2026-08-06 -->

### Added

- **A researched upgrade report for every frontend dependency**, at
  `docs/2026-08-06-frontend-dependency-upgrade-report.md`. Covers React, MUI,
  react-router, zustand, jsdom, TypeScript, Vite, ESLint and Testing Library —
  every version between what is installed and current latest, with the breaking
  changes per major, the codemods that exist, measured hit-counts against this
  codebase, and the dependency-ordered sequence to do it in.

  Two upgrades are blocked and the report says why, so nobody re-derives it:
  **TypeScript 7** cannot be installed (`typescript-eslint` peers `<6.1.0`; TS 7
  exposes no stable compiler API until 7.1), and **Vite 8** already crashed every
  page of this app in June 2026 and the fixing version was never confirmed.

  Where a claim mattered, the published tarball was unpacked and the shipped
  `.d.ts` read rather than trusting the migration guide. That caught a wrong
  claim in circulation: MUI's `InputProps`/`InputLabelProps` are removed at **v9**,
  not v7, which is worth ~78 edit sites landing in the right PR.
