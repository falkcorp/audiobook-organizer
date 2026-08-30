## testing/synctest adoption — remaining candidates (follow-up to the scoped pilot)

The pilot converted six tests across three packages (`internal/realtime`,
`internal/backup`, `internal/itunes/service`), removing ~35s of wall-clock
sleeping. This is the triaged backlog for the rest of the 84 files that contain
`time.Sleep` / `time.After` / `time.NewTicker` in `*_test.go`.

**The discriminator, stated once:** a synctest bubble's fake clock only advances
when EVERY goroutine in the bubble is *durably* blocked. Parking in netpoll, in a
signal wait, or in a subprocess wait is explicitly NOT durable, so those bubbles
freeze and the test hangs rather than failing. A syscall that *returns* (a file
write, a hash, an `os.Rename`) is fine. Classify by "does a goroutine park
somewhere only the outside world can wake it", not by "does this touch the disk".

### Tier 1 — highest value, do next

- [ ] **`internal/operations/registry` (24 test files, ~80 waits, ~5.7s of
      `time.Sleep`).** The single biggest remaining block. Every one of these
      tests drives the registry against `newFakeStore()`
      (`teststore_test.go:52`), a pure map-backed in-memory store — no Pebble, no
      netpoll, no subprocess. The shapes are textbook synctest: watchdog tickers,
      shutdown-drain timeouts, dispatcher dependency gates, abandoned-op sweeps.
      The 25 `time.After(5 * time.Second)` guards would become exact rather than
      generous. **Known obstacle:** `registry.Start(ctx)` spawns worker and
      sweeper goroutines, and `synctest.Test` panics if any bubbled goroutine is
      still alive when the function returns — every converted test must reach a
      clean `Shutdown` inside the bubble. Do this package as its own PR, one file
      at a time, not as a sweep.
      Exceptions inside the package: `promote_realstore_test.go` and
      `registry_pebble_race_test.go` use a real Pebble store — leave both alone.

- [ ] **`internal/realtime/events_test.go` — the remaining 25 waits.** The 26s
      heartbeat is already converted. The rest are 50–100ms handoffs and three
      tests that wait out a 100/200/300ms `context.WithTimeout`. Same package,
      already proven bubbleable, ~1s of wall clock plus a real determinism win.
      **Landmine to design around first:** `HandleSSE` builds its client ID from
      `time.Now().UnixNano()` (`events.go:225`). Inside a bubble that is a
      CONSTANT, so two clients registered in the same bubble collide on ID and
      the second silently displaces the first in `hub.clients`. Any multi-client
      test must either register clients at different fake instants or the ID
      derivation must change. This is a real, currently-latent aliasing bug that
      only a bubble exposes.

- [ ] **`internal/activity/batcher_test.go` (4 waits, 2.2s package).** Flush-timer
      and item-count-threshold tests against an in-memory batcher. Small, clean,
      high-confidence.

### Tier 2 — worth doing, needs a per-test read

- [ ] `internal/metadata/circuitbreaker_test.go` — open/half-open/closed state
      transitions are pure timer logic. Ideal shape.
- [ ] `internal/scheduler/periodic_library_scan_test.go`, `full_sweep_test.go` —
      interval scheduling; check for a real store first.
- [ ] `internal/metrics/metrics_test.go`, `internal/plugin/events_test.go`,
      `internal/cache/cache_test.go`, `internal/tools/embed_queue_test.go` —
      small, in-memory, debounce/TTL shapes.
- [ ] `internal/scanner/process_file_timeout_test.go` — timeout logic; confirm it
      does not actually shell out to ffprobe in the timing window.

### Tier 3 — DETERMINISM ONLY, not seconds

Roughly 60 of the 143 `time.Sleep` calls are 5–20ms "let the other goroutine
run" handoffs. The correct fix for those is **`synctest.Wait()`**, not the fake
clock: it buys milliseconds but removes a real class of CI flake. File these as
flake-removal, and do NOT count them as runtime savings.

### Do NOT convert — with the reason

- [ ] `internal/server/server_more_test.go` :: `TestServerStartGracefulShutdown`.
      **Measured against go1.26.0 on 2026-08-30, two independent fatal
      mechanisms.** (1) `signal.Notify` deadlocks a bubble outright — its enable
      path blocks on runtime sigqueue, which nothing in the bubble can service:
      `panic: deadlock: all goroutines in bubble are blocked`, raised at the
      Notify call before any signal is sent. (2) `httpServer.ListenAndServe`
      leaves an accept goroutine in netpoll, so the fake clock never advances and
      the 6s sleep never returns — observed as `[sleep (durable), synctest bubble
      1]` beside `[IO wait, synctest bubble 1]`, killed by timeout. Converting
      only the shutdown-path waits fails the same way. The full rationale is now
      in the test's own doc comment.
- [ ] `internal/watcher/*`, `internal/itunes/library_watcher_test.go` — fsnotify
      delivers events from an OS-level watcher outside any bubble.
- [ ] `internal/plugins/webhook`, `internal/mtls`, `internal/metadata/audnexus_test.go`
      — real `httptest.NewServer` listeners: netpoll, same freeze as above.
- [ ] `internal/transcode/transcode_coverage_test.go` — spawns ffmpeg/ffprobe.
- [ ] Everything using a real Pebble/NutsDB store in the wait window
      (`internal/database/*`, `internal/undo`, `internal/merge`,
      `internal/dedup/book_dedup_concurrent_test.go`,
      `internal/server/maintenance_*_test.go`, `internal/server/*_undo_test.go`).
      A fake clock does not make an fsync faster.

### Separate item — the `t.Parallel()` prohibition in package `server`

`TestServerStartGracefulShutdown` sends a process-wide SIGTERM, which forbids
`t.Parallel()` in **all 41 test files** of package `server`. synctest does NOT
lift this — see mechanism (1) above: a bubble cannot contain a process-wide
signal, it cannot even subscribe to one. Lifting it needs a different mechanism:
re-exec the test binary as a subprocess behind an env guard so the SIGTERM is
contained, or drive the shutdown path directly instead of through a real signal.
Given `internal/server` is the slowest package in the suite (~275s), unlocking
`t.Parallel()` there is worth more than every synctest conversion above combined.
