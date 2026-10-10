- [ ] **TS7-TYPECHECK-DOCS** After 06-P9 (#3933), plain `tsc` in `web/` can resolve to
      TypeScript 6: `@typescript/typescript6` pulls in `npm:typescript@^6`, whose `tsc` bin
      an incremental `npm install` was seen to link over TS 7's. Point every instruction that
      says `npx tsc --noEmit` at `npm --prefix web run typecheck` (or `make web-typecheck`):
      `.claude/agents/code-reviewer.md`, `.claude/agents/typescript-specialist.md`,
      `docs/proposals/2026-10-holistic/tasks/00-TEMPLATE.md` and the 31 task briefs that copy
      it (`grep -rln 'npx tsc' .claude docs/proposals`). The `Bash(npx tsc *)` allow entry in
      `.claude/settings.json` is a permission setting: the owner decides whether to add one
      for the new command.
- [ ] **TS6-PIN-OVERRIDE** typescript-eslint's `<6.1.0` peer range is checked against the
      `@typescript/typescript6` wrapper's own version (6.0.2), while the compiler it loads
      comes from `@typescript/old` (`npm:typescript@^6`). A lockfile refresh could pull a
      6.1 that typescript-eslint does not support, and npm would not warn. Add an
      `overrides` entry pinning `@typescript/old` to `~6.0.3` in `web/package.json`.
- [ ] **WOODPECKER-TYPECHECK** Woodpecker's `web-test` step runs only vitest and has never
      type-checked. Add `npm run typecheck` (about 2 s on TS 7) to that step in
      `.woodpecker/checks-build.yaml`.
