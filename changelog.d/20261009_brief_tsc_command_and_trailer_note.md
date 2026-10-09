### Fixed

- Task briefs: the TypeScript check is now `(cd web && npx tsc --noEmit)` in the template and 15 briefs; the root-level `npx tsc --noEmit -p web` resolves the wrong tsc and fails on `web/tsconfig.json`. The tasks README records that the `Claude-Session` trailer names the executing session.
