### Fixed

#### Deleting a narrator no longer removes another narrator's name lookup

Name-index keys now collapse internal whitespace, so two narrators such as
"John Smith" and "John  Smith" (double space, indexed under the older key) can
map to the same index entry. Deleting the double-spaced narrator removed the
index entry by raw key, which took the other narrator's entry: that narrator
could no longer be found by name, and the next import created a duplicate of
it. The deleted narrator's own older-style entry was also left behind.

`DeleteNarrator` now removes an index entry only when it belongs to the
narrator being deleted, the same ownership check authors, aliases and series
already use, and it removes both the current and the older-style entry.
