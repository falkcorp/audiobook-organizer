### Fixed

#### ABS home screen: Recently Added and Continue Listening now cover the whole library

The Audiobookshelf-compatible home screen (`GET /api/libraries/:id/personalized`)
built every book shelf from one window: the first 20 rows of the unfiltered
book list in storage order, which are the library's oldest rows and include
alternate versions and unorganized imports. That window was then sorted by date
added. So Recently Added showed books from months ago, and a book imported and
organized minutes earlier never appeared. Continue Listening only checked those
same 20 rows for progress, so on a large library it was usually missing
entirely.

Each shelf is now chosen from its own complete source:

- **Recently Added** lists the ten newest books by date added, taken from the
  same visible set the Library tab shows (primary version, organized, not
  quarantined). It runs the same store query as the Library tab's
  sort-by-added-date, so the order matches the `addedAt` dates the app
  displays. Rows with no creation date are left out by the store query itself
  (one query, however many undated rows there are) so they cannot sit at the
  top.
- **Continue Listening** reads the user's stored progress list, the same one
  `/api/me` serves. It shows books that are started, not finished and not
  hidden by the user, most recently listened first. Progress stored under a
  book that was later merged appears on the surviving book, unless that book
  has its own progress row. A single book whose progress cannot be read is
  skipped (and logged) instead of emptying the shelf; the progress list that
  `/api/me` sends to the app is unchanged and still all-or-nothing.
- **Discover** shows visible books in the store's default order, leaving out
  anything already on Continue Listening.

The Library tab's sort by added date did not have this bug: it already ordered
the whole visible set before paging. The unfiltered summary read has been
removed from the ABS handler's store interface, so no shelf or list on that
surface can go back to reading unfiltered rows.
