### Changed

- Every response carries an `X-App-Build` header naming the build (the
  `mediacore-build` tag of the embedded web app; `dev` for a build without
  one). The one exception is a request refused by the peer allow-list, which
  is answered before any header is added.
- Internal: the HTTP perimeter (peer allow-list, request IDs, tracing and the
  HTTP metrics, panic recovery, the access log, security headers, CORS,
  compression and the `/api` rate limit) runs in the core runtime instead of
  the gin engine. Only the Basic sign-in gate and the per-route body limits
  stay in gin for now.
- Every response carries an `X-Request-ID` header (again, except a refused
  peer's), and an error's problem body carries the same ID as `request_id`.
- A rate-limited request now gets an `application/problem+json` body with the
  code `rate_limited`, plus `RateLimit-Policy` and `RateLimit` headers next to
  `Retry-After`. When the tunnel connectors are listed in
  `TUNNEL_CONNECTOR_ADDRS`, the limit applies per visitor
  (`CF-Connecting-IP`) instead of one shared bucket per connector.
- A request from a peer outside `ALLOWED_PEER_CIDRS` is refused with a 403
  `application/problem+json` body (code `peer_refused`) instead of plain text.
- A cookie-carrying request from another site or a sibling subdomain that
  would change something (`POST`, `PUT`, `PATCH`, `DELETE`) is refused with
  403 `cross_origin_rejected` and logged as a security event.
- `Strict-Transport-Security` is sent over TLS, or when a trusted tunnel
  connector (`TUNNEL_CONNECTOR_ADDRS`) says the visitor used HTTPS; a caller
  can no longer turn it on by sending `X-Forwarded-Proto: https` itself.
- CORS no longer allows the Vite dev server's origin (port 5173) in debug
  builds; the dev server proxies `/api`, so the browser sees same-origin
  requests and never needed it.
- HTTP metrics: the `http_server_*` series are recorded by the core runtime
  for every request, gin's included. A request refused before a route (a
  refused peer, CORS, a cross-origin refusal, an unrouted 404 and, from F4,
  authentication) carries no `http_route`; a request refused by the rate
  limit or a body limit carries its route's pattern. Preflights and Basic-gate
  challenges are counted for the first time. `http_route` values follow
  core's route patterns (`/api/v1/audiobooks/{id}` where gin wrote
  `/api/v1/audiobooks/:id`). `http_server_response_body_size` measures bytes
  before compression.
- The HTTP access log changes format. gin's text lines
  (`[GIN] 2026/11/08 - 05:00:00 | 200 | ...`) become structured records with
  `msg="http access"` and the attributes `method`, `path`, `status`,
  `duration`, `bytes`, `proto`, `client`, `request_id` and `route`. A Loki
  query or alert that matches `[GIN]` no longer matches anything; match
  `msg="http access"` instead. The lines still go to standard output only and
  stay out of the Activity Log, as gin's did. A request whose handler panics
  still gets its line, with status 500.
- Server-sent event streams are no longer gzipped: every `text/event-stream`
  response is sent uncompressed, where HEAD excluded only `/api/events`.
- `error.type` on the `http_server_*` series and on request spans is set only
  when the request ended early (the client went away or a deadline passed). A
  gin handler's recorded error no longer sets it.
- When the Basic sign-in gate is on, a request without credentials now meets
  the core perimeter before the gate. Its 401 carries `X-App-Build` and
  `X-Request-ID`. A rate-limit (429), body-limit (413), peer (403) or
  cross-origin (403) refusal comes before the gate and is
  `application/problem+json`, and every such request spends the client's
  `/api` rate-limit tokens even if the gate would have refused it.
- A setting saved through the API for the listener, rate limit, body limits,
  peer allow-list or trusted proxies takes effect at the next restart.
