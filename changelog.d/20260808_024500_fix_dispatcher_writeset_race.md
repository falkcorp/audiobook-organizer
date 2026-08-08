### Fixed

#### Data race on the dispatcher's write-set deferral map

`Registry.writeSetDeferred` — the log-dedupe map added with the Gate 3b
write-set conflict gate — was read, ranged, and deleted from at six sites in
`internal/operations/registry/dispatcher.go` with no locking at all, while the
`Registry` already carried an `mu sync.RWMutex` used for its sibling state
(`pluginRunning`, `concurrencyKeys`, `running`). `-race` caught it as a
concurrent map read at the prune in `dispatchCycle` against a `mapdelete` from
a second `dispatchCycle` on the same `Registry`, failing
`TestServerStartGracefulShutdown` and with it the Coverage Floor gate on an
unrelated docs-only PR.

The code carried an explicit comment asserting the map was safe:

    // writeSetDeferred is only ever touched from the dispatcher goroutine
    // (dispatchCycle is single-caller), so it needs no locking

That is true within a single `Start()`, which spawns exactly one dispatcher
goroutine — but `Start()` is deliberately restartable after `Shutdown()` (it
clears `notifyStopped` for precisely that case) and neither waits for nor
excludes a prior dispatcher still draining. During a restart two
`dispatchCycle` calls therefore overlap on the same `Registry`, and the
assumption breaks.

All six accesses are now guarded by `r.mu`. The dedupe check-and-set in
`logWriteSetDeferral` takes a single acquisition rather than two: splitting it
would let two goroutines both miss the entry and emit the same line twice,
which is the exact log spam the dedupe exists to suppress. No call site held
`r.mu` already — the Gate 3b path releases it before logging — so no lock
ordering changed and there is no deadlock risk.

Verified with `go test -race`: `internal/server` (the failing test, 3
consecutive runs) and `internal/operations/registry` both pass with zero race
reports.
