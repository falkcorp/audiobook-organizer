## Applied books stay in the list in BulkMetadataSearchDialog

`web/src/components/audiobooks/BulkMetadataSearchDialog.tsx:157`:

```ts
const filteredBooks = skipApplied
  ? books.filter((b) => b.metadata_review_status !== 'matched')
  : books;
```

The filter reads `metadata_review_status` off the `books` **prop**, which the
dialog never refreshes, and ignores `bookStatuses` — the local map that records
what was applied in this session (set at :267 and :300). So applying a book marks
the button "Applied" and disables it, but the book stays in the list. `skipApplied`
also defaults to `false` (:139), so nothing is filtered at all until the reviewer
finds the toggle.

- [ ] Make the filter session-aware (also exclude `bookStatuses.get(id) === 'applied'`).
- [ ] Decide whether `skipApplied` should default to `true`.

**Cost, stated honestly:** this is not a one-line change. `currentIndex` indexes
into `filteredBooks`, and `advanceToNext` (:358) increments it. Removing the
current book from the list shifts every later index down by one, so a naive fix
makes the wizard skip a book on every apply. The fix needs `currentIndex` handled
together with the filter, plus tests for apply-then-advance at the list boundary.

Distinct from the review lane's queue (`useMetadataLane`), whose equivalent bug
was fixed 2026-08-29 — this dialog is the per-book wizard reached from the
audiobooks list.
