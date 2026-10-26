Collections backend (07-F5a). The one search syntax now takes rolling relative dates: a sign, a whole number
and a unit (`d` days, `w` weeks, `m` months, `y` years), for example
`last_played:>-30d`, `date_added:>-1y` and `last_played:-30d..now`. An offset is
resolved each time the search runs, so a saved search keeps sliding (a scheduled
op, `collections.refresh-relative-dynamic`, refreshes the stored members of
relative-date dynamic collections hourly); an unsigned `30d` is refused with a
message showing `-30d`, including on a per-user field: creating a dynamic collection or changing its query
with a query that cannot be parsed or translated answers 400 and stores nothing.
A date with no time of day names its whole UTC day: `<=2026-03-31` and
`2026-03-01..2026-03-31` include everything added on 31 March, `>2026-03-31`
starts after it. `date_added` (alias `added`) and
`date_modified` (alias `modified`) are new search fields, in the search index
and on the Library list; `last_played:*` means played at any time. The Library
list previously treated any `last_played:` value as "has been played" and
refused `date_added`; both now work. `read_status:unstarted` now finds books the
user has no listening row for, in the search, in collections and on the Library
list. A relative-date search is never cached, because its answer changes with
the clock.

One-time cost at the first start after deploy: adding the two date fields bumps
the search index mapping version, so the whole search index is rebuilt once.
While it rebuilds, Library search falls back to the slower substring path and
the index reports itself as rebuilding until it has caught up.

Adding and removing books on a static collection is now atomic in the store
(one lock around read, change and write), so two requests racing on the same
collection no longer lose one of the changes. New routes
`POST /api/v1/collections/:id/books` (body `{"book_ids": [...]}`, 1 to 500 ids)
and `DELETE /api/v1/collections/:id/books/:bookId` need the
`collections.manage` permission. The four Audiobookshelf membership handlers use
the same store operations; two concurrent adds now both succeed instead of one
answering 409. The existing collection routes are unchanged.

`/auth/me` and the login response now carry `permissions`, the signed-in user's
effective permission names, so the web app can show or hide collection controls.
The web `AuthUser` type has `permissions` (empty when the server does not send
it). A web search-syntax help entry and `docs/search-syntax.md` describe the
date terms.

Review fixes: `PUT /api/v1/collections/:id` with `book_ids` now requires `version` (400 when
missing, 409 with `current_version` when stale). `date_added:(a|b)` alternations
work. A value list on a per-user field
(`read_status:(finished|in_progress)`, `last_played:(2026-03-31|2026-01-01)`) now
means any of the values, as it already did on indexed fields (it used to require all
of them at once). The hourly refresh runs only when a relative-date dynamic collection
exists and the search index is open, and ends as a clean no-op when the index is closed.
