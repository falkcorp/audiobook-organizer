Collections backend (07-F5a). Dynamic collections are now worked out as their
owner: the user who created the collection (or, if that user is gone, the first
active admin) rather than whoever happens to be asking, so every viewer sees the
same set and `read_status:`, `progress_pct:` and `last_played:` terms mean the
owner's listening. A startup step gives existing dynamic collections with no
real owner (empty, `_local` or a deleted user) the first active admin. A server
with no active admin cannot work out a dynamic collection and answers 503.

The one search syntax now takes rolling relative dates: a sign, a whole number
and a unit (`d` days, `w` weeks, `m` months, `y` years), for example
`last_played:>-30d`, `date_added:>-1y` and `last_played:-30d..now`. An offset is
resolved each time the search runs, so a saved search keeps sliding; an unsigned
`30d` is refused with a message showing `-30d`. `date_added` (alias `added`) and
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
