<!-- file: docs/decisions/2026-10-30-select-all-per-user-filters.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1c77411d-f7e4-4deb-94c6-806f79c23d38 -->
<!-- last-edited: 2026-10-30 -->

# Select-all honours per-user filters

## Decision log

- Chosen: `resolveFilterToBookIDs(ctx, f, viewerID)` evaluates per-user field filters as `ListFilters.PerUserFilters` with `UserID` set, and per-user search text through the same `GetAudiobooks` path. This is the matcher the Library route uses (`search.MatchPerUserFilters` underneath); no second matcher was written.
- Chosen: the bulk fetch op reads the viewer from its own operation row (`ActorUserID`, stamped by `servermiddleware.ActorOptions` from the verified request), not from a user id written into the op params. The brief and the TODO entry suggested params. Rejected: params, because the generic `POST /operations/v2` route takes caller-supplied params, so a `user_id` there is spoofable and injecting one needs a per-def hook in a generic handler; the row's actor already exists, is not caller-writable, and is the precedent used by repairs, user-merge and purge-preview.
- Chosen: with no viewer id and a per-user term the resolver still refuses (the 2026-10-26 guard), naming the filter. Rejected: silently dropping the term (the original defect) or falling back to the whole library.
- Chosen: the continuation link replays the actor (`WithActor`, auth method, permissions when recorded) because it re-resolves the same selection and would otherwise refuse on link 2.
- Chosen: the batch-candidates handler passes the signed-in caller. With auth off there is no caller, so a per-user filter is refused with a 400.
- Not done: `RetryOperationV2` re-queues with the retrier's `ActorOptions`, so a retry by a different user judges the selection by the retrier's rows. Left as a follow-up.
- Note: the brief's example `read_status:unread` is not a status value; the matcher's no-row word is `unstarted`, which the tests use.
