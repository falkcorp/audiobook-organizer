Deleted production functions in `internal/activity`, `internal/cache`,
`internal/metrics`, `internal/realtime`, `internal/search`,
`internal/searchcache`, `internal/serviceregistry`, `internal/syncapi/progress`
and `internal/versionprimary` that no binary could reach, along with the tests
that covered only them: `activity.WithTags`, `cache.All`,
`metrics.IncFilenameParse`, `realtime.GetGlobalHub`,
`realtime.InitializeEventHub`, `search.ReindexBookByID`,
`serviceregistry.Groups`, `progress.MergeCombine` and
`versionprimary.TierName`. `searchcache.Cache.jobHoldsIDs` moved into the
searchcache tests, which are its only caller, so the job-memory test keeps its
coverage. `merge.FollowMergeWithStore` was already gone (01-P73).

With its only readers deleted, the `realtime.GlobalHub` variable, its mutex and
`realtime.SetGlobalHub` were a write-only global, so they and their two call
sites (server start-up and the integration-test setup) are removed too. The
server still hands its own hub to every consumer through `Server.hub`.

`metrics.IncFilenameParse` was the only writer of the
`audiobook_organizer_filename_parse_total` counter, so the counter, its
`ParseShape*` and `ParseOutcome*` label constants, its row in the `/metrics`
series contract (`series.golden`) and the two "Filename parse" panels of the
overnight Grafana dashboard (dashboard version 2) are removed with it. The
series never carried a sample in production because nothing incremented it, so
no panel loses data. `search`'s internal store interface drops `GetBookByID`,
which only `ReindexBookByID` called. Running behavior does not change.
