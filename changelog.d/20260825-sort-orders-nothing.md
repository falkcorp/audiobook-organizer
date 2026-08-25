### Fixed

- Sorting the library by author, series, year, genre, language, publisher,
  codec, quality, edition, bitrate or sample rate did nothing. The list came
  back in the order books happened to be stored, with no error and no visible
  sign that the sort had been ignored — 13 of the 23 available sort options
  were affected. Sorting by title, narrator, duration, file size or format
  always worked. All of them work now, including on later pages: asking for
  the second page of books sorted by author now returns the books that
  actually belong there.
