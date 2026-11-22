### Changed

#### The outbox operator routes' permission tests now run against the router's real authorization stage

The six outbox and event-topic operator routes (dead-letter list, retry and
discard, backlog purge, consumer list, topic catalog) had their sign-in and
permission tests written but switched off until the authorization stage
existed. They now run through the router's own stages 12 and 13 with the real
permission catalog: a request with no sign-in is answered 401, a caller
without the route's permission is answered 403 naming the missing
permission and leaves one denied-permission audit row, a refused request
never reaches the handler, and the purge still refuses an API key and an
auth-off request. Tests only; no behaviour change (F6-03 G3).
