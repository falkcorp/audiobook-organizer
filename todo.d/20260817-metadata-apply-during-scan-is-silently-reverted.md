### Applying metadata while a scan is running is silently reverted

There is no guard, warning, or queueing anywhere that stops a metadata apply from
racing an in-flight `library.scan`. When they overlap, the scan wins for a specific
set of fields and the user's apply is reverted with no error surfaced.

Hit live on 2026-08-17: a full `library.scan` was mid-run (≈19k of 30,562 books) when
metadata applies started. Books the scan had not yet reached were exposed.

**Mechanism** (`internal/scanner/scanner.go:2430-2452`). The rescan path is already
hardened against wholesale loss — an earlier data-loss bug wrote a partial `dbBook`
through full-replace `UpdateBook` and wiped fetched metadata, ratings and
transcriptions. The fix inverted it:

```go
merged := *existing                  // start from the COMPLETE existing row
applyScannerFields(&merged, dbBook)  // overlay ONLY scanner-authoritative fields
getStore().UpdateBook(existing.ID, &merged)
```

Anything outside `applyScannerFields` therefore survives by construction. The problem
is what is inside it. Each of these is overwritten whenever the scanner produced a
non-empty value (`if scanned.Title != "" { dst.Title = scanned.Title }`):

> `Title` `AuthorID` `SeriesID` `SeriesSequence` `Narrator` `Publisher` `Language`
> `ASIN` `WorkID` `OpenLibraryID` `HardcoverID` `GoogleBooksID`
> `Duration` `FileHash` `FilePath` `FileSize` `Format` `LibraryState` `Quantity`

`Title` is effectively always overwritten: when tags are empty the scanner falls back
to `extractInfoFromPath` (`scanner.go:1103`), so `scanned.Title` is virtually never
`""`. Provider IDs an apply just wrote (`ASIN`, `OpenLibraryID`, `HardcoverID`,
`GoogleBooksID`, `WorkID`) are in the overwrite set too.

Survives by construction: `Description`, `CoverURL`, `ISBN10`, `ISBN13`, `Edition`,
`PrintYear`, `AudiobookReleaseYear`, ratings, review status, quarantine, transcriptions.

**Two things that look like protection and are not:**

- `preserveExistingFields` — its own comment says it exists to "prevent rescan from
  wiping out data added by metadata fetch, AI parse, or manual edits", i.e. exactly
  this case. It has **one call site** (`scanner.go:2195`), inside the narrow branch
  where a book's file path moved. It is not on the general update path, and it omits
  `Title`/`AuthorID`/`SeriesID` regardless.
- The incremental-skip cache — a full run **deliberately disables** it
  (`scanner_reliability_test.go:99`: "an active full run must disable the shared
  incremental-skip cache"), so every book is processed and "unchanged file gets
  skipped" does not hold. `write_back_metadata` is `False` in prod anyway, so an
  apply never touches the file and could not mark it changed even if the cache were
  live.

**Proposed fix**, roughly in order of value:

1. Refuse or queue a metadata apply while a `library.scan` op is active, and say so in
   the UI. Cheapest, closes the hazard.
2. Narrow scanner authority: only claim a field when it was actually read from tags,
   not when it came from the `extractInfoFromPath` fallback. That fallback is a guess
   and should never outrank a fetched value.
3. Warn in the apply result when the applied book was re-scanned after the apply.

**Note for whoever picks this up:** the field list above was read at
`5dac7488`. Re-read `applyScannerFields` before relying on it — it is the kind of list
that grows silently.
