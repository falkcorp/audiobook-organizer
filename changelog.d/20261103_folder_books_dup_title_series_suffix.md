### Fixed

- The folder-books repair's duplicate-title check now compares core titles: a proposed new book whose title only adds a series suffix ("Title_ Series, Book", "Series - N - Title") to a live book of the same author is skipped as a duplicate, and new books are titled with the core title.
