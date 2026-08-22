### Fixed

#### N-10: advertised login rate limit (10/10min) now matches the real throttle (15/15min)

`GET /status` (and the `serverSettings` block in the `/login` and `/auth/refresh` bodies) advertised 10 requests per 600000ms, while the throttle in `absauth` actually allows 15 failures per 15 minutes. Both values are now derived from the `absauth` constants, so the advertisement cannot drift from the code that issues the 429.

The comment alongside them claimed a client could use these to pace itself. It cannot, and the comment now says so: `absauth` charges only failed attempts, and the budget is shared per source IP between `/login` and `/auth/refresh`, so the advertised number is an upper bound rather than a login allowance.

The two ABS oracle fixtures keep their captured values. Publishing our own policy where the oracle recorded its ABS defaults is a deliberate divergence, so it is declared as a named conformance allowance instead of being edited into the capture.
