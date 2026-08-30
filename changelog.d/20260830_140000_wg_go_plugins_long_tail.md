### Changed

#### `sync.WaitGroup` goroutine launches converted to `wg.Go` across the plugin packages and the small-package long tail

Twenty-one `wg.Add(1)` / `go func() { defer wg.Done(); ... }()` pairs in
`internal/plugins/` and eleven smaller packages (`transcribe`, `scheduler`,
`maintenance`, `itunes`, `errhandling`, `watcher`, `reconcile`, `organizer`,
`openlibrary`, `metafetch`, `logger`) now use `sync.WaitGroup.Go`, which does the
`Add` and the `Done` itself. There is no behaviour change — the counter is
incremented and decremented at exactly the same points — but the two halves of
the pair can no longer drift apart, which is the failure mode that leaks a
goroutine or panics with a negative counter.

Six of the twenty-one took explicit parameters rather than closing over their
values. Those arguments are now hoisted into locals immediately before the call
so the snapshot still happens at launch time, not at goroutine-execution time:
`go func(a, b)(x, y)` evaluates `x, y` at the `go` statement, and a plain closure
would not. Two more (`acoustid.duration-backfill`, `acoustid.fingerprint-rescan`)
released a semaphore and dropped the counter in one combined `defer`; the
semaphore release stays a `defer` inside the body, so it still runs before the
counter drops.

Most `.Add(1)` calls in `internal/plugins/` are `atomic.Int64` result counters,
not WaitGroups — 82 of the 87 there, and 121 of the 142 in the whole scope — and
were left alone. Also left alone: `itunes/service/path_repair_resolver.go`, which
uses a single `wg.Add(workers)` outside its loop rather than one `Add` per
goroutine, so it has no one-to-one pair to convert.
