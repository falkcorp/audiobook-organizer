### Changed

- The author-merge maintenance tests (strip-merge, title-as-author relink, conjunction repair, duplicate merge and the two manual-only guards) no longer carry real people, real book titles or a real publisher name in their fixtures. Every person is now a Batman character and every title a Batman storyline, with the same shapes the tests depend on (a numbered twin that strips to the real author's name, a title-as-author row, a case-variant duplicate, an initials-style name, a conjunction-joined pair). Test-only; no behaviour changes and the number of passing tests is unchanged.
