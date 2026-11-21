### Added

- New dark package `internal/books/searchindex`: books' native full-text index declaration over core's index contract, its document builder, its query translator and the declarations of the memdb, hnsw and chromem derived indexes (with the hnsw warm-start hooks). Nothing calls it yet; the F7-09 fold wires it. It also adds the native half of the "index rebuilt at cutover" test, which rebuilds the index and checks the answers against the ones the previous build recorded.
