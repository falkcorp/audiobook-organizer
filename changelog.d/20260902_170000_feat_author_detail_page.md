### Added

- **Clicking an author on a book now opens that author.** The app had no
  addressable author at all: `/authors` was a list and a book's author was
  plain text, so there was nothing to click. There is now an author page at
  `/authors/:id` showing the author's books, counts and aliases, reachable from
  the Authors list and from the Author field on any book. Each credited author
  is a separate link, so a co-authored book opens the co-author you clicked.
- New `GET /api/v1/authors/:id`, served from the same cached aggregate the
  authors list is built from, so the counts on the two screens agree by
  construction rather than by a second implementation that could drift.

### Notes

- An author name with no id behind it (the legacy `author_name` string) stays
  plain text rather than becoming a link that goes nowhere.
- The credited-titles count on the author page can exceed `book_count`: the
  books getter is junction-aware and includes titles where the author is a
  co-author. Both numbers are shown rather than hiding one.
