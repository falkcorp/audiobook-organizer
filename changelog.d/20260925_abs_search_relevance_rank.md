### Fixed

#### ABS search ranks results by relevance before applying the limit

`GET /api/libraries/:id/search` returned the first `limit` books whose title,
author or narrator contained the query, in creation (ULID) order, and stopped
there. On production q="Roll" returned "The Silas Kane Scrolls" and "The
Apocalypse Troll" but not the book titled exactly "Roll"; newly added books were
the likeliest to be cut. `SearchBooks` / `SearchBooksFiltered` now rank every
match (exact title, title prefix, whole word in title, title substring, author,
narrator; ties by shorter title, then ID) and apply offset and limit after
ranking. The memdb scan, the Pebble disk scan and the audiobooks service's
scoped substring fallback share one ranking (`SubstringSearchRank`), so they
return the same order. The ABS search `limit` ceiling is raised from 25 to 50;
the default stays 12.
