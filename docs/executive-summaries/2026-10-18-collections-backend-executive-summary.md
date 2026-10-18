<!-- file: docs/executive-summaries/2026-10-18-collections-backend-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: bc1b7020-e5ad-4493-90ab-243c0a1234fc -->
<!-- last-edited: 2026-10-18 -->

# Collections now show everyone the same books, searches understand "the last 30 days", and adding books can no longer lose changes

Task 07-F5a, branch `task/07-f5a`, PR not yet merged. The decisions behind it are
the owner's D74 to D77 (keep the collection routes, evaluate as the owner,
relative dates in the one search syntax, atomic add and remove).

## Executive Summary

- **A dynamic collection is the same for everyone.** A dynamic collection is a
  saved search, such as "books I have not finished". It used to be worked out
  for whoever was looking, so two people saw two different collections, and a
  term about listening history meant the viewer's own. It is now worked out as
  the collection's owner: the person who made it, or, if that person has been
  removed, the first active administrator. A start-up step gives existing
  collections without a real owner that administrator. A server with no active
  administrator cannot work such a collection out and says so, rather than
  guessing.
- **Searches take rolling dates.** `last_played:>-30d` means played in the last 30
  days, `date_added:>-1y` means added in the last year, and
  `last_played:-30d..now` is the same window as a range. Units are days, weeks,
  months and years. The window is worked out each time the search runs, so a
  saved search or a dynamic collection keeps sliding forward. A bare `30d`
  with no sign is refused with a message showing `-30d`, instead of being
  guessed at.
- **The Library page now honours those searches.** Until now the Library page
  treated any `last_played:` value as "has ever been played", and refused
  `date_added:` altogether. Both now work, and "added" and "modified" are new
  searchable dates.
- **"Not started" finds books you never opened.** `read_status:unstarted` used to
  miss every book with no listening record at all, which is most of a library.
  It is fixed in search, in collections and on the Library page.
- **Adding and removing books in a collection cannot lose a change.** Two
  requests changing one collection at the same moment could both read the old
  list and the later write would silently undo the earlier. The add and remove
  steps are now done as one locked step. Two new routes add and remove books
  directly, and the four Audiobookshelf calls that change membership use the
  same step. Two simultaneous adds now both succeed.
- **Nobody can read another person's listening through a shared collection.**
  Because a dynamic collection answers with its owner's listening history for
  everybody, anyone allowed to edit it could have rewritten its search to ask
  "which books has the owner finished?" and read the answer off the list. Only
  the person the collection is worked out as can now put listening-history terms
  (read status, progress, last played) into its search; anyone else is refused
  with a clear message and nothing changes. Ordinary searches, renames and book
  lists are unaffected.
  If a collection's owner has been removed, it is not worked out as the first
  administrator when its search uses those terms (that would show the
  administrator's listening to everyone): it keeps the books it had and says
  why, until the administrator saves its search again to take it over. Start-up
  leaves such collections alone and lists them once in the activity log. One
  gap remains: a listening-history search that someone other than the owner
  wrote before this fix cannot be found, because collections keep no record of
  who edited them; the new check stops any more being written.
- **The web app knows what the signed-in user may do.** The sign-in and
  "who am I" answers now list the user's permissions, so the app can show or
  hide collection controls.
- **One-time cost: the search index is rebuilt once after deploy.** The two new
  searchable dates change the index layout, so the first start after deploy
  rebuilds the whole search index. While it rebuilds, Library search uses the
  slower fallback and says it is rebuilding.
- **Not touched.** Listening data (durations, read status, progress, last
  played) is never removed or rewritten. The existing collection routes and the
  reading and position routes are unchanged.
- **Verification.** Automated tests with made-up books and users cover each
  behaviour above, including a real database with competing add and remove
  requests run many times over and the Library page route with each date term.
  The rolling-date change has not yet been tried on the production library.

## Dynamic collections evaluated as their owner

**What it was.** Whoever opened a collection decided what it contained.

**Why it mattered.** A shared collection that differs per viewer is not a
collection. Listening-based terms in it silently described the wrong person.

**The fix.** The owner is recorded when the collection is made. If the owner no
longer exists the first active administrator stands in, chosen by earliest
account creation (ties broken by account id). Collections already in the library
with no real owner are repaired once at start-up; running it again changes
nothing.

## Relative dates

**What it was.** Only exact dates were understood, and only for `last_played`.

**The fix.** One date reader serves the search index, the per-user matcher, the
Library page and the collection evaluator, so a date term means one thing
everywhere. Every row in one evaluation is judged against the same instant, so
a window cannot move halfway through a list. A search with a relative date is
never served from a cache, because the same words give a different answer
tomorrow. `docs/search-syntax.md` documents the syntax for users.

## Atomic add and remove

**What it was.** Adding a book read the collection, changed the list and wrote it
back, with nothing stopping a second request doing the same in between.

**The fix.** The store now does read, change and write under one lock. Changing
a collection by any other route while an add is running now gets a conflict
instead of overwriting it.
