### Fixed

- Editing a book's author now rejects unusable names (chapter numbering such as
  `Track 01`) instead of creating an author row for them. When no usable name
  remains the request fails with a clear error rather than silently writing
  `author_id = 0`, which was a reference to an id no row has.
