### Fixed

#### Fragment consolidation — a moved row takes its path twin too

After the path-twin change, six `moved` rows (439 fragment books: Eldest 313,
Foundation 74, The Great Northern War 38…) were still refused with "also owned
by book", because a moved pair took no twin. The twin's file IS the donor's
file, so it adds nothing to repoint: a moved row now carries the twin and
Apply repoints each parent row once per row, then retires donor and twin.
