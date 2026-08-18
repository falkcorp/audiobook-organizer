### Changed

#### `ServerDeps` split from 43 methods into fourteen focused interfaces

`ServerDeps` in `internal/plugins/maintenance` was **the widest interface in the
repository** at 43 methods — wider than anything in `internal/database`, which is
where the interface debt had been assumed to live. Its own neighbouring comment
described it as a "25-method" interface, which had not been true for some time.

It is now assembled from fourteen interfaces of 1–7 methods, named for what they
do: `StoreProvider`, `MetadataRunners`, `SeriesRunners`, `MediaFileRunners`,
`CleanupRunners`, `ActivityLogOps`, `WriteBackOps`, `DedupRunners`,
`CacheInvalidator`, `TranscriptionRunners`, `StoreOptimizer`, `CapabilityProbes`,
`RuntimeConfig`, and `OpEnqueuer`.

The name is retained as their composition, so `*server.Server` still satisfies it
implicitly and the test fakes asserting `var _ ServerDeps = ...` compile unchanged.

The concrete payoff is in `plugin_test.go`, which skips three tests with
"requires full ServerDeps stub". An op that needs only `CapabilityProbes` and
`RuntimeConfig` can now depend on those two instead of stubbing all 43 methods.
