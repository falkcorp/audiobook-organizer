### Fixed

- A rescan now notices an audiobook file that was replaced in place (a re-rip, another
  edition, a swapped file) under a book that already has its file rows. Before, the scanner
  skipped such books outright, so the row kept the old file's hash, size, duration, codec,
  stream info, fingerprint and intro transcript, and dedup and content matching kept using
  signals of bytes that no longer exist. The rescan now checks each stored file's size and
  modification time; only a file that looks changed is hashed, and when the hash differs
  the row is refreshed through the same merge that handles replaced files at a path taken
  over by another book (audio-derived data is dropped unless the new file's probed
  duration and codec prove it is the same recording). Unchanged files cost one stat, and a
  touched file with the same bytes is left alone. Track and disc placement are kept.
