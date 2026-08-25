## `recoverPebbleClosed` does not cover the WAL-write leg, so teardown still panics

`recoverPebbleClosed` (`internal/database/pebble_store_ops_v2.go:776`) exists to
turn "registry torn down without Shutdown" panics into errors. It recovers **only**
`pebble.ErrClosed` and deliberately re-panics anything else, so real bugs are not
masked.

A write to a closing store does not surface `pebble.ErrClosed`. It surfaces the
WAL writer's own error:

```
panic: pebble/record: closed LogWriter [recovered, repanicked]
```

`[recovered, repanicked]` is the proof — the guard ran, `errors.Is(recErr,
pebble.ErrClosed)` was false, and it re-raised. So the guard is a no-op on this
leg.

Observed in CI 2026-08-25, `Minimal CI / Go Tests (short, race)`, run
32819653444, failing `internal/server`. Stack:

```
dbReporter.flushLoop -> dbReporter.flushProgressLazy -> UpdateOpProgressV2
  -> pebbleSetJSON -> pebble.DB.Set -> commitWrite -> panic
```

The guard's own doc lists the legs it was built from — `ListWaitingDepsOps`,
`ListQueuedOperationsV2`, `UpdateOpProgressV2` — which were all observed as
**reads**. `UpdateOpProgressV2` also writes, and that path was never exercised
into the guard.

This is a **flaky test failure, not a product bug in the caller**: the reporter's
background flush loop races store teardown, so it fires only when the timing
lines up. It will keep failing PRs at random until fixed.

- [ ] Widen the sentinel check to cover the WAL-write error as well as
      `pebble.ErrClosed`, without widening it to "any panic" (the doc is explicit
      that masking real bugs is not acceptable). `record.ErrClosedLogWriter`, or
      a string check as a last resort, plus a test that writes to a closed store.
- [ ] Better: make the registry's `dbReporter` stop its flush loop before the
      store closes, so the guard is not load-bearing in tests at all. The guard's
      own warning text ("likely a registry torn down without Shutdown") already
      names this as the real cause.

Lane: `internal/database` / `internal/operations/registry`. Found from an
unrelated PR (#2888, scanner/metadata only — touches no file on that stack).
