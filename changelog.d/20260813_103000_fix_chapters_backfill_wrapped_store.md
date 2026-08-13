### Fixed

#### Chapter backfill op could not run in production — the store is wrapped

`maintenance.chapters-backfill` shipped hours earlier and refused on its very
first production run:

```
cannot run: store is *server.indexedStore, which does not persist chapters
```

`deps.Store()` returns `Server.store`, which is **replaced** by the
`*server.indexedStore` decorator at `server_lifecycle.go:290` once the search
index opens. That decorator embeds `database.Store`, so only methods on *that*
interface are promoted — and the chapter methods are not on it. The op's bare
`store.(chapterPersister)` therefore failed against every real server while
passing every test, because the tests handed it a raw `*PebbleStore` that
production never uses.

Resolution now goes through `database.AsCapability`, which walks the decorator
chain. This is the **third** recorded instance of this exact bug: `AsPebbleStore`
already documents two prod jobs silently degraded for weeks by the same wrapper.
Those failed silently because they had non-Pebble fallbacks; this one refused
loudly, which is the only reason it surfaced in a single run.

The test helper now wraps every case in a production-shaped decorator, so the
undecorated path is no longer reachable from the suite, plus an explicit
double-wrapped case proving the chain is walked rather than unwrapped once.
Verified by restoring the bare assertion: all 7 tests go red.
