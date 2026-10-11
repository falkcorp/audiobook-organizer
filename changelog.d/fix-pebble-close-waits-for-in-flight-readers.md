### Fixed

#### Closing the database no longer crashes a scan that is still running

`PebbleStore.Close` called pebble's `DB.Close` while other goroutines could still hold an iterator, snapshot or `Get` closer. Pebble drops the DB's memtable references on Close without waiting for those readers. A reader released afterwards unref'd memtables that were already gone and panicked with `pebble: inconsistent reference count: -1`. That panic is not `pebble.ErrClosed`, so `recoverPebbleClosed` re-raised it and the process died. CI hit this in `TestOpsV2Timeline_CloseRacesReconcile`: the startup timeline reconcile's snapshot iterator outlived Close. In production the same thing could happen on a shutdown or restart during any long scan. On a cleaner interleaving the same violation showed up as Close returning `leaked iterators` or `leaked snapshots`.

The store now reaches the engine only through a gated handle (`internal/database/pebble_gate.go`: `GatedDB`, `GatedIter`, `GatedSnapshot`, `GatedBatch`). The stores that borrow the main DB (embedding, activity, metrics, AI-scan, catalog, split-book candidates) get the same handle from `PebbleStore.DB()`. Every engine access holds a lease:

- iterators, snapshots and `Get` closers keep it until they are closed;
- writes, commits, flushes, compactions and metrics keep it for the duration of the call.

Close refuses new leases, waits for the held ones to be released, and only then closes the engine. Leases are a non-blocking counter rather than a lock, so nested reads and ops that call other ops cannot deadlock against a pending Close. Close also drains before taking any store mutex.

The lease count and the closing flag share one atomic word. With two separate atomics, a reader dropping the last lease could decide "drained" on a stale zero while a new lease was being counted, and the engine could close under it. A deterministic test now forces that interleaving.

After Close begins, new accesses get an error that wraps `pebble.ErrClosed` instead of a panic. The wait is bounded at 10 s. If readers are still holding leases at that point, Close leaves the engine open and returns an error, because closing it under them would crash. That error comes only after a WAL sync. pebble v2.1.7 keeps a `NoSync` WAL record in process memory until a later sync, a full block or engine close, so Close now syncs the WAL itself in three places: right after it starts refusing new work, after the drain, and after a drain timeout. A row committed `NoSync` before shutdown is therefore on disk even when the engine is left open. `GatedDB.SyncWAL`, the sync-only record that the book_file and dedup-candidate writers issue after a `NoSync` commit, is still admitted during the drain, so those writers no longer get `ErrBookFileDurabilityUnknown` at shutdown. Close is idempotent, including concurrent calls; a second Close used to panic `pebble: closed`. The OpenLibrary store uses the same gated handle, so its lookups no longer race its Close. None of the gated types embed a pebble type, so a new engine path cannot slip past the gate.

The startup timeline reconcile now also stops as soon as Close begins, so shutdown does not wait for a full pass. Two playback-stats existence checks that discarded their `Get` closer (leaking a pebble iterator each time) now close it.
