### Fixed

- **A book with no known author could never get one.** When the organizer files a
  book whose author it cannot determine, it puts the book in an "Unknown Author"
  folder and includes that name in the filename. The next library scan then read
  the name back out of the filename and recorded "Unknown Author" as if it were
  the real author — after which every part of the system that offers to fill in a
  missing author skipped the book, because as far as it could tell the author was
  already known. 3,407 books on the reference library were stuck this way. The
  scan now recognises the placeholder as the system's own "not known yet" marker
  instead of treating it as a real author, so these books are offered for
  automatic parsing again.
