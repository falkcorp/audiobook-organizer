### Changed

- `web/src/hooks/{useAsyncAction,useDebouncedSearch,useRowSelection}.ts` and `web/src/stores/operationGrouping.ts` are now one-line re-exports of `@mediacore/ui-kit` and `@mediacore/shell` (F9-05, dark: same exported names and behaviour, so no importer changes). Their tests moved to the packages with the code; `web/src/test/f905Reexports.test.ts` pins that the old paths are re-exports, not copies, and that ABO's own operation rows still fold through the shell grouping.
