<!-- file: todo.d/20260807_020500_memdb_warmup_caller_pointer_race.md -->
<!-- version: 1.0.0 -->
<!-- guid: d3f81a56-9c47-4e20-b7d8-52069fe1c4a3 -->
<!-- last-edited: 2026-08-07 -->

- [ ] 🔴 **Data race: `UpsertBookToMemDB` retains the CALLER's `*Book` and
  dereferences it later on the warmup goroutine.** Caught by the race detector
  on CI during PR #2170 (a parser PR that touches no database file). Diagnosed,
  not fixed — the fix lands in the production memdb write path and deserves its
  own PR with a regression test.

  **The race, verbatim from CI:**

  ```
  WARNING: DATA RACE
  Read at 0x00c000a96388 by goroutine 13725:
    database.stripBookForMemdb()        memdb_strip.go:33      // cp := *src
    database.UpsertBookToMemDB.func1()  memdb_sync.go:123
    database.applyMemSync()             memdb_sync.go:92
    database.publishWarmMemStore()      memdb_pending.go:211
    database.NewPebbleStore.func1()     pebble_store.go:320    // async warmup

  Previous write at 0x00c000a96388 by goroutine 13700:
    database.(*PebbleStore).UpdateBook() pebble_store.go:1827  // book.ID = id
    database.TestBook_TranscribeFields_RoundTrip()
                                        transcribe_stats_test.go:99
  ```

  **Mechanism.** `UpsertBookToMemDB` (`memdb_sync.go:114`) captures the caller's
  `book` pointer in a **closure** and hands it to `p.memSync`. While the store is
  still warming, that closure is not run inline — it is queued as a pending op
  and applied later by `publishWarmMemStore` → `applyMemSync`. So
  `stripBookForMemdb(book)`'s `cp := *src` reads the caller's **live** struct at
  an arbitrary later time. `CreateBook` (`pebble_store.go:1812`) and
  `UpdateBook` (`:2060`) both pass the caller's pointer in, and `UpdateBook`
  itself writes to it (`book.ID = id`, `:1827`).

  **Why it matters beyond the test.** This is not a test-only bug. Any caller
  doing the ordinary

  ```go
  b := &Book{...}
  store.CreateBook(b)
  b.SomeField = x        // caller mutates its own struct
  store.UpdateBook(b.ID, b)
  ```

  races with warmup whenever the store is still warming — which is exactly
  startup, when backfills and migrations run. A torn read here writes a
  half-updated Book projection into memdb. Same family as the memdb warmup
  write-loss fixed in #2166 and [[feedback_memdb_roundtrip_footgun]].

  **The fix (one line, at the enqueue boundary):** snapshot the struct when the
  op is *queued*, not when it is *applied*.

  ```go
  func (p *PebbleStore) UpsertBookToMemDB(ctx context.Context, book *Book) {
      if book == nil { return }
      snapshot := *book // copy NOW — the closure may run much later, on another goroutine
      p.memSync("UpsertBook", func(txn memTxn) error {
          if err := txn.Insert(memTableBooks, stripBookForMemdb(&snapshot)); err != nil {
  ```

  Check the sibling upserts (`UpsertBookFileToMemDB`, author/series equivalents)
  for the same shape before calling it done — the closure-captures-caller-pointer
  pattern is likely repeated.

  **Reproduction is timing-dependent.** The full `internal/database` package
  under `-race` passed locally (0 races, 305s) and `TestBook_TranscribeFields_
  RoundTrip` passed 15/15 in isolation; it fired on CI under coverage
  instrumentation. 🔴 Do NOT treat a green local run as evidence the race is
  gone — the regression test must force the interleaving (e.g. mutate the caller
  struct immediately after `CreateBook` while warmup is still pending) rather
  than hoping to catch it.
