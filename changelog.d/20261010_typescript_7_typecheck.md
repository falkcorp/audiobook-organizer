### Changed

#### Frontend type checking runs on TypeScript 7

`npm run build` and the new `npm run typecheck` in `web/` now type-check with
the TypeScript 7 native compiler (about 2 s instead of about 9 s on the same
1,613 files). TypeScript 7 is installed under the alias
`"@typescript/native": "npm:typescript@^7.0.2"`, and the scripts call its
binary by path (`node node_modules/@typescript/native/bin/tsc`) because
`node_modules/.bin/tsc` is claimed by both TS 7 and the TS 6 package nested
under the alias below, and an incremental `npm install` can relink it to TS 6.

The `typescript` dependency is now
`"typescript": "npm:@typescript/typescript6@^6.0.2"`, so `typescript-eslint`
(and anything else that does `require('typescript')`) still gets the TS 6 JS
API, which TS 7 does not ship. The wrapper re-exports TypeScript 6.0.3.
`npm run typecheck:ts6` runs the same check with `tsc6` as a control for one
release. `make web-typecheck` runs the TS 7 check.
