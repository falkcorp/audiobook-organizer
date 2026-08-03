<!-- file: changelog.d/20260803_030000_contributors_visible_only.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9d7b41-6c58-42ea-9013-8d24ba07c5e6 -->
<!-- last-edited: 2026-08-03 -->

### Fixed

- **The Authors and Narrators tabs disagreed with the Library about what the library
  is.** `/items` serves 16,491 primary+organized books, but the author and narrator
  lists were built from `GetAllAuthors` / `ListNarrators` — every row in the store,
  including contributors attached ONLY to the ~28,000 unorganized iTunes-tree books.
  That is where the junk came from: "authors" that are really track names
  (`065_Rise of the Corinari`, `13_Aurora`, `CD 12`), bare years, `Read by …` credits,
  and copyright notices.

  Both lists are now derived from the same visible-book set `/items` uses, in a single
  shared pass, so the three tabs cannot disagree.

- **Author book counts counted invisible books.** An author with forty unorganized rows
  and one real book read "41 books". Counts are now over visible books only.

- **Narrators gained a real `numBooks`**, which was previously omitted because there is
  no reverse narrator→book index; deriving both lists from the same junction pass
  supplies it for free.
