### Added

- **Fold junk co-owners (fragment repair).** A consolidation row held because
  another book also owns one of its files now gets a separate "Fold junk
  co-owner" row when that other book is the same work under a junk name: no
  title, a chapter-only title ("c5"), or the row's own title with a different
  leading number. Applying it retires the co-owner into the row's book, keeping
  its own file row (nothing is deleted); the next plan then finds the main row
  unblocked. A co-owner with a real title of its own, or with files outside the
  row, is still the owner's call. Owner decision 2026-10-03.
