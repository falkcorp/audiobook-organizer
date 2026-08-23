### Fixed

#### Frontend build no longer warns about deprecated Vite/Rolldown build options

`npm run build` was printing `advancedChunks option is deprecated, please use
codeSplitting instead` on every run. Vite 8.2.1 (rolldown-vite) has, as of this
version, deprecated both `build.rollupOptions` and its nested
`output.advancedChunks` in favor of `build.rolldownOptions` /
`output.codeSplitting` — same shape, just renamed; `web/vite.config.ts` now
uses the new names.

Re-verified the no-duplicate-React-instance invariant the manual chunking
depends on (a prior Vite 8 migration attempt shipped two live React instances
into the `mui` and `vendor` chunks, producing a React error #130 crash on
every page) by grouping every emitted chunk's sourcemap `sources` by *exact
module file path*, not just package name — a package can legitimately have
different files land in different chunks (e.g. `react-dom/client.js`'s actual
renderer vs. `react-dom`'s small shared-internals shim used by MUI), so a
per-package check produces false positives. Zero files land in more than one
chunk, before and after the rename; the emitted `dist/assets/*.js` manifest is
byte-identical to the pre-change build.

Also raised `chunkSizeWarningLimit` from the 500 kB default to 600 kB. The one
chunk over that line is `mui-*.js` (510 kB minified / 153.59 kB gzip) — needed
even on the login screen since MUI core is imported eagerly in `App.tsx`, and
its size is diffuse across 122 component subpaths with no dead weight to trim
(no single component over 5.5% of the chunk). Splitting it further would trade
one request for several at the same total byte count, so it's left as one
chunk rather than force-split to satisfy the warning.
