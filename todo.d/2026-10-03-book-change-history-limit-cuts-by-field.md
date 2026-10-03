- [ ] **HISTORY-LIMIT-FIELD-ORDER** `PebbleStore.GetBookChangeHistory(bookID, limit)`
      is not newest-first across fields. Keys are
      `metadata_change:<book>:<FIELD>:<nanos>`, so the scan comes back sorted
      field by field. The function reverses that list and cuts it at `limit`
      (`internal/database/pebble_store_metadata.go`, `GetBookChangeHistory`).
      Any caller passing a limit therefore drops whole fields, starting with
      the ones early in the alphabet, whatever their age:
      - the activity changelog (limit 100);
      - `revert_metadata_fetch` (limit 50). Its "fetched" rows of
        `author_name` and `description` sort first, so they are the first
        to be cut;
      - the version-group fixer and `retire_into` (limit 200).

      Since 2026-10-03 operation reverts record history rows, and those rows
      land in late-sorting fields (`series`, `marked_for_deletion`,
      `is_primary_version`), which makes the cut slightly worse.
      Callers that need correctness today must read with `1<<30` or field
      by field (`GetMetadataChangeHistory`), as the relink-stale-series fixer
      does.

      Done means: the function returns rows newest-first across fields before
      applying `limit`, by merging the per-field iterators on `ChangedAt` or
      by adding a time-ordered secondary index. Also add a test where 200
      newer rows of a late field must not hide an older row of an early field
      beyond the limit, and an older row must not displace a newer one.
