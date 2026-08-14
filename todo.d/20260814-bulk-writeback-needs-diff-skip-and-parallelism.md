- [ ] **`bulk-write-back` cannot do the approved E08 library-wide run as-is.**
      Canary (100 books, op `01M00PGZKA0KBMPTZMAJTEKPD5`, 2026-08-14) measured
      ~35 s/book, strictly serial, and 23/23 processed→written — it rewrites
      tags unconditionally instead of skipping files whose tags already match
      the DB. Library-wide (~40K organizer-tree books) is weeks, not the
      approved nightly window. Before the full run: (1) add a tag-diff skip
      (probe ≈1 s vs rewrite ≈35 s; only mismatched files rewritten — also
      turns the op into a usable "how many books actually have stale tags"
      census); (2) bounded worker pool inside the op per the concurrency
      mandate (`RunBulkWriteBack`,
      `internal/server/server_maintenance_deps.go:44`) — the ConcurrencyKey
      serializes whole ops, so chunk-parallelism across ops is not available.
      Owner approval for the full run already given (2026-08-14); only these
      prerequisites block it.
