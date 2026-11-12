### Added

- tools/fixerstores, internal/plugins/maintenance: a static guard for the repairs fixers' shell stores. `TestFixerStores_NoFixerReachesAStoreExceptThroughTheShell` runs a VTA call-graph pass (a module of its own, `tools/fixerstores`, so x/tools stays out of the root go.mod) from every fixer's Plan, Replan, Apply, BeginApply and AfterApply and fails on any `FixerStores` accessor called on a plugin store, or any other plugin dep, that is not on the reviewed allowlist (`testdata/fixer_stores_allowlist.txt`, shrink-only: a stale entry fails too). It skips under `-short` and runs in the "Fixture Tests (no -short, race)" jobs. It complements the tripwire tests: VTA is unsound for reflection and imprecise for function values stored in struct fields.

### Changed

- repairs: a refused apply run state (a writer or stand-down that would not start) is cached for the attempt instead of being rebuilt per row. The refusal is logged once, the stand-down is asked for once, and every row of the attempt still fails with the same error; a resumed attempt builds again.
