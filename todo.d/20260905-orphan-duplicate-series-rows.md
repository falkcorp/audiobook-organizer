## Orphan duplicate series rows (found fixing the ABS black tiles, 2026-09-05)

- [ ] Prod has 43,592 `series` rows for 83,229 books. "The Primal Hunter" exists 8
      times; 16 of the 25 search hits for "primal hunter" had no visible books (orphan
      duplicates, numbered variants like "01 The Primal Hunter 9_", "(Unabridged)"
      variants). ABS search now hides empty series (#3072) but `/api/libraries/:id/series`
      still lists every one with `numBooks: 0`. Census the rows with zero referencing
      books, then decide merge-into-canonical vs delete; the series-number-in-name
      cases are the `series that were really book numbers` defect again.
- [ ] `GET /api/v1/series?search=…` (also `q=`, `name=`) ignores its filter and returns
      all 43,592 rows. Verify the param is consumed, not just parsed.
