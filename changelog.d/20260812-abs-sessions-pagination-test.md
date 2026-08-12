### Fixed

- The session-pagination arithmetic added in #2347 was untested: the ABS oracle
  capture holds three sessions, so every fixture-derived assertion fit on one page
  and neither slice clamp was ever reached. Added a table test that seeds twelve
  sessions and asserts the page/`numPages`/`itemsPerPage` maths across five cases,
  plus the property that pages 0 and 1 partition the set exactly — every session
  once, none dropped, none repeated.
