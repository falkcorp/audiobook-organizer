### Fixed

- Bulk metadata apply: a candidate author credited as "Name - author/editor" is now refused as a non-author credit (the role check did not treat "/" as a word boundary), and role labels such as "editor", "author" and "narrator" are no longer read as a surname. Before this, "Radclyffe - author/editor" passed the 2026-09-13 prod preview because the word "editor" was taken as a surname and matched "read by narrator"-style words in a file path.
