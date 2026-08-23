### Fixed

- **Organizing a book no longer adopts a file that merely happens to be the same
  size as the one it was about to place.** When a file already sat at a book's
  destination path, the organizer decided whether that file was "ours" from byte
  length alone — so two unrelated audiobooks of identical length would be treated
  as one, and the book's row would silently be pointed at the other book's audio
  with nothing recording what it used to say. Equal size is now only a free
  pre-filter; sameness is proven by hashing both files in full, and anything the
  check cannot read or verify is left alone rather than adopted.
