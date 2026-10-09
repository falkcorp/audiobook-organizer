### Changed

#### Filter benchmarks measure the production path (benchmarks only, no behaviour change)

Added `BenchmarkCompiledPredicate_100k` and `BenchmarkHeavyPushdownWalk_40kPrimary`, renamed the per-row-compiling `BenchmarkOwnerQuery_100k` to `BenchmarkOwnerQuery_100k_ConveniencePath`, and added `TestListPath_CompilesFiltersOncePerRequest` with a nil-by-default test hook in `compileFieldFilters`.
