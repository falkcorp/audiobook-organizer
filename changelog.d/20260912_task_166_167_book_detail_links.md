### Added

#### Book page: the Series field links to the library filtered to that series, and author names are real links

On a book's detail page the Series field now links to the library narrowed to
exactly that series (the server's `series_id` filter, matched by id rather than
by a name substring, so "Dune" no longer also catches "Dune Chronicles"). The
library page reads that filter from the URL, keeps it when you page through
results, and sends it with every request. Author names on the same page are now
real links to each author's page, so they can be opened in a new tab or copied,
where before they were buttons. The series view is not ordered by position in
the series, because the server has no sort for that yet. Also known: typing a
search while the series filter is active currently drops the filter on the
server side.
