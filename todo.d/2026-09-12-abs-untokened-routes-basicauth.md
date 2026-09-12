- [ ] **ABS-SYNC: let ABS clients through BasicAuth on the 9 routes that have no
      token check.** #3296 exempts only the ABS routes whose handler chain contains
      `ABSRequireAuth`. The nine below still sit behind the global
      `servermiddleware.BasicAuth()`, so with `basic_auth_enabled` on (off in prod
      today) ABS clients still break: a client can't send Basic credentials and its
      ABS token together, because both use the `Authorization` header.
      - `POST /login`: new or logged-out clients can't get a token.
      - `POST /auth/refresh`: sessions can't renew, so every client is eventually
        logged out.
      - `GET /public/session/:id/track/:index`: playback through this path fails.
      - `GET`/`HEAD /api/items/:id/cover`: widget covers fail (the widget sends no
        headers).
      - `GET /ping`, `GET /status`: clients can't probe the server.
      - `GET /auth/openid`, `/auth/openid/callback`: SSO login is blocked.
      Each route already checks its own credential (password, refresh token,
      session id, OIDC code) or reveals next to nothing, so exempt them by exact
      method and route. Don't use a path prefix: `/api/items/:id` is token-gated
      but `/api/items/:id/cover` is not. Extend the route-walk test in
      `internal/server/handlers/abs/basicauth_exempt_test.go` to cover them. Only
      matters once someone turns BasicAuth on. The owner approved doing this
      eventually on 2026-09-12.
