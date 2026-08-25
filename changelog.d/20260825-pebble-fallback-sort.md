### Fixed

- **Library sorting returned an ordered page of the wrong books when memdb was
  unavailable.** While the in-memory index is not serving reads — during the
  startup warmup, when it is disabled, or permanently after its write buffer
  overflows — the store picked a page of books in storage order and only then
  sorted it. Because the page it handed back was internally in the right order,
  nothing looked wrong: asking for the 50 oldest books returned 50 books
  correctly ordered by year that were not the 50 oldest. The page is now chosen
  from the fully ordered set, so sorting means the same thing on both backends.
