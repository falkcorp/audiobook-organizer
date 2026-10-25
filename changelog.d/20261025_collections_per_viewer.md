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
