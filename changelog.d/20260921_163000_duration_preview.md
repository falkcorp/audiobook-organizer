### Fixed

- The duration backfill's preview now shows how many books would have their
  total held back, and says so on each example. Previously that count always
  read zero in a preview, because it was only tallied while actually writing —
  so a book whose running time really is shorter and a book where almost none
  of its files could be read looked identical, and the one number that told
  them apart was switched off exactly when you needed it to decide whether to
  run the thing.
