Deleted production functions in `internal/operations`, `operations/freshness`,
`operations/opmode`, `operations/registry` and `internal/scheduler` that no
binary could reach, along with the tests that covered only them: the whole
`PebbleFreshness` implementation (its constructor, methods and key helpers;
the `OpFreshness` interface stays), `opmode.Preview`,
`registry.WithRequires` (and the `EnqueueOptions.Requires` field that only it
set), `dbReporter.droppedLogCount`, and `operations.IsManual`.
`TaskDefinition.selfScheduled` moved into the scheduler's tests, which use it
to mirror `TaskScheduler.Start`'s timer branches. Running behavior does not
change.
