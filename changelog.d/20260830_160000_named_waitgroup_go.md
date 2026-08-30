### Changed

#### `namedWaitGroup` gained a `Go(name, fn)` method, closing out the `internal/server` sweep

The previous change converted twenty plain `sync.WaitGroup` sites in
`internal/server` to `wg.Go`, and explicitly left behind the nineteen background
goroutines tracked by `s.bgWG`, a `namedWaitGroup` whose API is `Add(name)` /
`Done(name)`. Those nineteen are now converted too.

`namedWaitGroup.Go(name, fn)` registers `name`, starts `fn` in a new goroutine,
and deregisters `name` when `fn` returns. It is the `sync.WaitGroup.Go` shape
without giving up the reason `namedWaitGroup` exists: the name registry, so that
when the 30-second shutdown grace period expires, `Server.Shutdown` logs *which*
goroutines are still running instead of an opaque count. `Running()` is unchanged
and still feeds that log.

The `Add` stays synchronous in the calling goroutine, before `fn` is started — the
type's own doc comment requires it ("Must be called before the associated
goroutine is started") and `Wait()` correctness depends on it. This matters at one
site in particular: the library-list trickle warmer is enrolled from inside the
eager warmer's own goroutine, and a comment there records that the registration
happens while the parent's entry is still held, so it can never race a completed
`bgWG.Wait` in `Stop()`. Moving the `Add` inside the new goroutine would have
broken that; it was not moved.

All nineteen sites had the identical shape — `Add(name)` immediately followed by
`go func() { defer Done(name); … }()` in the same function — so the rewrite is
mechanical and behavior is unchanged, including defer ordering: the cache warmers'
own `defer warmerRecover(…)` still runs inside `fn`, before the deregistration.

Also added the first direct unit tests for the type (`bg_wg_test.go`): that a name
is visible to `Running()` synchronously once `Go` returns, that a recovered panic
in `fn` still deregisters, and that two goroutines sharing one name are both
tracked. Nothing previously asserted that `Running()` reports names — the existing
`cache_warmers_bgwg_test.go` only calls it inside a timeout `t.Fatalf` — and the
shutdown-grace log line that consumes it in production remains untested.
