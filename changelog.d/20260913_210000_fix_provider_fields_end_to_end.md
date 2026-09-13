### Fixed

- **Google Books and Open Library fields now reach the book on every apply.**
  The Google Books client dropped page count, categories and every cover size
  but the thumbnail. The Open Library client dropped subtitle and page count.
  Its ISBN lookup decoded only title, publisher, year and cover. It also read
  "September 21, 1937" as year 0. Google categories now become the genre and
  category tags, and those tags record Google as their source rather than
  `audible_category`. The genre is the most specific category segment
  ("Science Fiction"), not the coarse `mainCategory` ("Fiction"), because the
  apply writes genre unconditionally. The cover proxy now also allows
  `books.googleusercontent.com`, where Google serves some cover sizes. Open Library editions now carry subtitle, pages, series,
  language, description, both ISBNs and narrators from `contributors`.
- **Editors, translators and illustrators are no longer written as authors.**
  A credit such as "John Joseph Adams - editor" or "Jane Doe (Translator)"
  used to go into the author string. Now a narrator credit goes to the
  narrator field, and every other non-author role is dropped.
- **Bulk metadata fetch no longer overwrites the audiobook release year with a
  print year.** It built its own copy of the candidate conversion. That copy
  wrote every year into `audiobook_release_year` and dropped ISBN-10/13,
  narrator, genre, subtitle and page count. It now uses the same conversion as the single-book
  apply and sends a print year to `print_year`.
- **Open Library search publisher is set only when all editions agree.** The
  publisher list is an unordered aggregate across editions, so its first entry
  was a guess. It now follows the same rule as language.
