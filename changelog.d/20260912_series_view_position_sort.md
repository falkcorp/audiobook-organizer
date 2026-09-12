### Added

- Library: a `series_position` sort orders a series by reading position (numeric, decimals such as 1.5 included, unnumbered books last). A `/library?series_id=N` view now defaults to it, and the series filter shows a removable chip with the series name.

### Fixed

- The author-books list used by the author merge popover no longer stops early when the server returns a page smaller than requested, and fails loudly instead of returning a truncated list when its page limit runs out.
- The table's "Series #" column sort now orders by series position; it previously sent a key the server did not recognise.
