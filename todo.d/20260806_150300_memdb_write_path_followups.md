<!-- file: todo.d/20260806_150300_memdb_write_path_followups.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7b09fd52-3e84-4c17-a1b6-5f2d80e94c63 -->
<!-- last-edited: 2026-08-06 -->

- [ ] **`UpsertBookToMemDB` holds go-memdb's global writer mutex across Pebble
  I/O.** Found 2026-08-06 while profiling `dedupe-book-file-rows` (fixed in
  #2161). This is a **system-wide ceiling on every `UpdateBook`**, not something
  specific to that op, and it is the natural next performance win.

  go-memdb has a single global writer mutex (`memdb.go:34-35`, `:73-76` — one
  writer at a time, `Txn(true)` takes `db.writer.Lock()`). Inside that lock,
  `UpsertBookToMemDB` performs three Pebble reads: `GetBookAuthors`
  (`memdb_sync.go:72`), `GetBookNarrators` (`:85`), and `loadBookFilesForBookID`
  (`:98` — a full prefix scan that unmarshals every remaining fingerprint-bearing
  row). Every other writer in the process waits on that I/O.

  Fix: fetch first, then take `Txn(true)`. Consequence worth stating — this is
  also why adding worker pools to book-level maintenance ops buys far less than
  `NumCPU×`: the workers serialize here regardless.

- [ ] **`DeleteBookFilesForBook` leaves stale memdb rows behind.** It never calls
  `DeleteBookFileFromMemDB` or `MarkQuickQueryDirty`, so Pebble and memdb diverge
  after it runs. Noticed 2026-08-06 while modelling `DeleteBookFilesByIDs` on it
  (#2161) — the new method does both; its model does not.

  Latent, and it pairs badly with the known "corrected aggregates are invisible
  until memdb refreshes" problem: a divergence here looks exactly like that
  staleness, so the two will be confused during diagnosis.
