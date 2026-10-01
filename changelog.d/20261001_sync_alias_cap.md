### Fixed

- Bookmarks and merged listening state for books with hundreds of consolidated chapter files. The sync alias cap was 256, sized for "a handful" of merges, but fragment consolidation retires every chapter book into its real book (American Gods: 306). Past the cap, each further retire failed to move its listening state, and that book's bookmarks stopped loading. The cap is now 4096 and still a hard error beyond that.
