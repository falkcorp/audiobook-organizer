### Changed

- After an unclean shutdown, the dedup vector index is brought up to date from its last snapshot at startup instead of being rebuilt from scratch. It is rebuilt only when an indexed book's or author's embedding changed, and the log names which ones.
