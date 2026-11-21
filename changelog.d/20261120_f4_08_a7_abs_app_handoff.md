### Added

- Dark, not registered anywhere: `abs.NewAppHandoff()` (`internal/server/handlers/abs/apphandoff.go`) is books' native-app hand-off for core's OIDC callback (F4-08 A7). It implements `auth.OIDCAppHandoff` over HEAD's existing ABS authorization-code store: the redirect allowlist is the one `/auth/openid` enforces, and `Mint` issues HEAD's single-use, PKCE-bound, epoch-stamped code and returns the app-scheme location. Nothing calls it until the identity flip registers it as `MethodSpec{Method: "books.abs", AppHandoff: ...}`, so the running server is unchanged.
