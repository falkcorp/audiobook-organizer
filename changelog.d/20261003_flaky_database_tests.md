### Fixed

- Two books (or any two records) created within the same millisecond could be given ids that sorted in the wrong order, so lists and search results that fall back on the id to break a tie could show that pair in a different order than they were added. Ids now always sort in the order they were created. Login session ids are deliberately left unordered, because they act as a credential and must not be guessable from one another. This was also the cause of an intermittently failing search test in CI.
