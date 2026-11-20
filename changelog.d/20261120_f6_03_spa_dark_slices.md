### Fixed

#### Core event-stream client: the #4785 round-2 flip preconditions (F6-03 E1)

The new `web/src/services/coreEvents` client (still unwired) no longer reconnects at once when a stream is resubscribed under a standing `Retry-After`. Its backoff lives in the manager per stream key, and a 429 or 503 `Retry-After` now holds back every stream in the tab, including a new keyed stream. By default, resync handlers take only the shared stream's resets, merge a burst into one call and run at most once every 5 seconds, so opening a run's log or a flapping server no longer refetches every view. The shared stream drops a keyed-only topic even for `*` subscribers. A reopen raises one resync, not two: a `gap` or `restart` reset sent as the frame right after an open that already resynced is swallowed, and an `overflow` reset never is.

### Added

#### Core event-stream adapters and an e2e stream mock (F6-03 E2, E3)

`bookChanges.ts` folds per-book `books.book.changed.v1` frames back into the `{kind, ids[]}` batches that Library's `applyBooksChanged` takes. It follows the legacy coalescer's rules: a fixed 250 ms window, deleted supersedes created and updated, flush order deleted/created/updated, at most 500 ids per batch. `shutdown.ts` watches for `core.stream.closed.v1{shutdown}` for the App backdrop. It holds the shared stream open itself, fires once per shutdown and detaches every listener it added. `web/tests/e2e/utils/mockCoreEvents.ts` is a fetch-stream mock for e2e: an init-script shim streams controllable frames (opened, events filtered by topic and key, reset, `closed{shutdown}`, 429 with `Retry-After`). A `page.route` guard records any request that reaches the network. Nothing imports any of this until the F6-03 flip; `eventSourceManager` stays until then.
