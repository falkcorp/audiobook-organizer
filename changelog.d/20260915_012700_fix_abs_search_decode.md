### Fixed

- ABS `/api/libraries/:id/search` now decodes in AudioBooth. Narrator hits carry the required `numBooks` (the count was dropped on purpose, after reading the `/narrators` tab model's optional count as the search rule), and genre hits are `{name, numItems}` objects instead of bare strings. AudioBooth's search decode is all-or-nothing, so one narrator hit without `numBooks` threw the whole document and every search with a narrator match showed "Search failed" and nothing else. `GetGenreCounts` joins the store so `/filterdata` and `/search` share one genre scan.
