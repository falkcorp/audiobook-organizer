### Security

Dynamic collections are now worked out for the person asking, not for the
collection's creator. A query on `read_status:`, `progress_pct:` or
`last_played:` is evaluated against the viewer's own listening state on every
read, in the native API (list, get, create, update, materialize) and in the
Audiobookshelf-compatible collection endpoints. Two people looking at the same
collection can see different members, and no response carries the creator's
listening state any more. This replaces the owner evaluation (D75): even
an admin is evaluated as themselves. The per-user filter pass now reads listening
state over a bounded worker pool instead of one book at a time.

A system evaluation with no viewer matches nothing for a per-user query and never
falls back to the creator. Previously a missing user was read as an empty
listening row, which made `read_status:unstarted`, `progress_pct:<50` and negated
filters match the whole library; the evaluator now returns an empty result.
Queries with no per-user field behave exactly as before.

### Changed

A collection whose query uses a per-user field no longer stores a member list:
the stored list is cleared on every write, ignored on every read, and scrubbed
for existing collections once at startup (`collection-peruser-purge`). The hourly
`collections.refresh-relative-dynamic` op skips these collections and no longer
reports a `blocked` count. Removed with the owner model: the 403
`COLLECTION_PER_USER_QUERY_OWNER_ONLY` gate, the `adopt_owner` update field, the
`evaluation_blocked` response fields and 409 `COLLECTION_OWNER_MISSING_PER_USER_QUERY`,
the startup owner backfill, and the "Take ownership" checkbox and blocked banner on
the collection page. `created_by_user_id` is attribution only.

### Fixed

A per-viewer collection whose members could not be worked out (search index not
ready, a query that no longer parses, an unreadable listening-state row) is flagged
in the native response with `members_unavailable` and `members_unavailable_message`,
shown as a warning on the collection page, and logged once per kind per minute,
instead of passing for a collection with no matches. A query that stopped parsing
counts as per-viewer, so a list it holds is never served on either surface; the
store itself drops a per-viewer collection's stored list on every create and update
(this closes the Audiobookshelf rename writing a creator's list back), and the startup
purge fails closed the same way and keeps going past a row the store refuses. A rename
or description-only update now answers with the caller's own members. The page no
longer offers Materialize for a per-user query.

A viewer's listening state is read in one scan per request instead of a point read per
candidate book per collection, and the 10,000-result window on a per-viewer evaluation
is gone: a library of any size is evaluated in full. Evaluations are bounded across
requests, and a failed state read is an error, not an empty history. The
Audiobookshelf `lastUpdate` of a per-viewer collection is the time of the request.

A per-user term (`read_status`, `progress_pct`, `last_played`) under an `OR` with another
term, or under a `NOT` over a group, used to be evaluated as an `AND` without a word,
so a dynamic collection or smart playlist showed far fewer books than its query said.
Create and update now refuse such a query with a 400 that names the
`read_status:(finished|in_progress)` spelling, and a stored one reads as
`members_unavailable` (query-invalid) until it is edited.

A member list is cut to 10,000 books, with the real count in `materialized_total` (a
collection), `book_ids_total` (a smart playlist) or `totalBooks` (the Audiobookshelf
collection row, whose `numBooks` stays the served count), so the removal of the
10,000-result window no longer lets a broad query store, return and expand the whole
library. A request cancelled while it waits for an evaluation slot is dropped instead
of queuing behind other evaluations. The evaluation-failure log line no longer carries
the store's error text, which can name a user and a book. A query that names a per-user
field anywhere in its text is treated as per-viewer, in the web page as well as on the
server. The collections list and detail pages say "members unavailable" instead of
"0 books" when a viewer's members could not be worked out.

A per-user term whose value the matcher cannot evaluate is refused like the boolean
shapes (`search.CheckPerUserTerms`, which runs the shape check and then
`search.CheckPerUserValues`): a `read_status` that is not `unstarted`, `in_progress`,
`finished` or `abandoned` (`read_status:completed` matched nothing and
`-read_status:completed` matched every book, for every viewer), a comparison or a
pattern on `read_status`, a `progress_pct` or `last_played` value that does not parse,
a pattern on any per-user field (`last_played:*` stays), and a field name written with
capitals. It runs in the evaluator, on collection and smart-playlist create and update,
and in the library list: `search=` answers 400 `invalid search: ...`, the `filters=` JSON
the web search box sends answers 400 `invalid filter value: ...` (checked in
`compileFieldFilterAt`, so the list, a bulk selection and a saved filter agree), and the
substring fallback used while the index rebuilds checks the query first. A filter cannot
carry an `OR` or a group, so the web box sends a query that names a per-user field and has
an `OR`, `AND`, `||`, `&&` or a parenthesis other than that field's own `(a|b)` list whole as `search=`
and shows the server's message; `read_status:(finished|in_progress)` works in a filter as
one term that matches either status. `read_status:finished OR author:kyle` no longer
answers as an `AND` there either. A stored row refused this way reads as `members_unavailable` with
the same guidance, and a stored smart playlist drops its cached list so the
Audiobookshelf surface stops serving it; `NOT NOT (group)` is no longer over-refused, and
the evaluator checks the query before the index state.

A smart playlist's materialized snapshot is cut at 10,000 books with the real count in
`materialized_total` and in its description. The playlist page shows `N of TOTAL` from
`book_ids_total` and a warning for `members_unavailable`. `sort_json` is validated on
create and on a changed update for collections and smart playlists
(`playlist.ValidateSortJSON`), and a playlist update checks the query only when it
changed. Only a failure to read the viewer's own state is withheld from the log; every
other evaluation failure is logged with its text. The search index query and the
evaluator take the request context, a cancelled request or refresh is neither a failure
nor a log line, and the Audiobookshelf collection routes answer 499 for a client that
went away and 503 on `GET /api/collections/:id` for a collection whose members could not
be worked out. A per-viewer collection's capped log line no longer carries one viewer's
match count.

A per-user term must now be spelled in one of the forms the matcher evaluates, and these
forms, which the old matcher accepted without a word, are refused: `read_status:finished*`,
`read_status:*finished`, `read_status:finished~`, `read_status:>finished`,
`progress_pct:50*`, `progress_pct:<inf`, `progress_pct:[* TO 50]` (write `<=50`),
`progress_pct:!=100`, `progress_pct:==50` and `last_played:2026-01-01*`. An empty list
(`read_status:()`) and a comparison inside a list (`progress_pct:(>50|<10)`, refused with a
message that says so) are refused, and a field name is judged with Unicode case folding.
A background metadata operation whose saved search holds a per-user term now fails
resolving its filter with `resolve filter: search: ...` instead of skipping the term.

A stored smart playlist whose query does not parse, is empty or has an unusable `sort_json`
reads as `members_unavailable` like the other refused definitions (no stacked `query:`
lead-in on the message); a failure that is the server's (an unreadable listening state) is a
500 with a generic message and a log line, no longer a 400 with its text. Materializing a
smart playlist answers 400 for a definition problem and 500 otherwise. A static copy made
from a cut smart playlist reports `book_ids_total` only while it is still the cut list.
The viewer-state scan and the playlist sort stop for a cancelled request
(`UserBookStateLister.ListUserBookStates` takes the request context). On the
Audiobookshelf surface the collection mutation routes answer with the committed change and
no books when the members cannot be worked out, a 499 after the write means the change was
kept, and a refused smart playlist is an empty one with no signal.
