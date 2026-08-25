### Fixed

- **Sorting the library in an Audiobookshelf client did nothing.** Picking "Published
  Year" (or Author, Date Added, Duration, File Size, Last Updated) returned the right
  books in arbitrary order behind a `200 OK`, with nothing logged. The entire sort
  parser for `/api/libraries/:id/items` was a substring test for `"title"`, so every
  other key left the sort field empty — and an empty sort field does not error, it
  falls through to an unordered index walk. The dotted keys clients actually send
  (`media.metadata.publishedYear`, `addedAt`, …) are now mapped to real store sort
  fields, the `year` and `author` sort indexes are enabled by default, and a sort whose
  index is disabled now logs a warning instead of silently returning unordered rows.
  Date Added, Last Updated, Duration and File Size map correctly but stay unindexed on
  purpose: each index taxes scan insert throughput, and enabling one is a single config
  line if it is ever asked for.
