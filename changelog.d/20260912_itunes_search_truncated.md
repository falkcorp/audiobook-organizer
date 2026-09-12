### Fixed

#### iTunes browse list: say when a search result was cut short

The "Browse & Select" list in the iTunes write-back dialog searches at most
10,000 books before keeping the ones with an iTunes ID. When a search filled
that limit, the list showed its total as if it were complete, so past the cap
the page count was wrong and nothing said so. The `/itunes/books` response now
includes `truncated: true` in that case (and `count` is documented as a lower
bound), and the dialog shows "Showing the first N matches — refine the search
to see the rest", with the pagination total marked "N+". Pagination still only
covers the matches that were fetched.
