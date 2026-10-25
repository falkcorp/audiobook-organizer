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
