### Fixed

- **Chapter files numbered "8-02 …", "01_07-…" or "…_002_of_349" are now read
  correctly.** The chapter parser, shared by the import scanner and the fragment
  repair, took only the first number of a leading disc-track pair, and did not
  recognise underscores around "of". Every chapter of a 349-file Eldest set got
  its own grouping key and the same chapter number, so the set was held as
  "duplicate chapter numbers". Both numbers of a leading pair are now part of
  the position and stripped from the key; "_of_" is the same as " of ". A
  numbered set whose files share the first number uses the second as the
  chapter number; one spanning several discs is held, as disc folders already
  were. A leading year ("2016 - …") is never read as a pair.
