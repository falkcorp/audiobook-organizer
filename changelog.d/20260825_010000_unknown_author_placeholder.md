### Fixed

- **A book with no known author could never get one.** When the organizer files a
  book whose author it cannot determine, it puts the book in an "Unknown Author"
  folder and includes that name in the filename. The next library scan then read
  the name back out of the filename and recorded "Unknown Author" as if it were
  the real author — after which every part of the system that offers to fill in a
  missing author skipped the book, because as far as it could tell the author was
  already known. The scan now recognises the placeholder as the system's own
  "not known yet" marker in both places it parses names out of file paths, so
  these books are offered for automatic parsing again and the answer is no longer
  thrown away on arrival. Books already recorded this way are re-examined the next
  time their file changes or a full rescan is run; they are not repaired
  retroactively by this change alone.
