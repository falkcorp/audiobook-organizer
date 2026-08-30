### Changed

#### `internal/server` goroutines now start via `sync.WaitGroup.Go`

Twenty `wg.Add(1)` / `go func() { defer wg.Done(); … }()` pairs in
`internal/server` are now `wg.Go(func() { … })`. `sync.WaitGroup.Go` (Go 1.25+)
increments the counter, starts the goroutine, and decrements on return, so the
counter and its matching `Done` can no longer drift apart — the failure mode
being removed is a goroutine that returns down a path the `defer` did not cover,
or an `Add` whose `go` statement is deleted in a later edit, either of which
hangs `Wait()` or panics with a negative counter.

The five converted background goroutines in `Server.Start` (WebSocket system
metrics, cache-stats snapshotter, auto-scan watcher supervisor, session cleanup,
stale-operation sweep) are the ones that matter: they are joined by the 30-second
shutdown grace period, and they have no test coverage, so a counter mistake there
would only ever surface in production at shutdown.

`FileIOPool.worker` no longer calls `p.wg.Done()` itself; its single caller
starts it through `p.wg.Go`, which owns the counter. A comment on `worker` records
that contract for any future caller.

Behavior is unchanged everywhere. Deliberately NOT converted: the 19 goroutines
tracked by `namedWaitGroup` (`s.bgWG`), whose `Add(name)` / `Done(name)` API would
need a new `Go(name, fn)` method rather than a mechanical rewrite; the barrier in
`browse_unsupported_sort_test.go`, which uses `Add(n)` and a mid-body `Done()`;
and `namedWaitGroup.Add` itself, which increments for a goroutine started by its
caller.
