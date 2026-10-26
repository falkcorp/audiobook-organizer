### Repairs visibility review fixes (PR 4166)

Security: account audit rows and id-only log lines are withheld from a caller without `users.manage`. Rows of type `auth_email_displaced`, `user_merge` and `guest_service_token_*` are hidden from every activity read, and the `actor` of the trash purge and admin debug rows is stripped from the copy such a caller receives (the rows themselves stay). The log-line heuristic now also matches a line that names a user only by id, and the writers use named constants for these types.

Security: the result of an operation that names users (`maintenance.repair-merged-user-state`, `maintenance.user-merge`) needs `users.manage` on `GET /operations/:id/result`, kept in one registry in `internal/server/handlers/opaccess` rather than as a rule per route.

Security: the diagnostics export is narrowed to what its requester may read. Operations they may not read, and log lines that name a user, are left out and listed by count in `operations_withheld.txt`. An export started without a recorded requester is redacted as for a caller with no extra permission.

Security: log lines that name a user are withheld from a caller without `users.manage` on every operation-log surface: the embedded logs of `GET /operations/v2/:id`, `GET /operations/v2/:id/logs`, the log download, `GET /system/logs`, the `op.log` events of `/operations/events` and the activity fallback, as well as `GET /system/activity-log`. One predicate (`activity.NamesOthersUserLog`) decides for the activity reader and the diagnostics export alike. The attributes served with a log line drop any key that names a user (`user`, `email`, `account`, `actor`, `owner`, `listener`, `login`) and any value that names one (`activity.WithoutUserAttrs`). The `op.log` event payload and the diagnostics export carry no attributes, so they needed no change.

Changed: undoing a journal row that carries user data needs `users.manage`, like reading it. An editor who could preflight and revert an operation whose journal holds user-state rows now gets 403 on both; an operation with only library data is unchanged.

Security: both event streams (`/operations/events` and `/api/v3/ops/events`) check the caller's current account roles and status before every event, so a role lowered or an account disabled mid-stream stops the events on the next one instead of at reconnect. Promotions still need a reconnect. `Broker.publish`, which stamped no scope, is removed so a publish must name its run.

Fixed: a read of `?operation_id=` or `?book_id=` on the activity log no longer materializes, decodes and sorts every row of the id when the caller's visibility predicate is set (every handler read). `queryByIndexPrefixExcluding` streams the id's index newest-first, judges each row, and keeps only the requested page, so memory is bounded by the page and the total stays exact. The rows of the one id are still decoded, because the predicate needs the decoded row.
