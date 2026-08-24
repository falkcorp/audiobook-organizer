## 🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration

Found 2026-08-24 by mutation-testing PR #2861. The duplicate rows are **pre-existing** and
not created by that change; what changed is where the damage lands.

`internal/database/pebble_store_bookfiles.go` — `BatchUpsertBookFiles` matches an existing
row via `GetBookFileByPath`, which reads `s.db.Get`, i.e. **committed** state. Row 2 of a
batch therefore cannot see row 1 sitting in the still-uncommitted `pebble.Batch`. Two rows
sharing a `FilePath` in one batch both miss the match, both get fresh ULIDs, and both land
under distinct `book_file:<bookID>:<id>` keys.

Measured on a two-row batch sharing one path:

```
rows stored for the duplicated path: 2
resulting Book.Duration: 1200   (single-row truth: 600)
```

Before #2861 the duplication stayed confined to `book_file` rows that nothing summed.
`BatchUpsertBookFiles` now recomputes aggregates, so the duplicate is summed into
`Book.Duration` and `Book.FileSize` and becomes visible to users.

**This is not a regression to revert.** Never recomputing was strictly worse. But the
duplication is now user-visible and should be fixed at the source.

### Why the test suite cannot see it

`sumStoredFileAggregates` in `batch_upsert_aggregates_test.go` derives expected values from
the **stored** rows, so a duplicated row is summed on both sides of the comparison and the
assertion still balances. That derivation is deliberate — it is what makes the helper
survive `normalizeBookFileDuration` (CONS-18) rewriting durations on the way in — so this is
a known blind spot rather than a test defect. It is now named as one in the helper's doc.

### Fix

De-duplicate within the batch: keep a `map[string]*BookFile` keyed on `FilePath` (and on
iTunes PID, which has the same read-committed problem) for the rows already staged in this
batch, and merge a later row into the earlier one instead of writing a second key.

- [ ] Dedup by FilePath within a single batch, before staging
- [ ] Same for iTunes PID — `enforceBookFilePIDUniqueness` has the identical read-committed gap
- [ ] Regression test: batch two rows with one path, assert 1 stored row and the un-doubled total
- [ ] Decide whether existing duplicate rows need a repair pass, and measure how many exist
