### Changed

- When an Audiobookshelf client asks to sort the library by something this
  server has no equivalent for — File Modified, File Birthtime, the three
  Progress sorts, or Randomly — the server now says so in its log once, and
  lists the sorts it does support. Previously it returned the books in an
  unspecified order and said nothing at all, so there was no way to tell an
  unsupported sort from one that had quietly stopped working.
