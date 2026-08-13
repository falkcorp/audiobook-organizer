### Fixed

- A filter with an empty value silently matched every book instead of narrowing anything.
  Filtering the library by, say, title with the box left empty returned the entire library
  rather than an error or an empty result — and the same filters are used to pick which
  books a background metadata operation runs against, so an empty value could quietly point
  a targeted job at the whole collection. Empty filter values are now rejected with a clear
  message explaining that omitting the filter is how you ask for everything.
