- [ ] **VGBACKFILL-SCAN-BOUNDS** The version-group backfill scans only ~13% of
      the library. Its Pebble iterator bounds exclude most book rows, and the
      run reports a clean `complete` regardless — so it looks like a full
      rebuild.

      **Measured on prod 2026-08-10, one boot, same process:**

          memdb warmup complete    books=366922 ...
          versiongroup-backfill: complete  scanned=48874 indexed=37377
              no_version_group=11497 unmarshal_errors=0 commits=4
              duration=12.943s

      48,874 / 366,922 = **13.3%**. The backfill declared success and set its
      sentinel, so it will not re-run: the remaining ~86.7% of books stay
      unindexed until the sentinel key is bumped again.

      **Suspected cause — iterator bounds, NOT the row filter.** The scan uses:

          LowerBound: []byte("book:0")
          UpperBound: []byte("book:;")

      `'0'` is `0x30` and `';'` is `0x3B`, so the range admits only IDs whose
      first byte is `0x30`–`0x3A` — the ten digits (plus `:`). Any book ID
      beginning with a letter is outside the range and is never visited.

      New books get ULIDs (`CreateBook` → `newULID()`), and a ULID minted this
      century starts with `'0'`, so ULID-keyed books ARE in range. But
      `CreateBook` only mints an ID `if book.ID == ""` — a caller may supply its
      own, and importers do. That is the likely source of the ~318k invisible
      rows.

      ⚠️ **NOT YET CONFIRMED**, and it must be before any fix: nobody has
      sampled real prod book IDs. The two numbers could also disagree because
      `books=366922` counts something the `book:<id>` keyspace does not
      (memdb rows built from a different source, tombstoned rows, etc.). Get
      actual key samples first — the `.api-token` at repo root is stale (401),
      so this needs a fresh credential.

      **This is pre-existing, not a regression from #2295.** The bounds are
      unchanged from the original implementation; #2293 replaced a substring
      blacklist with a `strings.Count(key, ":") != 1` structural filter and
      #2295 fixed the decorator so the backfill runs at all. Both are why the
      numbers are now visible: before tonight this code had never executed in
      production, so it under-scanned silently and invisibly.

      **Fix sketch (after confirming):** drop the bounds to a prefix scan over
      `book:` → `book;` and let the existing one-colon structural filter reject
      secondary indexes, which is what it was written to do. Then bump
      `versionGroupBackfillKey` to `_v3_done` so every deployment rebuilds.
      Assert on scanned-vs-total, not just on absence of error — a `complete`
      line that under-scans by 87% is the same class of defect as the parse that
      "succeeds" on garbage.

      Related: the under-reporting version-group index reproduced in #2277 is
      only partly explained by the never-running backfill if this holds — most
      books were never candidates for indexing in the first place.
