### Added

- Library: a `series_position` sort orders a series by reading position (numeric, decimals such as 1.5 included, unnumbered books last). A `/library?series_id=N` view now defaults to it (unless a search is active, which keeps title order), and the series filter shows a removable chip with the series name. The sort is offered only in a series view. Without an `author_id` or `series_id` the server ignores `sort_by=series_position` and returns the default paged order, reporting the drop by leaving `sort_by`/`sort_order` out of `applied_filters`, instead of fetching and sorting the whole library on every page request.

### Fixed

- The author-books list used by the author merge popover no longer stops early when the server returns a page smaller than requested, and fails loudly instead of returning a truncated list when its page limit runs out.
- The table's "Series #" column sort now orders by series position in a series view (it is not sortable elsewhere); it previously sent a key the server did not recognise.
