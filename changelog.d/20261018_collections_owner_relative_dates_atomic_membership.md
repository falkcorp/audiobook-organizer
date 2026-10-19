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

Security: because a dynamic collection is read as its owner, anyone who could
edit its query could have used `read_status`, `progress_pct` or `last_played` to
read the owner's listening state out of the members. Only the evaluating user
(the owner, or the first active admin for a collection that fell back to one) can
now write those fields into a query, when creating a dynamic collection, editing
or editing its query; anyone else gets a 403 with code
`COLLECTION_PER_USER_QUERY_OWNER_ONLY` and nothing is changed. Queries without
those fields, renames and re-sending the unchanged query are unaffected.

A stand-in admin no longer evaluates a per-user query. A dynamic collection whose
owner is gone and whose query uses `read_status`, `progress_pct` or `last_played`
is not evaluated as the first active admin (that would publish the admin's
listening state to every reader): materialize answers 409
`COLLECTION_OWNER_MISSING_PER_USER_QUERY`, a native read answers 200 with the
stored members and an `evaluation_blocked` flag, and the stored members are left
as they were. Queries without those fields still fall back to the admin. The first
active admin takes the collection over by saving its query again with
`"adopt_owner": true`; re-sending the query without the flag never adopts. The startup
owner backfill leaves such collections ownerless instead of assigning the admin,
and logs one warning listing their ids and names (also in the system activity
log) on every start until they are resolved. Per-user queries that someone other
than the owner wrote before the 403 gate cannot be detected, because collections
keep no authorship history.

Review fixes: with authentication off, `_local` is the owner and evaluator of
dynamic collections, and a query with no per-user field never answers 503.
`PUT /api/v1/collections/:id` with `book_ids` now requires `version` (400 when
missing, 409 with `current_version` when stale). `date_added:(a|b)` alternations
work. A `PUT` that changes only `sort_json` or `limit` on a blocked collection
answers with the `evaluation_blocked` flag. A value list on a per-user field
(`read_status:(finished|in_progress)`, `last_played:(2026-03-31|2026-01-01)`) now
means any of the values, as it already did on indexed fields (it used to require all
of them at once). The hourly refresh runs only when a relative-date dynamic collection
exists and the search index is open, and ends as a clean no-op when the index is closed.
