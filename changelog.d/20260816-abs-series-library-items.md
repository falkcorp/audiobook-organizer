### Fixed

- **Series now appear in ABS apps.** The series list returned book entries in an
  ad-hoc shape — six fields, with no `media`, `mediaType` or cover — instead of
  the `LibraryItem` objects the ABS API defines. Clients decode a series' books
  as a single typed list, so one undecodable entry discarded the whole response
  and the Series tab showed "No Series Found" even though 23 of the 50 series on
  the first page had real books. Series books are now built by the same
  serializer the (working) playlists route uses.

- **A series no longer reports a book count it cannot list.** 9 of 50 series on
  production reported one or more books while returning an empty list, because
  books without a resolvable sync id are dropped after the count is taken.
  `numBooks` now counts the books actually served — the rule `totalDuration`
  already followed — and a mismatch is logged rather than silently served.
