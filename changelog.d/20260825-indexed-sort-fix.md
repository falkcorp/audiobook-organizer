### Fixed

- Sorting the library by year came back in the order books were added rather
  than by year. This was the default configuration, so it affected a normal
  install. The same fault affected sorting by bitrate. Counter-intuitively, it
  was *switching a sort index on* that broke those sorts — with the index off
  they were already working.
- Sorting by author or series returned an arbitrary order in the fallback mode
  the server enters when its in-memory index is unavailable. The author's name
  is now looked up rather than read from a field the stored record does not
  always contain, so both modes agree.
- Sorted pages are now fetched a page at a time instead of loading the whole
  matching library and discarding all but one page.
