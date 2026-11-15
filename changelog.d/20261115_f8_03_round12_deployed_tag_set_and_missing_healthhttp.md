### Changed

- core: the real-binary test of the F8-03b refusal now builds the deployed tag set (`embed_frontend fts5 native_taglib pprof`, `make build-linux`). `archtest.F803bShippedTagSets` resolves make variables in `-tags` lists (`$(GO_BUILD_TAGS)`, from `=`, `:=`, `?=` and `+=` assignments, nested), fails on any `-tags` argument it cannot reduce to a concrete list instead of dropping it, and the test pins the deployed set. Tag sets that select the same files in the probe binary's dependency closure are built once, two builds at a time.
- core: the F8-03b guard's build-constraint rule fails when `internal/healthhttp` is not among the loaded packages, instead of skipping it without a word.
