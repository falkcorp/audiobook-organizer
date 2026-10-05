### Fixed

#### Every owner-manual-only check now has a test that fails without it

Five checks in the Doctor Who / Big Finish bulk-apply rule had no test that failed when they were removed:
- the book's own title, in `ManualOnlyDetail`;
- the candidate's title, in `ManualOnlyDetail`;
- the book-level transcribed title, in `BulkManualOnlyGuard`;
- the search query, in `BulkManualOnlyGuard`;
- the file-level transcribed title, in `BulkManualOnlyGuard`.

Each now has a case in which it is the only signal, and that case asserts the field named in the refusal. A new table test (`TestBulkManualOnlyGuard_EachLegAlone`) covers every leg of `BulkManualOnlyGuard` this way. The gate table also fails any refusing case that does not say which field should hold it.

The import-root cache now counts a waiter that timed out separately from one that found a list arriving just after its timeout. A deterministic test covers the second path.
