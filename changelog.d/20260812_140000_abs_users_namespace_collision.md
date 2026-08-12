### Fixed

- **`/api/users` 404'd instead of reaching its seven live app-API routes.** A second
  instance of the `#2332` namespace-collision regression that `#2333` was supposed to
  close. `/api/users` was left in `absUnimplementedNamespaces`, so the `/api/*` →
  `/api/v1/*` compatibility redirect skipped it and `NoRoute` answered 404 — on every
  deployment, including ones with the ABS surface disabled, since the middleware is not
  gated on `ABSAPIEnabled`. The seven routes behind it include
  `POST /api/v1/users/:id/reset-password`. Moved to `absAppAPICollisions`.

  `#2333` missed it because it prescribed grepping the source for `/api/v1` twins, and
  grep cannot answer that question: gin composes a route's path from its `RouterGroup` at
  registration time, so a grouped route (`protected.Group("/users")` → `users.GET("", …)`)
  has its final path written nowhere in the source. Six prefixed groups register that way.

### Added

- **`TestUnimplementedNamespacesHaveNoAppAPITwin` / `TestCollisionNamespacesAreStillColliding`**
  replace that grep with a check derived from `router.Routes()`, the flattened table gin
  actually matches against and the only complete oracle for which paths exist. The first
  fails if a namespace marked unimplemented has a live `/api/v1` twin; the second fails if
  a collision entry's twin ever disappears, so the list cannot rot into a lie. Both were
  verified by reintroducing the bug in each direction, and the first guards against a
  vacuous pass by asserting the route table is populated before trusting any negative
  result.

30 days of production logs show zero unversioned requests to any of the six namespaces —
validated method-agnostically against a query shape that does return traffic for the
`/api/v1` forms — so no client is known to have been affected by either regression.
